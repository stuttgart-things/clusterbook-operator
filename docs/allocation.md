# Allocations — reserve IPs/DNS and publish facts for consumers

`ClusterbookAllocation` reserves an IP (optionally with a DNS record) and writes the reservation facts into one or more **sinks** so downstream consumers — most commonly ArgoCD ApplicationSets — can read them.

Unlike `ClusterbookCluster` (which builds an ArgoCD cluster Secret) or `ClusterbookLoadBalancer` (which wires the IP to a Cilium pool / a Service's `loadBalancerIP`), this CRD **does not attach the IP to any workload**. It only reserves and publishes.

## When to use it

- You run an ArgoCD ApplicationSet that needs a stable IP / FQDN to template into Applications.
- You need the allocation facts available somewhere generic — a ConfigMap is the most universal.
- The IP attachment (Cilium LB, Service, nginx config, DNS entry, etc.) is handled by another system or not needed.

## Minimal example

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookAllocation
metadata:
  name: app-frontend
spec:
  networkKey: "10.31.101"
  name: app-frontend           # reservation key in clusterbook
  createDNS: true
  providerConfigRef:
    name: default
  sinks:
    configMap:
      name: app-frontend-allocation
      namespace: argocd
  releaseOnDelete: false
```

After the first reconcile:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-frontend-allocation
  namespace: argocd
  ownerReferences:
    - apiVersion: clusterbook.stuttgart-things.com/v1alpha1
      kind: ClusterbookAllocation
      name: app-frontend
      controller: true
data:
  ip: "10.31.101.42"
  fqdn: "app-frontend.example.com"
  zone: "example.com"
  networkKey: "10.31.101"
  name: "app-frontend"
```

CR `.status` gets `ip`, `fqdn`, `zone`, `configMapRef`.

## Sinks

At least one sink must be set. Both can be set at the same time — they're independent.

### `configMap` — operator-owned ConfigMap (recommended default)

Plain string keys, easy to read from anywhere. The ConfigMap is owned by the CR, so deleting the CR garbage-collects it.

Consumer patterns:
- ApplicationSet **Plugin** generator — fetch and template the values.
- ApplicationSet **List** generator — maintain a list of allocations, one entry per ConfigMap.
- Mount as a Helm value file in a subsequent Application via `configMapKeyRef`.

### `clusterSecretLabels` — enrich an existing ArgoCD cluster Secret

Points at an ArgoCD cluster Secret managed elsewhere (Crossplane, `ClusterbookCluster`, etc.) and merges `clusterbook.stuttgart-things.com/`-prefixed labels and annotations onto it. **Does not touch `data`, does not take ownership.** Stripped cleanly on CR delete.

```yaml
spec:
  sinks:
    clusterSecretLabels:
      name: cluster-prod-a
      namespace: argocd
```

The Secret gets (for an allocation named `app-frontend`):
```yaml
metadata:
  labels:
    clusterbook.stuttgart-things.com/allocation-app-frontend:          "true"
    clusterbook.stuttgart-things.com/allocation-app-frontend-ip:       "10.31.101.42"
    clusterbook.stuttgart-things.com/allocation-app-frontend-zone:     "example.com"
  annotations:
    clusterbook.stuttgart-things.com/allocation-app-frontend-ip:       "10.31.101.42"
    clusterbook.stuttgart-things.com/allocation-app-frontend-fqdn:     "app-frontend.example.com"
    clusterbook.stuttgart-things.com/allocation-app-frontend-zone:     "example.com"
```

Every key is namespaced by the allocation's `spec.name`, so multiple allocations (including a `ClusterbookCluster` cluster registration on the same Secret) coexist without overwriting each other. The `allocation-<name>` presence label is meant for `matchLabels` in Cluster generators; the `-ip` / `-fqdn` / `-zone` variants are for `goTemplate` `valuesObject`.

FQDN stays annotation-only because clusterbook returns it as `*.<cluster>.<zone>` and `*` isn't a valid label-value character.

Consumer pattern: ArgoCD's built-in **Cluster** generator with `selector.matchLabels: {clusterbook.stuttgart-things.com/allocation-<name>: "true"}` — one ApplicationSet per allocation.

If the referenced Secret is missing, the reconciler surfaces a `ClusterSecretFound=False` condition and continues — it doesn't error-loop.

## ApplicationSet wiring — ConfigMap via Plugin generator

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: app-frontend
  namespace: argocd
spec:
  generators:
    - plugin:
        configMapRef:
          name: allocation-plugin   # a plugin that returns the ConfigMap's data
        input:
          parameters:
            name: app-frontend-allocation
            namespace: argocd
  template:
    metadata:
      name: "app-frontend"
    spec:
      project: default
      source:
        repoURL: https://github.com/example/frontend
        path: deploy
        helm:
          values: |
            ingress:
              host: "{{ .fqdn }}"
            service:
              loadBalancerIP: "{{ .ip }}"
      destination:
        server: https://kubernetes.default.svc
        namespace: app-frontend
```

(Any ApplicationSet generator that can read a ConfigMap works — the exact wiring is up to your setup.)

## Delete semantics

- `configMap` sink: owned — deleted via `ownerReference` when the CR is removed (and explicitly on finalize as belt-and-braces).
- `clusterSecretLabels` sink: only prefix-scoped labels/annotations are stripped. The Secret stays.
- `releaseOnDelete: true` — releases the clusterbook IP. Default `false` so your first run doesn't accidentally drop the reservation while debugging.

## Verify

```bash
# CR status
kubectl get clusterbookallocation app-frontend \
  -o jsonpath='{"IP: "}{.status.ip}{"\nFQDN: "}{.status.fqdn}{"\nConfigMap: "}{.status.configMapRef.namespace}/{.status.configMapRef.name}{"\n"}'

# The owned ConfigMap
kubectl -n argocd get configmap app-frontend-allocation -o yaml
```
