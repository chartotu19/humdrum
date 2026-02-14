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

	dwsv1alpha1 "github.com/chartotu19/humdrum/k8s-dws-controller/api/v1alpha1"
	"github.com/chartotu19/humdrum/k8s-dws-controller/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(dwsv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr           string
		healthProbeAddr       string
		enableLeaderElection  bool
		maxConcurrentPerQueue int
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081",
		"The address the health probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager, ensuring only one active controller.")
	flag.IntVar(&maxConcurrentPerQueue, "max-concurrent-per-queue", 5,
		"Maximum number of jobs that can run concurrently per queue.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthProbeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "dws-controller-leader.dws.google.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create queue manager and rebuild state from cluster.
	queueMgr := controller.NewQueueManager(mgr.GetClient(), maxConcurrentPerQueue)

	reconciler := &controller.DWSJobReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		QueueManager: queueMgr,
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DWSJob")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Rebuild queue state on startup (runs after cache sync).
	go func() {
		<-mgr.Elected()
		ctx := ctrl.SetupSignalHandler()
		setupLog.Info("Rebuilding queue state from cluster")
		if err := queueMgr.RebuildQueues(ctx); err != nil {
			setupLog.Error(err, "Failed to rebuild queues on startup")
		}
	}()

	setupLog.Info("Starting DWS controller manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
