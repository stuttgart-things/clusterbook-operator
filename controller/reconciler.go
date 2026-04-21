// Package controller reconciles ClusterbookCluster resources into ArgoCD
// cluster Secrets by calling the clusterbook REST API.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	cbkclient "github.com/stuttgart-things/clusterbook-operator/pkg/client"
)

const (
	finalizer            = "clusterbook.stuttgart-things.com/finalizer"
	argoSecretTypeLabel  = "argocd.argoproj.io/secret-type"
	argoSecretTypeValue  = "cluster"
	defaultArgoNamespace = "argocd"
	defaultPort          = 6443

	clusterbookPrefix = "clusterbook.stuttgart-things.com/"
	annotationIP      = clusterbookPrefix + "ip"
	annotationFQDN    = clusterbookPrefix + "fqdn"
	annotationZone    = clusterbookPrefix + "zone"
)

type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&argov1.ClusterbookCluster{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	var cr argov1.ClusterbookCluster
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pc, err := r.loadProviderConfig(ctx, cr.Spec.ProviderConfigRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("load provider config: %w", err)
	}

	api, err := r.newClusterbookClient(ctx, pc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("build clusterbook client: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &cr, api)
	}
	if !controllerutil.ContainsFinalizer(&cr, finalizer) {
		controllerutil.AddFinalizer(&cr, finalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
	}

	ip, err := r.ensureReservation(ctx, api, &cr)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileDNSDrift(ctx, api, &cr, ip); err != nil {
		return ctrl.Result{}, err
	}

	info, _ := api.GetClusterInfo(ctx, cr.Spec.ClusterName)

	var secretName string
	if cr.Spec.ExistingSecretRef != nil {
		ref := *cr.Spec.ExistingSecretRef
		notFound, err := r.enrichExistingSecret(ctx, &cr, ip, info)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("enrich existing secret: %w", err)
		}
		if notFound {
			setCondition(&cr.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				Reason:             "ExistingSecretNotFound",
				Message:            fmt.Sprintf("secret %s/%s not found", ref.Namespace, ref.Name),
				LastTransitionTime: metav1.Now(),
			})
		} else {
			setCondition(&cr.Status.Conditions, metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				Reason:             "Reconciled",
				LastTransitionTime: metav1.Now(),
			})
		}
		secretName = ref.Name
	} else {
		kcfg, err := r.loadKubeconfig(ctx, *cr.Spec.KubeconfigSecretRef)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("load kubeconfig: %w", err)
		}

		server := buildServerURL(&cr, ip, info)
		argoCfg, caData, err := argoConfigFromKubeconfig(kcfg)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("extract kubeconfig: %w", err)
		}

		secret, err := r.upsertArgoSecret(ctx, &cr, ip, info, server, argoCfg, caData)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert argo secret: %w", err)
		}
		secretName = secret.Name
		setCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Reconciled",
			LastTransitionTime: metav1.Now(),
		})
	}

	cr.Status.IP = ip
	cr.Status.SecretName = secretName
	if info != nil {
		cr.Status.FQDN = info.FQDN
		cr.Status.Zone = info.Zone
	}
	if err := r.Status().Update(ctx, &cr); err != nil {
		lg.Error(err, "status update failed")
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) finalize(ctx context.Context, cr *argov1.ClusterbookCluster, api *cbkclient.Client) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, finalizer) {
		return ctrl.Result{}, nil
	}

	if cr.Spec.ExistingSecretRef != nil {
		if err := r.stripEnrichedMetadata(ctx, *cr.Spec.ExistingSecretRef); err != nil {
			return ctrl.Result{}, fmt.Errorf("strip enriched metadata: %w", err)
		}
	} else {
		ns := cr.Spec.ArgoCDNamespace
		if ns == "" {
			ns = defaultArgoNamespace
		}
		_ = r.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: argoSecretName(cr), Namespace: ns},
		})
	}

	if cr.Spec.ReleaseOnDelete && cr.Status.IP != "" {
		if err := api.ReleaseIPs(ctx, cr.Spec.NetworkKey, cbkclient.ReleaseRequest{IP: cr.Status.IP}); err != nil {
			return ctrl.Result{}, fmt.Errorf("release IP: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(cr, finalizer)
	return ctrl.Result{}, r.Update(ctx, cr)
}

func (r *Reconciler) loadProviderConfig(ctx context.Context, name string) (*argov1.ClusterbookProviderConfig, error) {
	var pc argov1.ClusterbookProviderConfig
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &pc); err != nil {
		return nil, err
	}
	return &pc, nil
}

func (r *Reconciler) newClusterbookClient(ctx context.Context, pc *argov1.ClusterbookProviderConfig) (*cbkclient.Client, error) {
	opts := &cbkclient.TLSOptions{InsecureSkipVerify: pc.Spec.InsecureSkipVerify}
	if ref := pc.Spec.CustomCASecretRef; ref != nil {
		var s corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &s); err != nil {
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

func (r *Reconciler) loadKubeconfig(ctx context.Context, ref argov1.SecretKeyRef) ([]byte, error) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &s); err != nil {
		return nil, err
	}
	key := ref.Key
	if key == "" {
		key = "kubeconfig"
	}
	data, ok := s.Data[key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", key, ref.Namespace, ref.Name)
	}
	return data, nil
}

// argoClusterConfig mirrors the JSON stored in the `config` field of an
// ArgoCD cluster Secret.
type argoClusterConfig struct {
	BearerToken     string                  `json:"bearerToken,omitempty"`
	TLSClientConfig argoTLSClientConfig     `json:"tlsClientConfig"`
	ExecProviderCfg *argoExecProviderConfig `json:"execProviderConfig,omitempty"`
}

type argoTLSClientConfig struct {
	Insecure   bool   `json:"insecure"`
	ServerName string `json:"serverName,omitempty"`
	CAData     []byte `json:"caData,omitempty"`
	CertData   []byte `json:"certData,omitempty"`
	KeyData    []byte `json:"keyData,omitempty"`
}

type argoExecProviderConfig struct{}

func argoConfigFromKubeconfig(raw []byte) ([]byte, []byte, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, nil, err
	}
	ctxEntry, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return nil, nil, fmt.Errorf("current-context %q missing", cfg.CurrentContext)
	}
	cluster := cfg.Clusters[ctxEntry.Cluster]
	user := cfg.AuthInfos[ctxEntry.AuthInfo]
	if cluster == nil || user == nil {
		return nil, nil, fmt.Errorf("kubeconfig missing cluster or user")
	}

	out := argoClusterConfig{
		BearerToken: user.Token,
		TLSClientConfig: argoTLSClientConfig{
			Insecure: cluster.InsecureSkipTLSVerify,
			CAData:   cluster.CertificateAuthorityData,
			CertData: user.ClientCertificateData,
			KeyData:  user.ClientKeyData,
		},
	}
	b, err := json.Marshal(out)
	return b, cluster.CertificateAuthorityData, err
}

