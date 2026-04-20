# clusterbook-operator

A Kubernetes operator that turns [clusterbook](https://github.com/stuttgart-things/clusterbook) entries into [ArgoCD](https://argo-cd.readthedocs.io/) cluster Secrets so that ApplicationSets can fan out across fleets via the built-in **Cluster** generator — no custom generator plugin required.

## What it does

```
ClusterbookCluster (CR)         -->   Secret (argocd.argoproj.io/secret-type=cluster)
  networkKey                           name:   <clusterName>
  clusterName                          server: https://<ip|fqdn>:6443
  createDNS                            config: {...from kubeconfigSecretRef...}
  kubeconfigSecretRef                  labels: <propagated from CR>
  labels
  providerConfigRef
```

On every reconcile:

1. Resolve `providerConfigRef` — clusterbook API URL + TLS options.
2. `ReserveIPs` against clusterbook (idempotent — returns the existing IP when the cluster already has one).
3. `GetClusterInfo` to pick up FQDN and zone.
4. Detect DNS drift — if `spec.createDNS` no longer matches the clusterbook reservation, call `UpdateIP`.
5. Load the kubeconfig from the referenced Secret, extract `server`, CA, and auth material, build the ArgoCD `config` JSON.
6. Create or update `Secret cluster-<clusterName>` in the ArgoCD namespace with `argocd.argoproj.io/secret-type: cluster` plus the labels declared on the CR.
7. On deletion (finalizer): delete the Secret, then `ReleaseIPs` (best-effort, gated by `releaseOnDelete`).

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
| **clusterbook-operator** (this repo) | plain controller-runtime | Turn clusterbook entries into ArgoCD cluster Secrets so ApplicationSets pick them up |

The REST client at `pkg/client` was copied (not forked) from `provider-clusterbook/internal/client`. Both projects evolve independently against the clusterbook API.

## Quick links

- [Install](install.md) — one `kubectl apply -k` on the OCI bundle
- [Usage](usage.md) — `ClusterbookProviderConfig` + `ClusterbookCluster` CR examples, ApplicationSet wiring
- [Configuration](configuration.md) — every `spec.*` field
- [Smoke Test](smoke-test.md) — end-to-end validation on a real cluster
- [Development](development.md) — `make test / generate / build`, envtest, release process
