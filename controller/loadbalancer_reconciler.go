// Package controller — LoadBalancerReconciler watches ClusterbookLoadBalancer
// resources and materialises them as CiliumLoadBalancerIPPool objects pinned
// to the IP reserved in clusterbook.
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	cbkclient "github.com/stuttgart-things/clusterbook-operator/pkg/client"
)

const (
	lbFinalizer = "clusterbook.stuttgart-things.com/lb-finalizer"

	// annotationPreviousLBIP records the .spec.loadBalancerIP value the
	// target Service had before the operator patched it. Stored on the CR
	// itself so we can restore it verbatim on delete — an empty string
	// means the Service had no loadBalancerIP set and should be cleared.
	annotationPreviousLBIP = "clusterbook.stuttgart-things.com/previous-loadbalancer-ip"
)

// ciliumIPPoolGVK points at the CiliumLoadBalancerIPPool CRD. Using
// unstructured to avoid vendoring all of github.com/cilium/cilium.
var ciliumIPPoolGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2alpha1",
	Kind:    "CiliumLoadBalancerIPPool",
}

type LoadBalancerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *LoadBalancerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&argov1.ClusterbookLoadBalancer{}).
		Complete(r)
}

func (r *LoadBalancerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	var cr argov1.ClusterbookLoadBalancer
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
	if !controllerutil.ContainsFinalizer(&cr, lbFinalizer) {
		controllerutil.AddFinalizer(&cr, lbFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
	}

	if cr.Spec.CiliumPool == nil && cr.Spec.ServiceRef == nil {
		return ctrl.Result{}, fmt.Errorf("spec.ciliumPool or spec.serviceRef must be set")
	}
	if cr.Spec.CiliumPool != nil && cr.Spec.ServiceRef != nil {
		return ctrl.Result{}, fmt.Errorf("spec.ciliumPool and spec.serviceRef are mutually exclusive")
	}

	ip, err := r.ensureReservation(ctx, api, &cr)
	if err != nil {
		return ctrl.Result{}, err
	}
	info, _ := api.GetClusterInfo(ctx, cr.Spec.Name)

	// Do all non-status mutations (Service patches, pool upserts, CR
	// annotation writes) first, then assign status in one shot. Mixing the
	// two leads to lost status updates: r.Update on the CR to persist the
	// previous-loadBalancerIP annotation returns an object with empty
	// status (status lives on a subresource), clobbering any cr.Status.*
	// we set beforehand.

	var (
		poolName         string
		targetServiceRef *argov1.ServiceObjectRef
	)

	switch {
	case cr.Spec.CiliumPool != nil:
		poolName = cr.Spec.CiliumPool.PoolName
		if poolName == "" {
			poolName = cr.Spec.Name + "-pool"
		}
		if err := r.upsertCiliumPool(ctx, &cr, poolName, ip, info); err != nil {
			return ctrl.Result{}, fmt.Errorf("upsert cilium pool: %w", err)
		}

	case cr.Spec.ServiceRef != nil:
		if err := r.patchServiceLBIP(ctx, &cr, ip); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch service loadBalancerIP: %w", err)
		}
		ref := *cr.Spec.ServiceRef
		targetServiceRef = &ref
	}

	cr.Status.IP = ip
	cr.Status.PoolName = poolName
	cr.Status.TargetServiceRef = targetServiceRef
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

func (r *LoadBalancerReconciler) finalize(ctx context.Context, cr *argov1.ClusterbookLoadBalancer, api *cbkclient.Client) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, lbFinalizer) {
		return ctrl.Result{}, nil
	}

	switch {
	case cr.Spec.ServiceRef != nil:
		if err := r.restoreServiceLBIP(ctx, cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("restore service loadBalancerIP: %w", err)
		}
	case cr.Status.PoolName != "":
		pool := &unstructured.Unstructured{}
		pool.SetGroupVersionKind(ciliumIPPoolGVK)
		pool.SetName(cr.Status.PoolName)
		_ = r.Delete(ctx, pool)
	}

	if cr.Spec.ReleaseOnDelete && cr.Status.IP != "" {
		if err := api.ReleaseIPs(ctx, cr.Spec.NetworkKey, cbkclient.ReleaseRequest{IP: cr.Status.IP}); err != nil {
			return ctrl.Result{}, fmt.Errorf("release IP: %w", err)
		}
	}

	controllerutil.RemoveFinalizer(cr, lbFinalizer)
	return ctrl.Result{}, r.Update(ctx, cr)
}