func buildServerURL(cr *argov1.ClusterbookCluster, ip string, info *cbkclient.ClusterInfo) string {
	port := cr.Spec.ServerPort
	if port == 0 {
		port = defaultPort
	}
	host := ip
	if cr.Spec.UseFQDNAsServer && info != nil && info.FQDN != "" {
		host = info.FQDN
	}
	return fmt.Sprintf("https://%s:%d", host, port)
}

func argoSecretName(cr *argov1.ClusterbookCluster) string {
	return "cluster-" + cr.Spec.ClusterName
}

func (r *Reconciler) upsertArgoSecret(ctx context.Context, cr *argov1.ClusterbookCluster, ip string, info *cbkclient.ClusterInfo, server string, cfgJSON, _ []byte) (*corev1.Secret, error) {
	ns := cr.Spec.ArgoCDNamespace
	if ns == "" {
		ns = defaultArgoNamespace
	}

	labels := map[string]string{argoSecretTypeLabel: argoSecretTypeValue}
	for k, v := range cr.Spec.Labels {
		labels[k] = v
	}

	annotations := map[string]string{annotationIP: ip}
	if info != nil {
		if info.FQDN != "" {
			annotations[annotationFQDN] = info.FQDN
		}
		if info.Zone != "" {
			annotations[annotationZone] = info.Zone
		}
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: argoSecretName(cr), Namespace: ns},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		for k, v := range labels {
			secret.Labels[k] = v
		}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		for k, v := range annotations {
			secret.Annotations[k] = v
		}
		secret.StringData = map[string]string{
			"name":   cr.Spec.ClusterName,
			"server": server,
			"config": string(cfgJSON),
		}
		return controllerutil.SetControllerReference(cr, secret, r.Scheme)
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return secret, nil
}

