// Package controller — VclusterReconciler provisions a loft-sh vcluster on
// a target host cluster (via an ArgoCD Application driving the upstream
// Helm chart), extracts and rewrites its kubeconfig, and emits a child
// ClusterbookCluster that the existing Reconciler turns into an ArgoCD
// cluster Secret. End-to-end: one CR → an ArgoCD-registered cluster.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	cbkclient "github.com/stuttgart-things/clusterbook-operator/pkg/client"
)

const (
	vclusterFinalizer = "clusterbook.stuttgart-things.com/vcluster-finalizer"

	// defaultVclusterChartVersion pins the loft-sh chart revision used when
	// spec.chartVersion is empty. Picked from the version verified during
	// the design-phase spike (vcluster 0.33.1 on RKE2 v1.35).
	defaultVclusterChartVersion = "0.33.1"
	defaultVclusterServerPort   = 443
	vclusterChartRepoURL        = "https://charts.loft.sh"

	// loft-sh's vcluster chart writes the in-cluster kubeconfig into a
	// Secret named "vc-<releaseName>" with the kubeconfig YAML under the
	// "config" key. The operator pins releaseName = spec.clusterName so the
	// Secret name is predictable.
	vclusterUpstreamSecretKey = "config"

	// vclusterExternalSecretKey is what the existing ClusterbookCluster
	// reconciler reads by default (controller/reconciler.go:260). The
	// upstream "config" key is rewritten and republished here.
	vclusterExternalSecretKey = "kubeconfig"

	conditionVclusterReady    = "VclusterReady"
	conditionKubeconfigReady  = "KubeconfigReady"
	conditionArgoCDRegistered = "ArgoCDRegistered"
)

// argoApplicationGVK identifies the ArgoCD Application CRD. Treated as
// unstructured throughout — no argoproj.io client deps.
var argoApplicationGVK = schema.GroupVersionKind{
	Group:   "argoproj.io",
	Version: "v1alpha1",
	Kind:    "Application",
}

type VclusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// NewTargetClient builds a client.Client for the host cluster a
	// vcluster is installed on. Defaults to client.New; tests override it
	// to return envtest's k8sClient so the cross-cluster path runs against
	// the same control plane as the in-cluster path.
	NewTargetClient func(*rest.Config) (client.Client, error)
}

func (r *VclusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.NewTargetClient == nil {
		r.NewTargetClient = func(cfg *rest.Config) (client.Client, error) {
			return client.New(cfg, client.Options{Scheme: r.Scheme})
		}
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&argov1.Vcluster{}).
		Owns(&corev1.Secret{}).
		Owns(&argov1.ClusterbookCluster{}).
		Complete(r)
}

