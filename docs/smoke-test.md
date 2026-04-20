# Smoke Test

End-to-end validation on a real cluster. Takes ~5 min.

## Prerequisites

- A Kubernetes cluster with ArgoCD installed in the `argocd` namespace
- A reachable clusterbook API
- A Secret holding a kubeconfig for *some* workload cluster (can even be the same cluster's own kubeconfig for a loopback run)
- `flux` CLI and `kubectl`

## Step 1 — Install

```bash
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 --output /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
kubectl -n clusterbook-system logs deploy/clusterbook-operator | tail
```

Gate: pod reaches Ready. That validates the bundled CRDs, the RBAC, and the `/healthz`/`/readyz` probes are all wired correctly.

## Step 2 — Happy-path reconcile

Fill in the **four placeholders** (`<...>`) below.

```yaml
# test-clusterbook.yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookProviderConfig
metadata:
  name: default
spec:
  apiURL: <https://clusterbook.your-domain>
  insecureSkipVerify: false   # true for self-signed certs
---
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: smoke-test
spec:
  networkKey: "<10.31.103>"
  clusterName: smoke-test
  createDNS: false
  kubeconfigSecretRef:
    name: <your-kubeconfig-secret-name>
    namespace: <your-kubeconfig-secret-ns>
    key: kubeconfig
  providerConfigRef:
    name: default
  argocdNamespace: argocd
  labels:
    env: smoke-test
  releaseOnDelete: false      # keep the IP after delete — safer first run
```

```bash
kubectl apply -f test-clusterbook.yaml
kubectl -n clusterbook-system logs deploy/clusterbook-operator -f
```

## Three checks to confirm success

```bash
# 1. Status populated on the CR
kubectl get clusterbookcluster smoke-test \
  -o jsonpath='{"IP: "}{.status.ip}{"\nFQDN: "}{.status.fqdn}{"\nSecret: "}{.status.secretName}{"\n"}'
# expect a non-empty IP and secretName=cluster-smoke-test

# 2. ArgoCD cluster secret exists with the right label
kubectl -n argocd get secret -l argocd.argoproj.io/secret-type=cluster
# expect a secret named cluster-smoke-test

# 3. Secret has the expected server URL
kubectl -n argocd get secret cluster-smoke-test \
  -o jsonpath='{.data.server}' | base64 -d
# expect https://<ip>:6443
```

## Step 3 — ArgoCD sees it (optional)

```bash
argocd cluster list
# or use the UI — the cluster should appear and be reachable
```

## Step 4 — Delete

```bash
kubectl delete clusterbookcluster smoke-test

# The finalizer clears after one reconcile
kubectl -n argocd get secret cluster-smoke-test   # should be NotFound
```

`releaseOnDelete: false` leaves the clusterbook IP reserved — set to `true` once you're confident in the release flow.

## Troubleshooting

Failure points in order of likelihood:

| Symptom | Likely cause |
|---|---|
| Pod `ImagePullBackOff` | Image private / network can't reach `ghcr.io` |
| `reserve IP: ... 401/403` | Wrong `apiURL` or TLS verification failing (try `insecureSkipVerify: true`) |
| `load kubeconfig: key "kubeconfig" not found` | Adjust `kubeconfigSecretRef.key` to match your Secret |
| ArgoCD secret never appears | Check controller logs for RBAC denials; confirm `argocdNamespace` exists |
| `reconcileDNSDrift: list IPs: ... 404` | clusterbook API doesn't expose `/networks/{key}/ips` — upgrade clusterbook or open an issue |

## Future automation

A [`stuttgart-things/dagger/cluster-test`](https://github.com/stuttgart-things/dagger) module that spins up a kind cluster, installs ArgoCD and a fake clusterbook API, and runs steps 1–2 automatically in CI is tracked in the repo's issue tracker.
