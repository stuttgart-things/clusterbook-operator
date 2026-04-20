# clusterbook-operator

A Kubernetes controller that turns [clusterbook](https://github.com/stuttgart-things/clusterbook)
entries into [ArgoCD](https://argo-cd.readthedocs.io/) cluster Secrets so that
ApplicationSets can fan out across fleets via the built-in **Cluster**
generator — no custom generator plugin required.

## What it does

```
ClusterbookCluster (CR)         -->   Secret (argocd.argoproj.io/secret-type=cluster)
  networkKey                           name:   <clusterName>
  clusterName                          server: https://<ip|fqdn>:6443
  createDNS                            config: {...from kubeconfigSecretRef...}
  kubeconfigSecretRef                  labels: <propagated from CR>
  labels
  providerConfigRef
```

## Reconcile loop

1. Resolve `providerConfigRef` — clusterbook API URL + TLS options.
2. `ReserveIPs` against clusterbook (idempotent — returns the existing IP
   when the cluster already has one).
3. `GetClusterInfo` to pick up FQDN and zone.
4. Load kubeconfig from the referenced Secret, extract `server`, CA, and
   auth material; build the ArgoCD `config` JSON.
5. Create or update `Secret cluster-<clusterName>` in the ArgoCD namespace
   with label `argocd.argoproj.io/secret-type: cluster` plus the labels
   declared on the CR.
6. On deletion (finalizer): delete the Secret, then `ReleaseIPs`
   (best-effort, gated by `releaseOnDelete`).

## Relation to provider-clusterbook

[`provider-clusterbook`](https://github.com/stuttgart-things/xplane-provider-clusterbook)
is the Crossplane provider for the same clusterbook API. It is independent:
same upstream API, different control plane.

- **Crossplane provider** — reserve IPs / create DNS records from
  Crossplane compositions. Useful inside Crossplane-driven cluster
  provisioning.
- **clusterbook-operator** (this repo) — turn clusterbook entries into
  ArgoCD cluster secrets so ApplicationSets pick them up. Useful when
  Argo is the delivery control plane.

The REST client at `pkg/client` was copied (not forked) from
`provider-clusterbook/internal/client`. Both projects evolve independently
against the clusterbook API.

## Consuming from an ApplicationSet

```yaml
apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: workloads
spec:
  generators:
    - clusters:
        selector:
          matchLabels:
            env: prod
  template: ...
```

See [`examples/clusterbookcluster.yaml`](examples/clusterbookcluster.yaml)
for a full CR + ApplicationSet pairing.

## Status

Sketch. Not yet wired up: deepcopy codegen, CRD manifests under
`config/crd`, container image, chart. Run `make generate` to produce
generated code and CRDs.
