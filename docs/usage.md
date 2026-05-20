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

## 5. Enrich mode — for externally-managed ArgoCD cluster Secrets

If your ArgoCD cluster Secrets are already managed by something else (manual, Crossplane, another operator), point `ClusterbookCluster` at them with `existingSecretRef` instead of `kubeconfigSecretRef`. The operator still reserves an IP and (optionally) DNS, but only merges metadata under `clusterbook.stuttgart-things.com/` — it never touches the Secret's `data` fields and never takes ownership.

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: externally-managed
spec:
  networkKey: "10.31.103"
  clusterName: externally-managed
  createDNS: true
  existingSecretRef:
    name: cluster-externally-managed
    namespace: argocd
  providerConfigRef:
    name: default
  labels:
    env: smoke-test
    region: eu-central-1
  releaseOnDelete: false
```

Exactly one of `kubeconfigSecretRef` or `existingSecretRef` must be set — the CRD rejects specs with both or neither.

## 6. Registration-only mode — for externally-built clusters (`skipReservation`)

If the cluster you want to register was provisioned by *something else* (a Crossplane Composition, a Terraform module, a manual `helm install` of vcluster, the operator's own `Vcluster` CRD which uses this same path internally) and already has a usable kubeconfig Secret, you don't need clusterbook reservation at all — just the kubeconfig-to-ArgoCD-Secret transformation plus the labels/annotations machinery. Set:

- **`spec.skipReservation: true`** — no IP allocation, no DNS creation, no `release on delete`. The operator never contacts the clusterbook server.
- **`spec.preserveKubeconfigServer: true`** — `data.server` on the rendered ArgoCD Secret is taken from the kubeconfig's current-context cluster verbatim (no clusterbook IP to rewrite it with).
- **`spec.clusterType`** — free-form discriminator; for vclusters use `vcluster` so the resulting Secret carries `clusterbook.stuttgart-things.com/cluster-type=vcluster` for ApplicationSet selection.

`spec.networkKey` and `spec.providerConfigRef` are not consulted (the CRD validation explicitly allows omitting them when `skipReservation: true`). No `ClusterbookProviderConfig` is required for this path.

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: my-external-vcluster
spec:
  clusterName: my-external-vcluster
  clusterType: vcluster
  skipReservation: true
  preserveKubeconfigServer: true
  kubeconfigSecretRef:
    name: my-external-vcluster-kubeconfig
    namespace: argocd
    key: kubeconfig
  labels:
    env: dev
```

After the first reconcile you get a Secret `cluster-my-external-vcluster` in the ArgoCD namespace with `argocd.argoproj.io/secret-type=cluster`, the `clusterbook.stuttgart-things.com/cluster-type=vcluster` label, all your custom labels propagated, and `data.server` set to whatever the kubeconfig says.

**Source URL caveat** — the kubeconfig in the referenced Secret must have a `server:` field that ArgoCD can actually reach. The loft-sh upstream `vc-<name>` Secret hardcodes `https://localhost:8443`, which only works behind a port-forward; for ArgoCD you'd want to rewrite it first (or have your provisioning tool emit a Secret whose kubeconfig already points at the externally-reachable URL).

