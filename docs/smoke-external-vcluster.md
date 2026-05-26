# Smoke: registration-only path for an externally-built vcluster

Validation of the [§6 "Registration-only mode"](usage.md#6-registration-only-mode--for-externally-built-clusters-skipreservation) pattern from `usage.md`. Sibling to [`smoke-test.md`](smoke-test.md), which covers the *reservation* path; this one covers the *no-reservation* path that `skipReservation: true + preserveKubeconfigServer: true` enables.

The test cluster was named **iceman** (sibling to maverick — the [`Vcluster` CR variant-1 reference](vcluster.md)). The reference run was carried out on platform-sthings on 2026-05-26 against clusterbook-operator v0.19.0; that run is the basis for the verified results in the [Results](#results) table below.

## What this smoke proves

When the operator handles a `ClusterbookCluster` with the registration-only flag combo, the rendered `argocd/cluster-<name>` Secret has the same shape as the one a `Vcluster` CR emits for its child — but produced from a kubeconfig the operator did **not** provision. End to end:

- `spec.skipReservation: true` short-circuits every clusterbook-server call (`GetIPs` / `ReserveIPs` / `GetClusterInfo` on reconcile, `ReleaseIPs` on delete)
- `spec.preserveKubeconfigServer: true` makes `data.server` on the rendered Secret a verbatim copy of `current-context.cluster.server` from the source kubeconfig — no rewriting in the operator
- `spec.clusterType: vcluster` surfaces as the `clusterbook.stuttgart-things.com/cluster-type=vcluster` label, the same selector key `ApplicationSet`s use against operator-provisioned vclusters

Because nothing in this path touches the clusterbook server, no `ClusterbookProviderConfig` is needed.

## Prerequisites

- clusterbook-operator ≥ v0.19.0 running (the `SkipReservation` field shipped in [#92](https://github.com/stuttgart-things/clusterbook-operator/pull/92))
- ArgoCD installed in `argocd` ns (or wherever `argocdNamespace` points)
- A vcluster (or any cluster) provisioned *outside* the operator
- A kubeconfig Secret in `argocd` ns whose `current-context.cluster.server` is an **ArgoCD-reachable URL** — NOT `https://localhost:8443` from the loft-sh upstream `vc-<name>` Secret. See [The kubeconfig-rewrite caveat](#the-kubeconfig-rewrite-caveat) below.

## Reference artifact set (iceman)

The four files used for the reference run live in this repo's gitignored scratch area:

```
.task/iceman/
├── 01-cilium-lb-pool.yaml                  # single-IP Cilium pool (10.31.102.15) scoped via serviceSelector
├── 02-iceman-vcluster-application.yaml     # ArgoCD Application → loft-sh/vcluster 0.29.1 direct
├── 03-clusterbookcluster-iceman.yaml       # the smoke target: ClusterbookCluster with the flag combo
└── 04-extract-kubeconfig.sh                # reads vc-vcluster Secret, sed-rewrites server URL, writes argocd/iceman-kubeconfig
```

Two non-obvious choices in that set:

1. **Direct loft-sh chart, not the `cicd/vcluster/install` wrapper** in `stuttgart-things/argocd`. The wrapper's `valuesObject` puts `persistence`, `backingStore.resources`, `backingStore.highAvailability` at paths vcluster 0.29.x rejects (moved under `controlPlane.statefulSet.*`). Tracked at [stuttgart-things/argocd#199](https://github.com/stuttgart-things/argocd/issues/199).
2. **`controlPlane.proxy.extraSANs: [<lb-ip>]`** in the chart values, so the LB IP is in the API cert SANs and the rewritten kubeconfig passes TLS verification without `insecure-skip-tls-verify`.

## Apply order

```bash
# 1. Pool (must own the LB IP before the LB service comes up)
kubectl apply -f .task/iceman/01-cilium-lb-pool.yaml

# 2. ArgoCD Application — provisions iceman-vcluster ns + vcluster, LB at the pinned IP
kubectl apply -f .task/iceman/02-iceman-vcluster-application.yaml
kubectl -n argocd wait --for=jsonpath='{.status.health.status}'=Healthy \
    application/iceman-vcluster --timeout=5m

# 3. Rewrite kubeconfig + create the source Secret
.task/iceman/04-extract-kubeconfig.sh

# 4. The smoke target
kubectl apply -f .task/iceman/03-clusterbookcluster-iceman.yaml
```

If you applied #4 before #3, nudge a reconcile with an annotation:

```bash
kubectl annotate clusterbookcluster iceman \
    reconcile.clusterbook.stuttgart-things.com/at=$(date +%s) --overwrite
```

Otherwise the operator will retry on its own with exponential backoff (up to ~10 min between attempts).

## Verify checklist

| Check | How to verify |
|---|---|
| `Ready=True` / `Reason=Reconciled` | `kubectl get clusterbookcluster iceman -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'` |
| `status.secretName=cluster-iceman`, `status.ip` empty | same JSONPath, look at the fields |
| No clusterbook RPCs | Operator log has zero `GetIPs`/`ReserveIPs`/`GetClusterInfo` lines for this CR |
| Secret labels | `argocd.argoproj.io/secret-type=cluster`, `clusterbook.stuttgart-things.com/cluster-type=vcluster`, plus every label from `spec.labels` |
| Secret annotations | Only `clusterbook.stuttgart-things.com/cluster-name=<name>` — **no** `ip` / `fqdn` / `zone` |
| `data.server` verbatim from kubeconfig | `kubectl -n argocd get secret cluster-<name> -o jsonpath='{.data.server}' \| base64 -d` matches `current-context.cluster.server` of the source kubeconfig |
| `data.name == <clusterName>`, `data.config` populated | base64-decode and inspect |
| ArgoCD destination | Secret appears in `kubectl -n argocd get secrets -l argocd.argoproj.io/secret-type=cluster` |
| Delete short-circuits release | `kubectl delete clusterbookcluster <name>` removes `cluster-<name>` Secret with **zero** `ReleaseIPs` log entries (finalize sees `status.ip == ""` and skips the call) |

## Results

Reference run, 2026-05-26 (operator v0.19.0 on platform-sthings, cluster iceman):

| Check | Result |
|---|---|
| Ready=True / Reconciled | ✅ |
| secretName=cluster-iceman, status.ip empty | ✅ |
| Zero clusterbook RPCs across 30 min log window | ✅ |
| Labels: secret-type=cluster, cluster-type=vcluster, env=smoke (propagated) | ✅ |
| No ip/fqdn/zone annotations on the Secret | ✅ only `cluster-name=iceman` |
| `data.server` verbatim | ✅ `https://10.31.102.15:443` (matches kubeconfig's current-context) |
| `data.name` / `data.config` | ✅ |
| ArgoCD destination registered | ✅ listed alongside `cluster-maverick` |
| Delete: Secret removed, zero `ReleaseIPs` calls | ✅ |

The rendered `cluster-iceman` Secret was structurally identical to `cluster-maverick` (variant-1 `Vcluster` CR output), with `cluster-type=vcluster` and the same set of `data` keys — the premise of [#98](https://github.com/stuttgart-things/clusterbook-operator/issues/98).

## The kubeconfig-rewrite caveat

The `Vcluster` CR's reconciler does the loft-sh `server: https://localhost:8443` → reachable-URL rewrite internally. The `ClusterbookCluster` reconciler under `preserveKubeconfigServer: true` does **not** — it copies `current-context.cluster.server` verbatim. See `controller/reconciler.go`:

```go
if cr.Spec.PreserveKubeconfigServer && kubeconfigServer != "" {
    return kubeconfigServer
}
```

So on the registration-only path, whatever produced the kubeconfig Secret (Crossplane Composition, Terraform, a shell script, an operator in another product) is responsible for writing a `server:` URL ArgoCD can reach. The reference run used `04-extract-kubeconfig.sh` for that step. The same rewrite can sit inside a Crossplane Composition — `stuttgart-things/crossplane` already ships an `XVcluster` Composition (`configurations/k8s/vcluster`) whose KCL module at `oci://ghcr.io/stuttgart-things/xplane-vcluster` handles vcluster provisioning + kubeconfig rewrite + destination Secret + optional Vault push, end-to-end.

## Side notes / out of scope

- **HTTPRoute sync into the vcluster** is not a thing in upstream vcluster (verified against chart versions through 0.34.x). For workloads inside an iceman-style vcluster to be reachable via the host's Cilium Gateway, write HTTPRoutes on the host pointing at the synced services (`<svc>-x-<ns>-x-vcluster` in the vcluster's host namespace). Self-service HTTPRoute editing from inside the vcluster requires running a full Gateway controller (Envoy Gateway / Contour) inside the vcluster; no built-in sync exists.
- **Wrapper chart bug** in `stuttgart-things/argocd` at `cicd/vcluster/install` — see [stuttgart-things/argocd#199](https://github.com/stuttgart-things/argocd/issues/199). Bypassed in step 2 by going direct to the upstream chart.

## Cleanup (reverse order)

```bash
kubectl delete clusterbookcluster iceman
kubectl -n argocd delete secret iceman-kubeconfig
kubectl -n argocd delete application iceman-vcluster
# ArgoCD won't prune the namespace or the StatefulSet PVC:
kubectl -n iceman-vcluster delete pvc data-vcluster-0
kubectl delete ns iceman-vcluster
kubectl delete ciliumloadbalancerippool iceman-lb-pool
```