func (r *VclusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	var cr argov1.Vcluster
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Clusterbook client is only built when a reservation is wanted.
	// Without NetworkKey the operator treats the install as in-cluster:
	// no IP, no DNS, the kubeconfig points at the in-cluster service DNS
	// name (which the vcluster server cert SANs cover).
	var api *cbkclient.Client
	if cr.Spec.NetworkKey != "" {
		pc, err := loadProviderConfig(ctx, r.Client, cr.Spec.ProviderConfigRef.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("load provider config: %w", err)
		}
		api, err = newClusterbookClient(ctx, r.Client, pc)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("build clusterbook client: %w", err)
		}
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &cr, api)
	}
	if !controllerutil.ContainsFinalizer(&cr, vclusterFinalizer) {
		controllerutil.AddFinalizer(&cr, vclusterFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 1: optional clusterbook reservation.
	var (
		ip   string
		info *cbkclient.ClusterInfo
	)
	if api != nil {
		var err error
		ip, err = r.ensureReservation(ctx, api, &cr)
		if err != nil {
			return ctrl.Result{}, err
		}
		info, _ = api.GetClusterInfo(ctx, cr.Spec.ClusterName)
	}

	// Step 2: resolve the host cluster the vcluster runs on.
	target, err := r.resolveTarget(ctx, &cr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolve target: %w", err)
	}

	// Step 3: upsert the ArgoCD Application driving the vcluster Helm chart.
	appName, err := r.upsertApplication(ctx, &cr, target, ip, info)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("upsert application: %w", err)
	}

	// Step 4: wait for vcluster to produce its kubeconfig Secret on the
	// host cluster. Absent / empty is normal during install — requeue.
	raw, found, err := r.readUpstreamKubeconfig(ctx, target.Client, &cr)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("read upstream kubeconfig: %w", err)
	}
	if !found {
		cr.Status.IP = ip
		if info != nil {
			cr.Status.FQDN = info.FQDN
			cr.Status.Zone = info.Zone
		}
		cr.Status.ArgoCDApplicationRef = appName
		setCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               conditionVclusterReady,
			Status:             metav1.ConditionFalse,
			Reason:             "WaitingForVclusterSecret",
			Message:            fmt.Sprintf("secret vc-%s not yet present in %s", cr.Spec.ClusterName, cr.Spec.TargetNamespace),
			LastTransitionTime: metav1.Now(),
		})
		if err := r.Status().Update(ctx, &cr); err != nil {
			lg.Error(err, "status update failed")
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Step 5: rewrite the kubeconfig and publish it on the management
	// cluster for the child ClusterbookCluster (and any other consumer) to
	// read.
	extRef, err := r.writeExternalKubeconfig(ctx, &cr, raw, info)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("write external kubeconfig: %w", err)
	}

	// Step 6: optional child ClusterbookCluster — the existing reconciler
	// turns it into an ArgoCD cluster Secret on the management cluster.
	var childRef string
	if registerArgoCD(&cr) {
		childRef, err = r.upsertChildCluster(ctx, &cr, extRef)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert child cluster: %w", err)
		}
	}

	// Step 7: status.
	cr.Status.IP = ip
	if info != nil {
		cr.Status.FQDN = info.FQDN
		cr.Status.Zone = info.Zone
	}
	cr.Status.KubeconfigSecretRef = &extRef
	cr.Status.ArgoCDApplicationRef = appName
	cr.Status.ClusterbookClusterRef = childRef
	setCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               conditionVclusterReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		LastTransitionTime: metav1.Now(),
	})
	setCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               conditionKubeconfigReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Written",
		LastTransitionTime: metav1.Now(),
	})
	if registerArgoCD(&cr) {
		setCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               conditionArgoCDRegistered,
			Status:             metav1.ConditionTrue,
			Reason:             "ChildEmitted",
			LastTransitionTime: metav1.Now(),
		})
	}
	if err := r.Status().Update(ctx, &cr); err != nil {
		lg.Error(err, "status update failed")
	}
	return ctrl.Result{}, nil
}

