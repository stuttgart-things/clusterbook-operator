package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="has(self.sinks) && (has(self.sinks.configMap) || has(self.sinks.clusterSecretLabels))",message="at least one sink must be set (sinks.configMap or sinks.clusterSecretLabels)"
type ClusterbookAllocationSpec struct {
	// NetworkKey is the clusterbook network pool (e.g. "10.31.101").
	NetworkKey string `json:"networkKey"`

	// Name is the reservation key under which clusterbook tracks this IP.
	Name string `json:"name"`

	// CreateDNS asks clusterbook to create a wildcard DNS record for the
	// reserved IP. The resulting FQDN is exposed in status and written to
	// every configured sink.
	// +optional
	CreateDNS bool `json:"createDNS,omitempty"`

	// ProviderConfigRef points to a ClusterbookProviderConfig with the
	// clusterbook API URL and TLS options.
	ProviderConfigRef corev1.LocalObjectReference `json:"providerConfigRef"`

	// Sinks controls where the reservation facts (IP, FQDN, zone) are
	// published. At least one sink must be set; both can be set together.
	Sinks AllocationSinks `json:"sinks"`

	// ReleaseOnDelete releases the clusterbook IP when the CR is deleted.
	// +optional
	ReleaseOnDelete bool `json:"releaseOnDelete,omitempty"`
}

// AllocationSinks lists every place the operator publishes the
// allocation facts. Each sink is independent — presence of one does not
// affect the others.
type AllocationSinks struct {
	// ConfigMap creates (and owns) a ConfigMap with the reservation facts
	// as plain string keys — convenient for ApplicationSet list/plugin
	// generators, Helm value files, or any consumer that can read a
	// ConfigMap.
	// +optional
	ConfigMap *ConfigMapSink `json:"configMap,omitempty"`

	// ClusterSecretLabels points at an existing ArgoCD cluster Secret
	// and merges clusterbook-prefixed labels and annotations onto it.
	// The Secret's data is never touched and no owner reference is set —
	// same contract as ClusterbookCluster's enrich mode. Useful when the
	// consumer is the built-in ApplicationSet Cluster generator.
	// +optional
	ClusterSecretLabels *SecretObjectRef `json:"clusterSecretLabels,omitempty"`
}

// ConfigMapSink identifies the ConfigMap the operator writes the
// allocation facts into.
type ConfigMapSink struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type ClusterbookAllocationStatus struct {
	// IP is the IP reserved in clusterbook.
	IP string `json:"ip,omitempty"`

	// FQDN is the FQDN returned by clusterbook (only when createDNS=true).
	FQDN string `json:"fqdn,omitempty"`

	// Zone is the DNS zone returned by clusterbook.
	Zone string `json:"zone,omitempty"`

	// ConfigMapRef echoes spec.sinks.configMap when that sink is active.
	// +optional
	ConfigMapRef *ConfigMapSink `json:"configMapRef,omitempty"`

	// ClusterSecretRef echoes spec.sinks.clusterSecretLabels when that
	// sink is active.
	// +optional
	ClusterSecretRef *SecretObjectRef `json:"clusterSecretRef,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cbka
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=`.spec.networkKey`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ip`
// +kubebuilder:printcolumn:name="FQDN",type=string,JSONPath=`.status.fqdn`
type ClusterbookAllocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterbookAllocationSpec   `json:"spec,omitempty"`
	Status ClusterbookAllocationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterbookAllocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterbookAllocation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterbookAllocation{}, &ClusterbookAllocationList{})
}
