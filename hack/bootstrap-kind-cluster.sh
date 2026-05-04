#!/usr/bin/env bash
# Bootstrap a fresh kind cluster onto a clusterbook-operator + ArgoCD fleet.
#
# Wraps the manual steps in docs/tutorial-bootstrap-kind.md:
#   1. kind create (no CNI, kube-proxy=none)
#   2. stash kubeconfig as a Secret on the management cluster
#   3. apply a ClusterbookCluster CR with clusterType=kind + lbRange + auto-project
#
# Cilium / cert-manager / Gateway / LB pool come from the platforms/kind
# AppSet bundle in stuttgart-things/argocd; this script does NOT install
# the bundle (one-time, mgmt-cluster-wide) — it only registers one new
# kind cluster against an already-installed bundle.
#
# Usage:
#   hack/bootstrap-kind-cluster.sh \
#     --name dev-a \
#     --mgmt-kubeconfig ~/.kube/platform-sthings \
#     [--lb-start 172.18.255.200] [--lb-stop 172.18.255.250] \
#     [--network-key 10.31.103] [--provider-config default] \
#     [--argocd-namespace argocd] [--release-on-delete true] \
#     [--dry-run]
#
# Defaults are tuned for the conventional kind-on-kind dev setup
# (default kind docker subnet 172.18.0.0/16, ArgoCD on the same docker
# network). Override --lb-start / --lb-stop when running multiple kind
# clusters on the same docker network — ranges must not overlap.

set -euo pipefail

# ----- defaults --------------------------------------------------------------

NAME=""
MGMT_KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
LB_START="172.18.255.200"
LB_STOP="172.18.255.250"
NETWORK_KEY="10.31.103"
PROVIDER_CONFIG="default"
ARGOCD_NS="argocd"
RELEASE_ON_DELETE="true"
DRY_RUN="false"

# ----- arg parsing -----------------------------------------------------------

usage() {
  sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)              NAME="$2"; shift 2 ;;
    --mgmt-kubeconfig)   MGMT_KUBECONFIG="$2"; shift 2 ;;
    --lb-start)          LB_START="$2"; shift 2 ;;
    --lb-stop)           LB_STOP="$2"; shift 2 ;;
    --network-key)       NETWORK_KEY="$2"; shift 2 ;;
    --provider-config)   PROVIDER_CONFIG="$2"; shift 2 ;;
    --argocd-namespace)  ARGOCD_NS="$2"; shift 2 ;;
    --release-on-delete) RELEASE_ON_DELETE="$2"; shift 2 ;;
    --dry-run)           DRY_RUN="true"; shift ;;
    -h|--help)           usage 0 ;;
    *) echo "unknown flag: $1" >&2; usage 1 ;;
  esac
done

[[ -z "$NAME" ]] && { echo "ERROR: --name is required" >&2; usage 1; }

# Pre-flight tool check — fail loudly with a single message rather than
# bailing mid-way through with a cryptic "command not found".
for cmd in kind kubectl docker; do
  command -v "$cmd" >/dev/null || { echo "ERROR: '$cmd' not on PATH" >&2; exit 1; }
done

CR_NAME="kind-$NAME"
SECRET_NAME="kind-${NAME}-kubeconfig"

echo "==> bootstrap kind cluster '$NAME'"
echo "    cr.metadata.name        = $CR_NAME"
echo "    kubeconfig secret       = $SECRET_NAME (ns: $ARGOCD_NS)"
echo "    lbRange                 = $LB_START → $LB_STOP"
echo "    clusterbook networkKey  = $NETWORK_KEY"
echo "    mgmt kubeconfig         = $MGMT_KUBECONFIG"
echo "    dry-run                 = $DRY_RUN"
echo

# ----- step 1: kind create ---------------------------------------------------

