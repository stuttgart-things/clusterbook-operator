# Tutorial: Register a new cluster in ArgoCD with a clusterbook-reserved IP + DNS

End-to-end walkthrough: starting from a kubeconfig file on your laptop, get a new Kubernetes cluster registered in ArgoCD with a clusterbook-managed IP and PowerDNS record, and an ApplicationSet targeting it by label — in **one CR**.

## What you end up with

After the last step:

- An ArgoCD cluster Secret (`argocd.argoproj.io/secret-type=cluster`) built from your kubeconfig
- The cluster's API server URL pointing at a wildcard-DNS FQDN backed by a clusterbook reservation
- `clusterbook.stuttgart-things.com/ip` / `/fqdn` / `/zone` annotations on the Secret for ApplicationSet template consumption
- Your chosen labels (e.g. `env=prod`) directly on the Secret so the built-in ArgoCD **Cluster** generator can select it

## Prerequisites

- `clusterbook-operator` installed on the ArgoCD cluster, **v0.12.3 or later** (adds `preserveKubeconfigServer`; earlier versions forcibly rewrite `data.server` to the clusterbook endpoint) — see [Install](install.md).
- `clusterbook` server **v1.25.1 or later** if you want `createDNS: true` to actually create a DNS record — see [Compatibility](compatibility.md).
- A `ClusterbookProviderConfig` named `default` (or whatever name you'll reference) already applied, pointing at the clusterbook API.
- A kubeconfig file on your workstation for the **new cluster** (not the ArgoCD cluster). Context already selected; single-cluster kubeconfigs work as-is, multi-cluster kubeconfigs need a `kubectl config view --minify --flatten --context=<ctx>` first.
- Network reachability from the ArgoCD cluster's pods to the new cluster's API server address.

For the rest of this tutorial the ArgoCD cluster is referenced via `KUBECONFIG=~/.kube/platform-sthings`; substitute your own.

## Step 1 — Inspect the kubeconfig to pick the clusterbook network

Look at the `server:` line in your kubeconfig — the first three octets of the IP tell you which clusterbook network to reserve from.

```bash
grep server /home/sthings/.kube/ci-mgmt-t1
# server: https://10.31.104.107:6443
```

→ network key is `10.31.104`.

(If your kubeconfig already uses an FQDN, pick the network the target cluster lives on by other means — check the clusterbook dashboard or your network design doc.)

## Step 2 — Wrap the kubeconfig into a Secret

The operator reads the kubeconfig from a Secret in any namespace. Convention is to land it next to the ArgoCD cluster Secrets in `argocd`.

```bash
export KUBECONFIG=~/.kube/platform-sthings   # operator/ArgoCD cluster

kubectl -n argocd create secret generic ci-mgmt-t1-kubeconfig \
  --from-file=kubeconfig=/home/sthings/.kube/ci-mgmt-t1
```

The `--from-file=kubeconfig=...` form matters — the operator defaults to reading from a data key named `kubeconfig`. (If you use a different key, set `spec.kubeconfigSecretRef.key` on the CR.)

Verify:

```bash
kubectl -n argocd get secret ci-mgmt-t1-kubeconfig \
  -o jsonpath='{.data.kubeconfig}' | base64 -d | grep "server:"
# server: https://10.31.104.107:6443
```

## Step 3 — Apply the `ClusterbookCluster` CR

One CR does: IP reservation + DNS record + ArgoCD cluster Secret build, all in a single reconcile.

```yaml
# cr-ci-mgmt-t1.yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: ci-mgmt-t1
spec:
  networkKey: "10.31.104"
  clusterName: ci-mgmt-t1
  createDNS: true                       # clusterbook → PowerDNS record
  preserveKubeconfigServer: true        # keep data.server = kubeconfig's URL — see note below
  # useFQDNAsServer: true               # alternative: rewrite data.server to the clusterbook FQDN
  # serverSubdomain: api                # substitution label when useFQDNAsServer is set (default "api")
  kubeconfigSecretRef:
    name: ci-mgmt-t1-kubeconfig
    namespace: argocd
  providerConfigRef:
    name: default
  argocdNamespace: argocd
  labels:
    env: lab
    role: mgmt
    # auto-project: "true"              # example — match whatever your fleet's ApplicationSets select on
  releaseOnDelete: false                # keep the clusterbook reservation when the CR is deleted
```

### Match `spec.labels` to your fleet's ApplicationSet selectors

`spec.labels` land on the cluster Secret unchanged (not prefixed), so ArgoCD's built-in Cluster generator can select on them directly. Which labels you need depends on **whatever ApplicationSets already run in your fleet**. On a typical `stuttgart-things/argocd` install that's often:

- `auto-project: "true"` — picked up by the `cluster-projects` ApplicationSet, auto-provisions a matching `AppProject` per cluster
- `env`, `tier`, `allow-all` — read by workload ApplicationSets as selector or template input

If you omit the selector label your ApplicationSets expect, the cluster will register correctly but **no Applications will materialise for it** — the ApplicationSet's generator simply won't match the Secret. Check `kubectl -n argocd get applicationset -o yaml` for the exact `selector.matchLabels` in use, then mirror those into `spec.labels` here. (Labels can be edited after the fact — re-apply the CR with additions and the Secret picks them up on the next reconcile.)

### Which server-URL mode to pick

| Mode | When to use |
|---|---|
| **`preserveKubeconfigServer: true`** (recommended for most labs) | The target cluster's API lives at whatever address the kubeconfig reports (e.g. a node IP, or a pre-existing LB the kubeconfig already points at). The reservation + DNS are useful for IPAM/DNS bookkeeping and for templating into downstream workloads, but ArgoCD uses the kubeconfig URL to connect. |
| `useFQDNAsServer: true` | The target cluster has **kube-vip / Cilium LB IPAM / MetalLB** bound to the clusterbook-reserved IP as a stable API endpoint — ArgoCD should connect through that stable URL so it survives node replacement. Needs cluster-side plumbing. |
| neither set | Same idea as `useFQDNAsServer: true` but `data.server` uses the raw IP (`https://<ip>:6443`). Subject to TLS SAN mismatches unless the cert includes the IP. |

In all three modes, clusterbook IP/FQDN/zone still land on the Secret as labels (`clusterbook.stuttgart-things.com/allocation-ip`, `/allocation-zone`) and annotations (`/ip`, `/fqdn`, `/zone`) for ApplicationSet selection and templating.

Apply it:

```bash
kubectl apply -f cr-ci-mgmt-t1.yaml
```

### What the reconcile does on apply

1. **Reserves an IP** from the `spec.networkKey` pool via clusterbook. The operator lists first and only reserves when no entry for `spec.clusterName` is already found — safe to re-apply.
2. **Creates a PowerDNS record** (wildcard, e.g. `*.ci-mgmt-t1.sthings-vsphere.labul.sva.de` → reserved IP) because `createDNS: true`. Needs clusterbook ≥ v1.25.1 to actually propagate.
3. **Builds `argocd/cluster-<spec.clusterName>`** from the kubeconfig Secret:
   - `data.name` = `spec.clusterName`
   - `data.server` = `https://<host>:6443` — `<host>` is the FQDN when `useFQDNAsServer: true`, otherwise the raw IP
   - `data.config` — JSON derived from the kubeconfig (`bearerToken`, `tlsClientConfig.caData`, optional cert/key pair)
   - Labels: everything from `spec.labels` verbatim, plus `argocd.argoproj.io/secret-type: cluster`
   - Annotations: `clusterbook.stuttgart-things.com/ip`, `/fqdn`, `/zone`
   - Owner reference back to the CR so the Secret is garbage-collected on CR delete.
4. **Populates `.status`** on the CR: `ip`, `fqdn`, `zone`, `secretName`, plus a `Ready=True/Reconciled` condition. Everything you need for scripted verification is on the status.
5. **Installs a finalizer** (`clusterbook.stuttgart-things.com/finalizer`). On CR delete the finalizer deletes the Secret and, if `releaseOnDelete: true`, releases the clusterbook reservation. `releaseOnDelete: false` keeps the reservation around — preferred on first runs so a stray delete doesn't drop an IP you were about to reuse.

## Step 4 — Verify

The CR should go `Ready=True` within a few seconds:

```bash
kubectl get clusterbookcluster ci-mgmt-t1 \
  -o jsonpath='{"IP:  "}{.status.ip}{"\nFQDN: "}{.status.fqdn}{"\nZone: "}{.status.zone}{"\nReady: "}{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
# IP:   10.31.104.20
# FQDN: *.ci-mgmt-t1.sthings-vsphere.labul.sva.de
# Zone: sthings-vsphere.labul.sva.de
# Ready: True
```

The produced ArgoCD cluster Secret:

```bash
kubectl -n argocd get secret cluster-ci-mgmt-t1 -o yaml | head -40
```

You should see:

- `metadata.labels.argocd.argoproj.io/secret-type: cluster`
- `metadata.labels.env: lab` / `metadata.labels.role: mgmt` (from `spec.labels` — applied raw, not prefixed)
- `metadata.annotations.clusterbook.stuttgart-things.com/ip`, `/fqdn`, `/zone`
- `data.name`, `data.server` (FQDN-based), `data.config` (decoded: `{"bearerToken":..., "tlsClientConfig":...}` derived from the kubeconfig)

Confirm ArgoCD sees the cluster:

```bash
argocd cluster list
# SERVER                                            NAME           VERSION  STATUS      MESSAGE  PROJECT
# https://ci-mgmt-t1.labul.sva.de:6443              ci-mgmt-t1     1.35     Successful
# ...
```

(Cluster name in `argocd cluster list` comes from `data.name` — matches `spec.clusterName`.)

### Smoke test — deploy guestbook

Quickest way to prove the registration actually works, end-to-end — apply the canonical guestbook Application against the new cluster:

```yaml
# app-guestbook-ci-mgmt-t1.yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: guestbook-ci-mgmt-t1
  namespace: argocd
spec:
  project: ci-mgmt-t1           # or default — must allow this repo + destination
  source:
    repoURL: https://github.com/argoproj/argocd-example-apps.git
    targetRevision: HEAD
    path: guestbook
  destination:
    name: ci-mgmt-t1            # resolves to data.server via the cluster Secret (see note)
    namespace: guestbook
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [ CreateNamespace=true, ServerSideApply=true ]
```

```bash
kubectl apply -f app-guestbook-ci-mgmt-t1.yaml
kubectl -n argocd get application guestbook-ci-mgmt-t1 -w
```

Expected: `SYNC=Synced` within a few seconds, then `HEALTH=Healthy` once the Deployment rolls out. Verify on the target cluster:

```bash
KUBECONFIG=~/.kube/ci-mgmt-t1 kubectl -n guestbook get pods,svc
# NAME                              READY   STATUS    RESTARTS   AGE
# pod/guestbook-ui-...              1/1     Running   0          1m
# service/guestbook-ui              ClusterIP   …      80/TCP
```

Cleanup:
```bash
kubectl -n argocd delete application guestbook-ci-mgmt-t1
KUBECONFIG=~/.kube/ci-mgmt-t1 kubectl delete ns guestbook
```

**`destination.name` vs `destination.server`:** prefer `name`. ArgoCD resolves it against the cluster Secret's `data.name` at runtime, so Applications keep working even if `data.server` changes later — e.g. when you flip `preserveKubeconfigServer` → `useFQDNAsServer`, or when the target cluster's node IP changes. Hardcoding a URL forces a rewrite on every Application manifest. The one requirement is that the target AppProject's `destinations:` allowlist contains an entry with a matching `name` — the `cluster-projects` ApplicationSet-generated projects already include one.

## Step 5 — Consume the cluster from an ApplicationSet

The cluster Secret now satisfies the built-in Cluster generator. No plugin, no extra controller.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: lab-workloads
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - clusters:
        selector:
          matchLabels:
            env: lab                 # matches what we set in the CR
  template:
    metadata:
      name: 'workloads-{{ .name }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example/workloads
        targetRevision: main
        path: base
        helm:
          valuesObject:
            # The allocation facts are right here on the Secret — pull them
            # out via goTemplate.
            apiHost: '{{ index .metadata.annotations "clusterbook.stuttgart-things.com/fqdn" }}'
            apiIP:   '{{ index .metadata.annotations "clusterbook.stuttgart-things.com/ip" }}'
      destination:
        server: '{{ .server }}'
        namespace: default
      syncPolicy:
        automated: { prune: true, selfHeal: true }
```

Apply and watch:

```bash
kubectl apply -f lab-workloads-appset.yaml
kubectl -n argocd get applications | grep workloads-ci-mgmt-t1
```

## Cleanup

```bash
# 1) Remove the CR — finalizer deletes the cluster Secret. Reservation
#    stays because releaseOnDelete: false.
kubectl delete clusterbookcluster ci-mgmt-t1

# 2) Optional — also free the clusterbook IP by flipping the flag first,
#    or release it manually via the clusterbook API.
kubectl patch clusterbookcluster ci-mgmt-t1 --type merge \
  -p '{"spec":{"releaseOnDelete":true}}' && kubectl delete clusterbookcluster ci-mgmt-t1

# 3) Drop the kubeconfig Secret when you're done.
kubectl -n argocd delete secret ci-mgmt-t1-kubeconfig
```

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `status.fqdn` stays empty with `createDNS: true` | clusterbook server < v1.25.1 — upgrade and recreate the CR. See [Compatibility](compatibility.md). |
| Reconcile loop with `409 no available IPs in network` despite `status.ip` being set | operator < v0.12.1 — the older idempotency path re-tries Reserve when the listing's `cluster` field doesn't match. Upgrade. |
| `argocd cluster list` shows the cluster but syncs fail with TLS errors | The kubeconfig's CA data is what's used for verification; the server URL is overridden. If the new cluster's cert doesn't include the FQDN as a SAN, either add it, set `useFQDNAsServer: false` so the raw IP is used (the IP likely isn't a SAN either — you'd need `insecure: true` in the ArgoCD config, not recommended), or regenerate the cert with both the IP and the clusterbook FQDN in SANs. |
| `data.server` on the cluster Secret contains a literal `*.` (e.g. `https://*.ci-mgmt-t1...`) | Clusterbook returns the DNS record as a wildcard FQDN and the operator currently concatenates it as-is into `data.server`. Not a valid ArgoCD server URL — ArgoCD will DNS-fail on the `*.`. Workaround until [#71](https://github.com/stuttgart-things/clusterbook-operator/issues/71) lands: set `useFQDNAsServer: false` (server becomes `https://<ip>:6443` — subject to the TLS-SAN caveat above), or post-process the Secret with an admission webhook / kustomize patch that strips the `*.` and substitutes `api.` or the bare subdomain. |
| ArgoCD ApplicationSet doesn't pick up the cluster | Check `spec.labels` on the CR and the Secret's labels — the selector matches on the Secret, not the CR. Also make sure the ArgoCD namespace in `spec.argocdNamespace` matches where your ApplicationSet looks. |

## Next steps

- Multiple IPs on the same cluster (e.g. one for the API server, one for an ingress LB): use `ClusterbookAllocation` for the secondary IPs — see [Allocation](allocation.md).
- Let Cilium LB IPAM manage the reserved IP for a Service, not the cluster API: use `ClusterbookLoadBalancer` — see [LoadBalancer](loadbalancer.md).
- Pre-existing cluster Secrets managed by Crossplane or similar, where you only want the clusterbook metadata: use `ClusterbookCluster` in **enrich mode** (`existingSecretRef`) — see [Cluster registration](usage.md).
