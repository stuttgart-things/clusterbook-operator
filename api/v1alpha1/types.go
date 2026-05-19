package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="(has(self.kubeconfigSecretRef) ? 1 : 0) + (has(self.existingSecretRef) ? 1 : 0) == 1",message="exactly one of kubeconfigSecretRef or existingSecretRef must be set"
// +kubebuilder:validation:XValidation:rule="self.skipReservation || self.clusterType == 'kind' || has(self.networkKey)",message="networkKey is required unless clusterType is 'kind' or skipReservation is true (registration-only)"
// +kubebuilder:validation:XValidation:rule="!has(self.networkKey) || (has(self.providerConfigRef) && size(self.providerConfigRef.name) > 0)",message="providerConfigRef.name is required when networkKey is set"
// +kubebuilder:validation:XValidation:rule="has(self.networkKey) || self.preserveKubeconfigServer",message="preserveKubeconfigServer must be true on the registration-only path (no IP/FQDN allocated for data.server)"
type ClusterbookClusterSpec struct {
	// NetworkKey is the clusterbook network pool (e.g. "10.31.103") used
	// for IP/DNS allocation. Optional only when ClusterType is "kind" — a
	// kind cluster registered without NetworkKey takes the
	// registration-only path: the operator skips clusterbook server calls
	// entirely and only writes the ArgoCD cluster Secret + labels +
	// LBRange annotations. Required for all other cluster types.
	// +optional
	NetworkKey string `json:"networkKey,omitempty"`

	// SkipReservation explicitly disables the clusterbook reservation step
	// for this CR — no IP allocation, no DNS creation, no GetClusterInfo
	// call, no release-on-delete. Intended for child CRs emitted by a parent
	// controller (e.g. Vcluster) that has already reserved the IP/FQDN
	// itself and rewritten the kubeconfig's server URL. When true,
	// NetworkKey/ProviderConfigRef/CreateDNS/LBRange are not consulted, and
	// PreserveKubeconfigServer must be true (rule 4) so data.server is taken
	// from the kubeconfig rather than from an absent reservation. Distinct
	// from the implicit kind-without-networkKey path, which keys off
	// ClusterType="kind"; SkipReservation generalises the same behaviour to
	// any ClusterType.
	// +optional
	SkipReservation bool `json:"skipReservation,omitempty"`

	// ClusterName is the cluster identifier registered in clusterbook.
	// Used as the ArgoCD cluster name and as the IP reservation key.
	ClusterName string `json:"clusterName"`

	// CreateDNS asks clusterbook to create a wildcard DNS record.
	// +optional
	CreateDNS bool `json:"createDNS,omitempty"`

	// UseFQDNAsServer builds the ArgoCD server URL from the FQDN instead
	// of the reserved IP. Requires CreateDNS=true. Ignored in enrich mode.
	// +optional
	UseFQDNAsServer bool `json:"useFQDNAsServer,omitempty"`

	// ServerPort is the port appended to the ArgoCD server URL. Defaults to 6443.
	// +optional
	ServerPort int `json:"serverPort,omitempty"`

	// ServerSubdomain is substituted for the wildcard "*" label in the
	// clusterbook FQDN when UseFQDNAsServer is true. Clusterbook's DNS
	// integration creates wildcard records (*.<cluster>.<zone>) which
	// cannot be used verbatim as a hostname. Defaults to "api" —
	// conventional for Kubernetes API servers and always resolvable via
	// the same wildcard record. Ignored when UseFQDNAsServer is false
	// or the FQDN is not in wildcard form.
	// +optional
	ServerSubdomain string `json:"serverSubdomain,omitempty"`

	// PreserveKubeconfigServer keeps data.server on the cluster Secret
	// set to whatever the referenced kubeconfig's current-context cluster
	// uses, instead of rewriting it to the clusterbook-reserved IP/FQDN.
	// Use this when the target cluster's API server lives at the
	// kubeconfig's address and nothing on the target cluster is bound to
	// the clusterbook reservation (no kube-vip, no Cilium LB for the API,
	// etc.) — without it ArgoCD would fail to connect. The reservation
	// is still made and DNS is still created; IP/FQDN/zone are still
	// exposed on the Secret as labels and annotations for ApplicationSet
	// selection and templating. Takes precedence over UseFQDNAsServer.
	// +optional
	PreserveKubeconfigServer bool `json:"preserveKubeconfigServer,omitempty"`

	// KubeconfigSecretRef references a Secret holding the target cluster's
	// kubeconfig. The controller extracts server/CA/auth from it and writes
	// a new ArgoCD cluster Secret. Mutually exclusive with ExistingSecretRef.
	// +optional
	KubeconfigSecretRef *SecretKeyRef `json:"kubeconfigSecretRef,omitempty"`

	// ExistingSecretRef points at an ArgoCD cluster Secret that is already
	// managed elsewhere. In this "enrich" mode the operator still reserves
	// an IP/DNS and populates status, but only merges clusterbook-prefixed
	// labels and annotations onto the Secret — it does not create, own, or
	// modify the Secret's data fields. Mutually exclusive with KubeconfigSecretRef.
	// +optional
	ExistingSecretRef *SecretObjectRef `json:"existingSecretRef,omitempty"`

	// ProviderConfigRef points to a ClusterbookProviderConfig with the
	// clusterbook API URL and TLS options. Required whenever NetworkKey
	// is set; may be empty (Name="") in the kind registration-only path
	// (NetworkKey empty + ClusterType "kind"), since no clusterbook API
	// calls are made then.
	// +optional
	ProviderConfigRef corev1.LocalObjectReference `json:"providerConfigRef,omitempty"`

	// ArgoCDNamespace is where the generated cluster Secret is written.
	// Defaults to "argocd". Ignored in enrich mode (the existing Secret's
	// namespace is taken from ExistingSecretRef).
	// +optional
	ArgoCDNamespace string `json:"argocdNamespace,omitempty"`

	// Labels are applied to the cluster Secret under the
	// clusterbook.stuttgart-things.com/ prefix. Use them as selectors in
	// ApplicationSet cluster generators.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations are additional annotations stamped onto the rendered
	// ArgoCD cluster Secret, merged on top of the operator-managed ones
	// (cluster-name, ip, fqdn, zone, lb-range-*). Useful for AppSets'
	// chart values that read via `{{ index .metadata.annotations "…" }}`
	// — e.g. the cert-manager-vault-pki AppSet reads `vault-server`,
	// `vault-pki-path`, `vault-token-secret` annotations. Operator-managed
	// annotations win on conflict.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// ReleaseOnDelete releases the clusterbook IP when the CR is deleted.
	// +optional
	ReleaseOnDelete bool `json:"releaseOnDelete,omitempty"`

	// ClusterType is a free-form discriminator written to the ArgoCD cluster
	// Secret as the label clusterbook.stuttgart-things.com/cluster-type.
	// Used by ApplicationSet selectors to fan out type-specific platform
	// bundles (e.g. "kind", "vsphere", "talos"). Optional.
	// +optional
	ClusterType string `json:"clusterType,omitempty"`

	// LBRange optionally reserves a contiguous IP range (in addition to the
	// primary allocated IP) intended for LoadBalancer pools — e.g. a Cilium
	// CiliumLoadBalancerIPPool block. Resolved range is exposed on the
	// Secret as the annotations clusterbook.stuttgart-things.com/lb-range-start
	// and /lb-range-stop. Optional.
	// +optional
	LBRange *LBRange `json:"lbRange,omitempty"`
}

