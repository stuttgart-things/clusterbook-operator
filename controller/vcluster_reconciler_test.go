package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
)

// ensureNamespace creates a namespace if absent. Tests share the envtest
// API server across runs so all "create" calls must be idempotent.
func ensureNamespace(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := k8sClient.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create ns %s: %v", name, err)
	}
}

// inClusterTargetClient returns the envtest k8sClient regardless of
// rest.Config. Used by tests exercising the targetClusterRef path so the
// "remote" cluster IS the envtest cluster — we can read/write the
// upstream vc-<name> Secret with the same shared client.
func inClusterTargetClient(*rest.Config) (client.Client, error) {
	return k8sClient, nil
}

// getApplication fetches the unstructured Application by name+namespace.
// Returns NotFound directly so callers can branch on it.
func getApplication(ctx context.Context, name, ns string) (*unstructured.Unstructured, error) {
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(argoApplicationGVK)
	return app, k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, app)
}

// TestVclusterReconcileInClusterMinimal — no targetClusterRef, no
// networkKey, no chart values. Verifies the in-cluster pure-provision
// path: the operator skips clusterbook entirely, the external server URL
// uses the in-cluster service DNS, the Application carries the built-in
// kubernetes.default.svc destination, and a child ClusterbookCluster is
// emitted with skipReservation=true.
func TestVclusterReconcileInClusterMinimal(t *testing.T) {
	ctx := context.Background()
	ensureNamespace(ctx, t, "argocd")
	ensureNamespace(ctx, t, "vc-mini")

	// The upstream loft-sh-managed Secret. In real life ArgoCD installs
	// the Helm chart and the chart writes this. In test we pre-create it
	// so the reconciler's step-4 poll finds it on the first pass.
	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-mini-vc", Namespace: "vc-mini"},
		Data:       map[string][]byte{vclusterUpstreamSecretKey: []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.Vcluster{
		ObjectMeta: metav1.ObjectMeta{Name: "mini-vc"},
		Spec: argov1.VclusterSpec{
			ClusterName:     "mini-vc",
			TargetNamespace: "vc-mini",
		},
	})

	r := &VclusterReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "mini-vc"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// External Secret — owned by the parent, written under "kubeconfig"
	// (the default the child ClusterbookCluster reads).
	var ext corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "vc-mini-vc-external", Namespace: "argocd"}, &ext); err != nil {
		t.Fatalf("external secret missing: %v", err)
	}
	if _, ok := ext.Data[vclusterExternalSecretKey]; !ok {
		t.Errorf("external secret missing data.%s", vclusterExternalSecretKey)
	}
	if !ownedByVcluster(&ext, "mini-vc") {
		t.Errorf("external secret missing parent owner ref; got %+v", ext.OwnerReferences)
	}

	// Application — destination must be the built-in in-cluster ref
	// (no targetClusterRef → no named destination).
	app, err := getApplication(ctx, "vcluster-mini-vc", "argocd")
	if err != nil {
		t.Fatalf("application missing: %v", err)
	}
	if got, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "server"); got != "https://kubernetes.default.svc" {
		t.Errorf("destination.server = %q, want in-cluster", got)
	}
	if got, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "namespace"); got != "vc-mini" {
		t.Errorf("destination.namespace = %q, want vc-mini", got)
	}
	if got, _, _ := unstructured.NestedString(app.Object, "spec", "source", "helm", "releaseName"); got != "mini-vc" {
		t.Errorf("helm.releaseName = %q, want mini-vc", got)
	}
	if got, _, _ := unstructured.NestedString(app.Object, "spec", "source", "targetRevision"); got != defaultVclusterChartVersion {
		t.Errorf("targetRevision = %q, want %q", got, defaultVclusterChartVersion)
	}

	// Child ClusterbookCluster — skipReservation, vcluster type,
	// preserveKubeconfigServer, kubeconfigSecretRef pointing at the
	// external Secret with key "kubeconfig".
	var child argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "mini-vc"}, &child); err != nil {
		t.Fatalf("child ClusterbookCluster missing: %v", err)
	}
	if !child.Spec.SkipReservation {
		t.Errorf("child.spec.skipReservation = false, want true")
	}
	if child.Spec.ClusterType != "vcluster" {
		t.Errorf("child.spec.clusterType = %q, want vcluster", child.Spec.ClusterType)
	}
	if !child.Spec.PreserveKubeconfigServer {
		t.Errorf("child.spec.preserveKubeconfigServer = false, want true")
	}
	if k := child.Spec.KubeconfigSecretRef; k == nil || k.Name != "vc-mini-vc-external" || k.Key != vclusterExternalSecretKey {
		t.Errorf("child.spec.kubeconfigSecretRef = %+v", k)
	}

	// External kubeconfig server URL — in-cluster service DNS form,
	// matches the default vcluster cert SANs.
	wantServer := "https://mini-vc.vc-mini"
	if got := serverFromKubeconfig(t, ext.Data[vclusterExternalSecretKey]); got != wantServer {
		t.Errorf("kubeconfig server = %q, want %q", got, wantServer)
	}

	// Status snapshot.
	var fresh argov1.Vcluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "mini-vc"}, &fresh); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if fresh.Status.ArgoCDApplicationRef != "vcluster-mini-vc" {
		t.Errorf("status.argoCDApplicationRef = %q", fresh.Status.ArgoCDApplicationRef)
	}
	if fresh.Status.ClusterbookClusterRef != "mini-vc" {
		t.Errorf("status.clusterbookClusterRef = %q", fresh.Status.ClusterbookClusterRef)
	}
	if fresh.Status.IP != "" {
		t.Errorf("status.ip = %q, want empty (no networkKey)", fresh.Status.IP)
	}
}

