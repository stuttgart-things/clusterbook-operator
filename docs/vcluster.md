# Vcluster

`Vcluster` is the only CRD in this operator that *composes* the others. Where `ClusterbookCluster` registers an *existing* Kubernetes cluster in ArgoCD, `Vcluster` provisions one (a loft-sh vcluster, via the upstream Helm chart) and then emits a child `ClusterbookCluster` so the existing reconciler closes the registration loop. End-to-end: one CR → an ArgoCD-managed cluster Secret.

```
Vcluster (CR, cluster-scoped)
  │
  ├─[1]─► clusterbook reservation (optional, only when spec.networkKey set)
  ├─[2]─► resolve target host cluster (in-cluster, or via spec.targetClusterRef)
  ├─[3]─► upsert  Application "vcluster-<name>"  in argocd ns
  │         source: loft-sh/vcluster chart, releaseName=<clusterName>
  │         helm.valuesObject: <user chartValues> + operator overlay
  ├─[4]─► poll target for  Secret  vc-<name>  (vcluster's own kubeconfig)
  ├─[5]─► rewrite kubeconfig server URL → external Secret  vc-<name>-external
  ├─[6]─► upsert child  ClusterbookCluster "<name>"  (skipReservation=true)
  │         └─> existing reconciler materialises  Secret  cluster-<name>
  └─[7]─► status conditions: VclusterReady, KubeconfigReady, ArgoCDRegistered
```

## Three variants

[`examples/vcluster.yaml`](https://github.com/stuttgart-things/clusterbook-operator/blob/main/examples/vcluster.yaml) ships all three.

### 1. In-cluster minimal

No `networkKey`, no `targetClusterRef`. The vcluster runs on the management cluster itself, the rewritten kubeconfig points at `https://<clusterName>.<namespace>` (which the default vcluster cert SANs cover), and clusterbook is never contacted. Disposable PR-preview shape.

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: Vcluster
metadata:
  name: pr-1234
spec:
  clusterName: pr-1234
  targetNamespace: vcluster-pr-1234
  chartValues:
    controlPlane:
      statefulSet:
        persistence:
          volumeClaim:
            enabled: true
            size: 5Gi
            storageClass: openebs-hostpath   # see "Storage class" below
```

### 2. Cross-cluster with reservation

`targetClusterRef` points at an existing ArgoCD cluster Secret on the management cluster (the *host* cluster the vcluster will run on). `networkKey` triggers a clusterbook reservation; the operator injects the resulting FQDN as `controlPlane.proxy.extraSANs` and the IP as `controlPlane.service.spec.loadBalancerIP`, then rewrites the kubeconfig's server URL to `https://<fqdn>:<port>` — externally reachable and SAN-validated.

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: Vcluster
metadata:
  name: dev-sandbox
spec:
  clusterName: dev-sandbox
  targetClusterRef:
    name: dev-host-cluster
    namespace: argocd
  targetNamespace: vcluster-dev-sandbox
  chartVersion: "0.33.1"
  networkKey: "10.31.101"
  serverPort: 6443
  providerConfigRef:
    name: default
  argoCD:
    labels:
      env: dev
      cicd-platform: "true"
  releaseOnDelete: true
```

`spec.argoCD.labels` and `.annotations` propagate onto the emitted child `ClusterbookCluster` — they end up on the rendered `cluster-<name>` Secret and are how ApplicationSets fan workloads onto the new vcluster.

### 3. Provision-only

`argoCD.register: false` spins up the vcluster but does NOT emit a child `ClusterbookCluster`. Useful for integration test rigs that talk to the vcluster directly via `status.kubeconfigSecretRef`, or when smoke-testing chart upgrades without ArgoCD cluster-Secret churn.

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: Vcluster
metadata:
  name: integration-rig
spec:
  clusterName: integration-rig
  targetNamespace: vcluster-integration-rig
  chartValues:
    sync:
      toHost:
        ingresses:
          enabled: true
  argoCD:
    register: false
```

## Status conditions

| Condition | Meaning |
|---|---|
| `VclusterReady` | The upstream `vc-<name>` Secret has appeared on the target cluster (i.e. the Helm release finished bootstrapping). False with reason `WaitingForVclusterSecret` while the chart installs. |
| `KubeconfigReady` | The rewritten external Secret `vc-<name>-external` has been written on the management cluster. |
| `ArgoCDRegistered` | The child `ClusterbookCluster` has been emitted (absent entirely when `argoCD.register: false`). |

## Gotchas

### Storage class

The loft-sh chart's StatefulSet PVC has no default `storageClass` — it works only on clusters with a default StorageClass. On clusters without one (no SC has `storageclass.kubernetes.io/is-default-class: "true"`) the PVC sits Pending forever and the chart never finishes installing, so step 4 never completes. Set it explicitly:

```yaml
spec:
  chartValues:
    controlPlane:
      statefulSet:
        persistence:
          volumeClaim:
            enabled: true
            size: 5Gi
            storageClass: openebs-hostpath
```

Pick the right value for your environment (`openebs-hostpath`, `standard`, `gp3`, etc.) or mark one of your existing StorageClasses as default cluster-wide and omit the block.

### TLS SANs

vcluster's API cert covers `<vclusterName>` and `<vclusterName>.<namespace>` but **not** `<vclusterName>.<namespace>.svc`. The operator's rewritten server URL deliberately uses the two-component DNS form for the no-reservation path. For the reservation path the operator injects the reserved FQDN into `controlPlane.proxy.extraSANs` so the cert covers it.

### Pinned chart version

`spec.chartVersion` defaults to a constant in the operator (the version validated during the design-phase spike). Bump it for newer features but be aware: the operator's overlay assumes the 0.20+ chart key layout (`controlPlane.service.spec.*`, `controlPlane.proxy.extraSANs`). Pre-0.20 charts use a flatter shape — you'd need to override via `chartValues`.

### Helm releaseName is pinned to `clusterName`

The operator sets `source.helm.releaseName: <spec.clusterName>` so the upstream Secret name is predictable as `vc-<clusterName>`. Don't try to override it via `chartValues` — the polling step keys off this convention.

### Don't switch destination ref format mid-life

For cross-cluster targeting the operator pins `destination.name` (resolved from the target ArgoCD cluster Secret's `data.name`). Switching a live Application from `destination.name` to `destination.server` (or vice versa) causes ArgoCD to re-render the Application's hash-derived inner names, which can cascade-reinstall any workload Apps deployed *onto* the vcluster. Pick one and stay with it for the lifetime of a given vcluster.

## Reuse map

| Need | Where it lives |
|---|---|
| Reserve IP / FQDN | own copy (loadbalancer-shaped, single IP) in the Vcluster reconciler |
| ArgoCD cluster Secret materialisation | reused verbatim — the emitted child `ClusterbookCluster` runs through the existing reconciler |
| `ProviderConfig` + TLS loading | `controller/shared.go` helpers (`loadProviderConfig`, `newClusterbookClient`) |
| Kubeconfig parsing / rewriting | `k8s.io/client-go/tools/clientcmd` (load → mutate `cluster.Server` → write) |
| ArgoCD Application upsert | `*unstructured.Unstructured` — no `argoproj.io` client deps |