// enrichExistingSecret merges clusterbook-prefixed labels and annotations
// onto an ArgoCD cluster Secret that the operator does NOT own — another
// system (Crossplane, manual, another operator) created it, and clusterbook
// is only contributing metadata so ApplicationSet selectors can target it.
//
// Invariants for enrich mode:
//   - data (name/server/config) is never touched
//   - no controller reference — the Secret's lifecycle is not ours
//   - everything we write is under clusterbookPrefix so stripEnrichedMetadata
//     can reverse it cleanly on delete
//
// Returns notFound=true if the referenced Secret is missing; the caller
// surfaces this via a condition rather than erroring the reconcile.
func (r *Reconciler) enrichExistingSecret(ctx context.Context, cr *argov1.ClusterbookCluster, ip string, info *cbkclient.ClusterInfo) (bool, error) {
	ref := *cr.Spec.ExistingSecretRef
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	for k, v := range cr.Spec.Labels {
		secret.Labels[clusterbookPrefix+k] = v
	}
	secret.Annotations[annotationIP] = ip
	if info != nil {
		if info.FQDN != "" {
			secret.Annotations[annotationFQDN] = info.FQDN
		}
		if info.Zone != "" {
			secret.Annotations[annotationZone] = info.Zone
		}
	}
	return false, r.Update(ctx, &secret)
}

// stripEnrichedMetadata removes every label and annotation under
// clusterbookPrefix from the referenced Secret. The Secret itself stays.
// A missing Secret is a no-op — nothing to strip.
func (r *Reconciler) stripEnrichedMetadata(ctx context.Context, ref argov1.SecretObjectRef) error {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	for k := range secret.Labels {
		if strings.HasPrefix(k, clusterbookPrefix) {
			delete(secret.Labels, k)
		}
	}
	for k := range secret.Annotations {
		if strings.HasPrefix(k, clusterbookPrefix) {
			delete(secret.Annotations, k)
		}
	}
	return r.Update(ctx, &secret)
}

// ensureReservation returns the IP already reserved for this cluster in
// clusterbook, reserving a fresh one only when no existing reservation is
// found. This is defensive: the clusterbook API is not strictly idempotent
// by cluster name, so calling Reserve on every reconcile tick can drain
// the pool when something else (e.g. a stuck reconcile loop) retries
// aggressively.
func (r *Reconciler) ensureReservation(ctx context.Context, api *cbkclient.Client, cr *argov1.ClusterbookCluster) (string, error) {
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
		CreateDNS: cr.Spec.CreateDNS,
	})
	if err != nil {
		return "", fmt.Errorf("reserve IP: %w", err)
	}
	if len(resv.IPs) == 0 {
		return "", fmt.Errorf("clusterbook returned no IPs")
	}
	return resv.IPs[0], nil
}

// reconcileDNSDrift compares the observed createDNS state on the clusterbook
// side (derived from the IP's Status — "ASSIGNED" vs "ASSIGNED:DNS") with
// cr.Spec.CreateDNS and issues an UpdateIP call when they diverge. Noop when
// they already match or when the IP is not yet visible in the listing.
func (r *Reconciler) reconcileDNSDrift(ctx context.Context, api *cbkclient.Client, cr *argov1.ClusterbookCluster, ip string) error {
	ips, err := api.GetIPs(ctx, cr.Spec.NetworkKey)
	if err != nil {
		return fmt.Errorf("list IPs: %w", err)
	}
	for _, entry := range ips {
		if entry.IP != ip {
			continue
		}
		observedDNS := strings.Contains(entry.Status, "DNS")
		if observedDNS == cr.Spec.CreateDNS {
			return nil
		}
		status := "ASSIGNED"
		if cr.Spec.CreateDNS {
			status = "ASSIGNED:DNS"
		}
		if err := api.UpdateIP(ctx, cr.Spec.NetworkKey, ip, cbkclient.ReserveRequest{
			Cluster:   cr.Spec.ClusterName,
			CreateDNS: cr.Spec.CreateDNS,
			Status:    status,
		}); err != nil {
			return fmt.Errorf("update IP: %w", err)
		}
		return nil
	}
	return nil
}

func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	for i := range *conds {
		if (*conds)[i].Type == c.Type {
			(*conds)[i] = c
			return
		}
	}
	*conds = append(*conds, c)
}
