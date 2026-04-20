# LoadBalancer IPs via Cilium

`ClusterbookLoadBalancer` reserves an IP from clusterbook and materialises it as a `CiliumLoadBalancerIPPool`. Any Service matching the pool's `serviceSelector` receives that IP when it enters `LoadBalancer` mode.

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
    - Deletes the CiliumLoadBalancerIPPool (the Service loses its IP; Cilium will re-allocate from another matching pool if one exists)
    - Releases the clusterbook IP **only if** `releaseOnDelete: true`
    - Removes the finalizer, CR is garbage-collected

## Current limitations

- Only `ciliumPool` target mode is supported. Setting `loadBalancerIP` directly on an existing Service via `serviceRef` is a tracked follow-up.
- MetalLB and Gateway API integrations land later as additional target modes on the same CRD.
