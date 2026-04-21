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
	// Allocation facts are written under a name-scoped key prefix so
	// multiple allocations can coexist on the same Secret.
	if got.Labels["clusterbook.stuttgart-things.com/allocation-alloc-sec"] != "true" {
		t.Errorf("presence label missing: %v", got.Labels)
	}
	if got.Labels["clusterbook.stuttgart-things.com/allocation-alloc-sec-ip"] != "10.0.0.42" {
		t.Errorf("namespaced ip label = %q", got.Labels["clusterbook.stuttgart-things.com/allocation-alloc-sec-ip"])
	}
	if got.Labels["clusterbook.stuttgart-things.com/allocation-alloc-sec-zone"] != "example.com" {
		t.Errorf("namespaced zone label = %q", got.Labels["clusterbook.stuttgart-things.com/allocation-alloc-sec-zone"])
	}
	if got.Annotations["clusterbook.stuttgart-things.com/allocation-alloc-sec-ip"] != "10.0.0.42" {
		t.Errorf("namespaced ip annotation = %q", got.Annotations["clusterbook.stuttgart-things.com/allocation-alloc-sec-ip"])
	}
	if got.Annotations["clusterbook.stuttgart-things.com/allocation-alloc-sec-fqdn"] != "prod-a.example.com" {
		t.Errorf("namespaced fqdn annotation = %q", got.Annotations["clusterbook.stuttgart-things.com/allocation-alloc-sec-fqdn"])
	}
	// The bare un-namespaced keys must NOT be written — they're reserved
	// for ClusterbookCluster's own cluster-registration annotations.
	if _, set := got.Annotations["clusterbook.stuttgart-things.com/ip"]; set {
		t.Errorf("bare ip annotation must NOT be set by allocation enrich: %v", got.Annotations)
	}
	// data must not have been mutated
	if string(got.Data["server"]) != "https://10.99.99.99:6443" {
		t.Errorf("data.server mutated: %q", got.Data["server"])
	}
	if len(got.OwnerReferences) != 0 {
		t.Errorf("enrich mode set owner references: %v", got.OwnerReferences)
	}
}

// TestAllocationReconcileTwoAllocationsCoexist — two allocations
// enriching the same cluster Secret must not overwrite each other's
// facts. Each allocation's metadata lives under its own
// allocation-<spec.name>-* key namespace.
func TestAllocationReconcileTwoAllocationsCoexist(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	fake.fqdn = "shared.example.com"
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-shared",
			Namespace: "argocd",
			Labels:    map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
		},
		Data: map[string][]byte{"name": []byte("shared")},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-coexist"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	for _, name := range []string{"api-shared", "lb-shared"} {
		mustCreate(ctx, t, &argov1.ClusterbookAllocation{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: argov1.ClusterbookAllocationSpec{
				NetworkKey:        "10.0.0",
				Name:              name,
				CreateDNS:         true,
				ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-coexist"},
				Sinks: argov1.AllocationSinks{
					ClusterSecretLabels: &argov1.SecretObjectRef{Name: "cluster-shared", Namespace: "argocd"},
				},
			},
		})
	}

	r := &AllocationReconciler{Client: k8sClient, Scheme: scheme}
	for _, name := range []string{"api-shared", "lb-shared"} {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
			t.Fatalf("reconcile %s: %v", name, err)
		}
	}

	var got corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-shared", Namespace: "argocd"}, &got); err != nil {
		t.Fatalf("get cluster-shared: %v", err)
	}

	// Both allocations must have left their IP on the Secret under
	// non-colliding keys, and each must match a distinct reservation.
	apiIP := got.Annotations["clusterbook.stuttgart-things.com/allocation-api-shared-ip"]
	lbIP := got.Annotations["clusterbook.stuttgart-things.com/allocation-lb-shared-ip"]
	if apiIP == "" || lbIP == "" {
		t.Fatalf("missing per-allocation ip annotations: %v", got.Annotations)
	}
	if apiIP == lbIP {
		t.Errorf("expected two distinct IPs, both allocations have %q", apiIP)
	}
	// Presence labels are also namespaced.
	if got.Labels["clusterbook.stuttgart-things.com/allocation-api-shared"] != "true" ||
		got.Labels["clusterbook.stuttgart-things.com/allocation-lb-shared"] != "true" {
		t.Errorf("missing per-allocation presence labels: %v", got.Labels)
	}
	// The pre-existing Secret's foreign label stays.
	if got.Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Errorf("foreign label stripped: %v", got.Labels)
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

// TestAllocationReconcileSurvivesClusterFieldMangling — reproduces a
// clusterbook upstream bug: when a reservation is made with
// createDNS=true, the /ips listing comes back with cluster="DNS"
// instead of the requested name. The name-match path in ensureReservation
// can't recover, so subsequent reconciles would re-call Reserve and
// drain the pool. Guard: trust cr.Status.IP once set.
func TestAllocationReconcileSurvivesClusterFieldMangling(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-alloc-mangle"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookAllocation{
		ObjectMeta: metav1.ObjectMeta{Name: "alloc-mangle"},
		Spec: argov1.ClusterbookAllocationSpec{
			NetworkKey:        "10.0.0",
			Name:              "alloc-mangle",
			CreateDNS:         true,
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-alloc-mangle"},
			Sinks: argov1.AllocationSinks{
				ConfigMap: &argov1.ConfigMapSink{Name: "alloc-mangle-facts", Namespace: "argocd"},
			},
		},
	})

	r := &AllocationReconciler{Client: k8sClient, Scheme: scheme}
	// First reconcile — normal reserve.
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-mangle"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	// Simulate the upstream bug: the reservation we just made comes back
	// in the listing with the cluster field rewritten to "DNS".
	fake.mangleCluster("10.0.0.42", "DNS")

	// A few more reconciles — none should call Reserve again, even though
	// the name-match in the listing now fails.
	for i := 0; i < 3; i++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "alloc-mangle"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	if got := fake.reserveCount(); got != 1 {
		t.Errorf("reserve count = %d, want 1 (cr.Status.IP must shield against listing mismatches)", got)
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
