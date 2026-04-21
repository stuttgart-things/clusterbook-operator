# LoadBalancer IPs

`ClusterbookLoadBalancer` reserves an IP from clusterbook and wires it to a workload through one of two target modes:

- **`ciliumPool`** — materialise the IP as a `CiliumLoadBalancerIPPool` so any Service matching the pool's `serviceSelector` receives it (Cilium LB IPAM)
- **`serviceRef`** — set `.spec.loadBalancerIP` directly on an existing Service (for setups without LB IPAM, or when you want tight control of which Service gets which IP)

Exactly one target mode must be set — the CRD rejects specs with both or neither.

## Prerequisites

- Cilium with LB IPAM enabled (`loadBalancer.mode: lb-ipam` or the default in recent Cilium versions)
- A `ClusterbookProviderConfig` pointing at your clusterbook API

## Minimal example

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookLoadBalancer
metadata:
  name: ingress-main
spec:
  networkKey: "10.31.101"
  name: ingress-main
  createDNS: true
  providerConfigRef:
    name: default
  ciliumPool:
    serviceSelector:
      matchLabels:
        app.kubernetes.io/name: ingress-nginx
  releaseOnDelete: true
```

## What happens

1. Reconciler calls `GetIPs` on clusterbook; if no existing reservation matches `spec.name`, it calls `ReserveIPs`.
2. Optional `createDNS: true` asks clusterbook to create a wildcard PowerDNS record pointing at the IP.
3. `CiliumLoadBalancerIPPool` is created (or updated) with:
   ```yaml
   apiVersion: cilium.io/v2alpha1
   kind: CiliumLoadBalancerIPPool
   metadata:
     name: ingress-main-pool             # <spec.name>-pool by default
     annotations:
       clusterbook.stuttgart-things.com/ip:   10.31.101.42
       clusterbook.stuttgart-things.com/fqdn: "*.ingress-main.your-zone"
       clusterbook.stuttgart-things.com/zone: your-zone
     ownerReferences:
       - apiVersion: clusterbook.stuttgart-things.com/v1alpha1
         kind: ClusterbookLoadBalancer
         name: ingress-main
         controller: true
   spec:
     blocks:
       - cidr: 10.31.101.42/32
     serviceSelector:
       matchLabels:
         app.kubernetes.io/name: ingress-nginx
   ```
4. Cilium assigns the IP to any matching `Service` of type `LoadBalancer`.
5. Status on the CR is populated — `ip`, `fqdn`, `zone`, `poolName`.

## End-to-end with ingress-nginx

```yaml
# Service labelled so the pool's selector matches it
apiVersion: v1
kind: Service
metadata:
  name: ingress-nginx-controller
  namespace: ingress-nginx
  labels:
    app.kubernetes.io/name: ingress-nginx
spec:
  type: LoadBalancer
  selector:
    app.kubernetes.io/name: ingress-nginx
    app.kubernetes.io/component: controller
  ports:
    - name: http
      port: 80
      targetPort: http
    - name: https
      port: 443
      targetPort: https
```

After apply:

```bash
kubectl -n ingress-nginx get svc ingress-nginx-controller
# NAME                       TYPE           EXTERNAL-IP    PORT(S)
# ingress-nginx-controller   LoadBalancer   10.31.101.42   80:…/TCP,443:…/TCP
```

## Verify

```bash
# CR status
kubectl get clusterbookloadbalancer ingress-main \
  -o jsonpath='{"IP: "}{.status.ip}{"\nFQDN: "}{.status.fqdn}{"\nPool: "}{.status.poolName}{"\n"}'

# The pool itself
kubectl get ciliumloadbalancerippool ingress-main-pool -o yaml

# The Service getting its IP from the pool
kubectl -n ingress-nginx get svc ingress-nginx-controller
```

## Delete semantics

- `kubectl delete clusterbookloadbalancer ingress-main` → finalizer runs:
    - `ciliumPool` mode: deletes the `CiliumLoadBalancerIPPool` (the Service loses its IP; Cilium will re-allocate from another matching pool if one exists)
    - `serviceRef` mode: restores the target Service's `.spec.loadBalancerIP` to whatever value it had before the operator patched it (captured in the CR annotation `clusterbook.stuttgart-things.com/previous-loadbalancer-ip`). The Service itself is never deleted.
    - Releases the clusterbook IP **only if** `releaseOnDelete: true`
    - Removes the finalizer, CR is garbage-collected

## Alternate target mode — `serviceRef`

When you already have a `LoadBalancer` Service and just want to pin its IP to a clusterbook-reserved one — without introducing a Cilium IP pool — use `serviceRef`:

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookLoadBalancer
metadata:
  name: ingress-main
spec:
  networkKey: "10.31.101"
  name: ingress-main
  createDNS: true
  providerConfigRef:
    name: default
  serviceRef:
    name: ingress-nginx-controller
    namespace: ingress-nginx
  releaseOnDelete: true
```

What happens:

1. Reserve the IP (+ optional DNS) same as before.
2. `GET` the target Service, capture its current `.spec.loadBalancerIP` in a CR annotation (`clusterbook.stuttgart-things.com/previous-loadbalancer-ip`) on the first reconcile only — so later user edits don't overwrite the recorded prior value.
3. Patch `.spec.loadBalancerIP` to the reserved IP.
4. Status: `ip`, `fqdn`, `zone`, `targetServiceRef`. `poolName` is empty in this mode.
5. On CR delete: restore `.spec.loadBalancerIP` from the annotation (empty string if there was nothing originally). Release the clusterbook IP if `releaseOnDelete: true`.

If the target Service doesn't exist at reconcile time the operator returns an error and retries — create the Service first, then the `ClusterbookLoadBalancer`.

## Current limitations

- MetalLB and Gateway API integrations land later as additional target modes on the same CRD.
