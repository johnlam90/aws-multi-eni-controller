// Package main implements the AWS Multi-ENI Controller, which manages the lifecycle
// of AWS Elastic Network Interfaces (ENIs) for Kubernetes nodes.
//
// The controller watches NodeENI custom resources and automatically creates, attaches,
// and manages ENIs for nodes that match the specified selectors. It supports multiple
// subnets and security groups, and can be configured through environment variables.
package main

import (
	"flag"
	"os"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/controller"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp" // Enable GCP authentication
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
	version  = "v1.3.0" // Version of the controller
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = networkingv1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var enableDevMode bool

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableDevMode, "dev-mode", true, "Enable development mode for logging.")
	flag.Parse()

	// Set up logging
	ctrl.SetLogger(zap.New(zap.UseDevMode(enableDevMode)))

	// Log startup information
	setupLog.Info("Starting AWS Multi-ENI Controller", "version", version)

	// Create the controller manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "nodeeni-controller-leader-election",
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	// Create the NodeENI controller
	nodeENIReconciler, err := controller.NewNodeENIReconciler(mgr)
	if err != nil {
		setupLog.Error(err, "Unable to create NodeENI controller")
		os.Exit(1)
	}

	if err = nodeENIReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to set up NodeENI controller", "controller", "NodeENI")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