# kind ships with a default CNI (kindnet) and kube-proxy. We disable both:
# Cilium (installed by the platforms/kind AppSet bundle) is the CNI and
# replaces kube-proxy. Skipping this would create pod-CIDR conflicts and a
# kube-proxy DaemonSet fighting Cilium for the IPVS rules.
if kind get clusters 2>/dev/null | grep -qx "$NAME"; then
  echo "==> kind cluster '$NAME' already exists — skipping create"
else
  echo "==> creating kind cluster '$NAME' (no CNI, kube-proxy=none)"
  if [[ "$DRY_RUN" == "true" ]]; then
    echo "    [dry-run] would: kind create cluster --name $NAME ..."
  else
    cat <<EOF | kind create cluster --name "$NAME" --config -
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  kubeProxyMode: none
nodes:
  - role: control-plane
EOF
  fi
fi

# ----- step 2: stash internal kubeconfig as a Secret on the mgmt cluster -----

# `kind get kubeconfig --internal` rewrites the server URL from
# 127.0.0.1:<random-port> (only reachable from the host) to the
# docker-hostname form https://<cluster>-control-plane:6443 — what
# clusterbook-operator and ArgoCD pods on the mgmt cluster need.
echo "==> stashing internal kubeconfig as Secret '$SECRET_NAME' in $ARGOCD_NS"
TMP_KCFG="$(mktemp -t kc-${NAME}.XXXXXX)"
trap 'rm -f "$TMP_KCFG"' EXIT
if [[ "$DRY_RUN" == "true" ]]; then
  echo "    [dry-run] would: kind get kubeconfig --internal --name $NAME > <secret>"
else
  kind get kubeconfig --name "$NAME" --internal > "$TMP_KCFG"
  KUBECONFIG="$MGMT_KUBECONFIG" kubectl -n "$ARGOCD_NS" \
    create secret generic "$SECRET_NAME" \
    --from-file=kubeconfig="$TMP_KCFG" \
    --dry-run=client -o yaml | \
    KUBECONFIG="$MGMT_KUBECONFIG" kubectl apply -f -
fi

# ----- step 3: apply the ClusterbookCluster CR -------------------------------

# auto-project=true is the co-label that makes config/cluster-project's
# ApplicationSet provision the per-cluster AppProject the platforms/kind
# AppSets reference via project: '{{ .name }}'. Without it every generated
# Application stalls with "AppProject 'kind-<name>' not found".
echo "==> applying ClusterbookCluster '$CR_NAME'"
CR_YAML=$(cat <<EOF
apiVersion: clusterbook.stuttgart-things.com/v1alpha1
kind: ClusterbookCluster
metadata:
  name: $CR_NAME
spec:
  networkKey: "$NETWORK_KEY"
  clusterName: $CR_NAME
  clusterType: kind
  preserveKubeconfigServer: true
  kubeconfigSecretRef:
    name: $SECRET_NAME
    namespace: $ARGOCD_NS
    key: kubeconfig
  providerConfigRef:
    name: $PROVIDER_CONFIG
  argocdNamespace: $ARGOCD_NS
  lbRange:
    start: "$LB_START"
    stop:  "$LB_STOP"
  labels:
    auto-project: "true"
  releaseOnDelete: $RELEASE_ON_DELETE
EOF
)

if [[ "$DRY_RUN" == "true" ]]; then
  echo "    [dry-run] would apply:"
  echo "$CR_YAML" | sed 's/^/      /'
else
  echo "$CR_YAML" | KUBECONFIG="$MGMT_KUBECONFIG" kubectl apply -f -
fi

echo
echo "==> done."
echo "    Watch reconcile:    kubectl get clusterbookcluster $CR_NAME -o yaml"
echo "    Watch Applications: kubectl -n $ARGOCD_NS get applications | grep $CR_NAME"
echo
echo "Reminder: platforms/kind/ AppSet bundle must already be installed on"
echo "the mgmt cluster:"
echo "    kubectl apply -k https://github.com/stuttgart-things/argocd.git/platforms/kind?ref=main"
