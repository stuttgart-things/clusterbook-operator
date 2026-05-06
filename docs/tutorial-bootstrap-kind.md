# Tutorial: Bootstrap a kind cluster onto an ArgoCD fleet

End-to-end walkthrough for taking a freshly-created kind cluster (nothing deployed) and wiring it into an ArgoCD fleet so that the [`platforms/kind/`](https://github.com/stuttgart-things/argocd/tree/main/platforms/kind) bundle fans Cilium + cert-manager + Gateway out onto it automatically.

## What you end up with

- A kind cluster running with **Cilium as the CNI** (installed by Argo, not by kind), **kube-proxy replaced by Cilium**, and a **`CiliumLoadBalancerIPPool`** carved out of the docker bridge.
- An ArgoCD cluster `Secret` on the management cluster, enriched by [clusterbook-operator](https://github.com/stuttgart-things/clusterbook-operator) with `cluster-type=kind` + `lb-range-{start,stop}`.
- Six `ApplicationSet`-generated Applications fanning the kind platform bundle onto the new cluster.

## Prerequisites

- A **management cluster** that already runs ArgoCD and `clusterbook-operator` **v0.15.0 or later** ([Install](install.md)). v0.15.0 is the version that introduces `clusterType` and `lbRange`.
- A **clusterbook server** the operator's `ClusterbookProviderConfig` points at, **v1.25.1 or later** if you want DNS records.
- The [`config/cluster-project`](https://github.com/stuttgart-things/argocd/tree/main/config/cluster-project) bundle deployed on the management cluster — the kind platform AppSets generate Applications with `project: '{{ .name }}'`, so the per-cluster `AppProject` must already be reconciling. Without it every Application errors out with `AppProject '<cluster-name>' not found`. **Workaround if you don't have it:** set `project: default` on each AppSet temporarily.
- `kind`, `kubectl`, `docker` wherever the kind cluster will run. ArgoCD's pods on the management cluster need network reachability to **the kind API endpoint** — see [Topologies](#topologies) below for what that means in practice.

## Topologies

This tutorial supports two layouts. Pick the one that matches yours and follow the variant noted in Step 3:

1. **kind-on-mgmt (kind-on-kind dev setup).** The management cluster runs as containers on the same docker host as the new kind cluster — they share the docker bridge network. ArgoCD pods reach the kind API at `https://<cluster>-control-plane:6443` over the docker network. This is what the `kind get kubeconfig --internal` rewrite is designed for, and the default in [`hack/bootstrap-kind-cluster.sh`](../hack/bootstrap-kind-cluster.sh).
2. **remote-kind.** The kind cluster runs on a separate VM (e.g. a developer / CD-mgmt machine) that has a **routable IP on the same network as the management cluster nodes**. The mgmt cluster cannot resolve docker-internal hostnames; instead the kubeconfig's `server:` URL must be the remote VM's IP at the kind API server's published port. Skip `--internal` for this case.

The LB pool is **always** carved out of the docker bridge subnet on the host running kind, so LB IPs are only reachable from that host (and any other workload sharing its docker network). On the remote-kind topology the smoke test in Step 6 has to be run on the kind host, not on your workstation.

## Step 1 — Bring kind up *without* a CNI and *without* kube-proxy

Cilium will be installed by Argo and replaces kube-proxy, so the kind cluster must come up with neither — otherwise you get pod CIDR conflicts and a kube-proxy DaemonSet that fights Cilium for IPVS rules.

> **remote-kind:** run this whole step on the remote VM that will host the kind cluster, not on your workstation.

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
# kind prefixes the kubectl context with "kind-", so a cluster created
# as --name dev-a yields the context "kind-dev-a".
```

> **Naming constraint.** The kind cluster name **must** match `spec.clusterName` in Step 4 (and conventionally `metadata.name` of the `ClusterbookCluster`). The `cilium-install` ApplicationSet templates the in-cluster K8s API hostname as `<spec.clusterName>-control-plane`, which is the docker container name kind creates. If the names diverge, Cilium init containers loop with `dial tcp: lookup <name>-control-plane: server misbehaving` and nodes never become Ready.

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

ArgoCD and `clusterbook-operator` both run on the *management* cluster and need a `server:` URL reachable from inside their pods. The default `kind get kubeconfig` output points at `127.0.0.1:<random-port>` — only reachable from the host that runs kind. Pick the variant that matches your topology.

### Variant A — kind-on-mgmt (same docker network)

The `--internal` flag rewrites the server URL to `https://<cluster>-control-plane:6443`, which resolves over the docker network when the management cluster is also on it.

```bash
kind get kubeconfig --name dev-a --internal > /tmp/kc-dev-a
kubectl -n argocd create secret generic dev-a-kubeconfig \
  --from-file=kubeconfig=/tmp/kc-dev-a
```

### Variant B — remote-kind (separate VM, routable IP)

`kind get kubeconfig` (without `--internal`) emits `https://127.0.0.1:<random-port>`. Replace the loopback with the remote VM's reachable IP — that IP needs to resolve and be routable from the management cluster nodes (typically same VLAN / VPC), and the published kind API port has to be open through any host firewall.

```bash
# On the remote VM that runs the kind cluster:
REMOTE_IP=10.31.104.101                          # IP reachable from the mgmt cluster
kind get kubeconfig --name dev-a > /tmp/kc-dev-a
sed -i "s#https://127\.0\.0\.1:#https://${REMOTE_IP}:#" /tmp/kc-dev-a
# Copy /tmp/kc-dev-a to a workstation that has the mgmt-cluster kubeconfig, then:

kubectl -n argocd create secret generic dev-a-kubeconfig \
  --from-file=kubeconfig=/tmp/kc-dev-a
```

Sanity check from the management cluster: `kubectl --kubeconfig /tmp/kc-dev-a get nodes` should hit the remote VM and return the kind nodes (`NotReady` is expected at this point).

> The kind API server's published port is randomized per cluster — confirm with `docker port <cluster>-control-plane 6443` on the remote VM if anything looks wrong.

## Step 4 — Apply the `ClusterbookCluster`

```yaml
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: dev-a
spec:
  networkKey: "10.31.103"           # any clusterbook pool — only used for the primary IP/FQDN
  clusterName: dev-a
  clusterType: kind                 # → label cluster-type=kind, what platforms/kind selects on
  preserveKubeconfigServer: true    # data.server stays at dev-a-control-plane:6443
  kubeconfigSecretRef:
    name: dev-a-kubeconfig
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

`kubectl apply -f dev-a.yaml` and verify:

```bash
kubectl get clusterbookcluster dev-a -o jsonpath='{.status}{"\n"}'
# {"ip":"10.31.103.X","secretName":"cluster-dev-a","lbRangeStart":"172.18.255.200",...}

kubectl -n argocd get secret cluster-dev-a -o jsonpath='{.metadata.labels}{"\n"}{.metadata.annotations}{"\n"}'
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
kubectl -n argocd get applications -l app.kubernetes.io/managed-by=argocd | grep dev-a
# cilium-install-dev-a       Synced  Healthy
# cert-manager-install-dev-a Synced  Healthy
# cilium-lb-dev-a            Synced  Healthy
# cert-manager-selfsigned-...     Synced  Healthy
# cert-manager-cluster-ca-...     Synced  Healthy
# cilium-gateway-dev-a       Synced  Healthy
```

The first one (`cilium-install-dev-a`, sync-wave -10) must land first — without a CNI everything else retries forever, but they will converge automatically once Cilium is up.

## Step 6 — Smoke-test the LB pool

```bash
kubectl --kubeconfig /tmp/kc-dev-a get nodes
# STATUS = Ready (Cilium is up)

kubectl --kubeconfig /tmp/kc-dev-a get ciliumloadbalancerippool dev-a-pool -o yaml
# spec.blocks should match the spec.lbRange you set in step 4

kubectl --kubeconfig /tmp/kc-dev-a create deploy nginx --image=nginx
kubectl --kubeconfig /tmp/kc-dev-a expose deploy nginx --port=80 --type=LoadBalancer
kubectl --kubeconfig /tmp/kc-dev-a get svc nginx -w
# EXTERNAL-IP from your lbRange (e.g. 172.18.255.200)
```

The LB IP is reachable directly over the docker bridge **on the host that runs kind** — that's your workstation in the kind-on-mgmt topology, or the remote VM in the remote-kind topology:

```bash
# Run this on the kind host (workstation OR remote VM, depending on topology):
curl -sS http://172.18.255.200/ | head -1
# <!DOCTYPE html>
```

The LB IPs are **not** routed beyond that host. From elsewhere (your workstation in the remote-kind topology, or the management cluster nodes) they're unreachable unless you publish them via Gateway/Ingress + DNS, or set up explicit routing.

## Caveats / gotchas

1. **`auto-project=true` co-label flow assumes `config/cluster-project` is deployed on the management cluster.** Every generated Application sets `project: '{{ .name }}'`, so the per-cluster `AppProject` must already exist. If it doesn't, every Application stalls with `AppProject 'dev-a' not found` — apply that bundle first, or set `project: default` on the kind AppSets temporarily.

2. **Pick the `lbRange` from a slice of the kind docker subnet that's outside docker's DHCP allocation.** Docker hands out IPs starting at the low end of the subnet for new containers; a high slice (e.g. `.255.200-250` on a `172.18.0.0/16`) is the conventional safe choice. Verify the subnet with `docker network inspect kind` **on the host that runs kind** (in the remote-kind topology that's the remote VM, not your workstation), and make sure multiple kind clusters on the same docker network use **non-overlapping** ranges.

3. **In the remote-kind topology, the kubeconfig server URL is what makes or breaks reconcile.** `--internal` is the wrong flag — it produces a docker-internal hostname that the management cluster can't resolve. Use the remote VM's routable IP (Variant B in Step 3) instead. If reconcile fails with a TLS / dial error, the operator pod cannot reach the kind API: check the secret's kubeconfig `server:` URL is reachable from the management cluster (`kubectl --kubeconfig <secret-extract> get nodes` from a mgmt-cluster node).

4. **`cert-manager-cluster-ca-<name>` and `cilium-gateway-<name>` will stay `Unknown / Healthy` until the cluster has an FQDN.** Both helm charts validate non-empty `wildcard.commonName` / `hostname`, which the AppSets template from a per-cluster DNS allocation. kind clusters don't normally get DNS records (set `createDNS: false` or rely on the clusterbook server skipping kind-typed entries), so without DNS these two Applications can't render. The other four (`cilium-install`, `cilium-lb`, `cert-manager-install`, `cert-manager-selfsigned`) are sufficient to bring the kind cluster Ready and serve workloads via the LB pool. To unblock the gateway/cluster-CA flow, register the cluster against a clusterbook server that issues records for kind clusters (so `status.fqdn` is populated and the cluster Secret gets a `cluster-fqdn` annotation).

## Repeating for additional kind clusters

Steps 1, 3, 4 are identical for each new kind cluster — only the name and `lbRange` change. The script at [`hack/bootstrap-kind-cluster.sh`](../hack/bootstrap-kind-cluster.sh) wraps them into a single command for the **kind-on-mgmt** topology (it uses `--internal`). For the **remote-kind** topology, follow Step 3 Variant B manually — the script's `--internal` rewrite would produce an unreachable server URL.

## Related

- [`examples/clusterbookcluster-kind.yaml`](../examples/clusterbookcluster-kind.yaml) — the `ClusterbookCluster` CR variants (user-pinned vs operator-allocated).
- [`stuttgart-things/argocd/platforms/kind`](https://github.com/stuttgart-things/argocd/tree/main/platforms/kind) — the AppSet bundle this tutorial wires up.
- [Tutorial: Register a new cluster](tutorial-register-new-cluster.md) — the vSphere/Talos counterpart that does not need to install a CNI.
