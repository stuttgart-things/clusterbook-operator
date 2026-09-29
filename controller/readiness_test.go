package controller

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
)

func probeUnreachable(context.Context, *rest.Config) error {
	return errors.New("readiness probe disabled in tests")
}

// TestProbeReadyz drives the real probe against a TLS server, with the CA
// and bearer token passed through the ArgoCD config JSON exactly as the
// rendered Secret carries them.
func TestProbeReadyz(t *testing.T) {
	const token = "s3cret"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	argoCfg := func(tok string) []byte {
		b, err := json.Marshal(argoClusterConfig{
			BearerToken:     tok,
			TLSClientConfig: argoTLSClientConfig{CAData: caPEM},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	cfg, err := restConfigFromArgo(srv.URL, argoCfg(token))
	if err != nil {
		t.Fatalf("restConfigFromArgo: %v", err)
	}
	if err := probeReadyz(context.Background(), cfg); err != nil {
		t.Errorf("probe with valid token: %v", err)
	}

	cfg, err = restConfigFromArgo(srv.URL, argoCfg("wrong"))
	if err != nil {
		t.Fatalf("restConfigFromArgo: %v", err)
	}
	if err := probeReadyz(context.Background(), cfg); err == nil {
		t.Error("probe with wrong token succeeded, want 401 error")
	}

	srv.Close()
	if err := probeReadyz(context.Background(), cfg); err == nil {
		t.Error("probe against closed server succeeded")
	}
}

// TestReconcileClusterReadyLatches — issue #119. The cluster-ready label
// must not appear while the API does not answer, must appear on the first
// successful probe, and must then stay without further probing.
func TestReconcileClusterReadyLatches(t *testing.T) {
	ctx := context.Background()
	ensureArgoNamespace(ctx, t)

	mustCreate(ctx, t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kc-ready", Namespace: "argocd"},
		Data:       map[string][]byte{"kubeconfig": []byte(fakeKubeconfig)},
	})
	mustCreate(ctx, t, &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "ready"},
		Spec: argov1.ClusterbookClusterSpec{
			ClusterName:              "ready",
			SkipReservation:          true,
			PreserveKubeconfigServer: true,
			KubeconfigSecretRef:      &argov1.SecretKeyRef{Name: "kc-ready", Namespace: "argocd"},
		},
	})

	var (
		probeErr   = errors.New("dial tcp: no route to host")
		probeCalls int
		probedHost string
		probedTok  string
	)
	r := &Reconciler{Client: k8sClient, Scheme: scheme, ProbeCluster: func(_ context.Context, cfg *rest.Config) error {
		probeCalls++
		probedHost, probedTok = cfg.Host, cfg.BearerToken
		return probeErr
	}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "ready"}}
	secretKey := types.NamespacedName{Name: "cluster-ready", Namespace: "argocd"}

	// 1) API not answering: no label, condition False, requeue.
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}
	if res.RequeueAfter != probeRequeue {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, probeRequeue)
	}
	if probedHost != "https://example.com:6443" || probedTok != "fake-token" {
		t.Errorf("probed %q with token %q, want the rendered server and credentials", probedHost, probedTok)
	}
	var sec corev1.Secret
	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if _, ok := sec.Labels[labelClusterReady]; ok {
		t.Errorf("cluster-ready label set before the API answered: %v", sec.Labels)
	}
	cond := reachableCondition(ctx, t, "ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "ProbeFailed" {
		t.Errorf("ClusterReachable = %+v, want False/ProbeFailed", cond)
	}

	// 2) API answers: label stamped, status latched, no requeue.
	probeErr = nil
	res, err = r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v after success, want 0", res.RequeueAfter)
	}
	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if got := sec.Labels[labelClusterReady]; got != "true" {
		t.Errorf("cluster-ready label = %q, want \"true\"", got)
	}
	var cr argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, req.NamespacedName, &cr); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if !cr.Status.ClusterReady {
		t.Error("status.clusterReady = false after a successful probe")
	}

	// 3) Cluster goes away later: latched, so no probe and the label stays.
	probeErr = errors.New("gone")
	calls := probeCalls
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile 3: %v", err)
	}
	if probeCalls != calls {
		t.Errorf("probe ran %d more time(s) after latching", probeCalls-calls)
	}
	if err := k8sClient.Get(ctx, secretKey, &sec); err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	if got := sec.Labels[labelClusterReady]; got != "true" {
		t.Errorf("cluster-ready label = %q after latching, want \"true\"", got)
	}
}

func reachableCondition(ctx context.Context, t *testing.T, name string) *metav1.Condition {
	t.Helper()
	var cr argov1.ClusterbookCluster
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &cr); err != nil {
		t.Fatalf("get CR %s: %v", name, err)
	}
	return meta.FindStatusCondition(cr.Status.Conditions, conditionClusterReachable)
}
