package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
)

func TestAllocationReconcileConfigMapSink(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	fake.fqdn = "app-frontend.example.com"
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-alloc-cm"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: "alloc-cm"},
		Spec: argov1.ClusterbookAllocationSpec{
			NetworkKey:        "10.0.0",
			Name:              "alloc-cm",
			CreateDNS:         true,
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-alloc-cm"},
			Sinks: argov1.AllocationSinks{
				ConfigMap: &argov1.ConfigMapSink{Name: "alloc-cm-facts", Namespace: "argocd"},
			},
		},
	})

	r := &AllocationReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-cm"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var cm corev1.ConfigMap
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "alloc-cm-facts", Namespace: "argocd"}, &cm); err != nil {
		t.Fatalf("sink ConfigMap not found: %v", err)
	}
	if cm.Data["ip"] != "10.0.0.42" {
		t.Errorf("data.ip = %q, want 10.0.0.42", cm.Data["ip"])
	}
	if cm.Data["fqdn"] != "app-frontend.example.com" {
		t.Errorf("data.fqdn = %q, want app-frontend.example.com", cm.Data["fqdn"])
	}
	if cm.Data["zone"] != "example.com" {
		t.Errorf("data.zone = %q, want example.com", cm.Data["zone"])
	}
	if cm.Data["networkKey"] != "10.0.0" {
		t.Errorf("data.networkKey = %q", cm.Data["networkKey"])
	}
	if cm.Data["name"] != "alloc-cm" {
		t.Errorf("data.name = %q", cm.Data["name"])
	}

	// Owner reference points back at the CR so a CR delete GCs the CM.
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Kind != "ClusterbookAllocation" {
		t.Errorf("owner refs = %+v, want single ClusterbookAllocation", cm.OwnerReferences)
	}

	var fresh argov1.ClusterbookAllocation
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "alloc-cm"}, &fresh); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if fresh.Status.IP != "10.0.0.42" {
		t.Errorf("status.ip = %q", fresh.Status.IP)
	}
	if fresh.Status.FQDN != "app-frontend.example.com" {
		t.Errorf("status.fqdn = %q", fresh.Status.FQDN)
	}
	if fresh.Status.ConfigMapRef == nil || fresh.Status.ConfigMapRef.Name != "alloc-cm-facts" {
		t.Errorf("status.configMapRef = %+v", fresh.Status.ConfigMapRef)
	}
}

func TestAllocationReconcileClusterSecretSink(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	fake.fqdn = "prod-a.example.com"
	defer fake.server.Close()

	preExisting := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-prod-a",
			Namespace: "argocd",
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "cluster",
				"owner":                          "platform",
			},
		},
		Data: map[string][]byte{
			"name":   []byte("prod-a"),
			"server": []byte("https://10.99.99.99:6443"),
			"config": []byte(`{"tlsClientConfig":{"insecure":true}}`),
		},
	}
	mustCreate(ctx, t, preExisting)

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-alloc-sec"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: "alloc-sec"},
		Spec: argov1.ClusterbookAllocationSpec{
			NetworkKey:        "10.0.0",
			Name:              "alloc-sec",
			CreateDNS:         true,
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-alloc-sec"},
			Sinks: argov1.AllocationSinks{
				ClusterSecretLabels: &argov1.SecretObjectRef{Name: "cluster-prod-a", Namespace: "argocd"},
			},
		},
	})

	r := &AllocationReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-sec"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-prod-a", Namespace: "argocd"}, &got); err != nil {
		t.Fatalf("get target Secret: %v", err)
	}
	if got.Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Errorf("pre-existing argocd label lost; labels = %v", got.Labels)
	}
	if got.Labels["owner"] != "platform" {
		t.Errorf("pre-existing owner label lost; labels = %v", got.Labels)
	}
	if got.Labels["clusterbook.stuttgart-things.com/allocation-name"] != "alloc-sec" {
		t.Errorf("allocation-name label missing/wrong: %v", got.Labels)
	}
	if got.Annotations["clusterbook.stuttgart-things.com/ip"] != "10.0.0.42" {
		t.Errorf("ip annotation = %q", got.Annotations["clusterbook.stuttgart-things.com/ip"])
	}
	if got.Annotations["clusterbook.stuttgart-things.com/fqdn"] != "prod-a.example.com" {
		t.Errorf("fqdn annotation = %q", got.Annotations["clusterbook.stuttgart-things.com/fqdn"])
	}
	// data must not have been mutated
	if string(got.Data["server"]) != "https://10.99.99.99:6443" {
		t.Errorf("data.server mutated: %q", got.Data["server"])
	}
	if len(got.OwnerReferences) != 0 {
		t.Errorf("enrich mode set owner references: %v", got.OwnerReferences)
	}
}

func TestAllocationReconcileFinalizeCleans(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	pre := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-cleanup",
			Namespace: "argocd",
			Labels:    map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
		},
		Data: map[string][]byte{"name": []byte("cleanup")},
	}
	mustCreate(ctx, t, pre)

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-alloc-clean"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	cr := &argov1.ClusterbookAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: "alloc-clean"},
		Spec: argov1.ClusterbookAllocationSpec{
			NetworkKey:        "10.0.0",
			Name:              "alloc-clean",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-alloc-clean"},
			Sinks: argov1.AllocationSinks{
				ConfigMap:           &argov1.ConfigMapSink{Name: "alloc-clean-facts", Namespace: "argocd"},
				ClusterSecretLabels: &argov1.SecretObjectRef{Name: "cluster-cleanup", Namespace: "argocd"},
			},
			ReleaseOnDelete: true,
		},
	}
	mustCreate(ctx, t, cr)

	r := &AllocationReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-clean"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	if err := k8sClient.Delete(ctx, cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-clean"}}); err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}

	// Owned ConfigMap deleted.
	var cm corev1.ConfigMap
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "alloc-clean-facts", Namespace: "argocd"}, &cm)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected ConfigMap gone after finalize, got err=%v", err)
	}

	// Cluster Secret survives, prefix-scoped metadata stripped.
	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-cleanup", Namespace: "argocd"}, &secret); err != nil {
		t.Fatalf("target Secret must remain after finalize: %v", err)
	}
	for k := range secret.Labels {
		if strings.HasPrefix(k, "clusterbook.stuttgart-things.com/") {
			t.Errorf("prefix-scoped label remained: %s", k)
		}
	}
	for k := range secret.Annotations {
		if strings.HasPrefix(k, "clusterbook.stuttgart-things.com/") {
			t.Errorf("prefix-scoped annotation remained: %s", k)
		}
	}
	if secret.Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Errorf("argocd label stripped: %v", secret.Labels)
	}

	// releaseOnDelete was true — clusterbook IP released.
	if got := fake.releasedCount(); got != 1 {
		t.Errorf("release count = %d, want 1", got)
	}
}

func TestAllocationReconcileReservesOnlyOnce(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-alloc-once"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: "alloc-once"},
		Spec: argov1.ClusterbookAllocationSpec{
			NetworkKey:        "10.0.0",
			Name:              "alloc-once",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-alloc-once"},
			Sinks: argov1.AllocationSinks{
				ConfigMap: &argov1.ConfigMapSink{Name: "alloc-once-facts", Namespace: "argocd"},
			},
		},
	})

	r := &AllocationReconciler{Client: k8sClient, Scheme: scheme}
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-once"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if got := fake.reserveCount(); got != 1 {
		t.Errorf("reserve count = %d, want 1 (adversarial fake)", got)
	}
}
