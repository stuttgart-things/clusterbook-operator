# clusterbook-operator

A Kubernetes operator for [clusterbook](https://github.com/stuttgart-things/clusterbook) — reserves IPs (and optional PowerDNS records) and turns them into the Kubernetes objects that actually consume them.

Two CRDs, two distinct use cases:

| CRD | Use case | Output |
|---|---|---|
| **`ClusterbookLoadBalancer`** | Give a Cilium LoadBalancer Service a stable IP (+ optional DNS) | `cilium.io/v2alpha1 CiliumLoadBalancerIPPool` |
| **`ClusterbookCluster`** | Register a Kubernetes cluster in ArgoCD with a clusterbook-backed IP/FQDN | `Secret` with `argocd.argoproj.io/secret-type=cluster` |

Both share `ClusterbookProviderConfig` for clusterbook API endpoint + TLS options, both participate in the same clusterbook reservation pool, both annotate their output with `clusterbook.stuttgart-things.com/ip` / `/fqdn` / `/zone` for downstream discovery.

## `ClusterbookLoadBalancer` — Cilium LB IPAM

```
ClusterbookLoadBalancer (CR)   -->   CiliumLoadBalancerIPPool
  networkKey                           name:    <spec.name>-pool
  name                                 blocks:  [{cidr: <ip>/32}]
  createDNS                            serviceSelector: <copied from spec>
  ciliumPool.serviceSelector           annotations:
  providerConfigRef                      clusterbook.stuttgart-things.com/ip
                                         clusterbook.stuttgart-things.com/fqdn
                                         clusterbook.stuttgart-things.com/zone
```

Reserve an IP from clusterbook → create a CiliumLoadBalancerIPPool pinned to that IP → Cilium assigns it to any Service matching `serviceSelector`. With `createDNS: true`, clusterbook also creates a wildcard PowerDNS record for Ingress / Gateway API frontends.

See [LoadBalancer](loadbalancer.md) for full usage.

## `ClusterbookCluster` — declarative cluster registration in ArgoCD

```
ClusterbookCluster (CR)         -->   Secret (argocd.argoproj.io/secret-type=cluster)
  networkKey                           name:   <clusterName>
  clusterName                          server: https://<ip|fqdn>:6443
  createDNS                            config: {...from kubeconfigSecretRef...}
  kubeconfigSecretRef                  labels: <propagated from CR>
  labels                               annotations:
  providerConfigRef                      clusterbook.stuttgart-things.com/ip
                                         clusterbook.stuttgart-things.com/fqdn
                                         clusterbook.stuttgart-things.com/zone
```

On every reconcile:

1. Resolve `providerConfigRef` — clusterbook API URL + TLS options.
2. Look up any existing reservation for `spec.clusterName` in clusterbook; reserve a new IP only if none exists.
3. `GetClusterInfo` to pick up FQDN and zone.
4. Detect DNS drift — if `spec.createDNS` no longer matches the clusterbook state, call `UpdateIP`.
5. Load the kubeconfig from the referenced Secret, extract `server`, CA, and auth material, build the ArgoCD `config` JSON.
6. Create or update `Secret cluster-<clusterName>` in the ArgoCD namespace.
7. On deletion (finalizer): delete the Secret, then `ReleaseIPs` (best-effort, gated by `releaseOnDelete`).

See [Usage](usage.md) for the full CR + ApplicationSet example.

## Why use this instead of `argocd cluster add`?

`argocd cluster add <context>` is the imperative path: you run a CLI against both kubeconfigs, it creates the cluster Secret, and that's it. You get a Secret with `name`, `server`, and `config`. Nothing more.

`ClusterbookCluster` is the declarative path, and it carries extra state that ApplicationSets can actually select on:

| | `argocd cluster add` | `ClusterbookCluster` |
|---|---|---|
| Declarative (fits GitOps) | no, imperative CLI | yes, a CR you commit |
| Server URL | whatever's in the kubeconfig | built from a **clusterbook-reserved IP** (or FQDN when `createDNS: true`) — stable, inventoried |
| IP / FQDN / zone visible on the Secret | no | yes, as `clusterbook.stuttgart-things.com/ip` / `/fqdn` / `/zone` annotations |
| ApplicationSet selector material | just the labels you pass at add-time | `spec.labels` + the annotations above |
| DNS record for cluster API | out of scope — you set it up elsewhere | optional via `createDNS: true` (PowerDNS record managed by clusterbook) |
| Lifecycle | orphaned if the CLI user forgets `argocd cluster rm` | finalizer-driven — delete the CR, Secret and (optionally) the clusterbook reservation go with it |
| Multi-cluster fan-out pattern | each new cluster = one CLI invocation | a GitOps repo with N `ClusterbookCluster` YAMLs |

So: same end result (a cluster Secret ArgoCD consumes), but sourced from a declarative CR, backed by a clusterbook reservation, and enriched with metadata that downstream ApplicationSets can filter on — all in one reconcile loop.

## Relation to `provider-clusterbook`

[`provider-clusterbook`](https://github.com/stuttgart-things/xplane-provider-clusterbook) is the Crossplane provider for the same clusterbook API. It is independent: same upstream API, different control plane.

| | Control plane | Typical use |
|---|---|---|
| **provider-clusterbook** | Crossplane | Reserve IPs / create DNS records from Crossplane Compositions, as part of a cluster-provisioning pipeline |
| **clusterbook-operator** (this repo) | plain controller-runtime | Turn clusterbook entries into CiliumLoadBalancerIPPools or ArgoCD cluster Secrets |

The REST client at `pkg/client` was copied (not forked) from `provider-clusterbook/internal/client`. Both projects evolve independently against the clusterbook API.

## Quick links

- [Install](install.md) — one `kubectl apply -k` on the OCI bundle
- [LoadBalancer](loadbalancer.md) — `ClusterbookLoadBalancer` CR and Cilium integration
- [Usage](usage.md) — `ClusterbookCluster` + ApplicationSet wiring
- [Configuration](configuration.md) — every `spec.*` field on both CRDs
- [Smoke Test](smoke-test.md) — end-to-end validation on a real cluster
- [Development](development.md) — `make test / generate / build`, envtest, release process
