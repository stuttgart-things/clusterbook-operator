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

`--output` expects an **existing directory** (the artifact is a bundle of files, not one YAML).

```bash
mkdir -p /tmp/cbk
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.6.0 --output /tmp/cbk
kubectl apply -k /tmp/cbk

kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

Inspect before applying:

```bash
ls /tmp/cbk                 # 7 manifests
kubectl kustomize /tmp/cbk  # preview as a single YAML stream
```

### Option B — `oras`

```bash
mkdir -p /tmp/cbk
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

- 4 `CustomResourceDefinition` — `ClusterbookCluster`, `ClusterbookLoadBalancer`, `ClusterbookAllocation`, `ClusterbookProviderConfig`
- `Namespace clusterbook-system`
- `ServiceAccount clusterbook-operator`
- `ClusterRole` + `ClusterRoleBinding` — watch the CRDs, read kubeconfig Secrets across namespaces, create/update ArgoCD cluster Secrets and allocation ConfigMaps, manage `CiliumLoadBalancerIPPool`s, manage leader-election Leases
- `Deployment clusterbook-operator` — distroless, non-root, `/healthz` + `/readyz` probes

## Verify

```bash
kubectl -n clusterbook-system get pods
kubectl -n clusterbook-system logs deploy/clusterbook-operator | tail
```

Expected log line from controller-runtime: `Starting workers`.

## Upgrade

Same command with a newer tag, followed by a rollout wait:

```bash
mkdir -p /tmp/cbk
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:<new-version> --output /tmp/cbk
kubectl apply -k /tmp/cbk
kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

From **v0.12.1** onward the bundle pins the exact image tag, so `apply -k` triggers a normal rollout between versions. Earlier releases pinned `:latest`, which required an explicit `kubectl -n clusterbook-system set image …` workaround (tracked in [#53](https://github.com/stuttgart-things/clusterbook-operator/issues/53) — fixed in v0.12.1, see [#66](https://github.com/stuttgart-things/clusterbook-operator/pull/66)).

Verify the running image matches what you expect:

```bash
kubectl -n clusterbook-system get deploy clusterbook-operator \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
```

### Clusterbook server compatibility

Some operator features depend on the clusterbook server version. In particular, any CR with `createDNS: true` needs **clusterbook ≥ v1.25.1** — earlier versions silently dropped the flag on the Reserve path and left reservations without an FQDN. See [Compatibility](compatibility.md) for the full matrix.

### Operator-side reservation idempotency

From v0.12.1 the reconcilers trust `cr.Status.IP` as the source of truth on every tick. This protects against clusterbook listing drift (e.g. a reservation whose `cluster` field gets rewritten server-side): once the CR has successfully reserved an IP, repeated reconciles return the stored value instead of re-matching against the listing and potentially triggering duplicate `Reserve` calls. See [#67](https://github.com/stuttgart-things/clusterbook-operator/pull/67) for background.

## Uninstall

```bash
kubectl delete -k /tmp/cbk
```

CRDs stay by default. To also delete the schemas (and with them every remaining CR of those kinds):

```bash
kubectl delete crd \
  clusterbookclusters.clusterbook.stuttgart-things.com \
  clusterbookloadbalancers.clusterbook.stuttgart-things.com \
  clusterbookallocations.clusterbook.stuttgart-things.com \
  clusterbookproviderconfigs.clusterbook.stuttgart-things.com
```