// finalize tears down the resources the reconcile loop created, in
// reverse-dependency order:
//
//  1. Child ClusterbookCluster — its own finalizer cleans up the ArgoCD
//     cluster Secret on the management cluster.
//  2. ArgoCD Application — lets ArgoCD cascade-delete the vcluster Helm
//     release on the host cluster (which removes the upstream vc-<name>
//     Secret and everything the chart provisioned).
//  3. Clusterbook IP reservation (best-effort; only when ReleaseOnDelete).
//
// The external kubeconfig Secret carries an owner reference back to the
// parent Vcluster, so kube GC handles it automatically once the finalizer
// is removed.
func (r *VclusterReconciler) finalize(ctx context.Context, cr *argov1.Vcluster, api *cbkclient.Client) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, vclusterFinalizer) {
		return ctrl.Result{}, nil
	}

	if cr.Status.ClusterbookClusterRef != "" {
		child := &argov1.ClusterbookCluster{
			ObjectMeta: metav1.ObjectMeta{Name: cr.Status.ClusterbookClusterRef},
		}
		if err := r.Delete(ctx, child); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete child cluster: %w", err)
		}
	}

	if cr.Status.ArgoCDApplicationRef != "" {
		ns := cr.Spec.ArgoCD.Namespace
		if ns == "" {
			ns = defaultArgoNamespace
		}
		app := &unstructured.Unstructured{}
		app.SetGroupVersionKind(argoApplicationGVK)
		app.SetName(cr.Status.ArgoCDApplicationRef)
		app.SetNamespace(ns)
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete application: %w", err)
		}
	}

	if api != nil && cr.Spec.ReleaseOnDelete && cr.Status.IP != "" {
		if err := api.ReleaseIPs(ctx, cr.Spec.NetworkKey, cbkclient.ReleaseRequest{IP: cr.Status.IP}); err != nil {
			return ctrl.Result{}, fmt.Errorf("release IP: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(cr, vclusterFinalizer)
	return ctrl.Result{}, r.Update(ctx, cr)
}

// ensureReservation — same layered strategy as the sibling reconcilers:
// trust cr.Status.IP once set, then fall back to a name-matched listing
// lookup, then reserve. CreateDNS is hard-wired on; the kubeconfig SAN
// rewrite (step 3 injecting controlPlane.proxy.extraSANs and step 5
// pointing data.server at the FQDN) requires a stable name.
func (r *VclusterReconciler) ensureReservation(ctx context.Context, api *cbkclient.Client, cr *argov1.Vcluster) (string, error) {
	if cr.Status.IP != "" {
		return cr.Status.IP, nil
	}
	existing, err := api.GetIPs(ctx, cr.Spec.NetworkKey)
	if err != nil {
		return "", fmt.Errorf("list IPs: %w", err)
	}
	for _, e := range existing {
		if e.Cluster == cr.Spec.ClusterName {
			return e.IP, nil
		}
	}
	resv, err := api.ReserveIPs(ctx, cr.Spec.NetworkKey, cbkclient.ReserveRequest{
		Cluster:   cr.Spec.ClusterName,
		Count:     1,
		CreateDNS: true,
	})
	if err != nil {
		return "", fmt.Errorf("reserve IP: %w", err)
	}
	if len(resv.IPs) == 0 {
		return "", fmt.Errorf("clusterbook returned no IPs")
	}
	return resv.IPs[0], nil
}

// targetResolution bundles the controllers' view of the host cluster the
// vcluster is installed on: a client that can read its Secrets and the
// Application destination map that points ArgoCD at it.
type targetResolution struct {
	Client      client.Client
	Destination map[string]interface{}
}

// resolveTarget returns the host-cluster client and the Application
// destination spec. With no TargetClusterRef set the operator targets its
// own cluster (the management cluster); a built-in https://kubernetes.default.svc
// destination is used so ArgoCD doesn't depend on the management cluster
// being registered under a particular name. With TargetClusterRef set,
// destination.name pins the cluster Secret — stable across credential
// rotation, per the catalog-side conventions (stuttgart-things/argocd
// PRs #169–#171).
func (r *VclusterReconciler) resolveTarget(ctx context.Context, cr *argov1.Vcluster) (*targetResolution, error) {
	dest := map[string]interface{}{"namespace": cr.Spec.TargetNamespace}
	if cr.Spec.TargetClusterRef == nil {
		dest["server"] = "https://kubernetes.default.svc"
		return &targetResolution{Client: r.Client, Destination: dest}, nil
	}
	ref := *cr.Spec.TargetClusterRef
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &s); err != nil {
		return nil, fmt.Errorf("get target cluster secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	restCfg, err := restConfigFromArgoSecret(s)
	if err != nil {
		return nil, err
	}
	cl, err := r.NewTargetClient(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build target client: %w", err)
	}
	argoName := string(s.Data["name"])
	if argoName == "" {
		argoName = ref.Name
	}
	dest["name"] = argoName
	return &targetResolution{Client: cl, Destination: dest}, nil
}

// restConfigFromArgoSecret reconstructs a *rest.Config from the JSON
// blob ArgoCD stores in cluster Secrets (the inverse of argoConfigFromKubeconfig
// in controller/reconciler.go). Bearer-token, mTLS, and tlsServerName are
// all carried through.
func restConfigFromArgoSecret(s corev1.Secret) (*rest.Config, error) {
	server := string(s.Data["server"])
	if server == "" {
		return nil, fmt.Errorf("secret %s/%s missing data.server", s.Namespace, s.Name)
	}
	var cfg argoClusterConfig
	if raw := s.Data["config"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("decode data.config: %w", err)
		}
	}
	return &rest.Config{
		Host:        server,
		BearerToken: cfg.BearerToken,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   cfg.TLSClientConfig.Insecure,
			ServerName: cfg.TLSClientConfig.ServerName,
			CAData:     cfg.TLSClientConfig.CAData,
			CertData:   cfg.TLSClientConfig.CertData,
			KeyData:    cfg.TLSClientConfig.KeyData,
		},
	}, nil
}

// upsertApplication writes the ArgoCD Application that drives the
// loft-sh vcluster Helm chart on the host cluster. Operator-managed
// values (service type / loadBalancerIP / extraSANs) overlay the user's
// chartValues — they're load-bearing for the kubeconfig rewrite to work,
// so the user can't override them.
func (r *VclusterReconciler) upsertApplication(ctx context.Context, cr *argov1.Vcluster, target *targetResolution, ip string, info *cbkclient.ClusterInfo) (string, error) {
	appName := "vcluster-" + cr.Spec.ClusterName
	ns := cr.Spec.ArgoCD.Namespace
	if ns == "" {
		ns = defaultArgoNamespace
	}

	values, err := buildHelmValues(cr, ip, info)
	if err != nil {
		return "", err
	}

	chartVersion := cr.Spec.ChartVersion
	if chartVersion == "" {
		chartVersion = defaultVclusterChartVersion
	}

	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(argoApplicationGVK)
	app.SetName(appName)
	app.SetNamespace(ns)

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, app, func() error {
		app.Object["spec"] = map[string]interface{}{
			"project":     "default",
			"destination": target.Destination,
			"source": map[string]interface{}{
				"repoURL":        vclusterChartRepoURL,
				"chart":          "vcluster",
				"targetRevision": chartVersion,
				"helm": map[string]interface{}{
					"releaseName":  cr.Spec.ClusterName,
					"valuesObject": values,
				},
			},
			"syncPolicy": map[string]interface{}{
				"automated": map[string]interface{}{
					"prune":    true,
					"selfHeal": true,
				},
				"syncOptions": []interface{}{"CreateNamespace=true"},
			},
		}
		return controllerutil.SetControllerReference(cr, app, r.Scheme)
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	return appName, nil
}

// buildHelmValues starts from the user's chartValues (JSON passthrough)
// and overlays the operator-managed keys: LB service type + IP when a
// reservation exists, FQDN as an extra TLS SAN so the rewritten
// kubeconfig's server URL validates against the vcluster API cert.
//
// Key layout follows the loft-sh chart 0.20+ schema (the one verified
// during the design-phase spike). Pre-0.20 charts use a different shape
// (service.* flat, syncer.extraArgs) — users on those versions must
// override via chartValues, or upgrade.
func buildHelmValues(cr *argov1.Vcluster, ip string, info *cbkclient.ClusterInfo) (map[string]interface{}, error) {
	values := map[string]interface{}{}
	if cv := cr.Spec.ChartValues; cv != nil && len(cv.Raw) > 0 {
		if err := json.Unmarshal(cv.Raw, &values); err != nil {
			return nil, fmt.Errorf("decode chartValues: %w", err)
		}
	}
	if ip != "" {
		if err := unstructured.SetNestedField(values, "LoadBalancer", "controlPlane", "service", "spec", "type"); err != nil {
			return nil, err
		}
		if err := unstructured.SetNestedField(values, ip, "controlPlane", "service", "spec", "loadBalancerIP"); err != nil {
			return nil, err
		}
	}
	if info != nil && info.FQDN != "" {
		if err := unstructured.SetNestedStringSlice(values, []string{info.FQDN}, "controlPlane", "proxy", "extraSANs"); err != nil {
			return nil, err
		}
	}
	return values, nil
}

// readUpstreamKubeconfig polls the host cluster for the loft-sh-managed
// Secret holding the freshly-provisioned vcluster's kubeconfig. Missing
// or empty is not an error — it just means the chart hasn't finished
// installing yet and the caller should requeue.
func (r *VclusterReconciler) readUpstreamKubeconfig(ctx context.Context, targetClient client.Client, cr *argov1.Vcluster) ([]byte, bool, error) {
	name := "vc-" + cr.Spec.ClusterName
	var s corev1.Secret
	if err := targetClient.Get(ctx, types.NamespacedName{Name: name, Namespace: cr.Spec.TargetNamespace}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get vc-secret %s/%s: %w", cr.Spec.TargetNamespace, name, err)
	}
	raw, ok := s.Data[vclusterUpstreamSecretKey]
	if !ok || len(raw) == 0 {
		return nil, false, nil
	}
	return raw, true, nil
}

// writeExternalKubeconfig rewrites the upstream kubeconfig's server URL
// to one the management cluster can reach (and that the vcluster cert
// SANs cover), then writes it to a Secret owned by the parent Vcluster.
// The child ClusterbookCluster's kubeconfigSecretRef points here.
func (r *VclusterReconciler) writeExternalKubeconfig(ctx context.Context, cr *argov1.Vcluster, raw []byte, info *cbkclient.ClusterInfo) (argov1.SecretObjectRef, error) {
	server := externalServerURL(cr, info)
	rewritten, err := rewriteKubeconfigServer(raw, server)
	if err != nil {
		return argov1.SecretObjectRef{}, fmt.Errorf("rewrite kubeconfig: %w", err)
	}
	ns := cr.Spec.ArgoCD.Namespace
	if ns == "" {
		ns = defaultArgoNamespace
	}
	name := "vc-" + cr.Spec.ClusterName + "-external"
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[vclusterExternalSecretKey] = rewritten
		return controllerutil.SetControllerReference(cr, secret, r.Scheme)
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return argov1.SecretObjectRef{}, err
	}
	return argov1.SecretObjectRef{Name: name, Namespace: ns}, nil
}

// externalServerURL picks a server URL that (a) the management cluster
// can reach and (b) is covered by the vcluster API server's TLS SANs.
//
//   - With a clusterbook reservation: the reserved FQDN + ServerPort. The
//     SAN match is guaranteed by buildHelmValues injecting the FQDN into
//     controlPlane.proxy.extraSANs.
//   - Without: the in-cluster service DNS name "<clusterName>.<namespace>",
//     which the default vcluster cert SANs cover. Only resolvable from
//     inside the host cluster — appropriate when the management cluster
//     IS the host cluster, which is the only no-reservation scenario the
//     operator is opinionated about.
func externalServerURL(cr *argov1.Vcluster, info *cbkclient.ClusterInfo) string {
	if info != nil && info.FQDN != "" {
		port := cr.Spec.ServerPort
		if port == 0 {
			port = defaultVclusterServerPort
		}
		return fmt.Sprintf("https://%s:%d", info.FQDN, port)
	}
	return fmt.Sprintf("https://%s.%s", cr.Spec.ClusterName, cr.Spec.TargetNamespace)
}

// rewriteKubeconfigServer replaces the current-context cluster's server
// URL — the loft-sh upstream Secret hard-codes it to
// https://localhost:8443, which is only useful for `vcluster connect`.
func rewriteKubeconfigServer(raw []byte, server string) ([]byte, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, err
	}
	ctxEntry, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return nil, fmt.Errorf("current-context %q missing", cfg.CurrentContext)
	}
	cluster := cfg.Clusters[ctxEntry.Cluster]
	if cluster == nil {
		return nil, fmt.Errorf("kubeconfig missing cluster %q", ctxEntry.Cluster)
	}
	cluster.Server = server
	return clientcmd.Write(*cfg)
}