// TestVclusterReconcileWithReservation — networkKey set, fakeClusterbook
// returns an FQDN. Verifies operator-managed helm values (loadBalancerIP +
// extraSANs) and that the external kubeconfig points at https://<fqdn>:443.
// Also exercises the requeue-on-missing-upstream-secret branch by running
// reconcile twice: first with the Secret absent (must requeue without
// erroring), then with it present (must succeed).
func TestVclusterReconcileWithReservation(t *testing.T) {
	ctx := context.Background()
	ensureNamespace(ctx, t, "argocd")
	ensureNamespace(ctx, t, "vc-resv")

	fake := newFakeClusterbook()
	fake.fqdn = "vc-resv.example.com"
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-vc-resv"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &argov1.Vcluster{
		ObjectMeta: metav1.ObjectMeta{Name: "resv-vc"},
		Spec: argov1.VclusterSpec{
			ClusterName:       "resv-vc",
			TargetNamespace:   "vc-resv",
			NetworkKey:        "10.0.0",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-vc-resv"},
		},
	})

	r := &VclusterReconciler{Client: k8sClient, Scheme: scheme}

	// First pass — upstream Secret absent. Must requeue (not error) and
	// surface VclusterReady=False.
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "resv-vc"}})
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter on first pass, got %+v", res)
	}
	var partial argov1.Vcluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "resv-vc"}, &partial); err != nil {
		t.Fatalf("get partial: %v", err)
	}
	if c := findCondition(partial.Status.Conditions, conditionVclusterReady); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("expected VclusterReady=False on first pass, got %+v", c)
	}

	// Now create the upstream Secret and re-run.
	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-resv-vc", Namespace: "vc-resv"},
		Data:       map[string][]byte{vclusterUpstreamSecretKey: []byte(fakeKubeconfig)},
	})
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "resv-vc"}}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	// Application helm values must carry the operator overlay.
	app, err := getApplication(ctx, "vcluster-resv-vc", "argocd")
	if err != nil {
		t.Fatalf("application missing: %v", err)
	}
	values, found, err := unstructured.NestedMap(app.Object, "spec", "source", "helm", "valuesObject")
	if err != nil || !found {
		t.Fatalf("helm.valuesObject missing: err=%v found=%v", err, found)
	}
	if got, _, _ := unstructured.NestedString(values, "controlPlane", "service", "spec", "type"); got != "LoadBalancer" {
		t.Errorf("controlPlane.service.spec.type = %q, want LoadBalancer", got)
	}
	if got, _, _ := unstructured.NestedString(values, "controlPlane", "service", "spec", "loadBalancerIP"); got != "10.0.0.42" {
		t.Errorf("controlPlane.service.spec.loadBalancerIP = %q, want 10.0.0.42", got)
	}
	sans, _, _ := unstructured.NestedStringSlice(values, "controlPlane", "proxy", "extraSANs")
	if len(sans) != 1 || sans[0] != "vc-resv.example.com" {
		t.Errorf("controlPlane.proxy.extraSANs = %v, want [vc-resv.example.com]", sans)
	}

	// External kubeconfig server URL — FQDN:port, matching the SAN we
	// just injected.
	var ext corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "vc-resv-vc-external", Namespace: "argocd"}, &ext); err != nil {
		t.Fatalf("external secret missing: %v", err)
	}
	if got := serverFromKubeconfig(t, ext.Data[vclusterExternalSecretKey]); got != "https://vc-resv.example.com:443" {
		t.Errorf("kubeconfig server = %q, want https://vc-resv.example.com:443", got)
	}

	// Status carries the reservation facts.
	var fresh argov1.Vcluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "resv-vc"}, &fresh); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if fresh.Status.IP != "10.0.0.42" || fresh.Status.FQDN != "vc-resv.example.com" || fresh.Status.Zone != "example.com" {
		t.Errorf("status reservation facts unexpected: ip=%q fqdn=%q zone=%q", fresh.Status.IP, fresh.Status.FQDN, fresh.Status.Zone)
	}
}

