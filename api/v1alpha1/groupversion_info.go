// Package v1alpha1 contains API types for the clusterbook-operator.
// +kubebuilder:object:generate=true
// +groupName=clusterbook.stuttgart-things.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "clusterbook.stuttgart-things.com", Version: "v1alpha1"}
	//nolint:staticcheck // scheme.Builder is deprecated in controller-runtime v0.24 but remains the standard kubebuilder-generated pattern; the object-based Register API is used across all api types.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&ClusterbookCluster{}, &ClusterbookClusterList{})
}
