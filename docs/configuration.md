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

## `ClusterbookCluster` status

| Field | Description |
|---|---|
| `ip` | Reserved IP returned by clusterbook |
| `fqdn` | FQDN returned by `/api/v1/clusters/{name}` (empty without DNS) |
| `zone` | DNS zone returned by the same endpoint |
| `secretName` | Name of the ArgoCD cluster Secret (always `cluster-<clusterName>`) |
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
