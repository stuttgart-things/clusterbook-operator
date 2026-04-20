package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
)

// Minimal but parseable kubeconfig used in tests. The CA and token are
// not validated — the reconciler only extracts fields to build the
// ArgoCD cluster secret.
const fakeKubeconfig = `apiVersion: v1
kind: Config
current-context: ctx
contexts:
- name: ctx
  context:
    cluster: clu
    user: u
clusters:
- name: clu
  cluster:
    server: https://example.com:6443
    certificate-authority-data: ZmFrZS1jYQ==
users:
- name: u
  user:
    token: fake-token
`

func ensureArgoNamespace(ctx context.Context, t *testing.T) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "argocd"}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create argocd ns: %v", err)
	}
}

func mustCreate(ctx context.Context, t *testing.T, obj client.Object) {
	t.Helper()
	if err := k8sClient.Create(ctx, obj); err != nil {
		t.Fatalf("create %T %s: %v", obj, obj.GetName(), err)
	}
}

func TestReconcileGoldenPath(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-golden", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-golden"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "golden"},
		Spec: argov1.ClusterbookClusterSpec{
			NetworkKey:          "10.0.0",
			ClusterName:         "golden",
			KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-golden", Namespace: "argocd"},
			ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-golden"},
			ArgoCDNamespace:     "argocd",
			Labels:              map[string]string{"env": "test"},
		},
	})

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "golden"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var argoSec corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-golden", Namespace: "argocd"}, &argoSec); err != nil {
		t.Fatalf("ArgoCD cluster secret not found: %v", err)
	}
	if argoSec.Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Errorf("argo-secret-type label = %q, want %q", argoSec.Labels["argocd.argoproj.io/secret-type"], "cluster")
	}
	if argoSec.Labels["env"] != "test" {
		t.Errorf("env label missing; got labels %v", argoSec.Labels)
	}
	if got, want := string(argoSec.Data["server"]), "https://10.0.0.42:6443"; got != want {
		t.Errorf("server = %q, want %q", got, want)
	}
	if got, want := string(argoSec.Data["name"]), "golden"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if string(argoSec.Data["config"]) == "" {
		t.Error("config field is empty")
	}

	var fresh argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "golden"}, &fresh); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if fresh.Status.IP != "10.0.0.42" {
		t.Errorf("status.ip = %q, want 10.0.0.42", fresh.Status.IP)
	}
	if fresh.Status.SecretName != "cluster-golden" {
		t.Errorf("status.secretName = %q", fresh.Status.SecretName)
	}
}

func TestReconcileUseFQDNAsServer(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	fake.fqdn = "mycluster.example.com"
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-fqdn", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-fqdn"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "fqdn-cluster"},
		Spec: argov1.ClusterbookClusterSpec{
			NetworkKey:          "10.0.0",
			ClusterName:         "fqdn-cluster",
			CreateDNS:           true,
			UseFQDNAsServer:     true,
			KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-fqdn", Namespace: "argocd"},
			ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-fqdn"},
			ArgoCDNamespace:     "argocd",
		},
	})

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fqdn-cluster"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var argoSec corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-fqdn-cluster", Namespace: "argocd"}, &argoSec); err != nil {
		t.Fatalf("ArgoCD cluster secret not found: %v", err)
	}
	if got, want := string(argoSec.Data["server"]), "https://mycluster.example.com:6443"; got != want {
		t.Errorf("server = %q, want %q", got, want)
	}
}

func TestReconcileFinalizeReleasesIP(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-finalize", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-finalize"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	cr := &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "finalize-me"},
		Spec: argov1.ClusterbookClusterSpec{
			NetworkKey:          "10.0.0",
			ClusterName:         "finalize-me",
			KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-finalize", Namespace: "argocd"},
			ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-finalize"},
			ArgoCDNamespace:     "argocd",
			ReleaseOnDelete:     true,
		},
	}
	mustCreate(ctx, t, cr)

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "finalize-me"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	if err := k8sClient.Delete(ctx, cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "finalize-me"}}); err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}

	if got := fake.releasedCount(); got != 1 {
		t.Errorf("fake.released count = %d, want 1", got)
	}

	var argoSec corev1.Secret
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-finalize-me", Namespace: "argocd"}, &argoSec)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected ArgoCD secret to be gone, got err=%v", err)
	}

	var fresh argov1.ClusterbookCluster
	err = k8sClient.Get(ctx, types.NamespacedName{Name: "finalize-me"}, &fresh)
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected CR to be gone after finalizer removal, got err=%v", err)
	}
}