This is the same code path the operator's own `Vcluster` reconciler uses internally — it provisions the vcluster, rewrites the kubeconfig's `server:` URL, and emits a child `ClusterbookCluster` with exactly the flags above. See [Vcluster](vcluster.md) for the bundled provisioning + registration variant, or [`examples/clusterbookcluster-external-vcluster.yaml`](https://github.com/stuttgart-things/clusterbook-operator/blob/main/examples/clusterbookcluster-external-vcluster.yaml) for the standalone "kubeconfig already exists" pattern.

## 7. Multi-cluster-type fan-out (`clusterType` + `lbRange`)

When the same Argo control plane registers heterogeneous clusters (kind for dev, vSphere/Talos for prod) and you want a different platform bundle per type, two optional fields on `ClusterbookCluster` carry the routing:

- **`spec.clusterType`** — free-form string written as the label `clusterbook.stuttgart-things.com/cluster-type`. ApplicationSets select on it via `matchLabels: { clusterbook.stuttgart-things.com/cluster-type: kind }`.
- **`spec.lbRange`** — contiguous IP range to attach to the Secret as the annotations `clusterbook.stuttgart-things.com/lb-range-start` and `…/lb-range-stop`. Two mutually exclusive modes:
  - **User-pinned (`start` + `stop`)** — typical for kind: LoadBalancer IPs come from the docker bridge network, not the clusterbook pool. The operator does not reserve them, only writes them through.
  - **Operator-allocated (`count`)** — typical for vSphere/Talos: the operator reserves `1 + count` IPs from `networkKey` in a single call, exposes the first as `status.ip`, and records the range as `status.lbRangeStart` / `status.lbRangeStop` so subsequent reconciles don't re-reserve.

```yaml
# kind — user-pinned docker-bridge range
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: kind-dev-a
spec:
  networkKey: "10.31.103"
  clusterName: kind-dev-a
  clusterType: kind
  preserveKubeconfigServer: true
  kubeconfigSecretRef:
    name: kind-dev-a-kubeconfig
    namespace: argocd
    key: kubeconfig
  providerConfigRef:
    name: default
  lbRange:
    start: 172.18.255.200
    stop:  172.18.255.250
---
# vSphere — operator carves a 4-IP LB block from the same pool
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: vsphere-prod-a
spec:
  networkKey: "10.31.103"
  clusterName: vsphere-prod-a
  clusterType: vsphere
  createDNS: true
  useFQDNAsServer: true
  kubeconfigSecretRef:
    name: vsphere-prod-a-kubeconfig
    namespace: argocd
    key: kubeconfig
  providerConfigRef:
    name: default
  lbRange:
    count: 4
```

**Consuming the range from an ApplicationSet** — pair `clusterType` selection with the range annotations to template a `CiliumLoadBalancerIPPool`:

```yaml
generators:
  - clusters:
      selector:
        matchLabels:
          clusterbook.stuttgart-things.com/cluster-type: kind
template:
  spec:
    source:
      helm:
        valuesObject:
          blocks:
            - start: '{{ index .metadata.annotations "clusterbook.stuttgart-things.com/lb-range-start" }}'
              stop:  '{{ index .metadata.annotations "clusterbook.stuttgart-things.com/lb-range-stop" }}'
```

In addition to the cluster-type/lb-range metadata, every reconcile also writes the annotation `clusterbook.stuttgart-things.com/cluster-name` (= `spec.clusterName`) so ApplicationSet templates can rely on it instead of the generator's `{{ .name }}`. All keys remain under the `clusterbook.stuttgart-things.com/` prefix and are stripped on CR delete in enrich mode.

Full side-by-side example: [`examples/clusterbookcluster-kind.yaml`](https://github.com/stuttgart-things/clusterbook-operator/blob/main/examples/clusterbookcluster-kind.yaml).

**What enrich mode writes** to the referenced Secret:

```yaml
metadata:
  labels:
    argocd.argoproj.io/secret-type: cluster                  # untouched
    some-team-label: foo                                     # untouched
    clusterbook.stuttgart-things.com/env: smoke-test         # from spec.labels.env
    clusterbook.stuttgart-things.com/region: eu-central-1    # from spec.labels.region
  annotations:
    clusterbook.stuttgart-things.com/ip: 10.31.103.42
    clusterbook.stuttgart-things.com/fqdn: externally-managed.example.com
    clusterbook.stuttgart-things.com/zone: example.com
```

Differences from the default (create) mode:

- `data.name`, `data.server`, `data.config` are **never** written — the Secret's auth material is owned elsewhere.
- No owner reference is set — deleting the CR does not delete the Secret.
- `spec.labels` land under the `clusterbook.stuttgart-things.com/` prefix so ApplicationSet selectors must match on the prefixed form.
- `useFQDNAsServer` is ignored (the `server` field is whatever the existing Secret has).
- On CR delete the operator removes only the labels and annotations it added under its prefix; everything else stays.
- If the referenced Secret is missing the reconciler surfaces `Ready=False` / `ExistingSecretNotFound` rather than error-looping.
