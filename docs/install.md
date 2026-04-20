# Install

From v0.4.0 onward the kustomize OCI bundle includes the CRDs — one `kubectl apply -k` installs everything.

## Prerequisites

- A Kubernetes cluster with ArgoCD installed in the `argocd` namespace
- [`flux` CLI](https://fluxcd.io/flux/cmd/flux_pull_artifact/) or any OCI puller (e.g. `oras`)
- Network access from the cluster to `ghcr.io`

## Install

```bash
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 --output /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

The bundle ships:

- 2 `CustomResourceDefinition` — `ClusterbookCluster`, `ClusterbookProviderConfig`
- `Namespace clusterbook-system`
- `ServiceAccount clusterbook-operator`
- `ClusterRole` + `ClusterRoleBinding` — watch CRDs, read kubeconfig Secrets across namespaces, write Secrets in the ArgoCD namespace, manage leader-election Leases
- `Deployment clusterbook-operator` — distroless, non-root, `/healthz` + `/readyz` probes

## Verify

```bash
# Pod Ready, probes green
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
