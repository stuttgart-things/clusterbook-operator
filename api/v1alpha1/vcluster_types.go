package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="!has(self.networkKey) || (has(self.providerConfigRef) && size(self.providerConfigRef.name) > 0)",message="providerConfigRef.name is required when networkKey is set"
type VclusterSpec struct {
	// ClusterName is the name under which the provisioned vcluster is
	// registered in ArgoCD. Used as the IP reservation key when NetworkKey
	// is set, and propagated to the emitted child ClusterbookCluster.
	ClusterName string `json:"clusterName"`

	// TargetClusterRef points at an existing ArgoCD cluster Secret on the
	// management cluster. The operator reads that Secret to obtain a
	// kubeconfig for the host cluster on which the vcluster is installed.
	// Omit to install on the operator's own cluster (in-cluster config).
	// +optional
	TargetClusterRef *SecretObjectRef `json:"targetClusterRef,omitempty"`

	// TargetNamespace is the namespace on the target cluster into which
	// the vcluster Helm release is deployed. Required.
	TargetNamespace string `json:"targetNamespace"`

	// ChartVersion pins the loft-sh vcluster Helm chart version.
	// +optional
	ChartVersion string `json:"chartVersion,omitempty"`

	// ChartValues is a verbatim passthrough merged on top of the
	// operator's defaults (service.type, loadBalancerIP, syncer extraArgs).
	// Operator-managed keys win on conflict.
	// +optional
	ChartValues *apiextensionsv1.JSON `json:"chartValues,omitempty"`

	// NetworkKey is the clusterbook network pool (e.g. "10.31.101") used
	// for IP/DNS allocation. Omit to skip the clusterbook reservation step
	// entirely — the operator then provisions the vcluster without
	// reserving an IP and the emitted child ClusterbookCluster runs in
	// registration-only mode (skipReservation=true).
	// +optional
	NetworkKey string `json:"networkKey,omitempty"`

	// ServerPort is the port appended to the external server URL when
	// rewriting the kubeconfig. Defaults to 443.
	// +optional
	ServerPort int `json:"serverPort,omitempty"`

	// ProviderConfigRef points to a ClusterbookProviderConfig with the
	// clusterbook API URL and TLS options. Required whenever NetworkKey
	// is set.
	// +optional
	ProviderConfigRef corev1.LocalObjectReference `json:"providerConfigRef,omitempty"`

	// ArgoCD controls registration of the provisioned vcluster as an
	// ArgoCD cluster on the management cluster.
	// +optional
	ArgoCD VclusterArgoCD `json:"argoCD,omitempty"`

	// ReleaseOnDelete releases the clusterbook IP when the CR is deleted.
	// +optional
	ReleaseOnDelete bool `json:"releaseOnDelete,omitempty"`
}

// VclusterArgoCD groups the ArgoCD-registration knobs for the emitted
// child ClusterbookCluster. Labels and annotations are propagated.
type VclusterArgoCD struct {
	// Namespace is where the child ClusterbookCluster's rendered cluster
	// Secret is written. Defaults to "argocd".
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Register controls whether the operator emits a child
	// ClusterbookCluster at all. Defaults to true; set to false for
	// provision-only flows (e.g. testing the chart upsert path without
	// touching ArgoCD). Tri-state pointer so the default is observable.
	// +optional
	Register *bool `json:"register,omitempty"`

	// Labels propagated onto the emitted child ClusterbookCluster's
	// spec.labels — applied to the rendered ArgoCD cluster Secret under
	// the clusterbook.stuttgart-things.com/ prefix.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations propagated onto the emitted child ClusterbookCluster's
	// spec.annotations — stamped onto the rendered ArgoCD cluster Secret.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

type VclusterStatus struct {
	// IP is the IP reserved in clusterbook (only when NetworkKey is set).
	IP string `json:"ip,omitempty"`

	// FQDN is the FQDN returned by clusterbook (only when NetworkKey is set).
	FQDN string `json:"fqdn,omitempty"`

	// Zone is the DNS zone returned by clusterbook.
	Zone string `json:"zone,omitempty"`

	// KubeconfigSecretRef points at the Secret on the management cluster
	// holding the rewritten external kubeconfig (data key "kubeconfig"),
	// consumed by the emitted child ClusterbookCluster.
	// +optional
	KubeconfigSecretRef *SecretObjectRef `json:"kubeconfigSecretRef,omitempty"`

	// ArgoCDApplicationRef is the name of the ArgoCD Application the
	// operator upserts to drive the vcluster Helm install on the target
	// cluster.
	ArgoCDApplicationRef string `json:"argoCDApplicationRef,omitempty"`

	// ClusterbookClusterRef is the name of the child ClusterbookCluster
	// the operator emits to register the provisioned vcluster in ArgoCD.
	ClusterbookClusterRef string `json:"clusterbookClusterRef,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=vc
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetNamespace`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ip`
// +kubebuilder:printcolumn:name="FQDN",type=string,JSONPath=`.status.fqdn`
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.status.argoCDApplicationRef`
type Vcluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VclusterSpec   `json:"spec,omitempty"`
	Status VclusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VclusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Vcluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Vcluster{}, &VclusterList{})
}
