# Install

From v0.4.0 onward the kustomize OCI bundle includes the CRDs — one `kubectl apply -k` on the pulled bundle installs everything.

## Prerequisites

- A Kubernetes cluster with ArgoCD installed in the `argocd` namespace
- An OCI-aware puller: `flux` CLI or `oras`
- Network access from the cluster to `ghcr.io`

> `kubectl` alone cannot pull OCI artifacts, and `kustomize build oci://…` is **not** supported by the kustomize CLI (as of v5.8). The pull step always needs a separate tool.

## Install

Pick whichever option matches your tooling — all three produce the same result.

### Option A — `flux` CLI

```bash
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 --output /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

### Option B — `oras`

```bash
oras pull ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 -o /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

### Option C — GitOps via flux-kustomize-controller

Continuously reconciles from the OCI registry, so upgrades happen automatically when a new tag is pushed.

```yaml
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata:
  name: clusterbook-operator
  namespace: flux-system
spec:
  interval: 10m
  url: oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize
  ref:
    tag: v0.6.0
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: clusterbook-operator
  namespace: flux-system
spec:
  interval: 10m
  sourceRef:
    kind: OCIRepository
    name: clusterbook-operator
  path: ./
  prune: true
```

## What's inside the bundle

- 2 `CustomResourceDefinition` — `ClusterbookCluster`, `ClusterbookProviderConfig`
- `Namespace clusterbook-system`
- `ServiceAccount clusterbook-operator`
- `ClusterRole` + `ClusterRoleBinding` — watch CRDs, read kubeconfig Secrets across namespaces, write Secrets in the ArgoCD namespace, manage leader-election Leases
- `Deployment clusterbook-operator` — distroless, non-root, `/healthz` + `/readyz` probes

## Verify

```bash
kubectl -n clusterbook-system get pods
kubectl -n clusterbook-system logs deploy/clusterbook-operator | tail
```

Expected log line from controller-runtime: `Starting workers`.

## Upgrade

Same command with a newer tag. Rolling update in place.

```bash
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:<new-version> --output /tmp/cbk
kubectl apply -k /tmp/cbk
```

## Uninstall

```bash
kubectl delete -k /tmp/cbk
```

CRDs stay by default. To also delete the schemas (and with them every remaining `ClusterbookCluster` CR):

```bash
kubectl delete crd clusterbookclusters.clusterbook.stuttgart-things.com \
                    clusterbookproviderconfigs.clusterbook.stuttgart-things.com
```