func TestReconcileDriftTriggersUpdate(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-drift", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-drift"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	cr := &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "drift-cluster"},
		Spec: argov1.ClusterbookClusterSpec{
			NetworkKey:          "10.0.0",
			ClusterName:         "drift-cluster",
			CreateDNS:           false,
			KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-drift", Namespace: "argocd"},
			ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-drift"},
			ArgoCDNamespace:     "argocd",
		},
	}
	mustCreate(ctx, t, cr)

	r := &Reconciler{Client: k8sClient, Scheme: scheme}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "drift-cluster"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if got := fake.updateCount(); got != 0 {
		t.Errorf("initial update count = %d, want 0 (createDNS matches spec)", got)
	}

	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "drift-cluster"}, cr); err != nil {
		t.Fatalf("refetch CR: %v", err)
	}
	cr.Spec.CreateDNS = true
	if err := k8sClient.Update(ctx, cr); err != nil {
		t.Fatalf("mutate CR: %v", err)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "drift-cluster"}}); err != nil {
		t.Fatalf("post-drift reconcile: %v", err)
	}

	if got := fake.updateCount(); got != 1 {
		t.Fatalf("post-drift update count = %d, want 1", got)
	}
	last := fake.lastUpdate()
	if !last.CreateDNS {
		t.Errorf("UpdateIP request CreateDNS = false, want true")
	}
	if last.Status != "ASSIGNED:DNS" {
		t.Errorf("UpdateIP request Status = %q, want ASSIGNED:DNS", last.Status)
	}
	if last.Cluster != "drift-cluster" {
		t.Errorf("UpdateIP request Cluster = %q, want drift-cluster", last.Cluster)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "drift-cluster"}}); err != nil {
		t.Fatalf("steady-state reconcile: %v", err)
	}
	if got := fake.updateCount(); got != 1 {
		t.Errorf("steady-state update count = %d, want still 1 (no spurious update)", got)
	}
}

func TestReconcileReleaseOnDeleteOff(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-keep", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-keep"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	cr := &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "keep-ip"},
		Spec: argov1.ClusterbookClusterSpec{
			NetworkKey:          "10.0.0",
			ClusterName:         "keep-ip",
			KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-keep", Namespace: "argocd"},
			ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-keep"},
			ArgoCDNamespace:     "argocd",
			// ReleaseOnDelete defaults to false
		},
	}
	mustCreate(ctx, t, cr)

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "keep-ip"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	if err := k8sClient.Delete(ctx, cr); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "keep-ip"}}); err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}

	if got := fake.releasedCount(); got != 0 {
		t.Errorf("fake.released count = %d, want 0 (releaseOnDelete=false)", got)
	}
}

func TestReconcileParallel(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-parallel"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})

	names := []string{"par-a", "par-b", "par-c"}
	for _, name := range names {
		mustCreate(ctx, t, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kc-" + name, Namespace: "argocd"},
			Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
		})
		mustCreate(ctx, t, &argov1.ClusterbookCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: argov1.ClusterbookClusterSpec{
				NetworkKey:          "10.0.0",
				ClusterName:         name,
				KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-" + name, Namespace: "argocd"},
				ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-parallel"},
				ArgoCDNamespace:     "argocd",
			},
		})
	}

	r := &Reconciler{Client: k8sClient, Scheme: scheme}

	var wg sync.WaitGroup
	errs := make(chan error, len(names))
	for _, name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: n}}); err != nil {
				errs <- fmt.Errorf("reconcile %s: %w", n, err)
			}
		}(name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	for _, name := range names {
		var s corev1.Secret
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "cluster-" + name, Namespace: "argocd"}, &s); err != nil {
			t.Errorf("argo secret for %s missing: %v", name, err)
		}
	}

	unique := map[string]struct{}{}
	for _, ip := range fake.reservedIPs() {
		unique[ip] = struct{}{}
	}
	if len(unique) != len(names) {
		t.Errorf("expected %d distinct reserved IPs, got %d: %v", len(names), len(unique), unique)
	}
}

func TestReconcileReservesOnlyOnce(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	fake := newFakeClusterbook()
	defer fake.server.Close()

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-once", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-once"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "once"},
		Spec: argov1.ClusterbookClusterSpec{
			NetworkKey:          "10.0.0",
			ClusterName:         "once",
			KubeconfigSecretRef: argov1.SecretKeyRef{Name: "kc-once", Namespace: "argocd"},
			ProviderConfigRef:   corev1.LocalObjectReference{Name: "pc-once"},
			ArgoCDNamespace:     "argocd",
		},
	})

	r := &Reconciler{Client: k8sClient, Scheme: scheme}
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "once"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	if got := fake.reserveCount(); got != 1 {
		t.Errorf("reserve count = %d, want 1 (the fake hands out fresh IPs on every call — the operator should only invoke Reserve once)", got)
	}
	if n := len(fake.reservedIPs()); n != 1 {
		t.Errorf("unique reserved IPs = %d, want 1", n)
	}
}
