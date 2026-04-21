package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
)

func TestLBReconcileGoldenPath(t *testing.T) {
	ctx := context.Background()

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-lb-golden"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookLoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Name: "lb-golden"},
		Spec: argov1.ClusterbookLoadBalancerSpec{
			NetworkKey:        "10.0.0",
			Name:              "lb-golden",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-lb-golden"},
			CiliumPool: &argov1.CiliumPoolTarget{
				ServiceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "ingress"},
				},
			},
		},
	})

	r := &LoadBalancerReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "lb-golden"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	pool := &unstructured.Unstructured{}
	pool.SetGroupVersionKind(ciliumIPPoolGVK)
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "lb-golden-pool"}, pool); err != nil {
		t.Fatalf("IP pool not found: %v", err)
	}

	blocks, found, err := unstructured.NestedSlice(pool.Object, "spec", "blocks")
	if err != nil || !found {
		t.Fatalf("spec.blocks missing: err=%v found=%v", err, found)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	cidr, _, _ := unstructured.NestedString(blocks[0].(map[string]interface{}), "cidr")
	if cidr != "10.0.0.42/32" {
		t.Errorf("cidr = %q, want 10.0.0.42/32", cidr)
	}

	sel, _, _ := unstructured.NestedMap(pool.Object, "spec", "serviceSelector", "matchLabels")
	if got := sel["app"]; got != "ingress" {
		t.Errorf("serviceSelector.matchLabels.app = %v, want ingress", got)
	}

	if got := pool.GetAnnotations()["clusterbook.stuttgart-things.com/ip"]; got != "10.0.0.42" {
		t.Errorf("ip annotation = %q, want 10.0.0.42", got)
	}

	var fresh argov1.ClusterbookLoadBalancer
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "lb-golden"}, &fresh); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if fresh.Status.IP != "10.0.0.42" {
		t.Errorf("status.ip = %q, want 10.0.0.42", fresh.Status.IP)
	}
	if fresh.Status.PoolName != "lb-golden-pool" {
		t.Errorf("status.poolName = %q, want lb-golden-pool", fresh.Status.PoolName)
	}
}

func TestLBReconcileReservesOnlyOnce(t *testing.T) {
	ctx := context.Background()

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-lb-once"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookLoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Name: "lb-once"},
		Spec: argov1.ClusterbookLoadBalancerSpec{
			NetworkKey:        "10.0.0",
			Name:              "lb-once",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-lb-once"},
			CiliumPool:        &argov1.CiliumPoolTarget{},
		},
	})

	r := &LoadBalancerReconciler{Client: k8sClient, Scheme: scheme}
	for i := 0; i < 4; i++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "lb-once"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	if got := fake.reserveCount(); got != 1 {
		t.Errorf("reserve count = %d, want 1 (adversarial fake; operator should only reserve once)", got)
	}
}

// TestLBReconcileServiceRefCreate — in serviceRef mode the operator sets
// .spec.loadBalancerIP on the target Service and records its previous
// loadBalancerIP on the CR so finalize can restore it.
func TestLBReconcileServiceRefCreate(t *testing.T) {
	ctx := context.Background()

	fake := newFakeClusterbook()
	defer fake.server.Close()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx"}}
	_ = k8sClient.Create(ctx, ns)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx", Namespace: "ingress-nginx"},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Port: 80}},
			LoadBalancerIP: "10.0.0.99",
		},
	}
	mustCreate(ctx, t, svc)

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-lb-svc"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookLoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Name: "lb-svc"},
		Spec: argov1.ClusterbookLoadBalancerSpec{
			NetworkKey:        "10.0.0",
			Name:              "lb-svc",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-lb-svc"},
			ServiceRef:        &argov1.ServiceObjectRef{Name: "ingress-nginx", Namespace: "ingress-nginx"},
		},
	})

	r := &LoadBalancerReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "lb-svc"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "ingress-nginx", Namespace: "ingress-nginx"}, &got); err != nil {
		t.Fatalf("get target Service: %v", err)
	}
	if got.Spec.LoadBalancerIP != "10.0.0.42" {
		t.Errorf("Service.spec.loadBalancerIP = %q, want 10.0.0.42", got.Spec.LoadBalancerIP)
	}

	var fresh argov1.ClusterbookLoadBalancer
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "lb-svc"}, &fresh); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if prev := fresh.Annotations["clusterbook.stuttgart-things.com/previous-loadbalancer-ip"]; prev != "10.0.0.99" {
		t.Errorf("previous-loadbalancer-ip annotation = %q, want 10.0.0.99", prev)
	}
	if fresh.Status.IP != "10.0.0.42" {
		t.Errorf("status.ip = %q", fresh.Status.IP)
	}
	if fresh.Status.PoolName != "" {
		t.Errorf("status.poolName = %q, want empty in serviceRef mode", fresh.Status.PoolName)
	}
	if fresh.Status.TargetServiceRef == nil || fresh.Status.TargetServiceRef.Name != "ingress-nginx" {
		t.Errorf("status.targetServiceRef = %+v", fresh.Status.TargetServiceRef)
	}
}

// TestLBReconcileServiceRefRestoresOldIPOnDelete — finalize must put
// back whatever loadBalancerIP the Service had before the operator
// patched it, including the empty string if there was nothing to begin
// with. The Service itself is never deleted.
func TestLBReconcileServiceRefRestoresOldIPOnDelete(t *testing.T) {
	ctx := context.Background()

	fake := newFakeClusterbook()
	defer fake.server.Close()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lb-restore-ns"}}
	_ = k8sClient.Create(ctx, ns)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-svc", Namespace: "lb-restore-ns"},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Port: 80}},
			// No pre-existing LoadBalancerIP — we expect finalize to clear
			// whatever the operator wrote.
		},
	}
	mustCreate(ctx, t, svc)

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-lb-restore"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	cr := &argov1.ClusterbookLoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Name: "lb-restore"},
		Spec: argov1.ClusterbookLoadBalancerSpec{
			NetworkKey:        "10.0.0",
			Name:              "lb-restore",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-lb-restore"},
			ServiceRef:        &argov1.ServiceObjectRef{Name: "restore-svc", Namespace: "lb-restore-ns"},
			ReleaseOnDelete:   true,
		},
	}
	mustCreate(ctx, t, cr)

	r := &LoadBalancerReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "lb-restore"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	var patched corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "restore-svc", Namespace: "lb-restore-ns"}, &patched); err != nil {
		t.Fatalf("get patched Service: %v", err)
	}
	if patched.Spec.LoadBalancerIP == "" {
		t.Fatalf("operator did not set loadBalancerIP on target Service")
	}

	if err := k8sClient.Delete(ctx, cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "lb-restore"}}); err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}

	var restored corev1.Service
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "restore-svc", Namespace: "lb-restore-ns"}, &restored); err != nil {
		t.Fatalf("Service missing after finalize — should have been preserved: %v", err)
	}
	if restored.Spec.LoadBalancerIP != "" {
		t.Errorf("Service.spec.loadBalancerIP = %q, want empty (restored from nil)", restored.Spec.LoadBalancerIP)
	}

	if got := fake.releasedCount(); got != 1 {
		t.Errorf("clusterbook release count = %d, want 1", got)
	}
}
