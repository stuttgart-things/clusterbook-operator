package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
)

// TestReconcilePrunesRemovedMetadata — issue #121. A label or annotation
// dropped from the spec must disappear from the rendered Secret; keys set
// by somebody else must survive.
func TestReconcilePrunesRemovedMetadata(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-prune", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prune"},
		Spec: argov1.ClusterbookClusterSpec{
			ClusterName:              "prune",
			ClusterType:              "kind",
			SkipReservation:          true,
			PreserveKubeconfigServer: true,
			KubeconfigSecretRef:      &argov1.SecretKeyRef{Name: "kc-prune", Namespace: "argocd"},
			Labels: map[string]string{
				"network-platform/cilium-lb":      "false",
				"network-platform/cilium-gateway": "false",
				"env":                             "dev",
			},
			Annotations: map[string]string{
				"vault-server": "https://vault.example.com",
				"stale-note":   "remove-me",
			},
		},
	})

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "prune"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}

	// Someone else labels / annotates the Secret out of band.
	secretKey := types.NamespacedName{Name: "cluster-prune", Namespace: "argocd"}
	var sec corev1.Secret
	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if sec.Labels["network-platform/cilium-lb"] != "false" {
		t.Fatalf("spec label not rendered: %v", sec.Labels)
	}
	sec.Labels["team/foreign"] = "keep"
	sec.Annotations["team/foreign-note"] = "keep"
	if err := k8sClient.Update(ctx, &sec); err != nil {
		t.Fatalf("add foreign metadata: %v", err)
	}

	// The toggles and one annotation are dropped, clusterType cleared.
	var cr argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, req.NamespacedName, &cr); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	cr.Spec.ClusterType = ""
	cr.Spec.Labels = map[string]string{"env": "dev"}
	cr.Spec.Annotations = map[string]string{"vault-server": "https://vault.example.com"}
	if err := k8sClient.Update(ctx, &cr); err != nil {
		t.Fatalf("update CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}

	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	for _, k := range []string{"network-platform/cilium-lb", "network-platform/cilium-gateway", labelClusterType} {
		if v, ok := sec.Labels[k]; ok {
			t.Errorf("label %s=%q survived its removal from the spec", k, v)
		}
	}
	if _, ok := sec.Annotations["stale-note"]; ok {
		t.Error("annotation stale-note survived its removal from the spec")
	}
	for k, want := range map[string]string{"env": "dev", "team/foreign": "keep", argoSecretTypeLabel: argoSecretTypeValue} {
		if got := sec.Labels[k]; got != want {
			t.Errorf("label %s = %q, want %q", k, got, want)
		}
	}
	for k, want := range map[string]string{"vault-server": "https://vault.example.com", "team/foreign-note": "keep", annotationClusterName: "prune"} {
		if got := sec.Annotations[k]; got != want {
			t.Errorf("annotation %s = %q, want %q", k, got, want)
		}
	}
}

// TestReconcileEnrichPrunesRemovedMetadata — the same for enrich mode, plus
// deletion stripping the unprefixed spec.annotations keys it wrote.
func TestReconcileEnrichPrunesRemovedMetadata(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "cluster-enrich-prune",
			Namespace:   "argocd",
			Labels:      map[string]string{argoSecretTypeLabel: argoSecretTypeValue},
			Annotations: map[string]string{"team/owner": "platform"},
		},
		Data: map[string][]byte{"server": []byte("https://10.1.1.1:6443")},
	})
	mustCreate(ctx, t, &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "enrich-prune"},
		Spec: argov1.ClusterbookClusterSpec{
			ClusterName:              "enrich-prune",
			SkipReservation:          true,
			PreserveKubeconfigServer: true,
			ExistingSecretRef:        &argov1.SecretObjectRef{Name: "cluster-enrich-prune", Namespace: "argocd"},
			Labels:                   map[string]string{"cilium-lb": "false", "env": "dev"},
			Annotations:              map[string]string{"vault-server": "v1", "stale-note": "x"},
		},
	})

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "enrich-prune"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}

	var cr argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, req.NamespacedName, &cr); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	cr.Spec.Labels = map[string]string{"env": "dev"}
	cr.Spec.Annotations = map[string]string{"vault-server": "v2"}
	if err := k8sClient.Update(ctx, &cr); err != nil {
		t.Fatalf("update CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}

	secretKey := types.NamespacedName{Name: "cluster-enrich-prune", Namespace: "argocd"}
	var sec corev1.Secret
	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if _, ok := sec.Labels[clusterbookPrefix+"cilium-lb"]; ok {
		t.Errorf("enrich label cilium-lb survived its removal: %v", sec.Labels)
	}
	if got := sec.Labels[clusterbookPrefix+"env"]; got != "dev" {
		t.Errorf("enrich label env = %q, want dev", got)
	}
	if _, ok := sec.Annotations["stale-note"]; ok {
		t.Error("annotation stale-note survived its removal")
	}
	if got := sec.Annotations["vault-server"]; got != "v2" {
		t.Errorf("annotation vault-server = %q, want v2 (spec change must propagate)", got)
	}

	// Delete: everything the operator wrote goes, foreign metadata stays.
	if err := k8sClient.Delete(ctx, &cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret after delete: %v", err)
	}
	if _, ok := sec.Annotations["vault-server"]; ok {
		t.Error("spec annotation vault-server left behind after delete")
	}
	if got := sec.Annotations["team/owner"]; got != "platform" {
		t.Errorf("foreign annotation team/owner = %q, want platform", got)
	}
	if got := sec.Labels[argoSecretTypeLabel]; got != argoSecretTypeValue {
		t.Errorf("secret-type label = %q, want %q", got, argoSecretTypeValue)
	}
	for k := range sec.Labels {
		if strings.HasPrefix(k, clusterbookPrefix) {
			t.Errorf("clusterbook label %s left behind after delete", k)
		}
	}
}
