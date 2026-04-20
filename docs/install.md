# Install

From v0.4.0 onward the kustomize OCI bundle includes the CRDs — one `kubectl apply -k` installs everything.

## Prerequisites

- A Kubernetes cluster with ArgoCD installed in the `argocd` namespace
- One of: `kustomize` ≥ v5.4, `flux` CLI, or `oras`
- Network access from the cluster to `ghcr.io`

## Install

Pick whichever option matches your tooling — all three produce the same result.

### Option A — `kustomize` (no intermediate files)

```bash
kustomize build oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 \
  | kubectl apply -f -

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

Requires `kustomize` v5.4+ (native OCI support). Nothing hits local disk.

### Option B — `flux` CLI

```bash
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 --output /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

### Option C — `oras`

```bash
oras pull ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 -o /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

### Option D — GitOps via flux-kustomize-controller

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
kustomize build oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:<new-version> \
  | kubectl apply -f -
```

(Or swap in the `flux` / `oras` / flux-kustomize-controller variant from above.)

## Uninstall

```bash
# If you installed via Option A
kustomize build oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 \
  | kubectl delete -f -

# If you pulled to /tmp/cbk (Option B or C)
kubectl delete -k /tmp/cbk
```

CRDs stay by default. To also delete the schemas (and with them every remaining `ClusterbookCluster` CR):

```bash
kubectl delete crd clusterbookclusters.clusterbook.stuttgart-things.com \
                    clusterbookproviderconfigs.clusterbook.stuttgart-things.com
```