// ensureReservation — same layered strategy as the ClusterbookCluster
// reconciler: trust cr.Status.IP once set (robust against clusterbook
// rewriting the listing's Cluster field, e.g. to "DNS" when
// createDNS=true), then fall back to name-matched listing lookup, then
// reserve. Prevents repeated reconciles from burning IPs.
func (r *LoadBalancerReconciler) ensureReservation(ctx context.Context, api *cbkclient.Client, cr *argov1.ClusterbookLoadBalancer) (string, error) {
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

func (r *LoadBalancerReconciler) loadProviderConfig(ctx context.Context, name string) (*argov1.ClusterbookProviderConfig, error) {
	var pc argov1.ClusterbookProviderConfig
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &pc); err != nil {
		return nil, err
	}
	return &pc, nil
}

func (r *LoadBalancerReconciler) newClusterbookClient(ctx context.Context, pc *argov1.ClusterbookProviderConfig) (*cbkclient.Client, error) {
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

func (r *LoadBalancerReconciler) upsertCiliumPool(ctx context.Context, cr *argov1.ClusterbookLoadBalancer, poolName, ip string, info *cbkclient.ClusterInfo) error {
	pool := &unstructured.Unstructured{}
	pool.SetGroupVersionKind(ciliumIPPoolGVK)
	pool.SetName(poolName)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pool, func() error {
		annotations := map[string]string{
			annotationIP: ip,
		}
		if info != nil {
			if info.FQDN != "" {
				annotations[annotationFQDN] = info.FQDN
			}
			if info.Zone != "" {
				annotations[annotationZone] = info.Zone
			}
		}
		if pool.GetAnnotations() == nil {
			pool.SetAnnotations(map[string]string{})
		}
		merged := pool.GetAnnotations()
		for k, v := range annotations {
			merged[k] = v
		}
		pool.SetAnnotations(merged)

		spec := map[string]interface{}{
			"blocks": []interface{}{
				map[string]interface{}{"cidr": ip + "/32"},
			},
		}
		if cr.Spec.CiliumPool.ServiceSelector != nil {
			sel, err := labelSelectorToMap(cr.Spec.CiliumPool.ServiceSelector)
			if err != nil {
				return err
			}
			spec["serviceSelector"] = sel
		}
		if err := unstructured.SetNestedField(pool.Object, spec["blocks"], "spec", "blocks"); err != nil {
			return err
		}
		if sel, ok := spec["serviceSelector"]; ok {
			if err := unstructured.SetNestedField(pool.Object, sel, "spec", "serviceSelector"); err != nil {
				return err
			}
		}
		return controllerutil.SetControllerReference(cr, pool, r.Scheme)
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// patchServiceLBIP sets the target Service's .spec.loadBalancerIP to the
// reserved clusterbook IP. The prior value is captured in an annotation on
// the CR the first time we patch — subsequent reconciles are no-ops when
// the field is already current, and never overwrite the stored prior
// value (which would lose the original loadBalancerIP if the user edited
// the Service after we first patched it).
func (r *LoadBalancerReconciler) patchServiceLBIP(ctx context.Context, cr *argov1.ClusterbookLoadBalancer, ip string) error {
	ref := *cr.Spec.ServiceRef
	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &svc); err != nil {
		return err
	}

	if _, captured := cr.Annotations[annotationPreviousLBIP]; !captured {
		if cr.Annotations == nil {
			cr.Annotations = map[string]string{}
		}
		cr.Annotations[annotationPreviousLBIP] = svc.Spec.LoadBalancerIP
		if err := r.Update(ctx, cr); err != nil {
			return fmt.Errorf("record previous loadBalancerIP: %w", err)
		}
	}

	if svc.Spec.LoadBalancerIP == ip {
		return nil
	}
	svc.Spec.LoadBalancerIP = ip
	return r.Update(ctx, &svc)
}

// restoreServiceLBIP puts back the .spec.loadBalancerIP the Service had
// before the operator first touched it. A missing Service is a no-op —
// there's nothing to restore if it was deleted out from under us.
func (r *LoadBalancerReconciler) restoreServiceLBIP(ctx context.Context, cr *argov1.ClusterbookLoadBalancer) error {
	ref := *cr.Spec.ServiceRef
	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &svc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	prior := cr.Annotations[annotationPreviousLBIP]
	if svc.Spec.LoadBalancerIP == prior {
		return nil
	}
	svc.Spec.LoadBalancerIP = prior
	return r.Update(ctx, &svc)
}

func labelSelectorToMap(sel *metav1.LabelSelector) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	if len(sel.MatchLabels) > 0 {
		m := map[string]interface{}{}
		for k, v := range sel.MatchLabels {
			m[k] = v
		}
		out["matchLabels"] = m
	}
	if len(sel.MatchExpressions) > 0 {
		exprs := []interface{}{}
		for _, e := range sel.MatchExpressions {
			values := make([]interface{}, 0, len(e.Values))
			for _, v := range e.Values {
				values = append(values, v)
			}
			exprs = append(exprs, map[string]interface{}{
				"key":      e.Key,
				"operator": string(e.Operator),
				"values":   values,
			})
		}
		out["matchExpressions"] = exprs
	}
	return out, nil
}
