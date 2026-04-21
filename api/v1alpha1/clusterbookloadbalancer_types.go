package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:validation:XValidation:rule="(has(self.ciliumPool) ? 1 : 0) + (has(self.serviceRef) ? 1 : 0) == 1",message="exactly one of ciliumPool or serviceRef must be set"
type ClusterbookLoadBalancerSpec struct {
	// NetworkKey is the clusterbook network pool (e.g. "10.31.101").
	NetworkKey string `json:"networkKey"`

	// Name is the reservation key under which clusterbook tracks this LB IP.
	Name string `json:"name"`

	// CreateDNS asks clusterbook to create a wildcard DNS record for the
	// reserved IP. Useful when the IP backs an Ingress or Gateway API
	// frontend — the DNS is managed by clusterbook's PowerDNS integration.
	// +optional
	CreateDNS bool `json:"createDNS,omitempty"`

	// ProviderConfigRef points to a ClusterbookProviderConfig with the
	// clusterbook API URL and TLS options.
	ProviderConfigRef corev1.LocalObjectReference `json:"providerConfigRef"`

	// CiliumPool creates a CiliumLoadBalancerIPPool pinned to the reserved
	// IP. Mutually exclusive with ServiceRef.
	// +optional
	CiliumPool *CiliumPoolTarget `json:"ciliumPool,omitempty"`

	// ServiceRef patches .spec.loadBalancerIP on an existing Service with
	// the reserved IP. The prior loadBalancerIP is recorded in a CR
	// annotation and restored when the CR is deleted. Mutually exclusive
	// with CiliumPool.
	// +optional
	ServiceRef *ServiceObjectRef `json:"serviceRef,omitempty"`

	// ReleaseOnDelete releases the clusterbook IP when the CR is deleted.
	// +optional
	ReleaseOnDelete bool `json:"releaseOnDelete,omitempty"`
}

// ServiceObjectRef identifies a Service by name and namespace.
type ServiceObjectRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type CiliumPoolTarget struct {
	// PoolName is the name of the CiliumLoadBalancerIPPool to create.
	// Defaults to "<spec.name>-pool".
	// +optional
	PoolName string `json:"poolName,omitempty"`

	// Namespace for the CiliumLoadBalancerIPPool. CiliumLoadBalancerIPPool
	// is actually cluster-scoped in Cilium; this field is kept empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// ServiceSelector is copied verbatim onto the IP pool's
	// spec.serviceSelector. Services matching this selector get an IP
	// from the pool.
	// +optional
	ServiceSelector *metav1.LabelSelector `json:"serviceSelector,omitempty"`
}

type ClusterbookLoadBalancerStatus struct {
	// IP is the IP reserved in clusterbook and written to the CiliumPool.
	IP string `json:"ip,omitempty"`

	// FQDN is the FQDN returned by clusterbook (only when createDNS=true).
	FQDN string `json:"fqdn,omitempty"`

	// Zone is the DNS zone returned by clusterbook.
	Zone string `json:"zone,omitempty"`

	// PoolName is the name of the CiliumLoadBalancerIPPool created by
	// the operator (only set in CiliumPool mode).
	PoolName string `json:"poolName,omitempty"`

	// TargetServiceRef echoes spec.serviceRef when that target mode is
	// active. Empty in CiliumPool mode.
	// +optional
	TargetServiceRef *ServiceObjectRef `json:"targetServiceRef,omitempty"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=cblb
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=`.spec.networkKey`
// +kubebuilder:printcolumn:name="IP",type=string,JSONPath=`.status.ip`
// +kubebuilder:printcolumn:name="FQDN",type=string,JSONPath=`.status.fqdn`
// +kubebuilder:printcolumn:name="Pool",type=string,JSONPath=`.status.poolName`
type ClusterbookLoadBalancer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterbookLoadBalancerSpec   `json:"spec,omitempty"`
	Status ClusterbookLoadBalancerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterbookLoadBalancerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterbookLoadBalancer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterbookLoadBalancer{}, &ClusterbookLoadBalancerList{})
}