// TestVclusterReconcileWithTargetClusterRef — pointing the Vcluster at a
// "remote" ArgoCD-registered cluster. The injected NewTargetClient short-
// circuits the rest.Config plumbing and returns the same envtest k8sClient,
// so the upstream Secret read still finds something — what we're really
// testing is that the destination.name path fires and that resolveTarget
// parses the ArgoCD cluster Secret without erroring.
func TestVclusterReconcileWithTargetClusterRef(t *testing.T) {
	ctx := context.Background()
	ensureNamespace(ctx, t, "argocd")
	ensureNamespace(ctx, t, "vc-cross")

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "remote-cluster",
			Namespace: "argocd",
			Labels:    map[string]string{argoSecretTypeLabel: argoSecretTypeValue},
		},
		Data: map[string][]byte{
			"name":   []byte("remote"),
			"server": []byte("https://remote.example.com"),
			"config": []byte(`{"tlsClientConfig":{"insecure":true}}`),
		},
	})
	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-cross-vc", Namespace: "vc-cross"},
		Data:       map[string][]byte{vclusterUpstreamSecretKey: []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.Vcluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cross-vc"},
		Spec: argov1.VclusterSpec{
			ClusterName:      "cross-vc",
			TargetNamespace:  "vc-cross",
			TargetClusterRef: &argov1.SecretObjectRef{Name: "remote-cluster", Namespace: "argocd"},
		},
	})

	r := &VclusterReconciler{Client: k8sClient, Scheme: scheme, NewTargetClient: inClusterTargetClient}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "cross-vc"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	app, err := getApplication(ctx, "vcluster-cross-vc", "argocd")
	if err != nil {
		t.Fatalf("application missing: %v", err)
	}
	if got, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "name"); got != "remote" {
		t.Errorf("destination.name = %q, want remote", got)
	}
	if _, found, _ := unstructured.NestedString(app.Object, "spec", "destination", "server"); found {
		t.Errorf("destination.server should be absent when using destination.name")
	}
}

// TestVclusterReconcileRegisterFalse — provision-only flow. The
// Application + external Secret are still created, but no child
// ClusterbookCluster is emitted.
func TestVclusterReconcileRegisterFalse(t *testing.T) {
	ctx := context.Background()
	ensureNamespace(ctx, t, "argocd")
	ensureNamespace(ctx, t, "vc-noreg")

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-noreg-vc", Namespace: "vc-noreg"},
		Data:       map[string][]byte{vclusterUpstreamSecretKey: []byte(fakeKubeconfig)},
	})
	noReg := false
	mustCreate(ctx, t, &argov1.Vcluster{
		ObjectMeta: metav1.ObjectMeta{Name: "noreg-vc"},
		Spec: argov1.VclusterSpec{
			ClusterName:     "noreg-vc",
			TargetNamespace: "vc-noreg",
			ArgoCD:          argov1.VclusterArgoCD{Register: &noReg},
		},
	})

	r := &VclusterReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "noreg-vc"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if _, err := getApplication(ctx, "vcluster-noreg-vc", "argocd"); err != nil {
		t.Fatalf("application should still exist with register=false: %v", err)
	}
	var ext corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "vc-noreg-vc-external", Namespace: "argocd"}, &ext); err != nil {
		t.Fatalf("external secret should still exist with register=false: %v", err)
	}

	var child argov1.ClusterbookCluster
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "noreg-vc"}, &child)
	if !apierrors.IsNotFound(err) {
		t.Errorf("child ClusterbookCluster should be absent; got err=%v object=%+v", err, child)
	}

	var fresh argov1.Vcluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "noreg-vc"}, &fresh); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if fresh.Status.ClusterbookClusterRef != "" {
		t.Errorf("status.clusterbookClusterRef = %q, want empty", fresh.Status.ClusterbookClusterRef)
	}
}

