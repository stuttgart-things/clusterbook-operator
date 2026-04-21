// Package controller — AllocationReconciler watches ClusterbookAllocation
// resources and publishes the reserved IP / FQDN / zone into one or more
// sinks (owned ConfigMap, prefixed labels on an existing Secret) so
// downstream consumers like ArgoCD ApplicationSets can read the facts.
package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	cbkclient "github.com/stuttgart-things/clusterbook-operator/pkg/client"
)

const (
	allocationFinalizer = "clusterbook.stuttgart-things.com/allocation-finalizer"

	// allocationNameLabel tags an enriched cluster Secret with the
	// allocation's spec.name so ApplicationSet selectors can target it.
	allocationNameLabel = clusterbookPrefix + "allocation-name"
)

type AllocationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *AllocationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&argov1.ClusterbookAllocation{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}

func (r *AllocationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	var cr argov1.ClusterbookAllocation
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
	if !controllerutil.ContainsFinalizer(&cr, allocationFinalizer) {
		controllerutil.AddFinalizer(&cr, allocationFinalizer)
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
	info, _ := api.GetClusterInfo(ctx, cr.Spec.Name)

	var (
		configMapRef     *argov1.ConfigMapSink
		clusterSecretRef *argov1.SecretObjectRef
	)

	if s := cr.Spec.Sinks.ConfigMap; s != nil {
		if err := r.upsertConfigMapSink(ctx, &cr, ip, info, s); err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert configmap sink: %w", err)
		}
		ref := *s
		configMapRef = &ref
	}
	if s := cr.Spec.Sinks.ClusterSecretLabels; s != nil {
		if err := r.enrichClusterSecret(ctx, &cr, ip, info, s); err != nil {
			return ctrl.Result{}, fmt.Errorf("enrich cluster secret: %w", err)
		}
		ref := *s
		clusterSecretRef = &ref
	}

	cr.Status.IP = ip
	cr.Status.ConfigMapRef = configMapRef
	cr.Status.ClusterSecretRef = clusterSecretRef
	if info != nil {
		cr.Status.FQDN = info.FQDN
		cr.Status.Zone = info.Zone
	}
	setCondition(&cr.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Update(ctx, &cr); err != nil {
		lg.Error(err, "status update failed")
	}
	return ctrl.Result{}, nil
}

func (r *AllocationReconciler) finalize(ctx context.Context, cr *argov1.ClusterbookAllocation, api *cbkclient.Client) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, allocationFinalizer) {
		return ctrl.Result{}, nil
	}

	if s := cr.Spec.Sinks.ConfigMap; s != nil {
		_ = r.Delete(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace},
		})
	}
	if s := cr.Spec.Sinks.ClusterSecretLabels; s != nil {
		if err := r.stripClusterSecret(ctx, *s); err != nil {
			return ctrl.Result{}, fmt.Errorf("strip cluster secret: %w", err)
		}
	}

	if cr.Spec.ReleaseOnDelete && cr.Status.IP != "" {
		if err := api.ReleaseIPs(ctx, cr.Spec.NetworkKey, cbkclient.ReleaseRequest{IP: cr.Status.IP}); err != nil {
			return ctrl.Result{}, fmt.Errorf("release IP: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(cr, allocationFinalizer)
	return ctrl.Result{}, r.Update(ctx, cr)
}

// upsertConfigMapSink creates or updates the ConfigMap sink, with
// ownership so deleting the CR garbage-collects the ConfigMap.
func (r *AllocationReconciler) upsertConfigMapSink(ctx context.Context, cr *argov1.ClusterbookAllocation, ip string, info *cbkclient.ClusterInfo, s *argov1.ConfigMapSink) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["ip"] = ip
		cm.Data["networkKey"] = cr.Spec.NetworkKey
		cm.Data["name"] = cr.Spec.Name
		if info != nil {
			if info.FQDN != "" {
				cm.Data["fqdn"] = info.FQDN
			} else {
				delete(cm.Data, "fqdn")
			}
			if info.Zone != "" {
				cm.Data["zone"] = info.Zone
			} else {
				delete(cm.Data, "zone")
			}
		}
		return controllerutil.SetControllerReference(cr, cm, r.Scheme)
	})
	return err
}

// enrichClusterSecret merges clusterbook-prefixed labels and annotations
// onto an existing ArgoCD cluster Secret the operator does NOT own.
// Same contract as ClusterbookCluster enrich mode: data untouched, no
// owner reference, prefix-scoped writes only — stripClusterSecret can
// reverse it cleanly on delete. A missing Secret surfaces a condition.
func (r *AllocationReconciler) enrichClusterSecret(ctx context.Context, cr *argov1.ClusterbookAllocation, ip string, info *cbkclient.ClusterInfo, ref *argov1.SecretObjectRef) error {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			setCondition(&cr.Status.Conditions, metav1.Condition{
				Type:               "ClusterSecretFound",
				Status:             metav1.ConditionFalse,
				Reason:             "ClusterSecretNotFound",
				Message:            fmt.Sprintf("secret %s/%s not found", ref.Namespace, ref.Name),
				LastTransitionTime: metav1.Now(),
			})
			return nil
		}
		return err
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Labels[allocationNameLabel] = cr.Spec.Name
	secret.Annotations[annotationIP] = ip
	if info != nil {
		if info.FQDN != "" {
			secret.Annotations[annotationFQDN] = info.FQDN
		}
		if info.Zone != "" {
			secret.Annotations[annotationZone] = info.Zone
		}
	}
	return r.Update(ctx, &secret)
}

func (r *AllocationReconciler) stripClusterSecret(ctx context.Context, ref argov1.SecretObjectRef) error {
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

// ensureReservation — same layered strategy as the sibling reconcilers:
// trust cr.Status.IP once set (robust against clusterbook rewriting the
// listing's Cluster field, e.g. to "DNS" when createDNS=true), then fall
// back to name-matched listing lookup, then reserve.
func (r *AllocationReconciler) ensureReservation(ctx context.Context, api *cbkclient.Client, cr *argov1.ClusterbookAllocation) (string, error) {
	if cr.Status.IP != "" {
		return cr.Status.IP, nil
	}
	existing, err := api.GetIPs(ctx, cr.Spec.NetworkKey)
	if err != nil {
		return "", fmt.Errorf("list IPs: %w", err)
	}
	for _, e := range existing {
		if e.Cluster == cr.Spec.Name {
			return e.IP, nil
		}
	}
	resv, err := api.ReserveIPs(ctx, cr.Spec.NetworkKey, cbkclient.ReserveRequest{
		Cluster:   cr.Spec.Name,
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

// reconcileDNSDrift mirrors the ClusterbookCluster implementation so
// flipping spec.createDNS after the fact propagates to clusterbook.
func (r *AllocationReconciler) reconcileDNSDrift(ctx context.Context, api *cbkclient.Client, cr *argov1.ClusterbookAllocation, ip string) error {
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
			Cluster:   cr.Spec.Name,
			CreateDNS: cr.Spec.CreateDNS,
			Status:    status,
		}); err != nil {
			return fmt.Errorf("update IP: %w", err)
		}
		return nil
	}
	return nil
}

func (r *AllocationReconciler) loadProviderConfig(ctx context.Context, name string) (*argov1.ClusterbookProviderConfig, error) {
	var pc argov1.ClusterbookProviderConfig
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &pc); err != nil {
		return nil, err
	}
	return &pc, nil
}

func (r *AllocationReconciler) newClusterbookClient(ctx context.Context, pc *argov1.ClusterbookProviderConfig) (*cbkclient.Client, error) {
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
