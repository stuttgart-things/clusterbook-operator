# Usage

Two cluster-scoped CRDs. One `ClusterbookProviderConfig` per clusterbook API, one `ClusterbookCluster` per workload cluster you want Argo to manage.

## 1. ProviderConfig

Points the operator at your clusterbook API.

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookProviderConfig
metadata:
  name: default
spec:
  apiURL: https://clusterbook.example.com
  insecureSkipVerify: false
  # For a private CA:
  # customCASecretRef:
  #   name: clusterbook-ca
  #   namespace: clusterbook-system
  #   key: ca.crt
```

## 2. Kubeconfig Secret

The CR references a `Secret` that holds the kubeconfig for the workload cluster. The operator extracts server / CA / auth material from it.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: prod-cluster-a-kubeconfig
  namespace: argocd
type: Opaque
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    # ...full kubeconfig for prod-cluster-a...
```

Any namespace works; the CR's `kubeconfigSecretRef` points at it explicitly.

## 3. ClusterbookCluster

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: prod-cluster-a
spec:
  networkKey: "10.31.103"
  clusterName: prod-cluster-a
  createDNS: true
  useFQDNAsServer: true
  kubeconfigSecretRef:
    name: prod-cluster-a-kubeconfig
    namespace: argocd
    key: kubeconfig
  providerConfigRef:
    name: default
  argocdNamespace: argocd
  labels:
    env: prod
    region: eu-central-1
  releaseOnDelete: false
```

After the first reconcile:

- `status.ip` — the reserved IP
- `status.fqdn` — FQDN from clusterbook (if `createDNS: true`)
- `status.secretName` — `cluster-<clusterName>`
- A Secret `cluster-prod-cluster-a` in `argocd` namespace with label `argocd.argoproj.io/secret-type=cluster`

## 4. ApplicationSet wiring

No custom generator needed — the built-in **Cluster** generator consumes the secrets the operator produces.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: workloads
  namespace: argocd
spec:
  generators:
    - clusters:
        selector:
          matchLabels:
            env: prod          # same label you set on the ClusterbookCluster CR
  template:
    metadata:
      name: "workloads-{{name}}"
    spec:
      project: default
      source:
        repoURL: https://github.com/stuttgart-things/workloads
        path: base
        targetRevision: main
      destination:
        server: "{{server}}"    # the server field on the cluster secret
        namespace: default
      syncPolicy:
        automated: {}
```

Every `ClusterbookCluster` with matching labels gets an Application automatically.
