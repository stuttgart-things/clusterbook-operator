package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterbookClusterSpec struct {
	// NetworkKey is the clusterbook network pool (e.g. "10.31.103").
	NetworkKey string `json:"networkKey"`

	// ClusterName is the cluster identifier registered in clusterbook.
	// Used as the ArgoCD cluster name and as the IP reservation key.
	ClusterName string `json:"clusterName"`

	// CreateDNS asks clusterbook to create a wildcard DNS record.
	// +optional
	CreateDNS bool `json:"createDNS,omitempty"`

	// UseFQDNAsServer builds the ArgoCD server URL from the FQDN instead
	// of the reserved IP. Requires CreateDNS=true.
	// +optional
	UseFQDNAsServer bool `json:"useFQDNAsServer,omitempty"`

	// ServerPort is the port appended to the ArgoCD server URL. Defaults to 6443.
	// +optional
	ServerPort int `json:"serverPort,omitempty"`

	// KubeconfigSecretRef references a Secret holding the target cluster's
	// kubeconfig. The controller extracts server/CA/auth from it.
	KubeconfigSecretRef SecretKeyRef `json:"kubeconfigSecretRef"`

	// ProviderConfigRef points to a ClusterbookProviderConfig with the
	// clusterbook API URL and TLS options.
	ProviderConfigRef corev1.LocalObjectReference `json:"providerConfigRef"`

	// ArgoCDNamespace is where the generated cluster Secret is written.
	// Defaults to "argocd".
	// +optional
	ArgoCDNamespace string `json:"argocdNamespace,omitempty"`

	// Labels are copied onto the generated cluster Secret. Use them as
	// selectors in ApplicationSet cluster generators.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// ReleaseOnDelete releases the clusterbook IP when the CR is deleted.
	// +optional
	ReleaseOnDelete bool `json:"releaseOnDelete,omitempty"`
}

type SecretKeyRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Key defaults to "kubeconfig".
	// +optional
	Key string `json:"key,omitempty"`
}

type ClusterbookClusterStatus struct {
	IP         string             `json:"ip,omitempty"`
	FQDN       string             `json:"fqdn,omitempty"`
	Zone       string             `json:"zone,omitempty"`
	SecretName string             `json:"secretName,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