// upsertChildCluster emits the ClusterbookCluster CR that the existing
// Reconciler turns into an ArgoCD cluster Secret. SkipReservation=true
// short-circuits every clusterbook server interaction on the child
// (the parent owns the reservation); PreserveKubeconfigServer=true makes
// data.server come from the kubeconfig (which is the URL we just wrote)
// instead of from an absent IP reservation.
func (r *VclusterReconciler) upsertChildCluster(ctx context.Context, cr *argov1.Vcluster, kubeconfigRef argov1.SecretObjectRef) (string, error) {
	child := &argov1.ClusterbookCluster{
		ObjectMeta: metav1.ObjectMeta{Name: cr.Spec.ClusterName},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, child, func() error {
		child.Spec = argov1.ClusterbookClusterSpec{
			ClusterName:              cr.Spec.ClusterName,
			ClusterType:              "vcluster",
			SkipReservation:          true,
			PreserveKubeconfigServer: true,
			KubeconfigSecretRef: &argov1.SecretKeyRef{
				Name:      kubeconfigRef.Name,
				Namespace: kubeconfigRef.Namespace,
				Key:       vclusterExternalSecretKey,
			},
			ArgoCDNamespace: cr.Spec.ArgoCD.Namespace,
			Labels:          cr.Spec.ArgoCD.Labels,
			Annotations:     cr.Spec.ArgoCD.Annotations,
		}
		return controllerutil.SetControllerReference(cr, child, r.Scheme)
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	return cr.Spec.ClusterName, nil
}

// registerArgoCD treats the tri-state *bool as "default true". Lets users
// opt out of ArgoCD registration with argoCD.register=false (provision-only
// flow) without flipping the polarity for everyone else.
func registerArgoCD(cr *argov1.Vcluster) bool {
	if cr.Spec.ArgoCD.Register == nil {
		return true
	}
	return *cr.Spec.ArgoCD.Register
}
