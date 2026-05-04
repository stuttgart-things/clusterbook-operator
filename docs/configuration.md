# Configuration

## `ClusterbookProviderConfig` spec

| Field | Type | Default | Description |
|---|---|---|---|
| `apiURL` | string | **required** | Base URL of the clusterbook API (e.g. `https://clusterbook.example.com`) |
| `insecureSkipVerify` | bool | `false` | Skip TLS verification. Use only with self-signed certs in dev |
| `customCASecretRef` | `{name, namespace, key}` | — | PEM CA bundle to trust. `key` defaults to `ca.crt` |

## `ClusterbookCluster` spec

| Field | Type | Default | Description |
|---|---|---|---|
| `networkKey` | string | **required** | clusterbook network pool key (e.g. `10.31.103`) |
| `clusterName` | string | **required** | Cluster identifier in clusterbook; also used as the ArgoCD cluster name |
| `createDNS` | bool | `false` | Ask clusterbook to create a wildcard DNS record |
| `useFQDNAsServer` | bool | `false` | Build ArgoCD `server` from the FQDN instead of the IP. Requires `createDNS: true` |
| `serverPort` | int | `6443` | Port appended to the ArgoCD server URL |
| `kubeconfigSecretRef.name` | string | **required** | Name of the Secret holding the kubeconfig |
| `kubeconfigSecretRef.namespace` | string | **required** | Namespace of that Secret |
| `kubeconfigSecretRef.key` | string | `kubeconfig` | Data key inside the Secret |
| `providerConfigRef.name` | string | **required** | Name of a `ClusterbookProviderConfig` |
| `argocdNamespace` | string | `argocd` | Where the generated cluster Secret is written |
| `labels` | `map[string]string` | `{}` | Labels copied onto the cluster Secret — use in ApplicationSet selectors |
| `releaseOnDelete` | bool | `false` | Release the clusterbook IP when the CR is deleted |
| `clusterType` | string | — | Free-form discriminator written as the label `clusterbook.stuttgart-things.com/cluster-type` (e.g. `kind`, `vsphere`). Lets ApplicationSet selectors fan out type-specific platform bundles |
| `lbRange.count` | int | — | Reserve `1 + count` IPs in a single call; the extras land as annotations `…/lb-range-start` and `…/lb-range-stop`. Mutually exclusive with `start`/`stop` |
| `lbRange.start` | string | — | User-pinned LB range start (e.g. an IP from the docker bridge for kind). When set, the operator does NOT reserve from `networkKey`. Requires `stop` |
| `lbRange.stop` | string | — | User-pinned LB range stop. Requires `start` |

## `ClusterbookCluster` status

| Field | Description |
|---|---|
| `ip` | Reserved IP returned by clusterbook |
| `fqdn` | FQDN returned by `/api/v1/clusters/{name}` (empty without DNS) |
| `zone` | DNS zone returned by the same endpoint |
| `lbRangeStart` | First LB IP. Mirrors `spec.lbRange.start` when user-pinned; recorded from the reservation result when operator-allocated |
| `lbRangeStop` | Last LB IP — see `lbRangeStart` |
| `secretName` | Name of the ArgoCD cluster Secret (always `cluster-<clusterName>`) |
| `conditions[type=Ready]` | `True` after a successful reconcile |

## Cluster Secret labels and annotations

The operator writes (and on CR delete strips, in enrich mode) the following keys under the `clusterbook.stuttgart-things.com/` prefix:

| Key | Kind | Source |
|---|---|---|
| `cluster-type` | label | `spec.clusterType` (only when set) |
| `allocation-ip` | label | reserved IP — non-enrich (create) mode only |
| `allocation-zone` | label | DNS zone — non-enrich (create) mode, only when zone is known |
| `ip` | annotation | reserved IP |
| `fqdn` | annotation | FQDN from clusterbook (only when known) |
| `zone` | annotation | DNS zone (only when known) |
| `cluster-name` | annotation | `spec.clusterName` — always |
| `lb-range-start` | annotation | resolved LB range start (only when `lbRange` is set) |
| `lb-range-stop` | annotation | resolved LB range stop (only when `lbRange` is set) |

## `ClusterbookLoadBalancer` spec

| Field | Type | Default | Description |
|---|---|---|---|
| `networkKey` | string | **required** | clusterbook network pool key |
| `name` | string | **required** | Reservation key in clusterbook |
| `createDNS` | bool | `false` | Ask clusterbook to create a wildcard DNS record |
| `providerConfigRef.name` | string | **required** | Name of a `ClusterbookProviderConfig` |
| `ciliumPool.poolName` | string | `<spec.name>-pool` | Name of the generated `CiliumLoadBalancerIPPool` |
| `ciliumPool.serviceSelector` | `LabelSelector` | — | Copied verbatim onto the pool. Services matching this selector get an IP from the pool |
| `releaseOnDelete` | bool | `false` | Release the clusterbook IP when the CR is deleted |

Exactly one target mode must be set. Today `ciliumPool` is the only supported mode; `serviceRef` is a tracked follow-up.

## `ClusterbookLoadBalancer` status

| Field | Description |
|---|---|
| `ip` | Reserved IP written into the pool |
| `fqdn` | FQDN returned by clusterbook (empty without DNS) |
| `zone` | DNS zone returned by clusterbook |
| `poolName` | Name of the generated `CiliumLoadBalancerIPPool` |
| `conditions[type=Ready]` | `True` after a successful reconcile |

## KCL deploy profile

Controls the operator's own Deployment. See [`tests/kcl-deploy-profile.yaml`](https://github.com/stuttgart-things/clusterbook-operator/blob/main/tests/kcl-deploy-profile.yaml).

| Parameter | Default | Description |
|---|---|---|
| `config.name` | `clusterbook-operator` | Resource name prefix |
| `config.namespace` | `clusterbook-system` | Operator namespace |
| `config.image` | `ghcr.io/stuttgart-things/clusterbook-operator:latest` | Controller image |
| `config.imagePullPolicy` | `IfNotPresent` | Image pull policy |
| `config.replicas` | `1` | Pod replicas (use leader election if >1) |
| `config.cpuRequest` / `Limit` | `50m` / `500m` | CPU |
| `config.memoryRequest` / `Limit` | `64Mi` / `256Mi` | Memory |
| `config.metricsPort` | `8080` | Prometheus metrics |
| `config.healthPort` | `8081` | `/healthz` and `/readyz` |

Override per-deploy:

```bash
kcl run kcl/ \
  -D config.image=ghcr.io/stuttgart-things/clusterbook-operator:v0.6.0 \
  -D config.replicas=2
```
