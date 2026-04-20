package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	argov1 "github.com/stuttgart-things/clusterbook-operator/api/v1alpha1"
	"github.com/stuttgart-things/clusterbook-operator/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(argov1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		ctrl.Log.Error(err, "manager")
		os.Exit(1)
	}

	if err := (&controller.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "setup ClusterbookCluster reconciler")
		os.Exit(1)
	}
	if err := (&controller.LoadBalancerReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "setup ClusterbookLoadBalancer reconciler")
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "set up healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		ctrl.Log.Error(err, "set up readyz")
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "run manager")
		os.Exit(1)
	}
}
