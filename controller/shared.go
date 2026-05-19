// Package controller — shared.go holds helpers used by every reconciler in
// this package. They take a client.Client argument rather than a receiver so
// they can be called from any reconciler (including future ones) without
// embedding or duplication.
package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	cbkclient "github.com/stuttgart-things/clusterbook-operator/pkg/client"
)

// loadProviderConfig fetches a cluster-scoped ClusterbookProviderConfig by name.
func loadProviderConfig(ctx context.Context, c client.Client, name string) (*argov1.ClusterbookProviderConfig, error) {
	var pc argov1.ClusterbookProviderConfig
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &pc); err != nil {
		return nil, err
	}
	return &pc, nil
}

// newClusterbookClient builds a clusterbook REST client from a ProviderConfig,
// loading the optional custom-CA secret when referenced.
func newClusterbookClient(ctx context.Context, c client.Client, pc *argov1.ClusterbookProviderConfig) (*cbkclient.Client, error) {
	opts := &cbkclient.TLSOptions{InsecureSkipVerify: pc.Spec.InsecureSkipVerify}
	if ref := pc.Spec.CustomCASecretRef; ref != nil {
		var s corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &s); err != nil {
			return nil, err
		}
		key := ref.Key
		if key == "" {
			key = "ca.crt"
		}
		opts.CustomCA = string(s.Data[key])
	}
	return cbkclient.NewClient(pc.Spec.APIURL, opts)
}