// TestVclusterReconcileFinalizeOrder — full reconcile, then delete. The
// finalizer must tear down the child + Application and release the IP
// before the parent's finalizer is removed.
func TestVclusterReconcileFinalizeOrder(t *testing.T) {
	ctx := context.Background()
	ensureNamespace(ctx, t, "argocd")
	ensureNamespace(ctx, t, "vc-fin")

	fake := newFakeClusterbook()
	fake.fqdn = "vc-fin.example.com"
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-vc-fin"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-fin-vc", Namespace: "vc-fin"},
		Data:       map[string][]byte{vclusterUpstreamSecretKey: []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.Vcluster{
		ObjectMeta: metav1.ObjectMeta{Name: "fin-vc"},
		Spec: argov1.VclusterSpec{
			ClusterName:       "fin-vc",
			TargetNamespace:   "vc-fin",
			NetworkKey:        "10.0.0",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-vc-fin"},
			ReleaseOnDelete:   true,
		},
	})

	r := &VclusterReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fin-vc"}}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}

	if _, err := getApplication(ctx, "vcluster-fin-vc", "argocd"); err != nil {
		t.Fatalf("application missing before finalize: %v", err)
	}
	var child argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "fin-vc"}, &child); err != nil {
		t.Fatalf("child missing before finalize: %v", err)
	}

	// Trigger deletion. The parent has a finalizer so it'll sit in
	// terminating; the next reconcile drives finalize.
	var parent argov1.Vcluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "fin-vc"}, &parent); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if err := k8sClient.Delete(ctx, &parent); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fin-vc"}}); err != nil {
		t.Fatalf("finalize reconcile: %v", err)
	}

	// Child cluster: the parent issued Delete on it. The child has no
	// finalizer of its own in this test (its reconciler never ran), so it
	// should be gone.
	err := k8sClient.Get(ctx, types.NamespacedName{Name: "fin-vc"}, &child)
	if !apierrors.IsNotFound(err) {
		t.Errorf("child should be deleted; got err=%v", err)
	}

	// Application: same — no finalizers, immediate GC.
	if _, err := getApplication(ctx, "vcluster-fin-vc", "argocd"); !apierrors.IsNotFound(err) {
		t.Errorf("application should be deleted; got err=%v", err)
	}

	// Reservation released.
	if got := fake.releasedCount(); got != 1 {
		t.Errorf("clusterbook release count = %d, want 1", got)
	}

	// Parent is gone too (finalizer removed → kube GC).
	err = k8sClient.Get(ctx, types.NamespacedName{Name: "fin-vc"}, &parent)
	if !apierrors.IsNotFound(err) {
		t.Errorf("parent should be GC'd; got err=%v", err)
	}
}

// TestVclusterReconcileReservesOnlyOnce — the adversarial fakeClusterbook
// hands out a fresh IP on every /reserve. Across five reconciles the
// operator must defend itself (trust status.IP, fall back to GetIPs) so
// /reserve is hit at most once.
func TestVclusterReconcileReservesOnlyOnce(t *testing.T) {
	ctx := context.Background()
	ensureNamespace(ctx, t, "argocd")
	ensureNamespace(ctx, t, "vc-once")

	fake := newFakeClusterbook()
	fake.fqdn = "vc-once.example.com"
	defer fake.server.Close()

	mustCreate(ctx, t, &argov1.ClusterbookProviderConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "pc-vc-once"},
		Spec:       argov1.ClusterbookProviderConfigSpec{APIURL: fake.server.URL},
	})
	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-once-vc", Namespace: "vc-once"},
		Data:       map[string][]byte{vclusterUpstreamSecretKey: []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.Vcluster{
		ObjectMeta: metav1.ObjectMeta{Name: "once-vc"},
		Spec: argov1.VclusterSpec{
			ClusterName:       "once-vc",
			TargetNamespace:   "vc-once",
			NetworkKey:        "10.0.0",
			ProviderConfigRef: corev1.LocalObjectReference{Name: "pc-vc-once"},
		},
	})

	r := &VclusterReconciler{Client: k8sClient, Scheme: scheme}
	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "once-vc"}}); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	if got := fake.reserveCount(); got != 1 {
		t.Errorf("reserve count = %d, want 1 (adversarial fake; operator must defend itself)", got)
	}
}

// ---- small helpers ----

func ownedByVcluster(o metav1.Object, name string) bool {
	for _, ref := range o.GetOwnerReferences() {
		if ref.Kind == "Vcluster" && ref.Name == name {
			return true
		}
	}
	return false
}

func findCondition(conds []metav1.Condition, typ string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == typ {
			return &conds[i]
		}
	}
	return nil
}

func serverFromKubeconfig(t *testing.T, raw []byte) string {
	t.Helper()
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	ctxEntry := cfg.Contexts[cfg.CurrentContext]
	if ctxEntry == nil {
		t.Fatalf("current-context %q missing", cfg.CurrentContext)
	}
	cluster := cfg.Clusters[ctxEntry.Cluster]
	if cluster == nil {
		t.Fatalf("cluster %q missing", ctxEntry.Cluster)
	}
	return cluster.Server
}
