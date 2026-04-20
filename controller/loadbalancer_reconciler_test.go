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
