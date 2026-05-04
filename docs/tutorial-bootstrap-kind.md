# Tutorial: Bootstrap a kind cluster onto an ArgoCD fleet

End-to-end walkthrough for taking a freshly-created kind cluster (nothing deployed) and wiring it into an ArgoCD fleet so that the [`platforms/kind/`](https://github.com/stuttgart-things/argocd/tree/main/platforms/kind) bundle fans Cilium + cert-manager + Gateway out onto it automatically.

## What you end up with

- A kind cluster running with **Cilium as the CNI** (installed by Argo, not by kind), **kube-proxy replaced by Cilium**, and a **`CiliumLoadBalancerIPPool`** carved out of the docker bridge.
- An ArgoCD cluster `Secret` on the management cluster, enriched by [clusterbook-operator](https://github.com/stuttgart-things/clusterbook-operator) with `cluster-type=kind` + `lb-range-{start,stop}`.
- Six `ApplicationSet`-generated Applications fanning the kind platform bundle onto the new cluster.

## Prerequisites

- A **management cluster** that already runs ArgoCD and `clusterbook-operator` **v0.15.0 or later** ([Install](install.md)). v0.15.0 is the version that introduces `clusterType` and `lbRange`.
- A **clusterbook server** the operator's `ClusterbookProviderConfig` points at, **v1.25.1 or later** if you want DNS records.
- The [`config/cluster-project`](https://github.com/stuttgart-things/argocd/tree/main/config/cluster-project) bundle deployed on the management cluster — the kind platform AppSets generate Applications with `project: '{{ .name }}'`, so the per-cluster `AppProject` must already be reconciling. Without it every Application errors out with `AppProject 'kind-<name>' not found`. **Workaround if you don't have it:** set `project: default` on each AppSet temporarily.
- `kind`, `kubectl`, `docker` on your workstation. ArgoCD's pods on the management cluster need network reachability to the kind API endpoint and to the LB range you choose (typically meaning the management cluster runs on, or routes to, the same docker network — most often this means kind-on-kind for dev).

## Step 1 — Bring kind up *without* a CNI and *without* kube-proxy

Cilium will be installed by Argo and replaces kube-proxy, so the kind cluster must come up with neither — otherwise you get pod CIDR conflicts and a kube-proxy DaemonSet that fights Cilium for IPVS rules.

```bash
# Make sure the kind docker network exists with a known subnet you can carve a LB block from.
docker network inspect kind -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' \
  || docker network create -d bridge --subnet 172.18.0.0/16 kind

cat <<'EOF' | kind create cluster --name dev-a --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  kubeProxyMode: none
nodes:
  - role: control-plane
EOF
```

Verify nothing is scheduling yet (no CNI → no pod IPs):

```bash
kubectl --context kind-dev-a get nodes
# STATUS = NotReady (expected — no CNI)
```

## Step 2 — Pick the LoadBalancer IP range

Kind LoadBalancer IPs come from the docker bridge network, not from the clusterbook pool. You need to pick a range that:

1. Is **inside** the docker network's subnet (e.g. `172.18.0.0/16`).
2. Is **outside** docker's own DHCP allocation range (docker hands out IPs starting from the low end of the subnet for new containers).

```bash
docker network inspect kind | jq '.[0].IPAM.Config'
# [{ "Subnet": "172.18.0.0/16", "Gateway": "172.18.0.1" }]
```

Conventional safe slice for the default `172.18.0.0/16` subnet: `172.18.255.200-172.18.255.250` — high in the subnet, far from where docker hands out DHCP leases.

Multiple kind clusters on the same docker network must use **non-overlapping** ranges.

## Step 3 — Stash the kind kubeconfig as a Secret on the management cluster

The kubeconfig from `kind get kubeconfig` points at `127.0.0.1:<random-port>` — only reachable from your laptop. ArgoCD and clusterbook-operator both run on the *management* cluster and need a server URL reachable from inside their pods. The `--internal` flag rewrites it to `https://<cluster>-control-plane:6443`, which resolves over the docker network when the management cluster is also on it (kind-on-kind).

```bash
kind get kubeconfig --name dev-a --internal > /tmp/kc-dev-a
kubectl -n argocd create secret generic kind-dev-a-kubeconfig \
  --from-file=kubeconfig=/tmp/kc-dev-a
```

If your management cluster is **not** on the same docker network as kind (e.g. it's a remote/cloud cluster), use a kubeconfig whose `server:` URL is reachable from there — typically by exposing the kind API server through a port-forward or a tunnel.

## Step 4 — Apply the `ClusterbookCluster`

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: kind-dev-a
spec:
  networkKey: "10.31.103"           # any clusterbook pool — only used for the primary IP/FQDN
  clusterName: kind-dev-a
  clusterType: kind                 # → label cluster-type=kind, what platforms/kind selects on
  preserveKubeconfigServer: true    # data.server stays at kind-dev-a-control-plane:6443
  kubeconfigSecretRef:
    name: kind-dev-a-kubeconfig
    namespace: argocd
    key: kubeconfig
  providerConfigRef:
    name: default
  argocdNamespace: argocd
  lbRange:
    start: 172.18.255.200
    stop:  172.18.255.250
  labels:
    auto-project: "true"            # picked up by config/cluster-project for the per-cluster AppProject
  releaseOnDelete: true
```

`kubectl apply -f kind-dev-a.yaml` and verify:

```bash
kubectl get clusterbookcluster kind-dev-a -o jsonpath='{.status}{"\n"}'
# {"ip":"10.31.103.X","secretName":"cluster-kind-dev-a","lbRangeStart":"172.18.255.200",...}

kubectl -n argocd get secret cluster-kind-dev-a -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}'
# Expect: cluster-type=kind label, plus cluster-name, ip, lb-range-start, lb-range-stop annotations.
```

If the per-cluster `AppProject` doesn't appear shortly after, your `config/cluster-project` setup isn't picking up the `auto-project=true` label — check its ApplicationSet selector.

## Step 5 — Install the kind platform bundle on the management cluster

```bash
kubectl apply -k https://github.com/stuttgart-things/argocd.git/platforms/kind?ref=main
```

Six ApplicationSets land in `argocd`. As soon as the cluster Secret has `cluster-type=kind`, they fire and generate one Application each per registered kind cluster.

Watch them converge:

```bash
kubectl -n argocd get applications -l app.kubernetes.io/managed-by=argocd | grep kind-dev-a
# cilium-install-kind-dev-a       Synced  Healthy
# cert-manager-install-kind-dev-a Synced  Healthy
# cilium-lb-kind-dev-a            Synced  Healthy
# cert-manager-selfsigned-...     Synced  Healthy
# cert-manager-cluster-ca-...     Synced  Healthy
# cilium-gateway-kind-dev-a       Synced  Healthy
```

The first one (`cilium-install-kind-dev-a`, sync-wave -10) must land first — without a CNI everything else retries forever, but they will converge automatically once Cilium is up.

## Step 6 — Smoke-test the LB pool

```bash
kubectl --kubeconfig /tmp/kc-dev-a get nodes
# STATUS = Ready (Cilium is up)

kubectl --kubeconfig /tmp/kc-dev-a get ciliumloadbalancerippool kind-dev-a-pool -o yaml
# spec.blocks should match the spec.lbRange you set in step 4

kubectl --kubeconfig /tmp/kc-dev-a create deploy nginx --image=nginx
kubectl --kubeconfig /tmp/kc-dev-a expose deploy nginx --port=80 --type=LoadBalancer
kubectl --kubeconfig /tmp/kc-dev-a get svc nginx -w
# EXTERNAL-IP from your lbRange (e.g. 172.18.255.200)
```

From the host, the LB IP should be reachable directly over the docker network:

```bash
curl -sS http://172.18.255.200/ | head -1
# <!DOCTYPE html>
```

## Caveats / gotchas

1. **`auto-project=true` co-label flow assumes `config/cluster-project` is deployed on the management cluster.** Every generated Application sets `project: '{{ .name }}'`, so the per-cluster `AppProject` must already exist. If it doesn't, every Application stalls with `AppProject 'kind-dev-a' not found` — apply that bundle first, or set `project: default` on the kind AppSets temporarily.

2. **Pick the `lbRange` from a slice of the kind docker subnet that's outside docker's DHCP allocation.** Docker hands out IPs starting at the low end of the subnet for new containers; a high slice (e.g. `.255.200-250` on a `172.18.0.0/16`) is the conventional safe choice. Verify the subnet with `docker network inspect kind` and make sure multiple kind clusters on the same docker network use **non-overlapping** ranges.

## Repeating for additional kind clusters

Steps 1, 3, 4 are identical for each new kind cluster — only the name and `lbRange` change. The script at [`hack/bootstrap-kind-cluster.sh`](../hack/bootstrap-kind-cluster.sh) wraps them into a single command.

## Related

- [`examples/clusterbookcluster-kind.yaml`](../examples/clusterbookcluster-kind.yaml) — the `ClusterbookCluster` CR variants (user-pinned vs operator-allocated).
- [`stuttgart-things/argocd/platforms/kind`](https://github.com/stuttgart-things/argocd/tree/main/platforms/kind) — the AppSet bundle this tutorial wires up.
- [Tutorial: Register a new cluster](tutorial-register-new-cluster.md) — the vSphere/Talos counterpart that does not need to install a CNI.
