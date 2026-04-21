# clusterbook-operator

A Kubernetes operator for [clusterbook](https://github.com/stuttgart-things/clusterbook) — reserves IPs (and optional PowerDNS records) and materialises them as the Kubernetes objects that actually consume them.

## Three CRDs at a glance

| CRD | Use case | Output |
|---|---|---|
| **`ClusterbookCluster`** | Register a Kubernetes cluster in ArgoCD with a clusterbook-backed IP/FQDN | `Secret` with `argocd.argoproj.io/secret-type=cluster` — or **enrich mode** decorates an externally-managed Secret |
| **`ClusterbookLoadBalancer`** | Give a Cilium LoadBalancer Service a stable IP (+ optional DNS) | `CiliumLoadBalancerIPPool` — or `serviceRef` mode patches `.spec.loadBalancerIP` on an existing Service |
| **`ClusterbookAllocation`** | Pure "reserve and publish" — no Service attachment, no kubeconfig | `ConfigMap` with `ip`/`fqdn`/`zone` keys — and/or prefixed labels on an existing ArgoCD cluster Secret |

All three share `ClusterbookProviderConfig` for the clusterbook API endpoint + TLS options and annotate their output with `clusterbook.stuttgart-things.com/ip` / `/fqdn` / `/zone` for downstream discovery.

Full docs: [index](docs/index.md) · [install](docs/install.md) · [cluster registration](docs/usage.md) · [loadbalancer](docs/loadbalancer.md) · [allocation](docs/allocation.md) · [compatibility](docs/compatibility.md).

## Install

The released kustomize OCI bundle ships all CRDs, RBAC, and the Deployment. One command (plus a pull):

```bash
mkdir -p /tmp/cbk
flux pull artifact oci://ghcr.io/stuttgart-things/clusterbook-operator-kustomize:v0.12.1 --output /tmp/cbk
kubectl apply -k /tmp/cbk
kubectl -n clusterbook-system rollout status deploy/clusterbook-operator --timeout=120s
```

From v0.12.1 the bundle pins the exact image tag, so `kubectl apply -k` triggers a normal rollout between versions — no `set image` workaround needed.

Full install details and alternative tooling (`oras`, flux-kustomize-controller) in [`docs/install.md`](docs/install.md).

**Requires clusterbook server v1.25.1 or later** when using `createDNS: true`. See [`docs/compatibility.md`](docs/compatibility.md) for the version matrix.

## Consuming from an ApplicationSet

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: workloads
spec:
  generators:
    - clusters:
        selector:
          matchLabels:
            env: prod
  template: ...
```

See [`examples/clusterbookcluster.yaml`](examples/clusterbookcluster.yaml) for a full CR + ApplicationSet pairing, and [`examples/applicationset-cilium-lb-pool.yaml`](examples/applicationset-cilium-lb-pool.yaml) for the Cilium LB pool pattern driven by `ClusterbookAllocation` + the Cluster generator.

## Relation to provider-clusterbook

[`provider-clusterbook`](https://github.com/stuttgart-things/xplane-provider-clusterbook) is the Crossplane provider for the same clusterbook API. It is independent: same upstream API, different control plane.

- **Crossplane provider** — reserve IPs / create DNS records from Crossplane compositions. Useful inside Crossplane-driven cluster provisioning.
- **clusterbook-operator** (this repo) — turn clusterbook entries into `CiliumLoadBalancerIPPool` / ArgoCD cluster Secrets / ConfigMaps. Useful when Argo is the delivery control plane.

The REST client at `pkg/client` was copied (not forked) from `provider-clusterbook/internal/client`. Both projects evolve independently against the clusterbook API.

## Status

Shipping. Three CRDs in `v1alpha1`, kustomize OCI bundle published per release at `ghcr.io/stuttgart-things/clusterbook-operator-kustomize`. See [the releases page](https://github.com/stuttgart-things/clusterbook-operator/releases) for the latest version.