// LBRange describes a LoadBalancer IP range to attach to the cluster Secret.
// Either Count (operator reserves the range from the clusterbook pool) or the
// Start/Stop pair (user pins the range verbatim) must be supplied; they are
// mutually exclusive.
// +kubebuilder:validation:XValidation:rule="(has(self.count) && self.count > 0 ? 1 : 0) + (has(self.start) && has(self.stop) ? 1 : 0) == 1",message="exactly one of lbRange.count or lbRange.start+stop must be set"
type LBRange struct {
	// Count is the number of contiguous IPs to reserve from networkKey in
	// addition to the primary cluster IP. Mutually exclusive with Start/Stop.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Count int `json:"count,omitempty"`

	// Start, Stop pin the range verbatim — typical for kind clusters whose
	// LoadBalancer IPs come from the docker bridge network rather than the
	// clusterbook pool. When set, the operator does NOT reserve them from
	// networkKey, only writes them through to the Secret. Both must be set
	// together. Mutually exclusive with Count.
	// +optional
	Start string `json:"start,omitempty"`
	// +optional
	Stop string `json:"stop,omitempty"`
}

type SecretKeyRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Key defaults to "kubeconfig".
	// +optional
	Key string `json:"key,omitempty"`
}

// SecretObjectRef points at a Secret by name and namespace, without a
// key. Used for enrich mode where the operator only touches metadata.
type SecretObjectRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type ClusterbookClusterStatus struct {
	IP   string `json:"ip,omitempty"`
	FQDN string `json:"fqdn,omitempty"`
	Zone string `json:"zone,omitempty"`
	// LBRangeStart, LBRangeStop record the resolved LoadBalancer range so
	// repeat reconciles in operator-allocated mode (spec.lbRange.count > 0)
	// don't re-reserve the range. In user-pinned mode they mirror
	// spec.lbRange.start/stop.
	LBRangeStart string             `json:"lbRangeStart,omitempty"`
	LBRangeStop  string             `json:"lbRangeStop,omitempty"`
	SecretName   string             `json:"secretName,omitempty"`
	Conditions   []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cbkc
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ip`
// +kubebuilder:printcolumn:name="FQDN",type=string,JSONPath=`.status.fqdn`
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
type ClusterbookCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterbookClusterSpec   `json:"spec,omitempty"`
	Status ClusterbookClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterbookClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterbookCluster `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type ClusterbookProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterbookProviderConfigSpec `json:"spec,omitempty"`
}

type ClusterbookProviderConfigSpec struct {
	APIURL             string        `json:"apiURL"`
	InsecureSkipVerify bool          `json:"insecureSkipVerify,omitempty"`
	CustomCASecretRef  *SecretKeyRef `json:"customCASecretRef,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterbookProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterbookProviderConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterbookProviderConfig{}, &ClusterbookProviderConfigList{})
}
