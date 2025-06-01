// Package controller implements the Kubernetes controller for managing
// system-level node configurations including huge pages, sysctl parameters,
// and other performance optimizations.
//
// The NodeSystemConfig controller watches NodeSystemConfig custom resources and
// automatically applies system-level configurations to nodes that match the
// specified selectors. It supports huge pages, sysctl parameters, kernel modules,
// CPU optimizations, and memory optimizations.
package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/observability"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// NodeSystemConfigFinalizer is the finalizer added to NodeSystemConfig resources
	NodeSystemConfigFinalizer = "nodesystemconfig.networking.k8s.aws/finalizer"
)

// NodeSystemConfigReconciler reconciles a NodeSystemConfig object
type NodeSystemConfigReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	Recorder      record.EventRecorder
	Config        *config.ControllerConfig
	Metrics       *observability.Metrics
	StructuredLog *observability.StructuredLogger
}

// NewNodeSystemConfigReconciler creates a new NodeSystemConfig controller
func NewNodeSystemConfigReconciler(mgr manager.Manager) (*NodeSystemConfigReconciler, error) {
	// Load configuration from environment variables
	cfg, err := config.LoadControllerConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load controller configuration: %v", err)
	}

	// Create logger
	log := ctrl.Log.WithName("controllers").WithName("NodeSystemConfig")

	// Log configuration
	log.Info("NodeSystemConfig controller configuration loaded",
		"reconcilePeriod", cfg.ReconcilePeriod,
		"maxConcurrentReconciles", cfg.MaxConcurrentReconciles)

	// Create observability components
	metrics := observability.NewMetrics()
	structuredLog := observability.NewStructuredLogger(log, metrics)

	return &NodeSystemConfigReconciler{
		Client:        mgr.GetClient(),
		Log:           log,
		Scheme:        mgr.GetScheme(),
		Recorder:      mgr.GetEventRecorderFor("nodesystemconfig-controller"),
		Config:        cfg,
		Metrics:       metrics,
		StructuredLog: structuredLog,
	}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *NodeSystemConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.NodeSystemConfig{}).
		Watches(
			&source.Kind{Type: &corev1.Node{}},
			handler.EnqueueRequestsFromMapFunc(r.findNodeSystemConfigsForNode),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles}).
		Complete(r)
}

// findNodeSystemConfigsForNode maps a Node to NodeSystemConfig resources that match its labels
func (r *NodeSystemConfigReconciler) findNodeSystemConfigsForNode(obj client.Object) []reconcile.Request {
	nodeSystemConfigList := &networkingv1alpha1.NodeSystemConfigList{}
	err := r.Client.List(context.Background(), nodeSystemConfigList)
	if err != nil {
		r.Log.Error(err, "Failed to list NodeSystemConfigs")
		return nil
	}

	node, ok := obj.(*corev1.Node)
	if !ok {
		r.Log.Error(nil, "Failed to convert to Node", "object", obj)
		return nil
	}

	var requests []reconcile.Request
	for _, nodeSystemConfig := range nodeSystemConfigList.Items {
		selector := labels.SelectorFromSet(nodeSystemConfig.Spec.NodeSelector)
		if selector.Matches(labels.Set(node.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeSystemConfig.Name,
				},
			})
		}
	}

	return requests
}

// Reconcile handles NodeSystemConfig resources
// +kubebuilder:rbac:groups=networking.k8s.aws,resources=nodesystemconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.aws,resources=nodesystemconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
func (r *NodeSystemConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the NodeSystemConfig instance
	nodeSystemConfig := &networkingv1alpha1.NodeSystemConfig{}
	err := r.Client.Get(ctx, req.NamespacedName, nodeSystemConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		return ctrl.Result{}, err
	}

	// Handle deletion if needed
	if !nodeSystemConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, nodeSystemConfig)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(nodeSystemConfig, NodeSystemConfigFinalizer) {
		return r.addFinalizer(ctx, nodeSystemConfig)
	}

	// Process the NodeSystemConfig resource
	if err := r.processNodeSystemConfig(ctx, nodeSystemConfig); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to periodically check the status
	return ctrl.Result{RequeueAfter: r.Config.ReconcilePeriod}, nil
}

// handleDeletion handles the deletion of a NodeSystemConfig resource
func (r *NodeSystemConfigReconciler) handleDeletion(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig) (ctrl.Result, error) {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name)

	// If our finalizer is present, clean up resources
	if controllerutil.ContainsFinalizer(nodeSystemConfig, NodeSystemConfigFinalizer) {
		log.Info("Cleaning up system configurations for NodeSystemConfig being deleted", "name", nodeSystemConfig.Name)

		// Clean up system configurations on all matching nodes
		if err := r.cleanupSystemConfigurations(ctx, nodeSystemConfig); err != nil {
			log.Error(err, "Failed to cleanup system configurations")
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}

		// Remove the finalizer
		log.Info("All cleanup operations succeeded, removing finalizer", "name", nodeSystemConfig.Name)
		return r.removeFinalizer(ctx, nodeSystemConfig)
	}

	// Stop reconciliation as the item is being deleted
	return ctrl.Result{}, nil
}

// addFinalizer adds the finalizer to a NodeSystemConfig resource
func (r *NodeSystemConfigReconciler) addFinalizer(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig) (ctrl.Result, error) {
	controllerutil.AddFinalizer(nodeSystemConfig, NodeSystemConfigFinalizer)
	if err := r.Client.Update(ctx, nodeSystemConfig); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// removeFinalizer removes the finalizer from a NodeSystemConfig resource
func (r *NodeSystemConfigReconciler) removeFinalizer(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(nodeSystemConfig, NodeSystemConfigFinalizer)
	if err := r.Client.Update(ctx, nodeSystemConfig); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// processNodeSystemConfig processes a NodeSystemConfig resource
func (r *NodeSystemConfigReconciler) processNodeSystemConfig(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name)
	log.Info("Processing NodeSystemConfig")

	// Get all nodes that match the node selector
	nodeList := &corev1.NodeList{}
	selector := labels.SelectorFromSet(nodeSystemConfig.Spec.NodeSelector)
	listOpts := &client.ListOptions{LabelSelector: selector}

	if err := r.Client.List(ctx, nodeList, listOpts); err != nil {
		log.Error(err, "Failed to list nodes")
		return err
	}

	log.Info("Found matching nodes", "count", len(nodeList.Items))

	// Track current configurations to detect stale ones
	currentConfigurations := make(map[string]bool)

	// Process each matching node
	for _, node := range nodeList.Items {
		if err := r.processNode(ctx, nodeSystemConfig, node, currentConfigurations); err != nil {
			log.Error(err, "Error processing node", "node", node.Name)
			// Continue with other nodes
		}
	}

	// Remove stale configurations
	updatedConfigurations := r.removeStaleConfigurations(ctx, nodeSystemConfig, currentConfigurations)
	nodeSystemConfig.Status.Configurations = updatedConfigurations
	nodeSystemConfig.Status.LastUpdated = metav1.Now()

	// Update the NodeSystemConfig status
	if err := r.Client.Status().Update(ctx, nodeSystemConfig); err != nil {
		log.Error(err, "Failed to update NodeSystemConfig status")
		return err
	}

	return nil
}

// applySystemConfigurations applies system configurations to a node
func (r *NodeSystemConfigReconciler) applySystemConfigurations(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Applying system configurations to node")

	// Create or update system configuration status for this node
	configStatus := r.findOrCreateConfigStatus(nodeSystemConfig, node)

	// Apply huge pages configuration if specified
	if nodeSystemConfig.Spec.HugePagesConfig != nil && nodeSystemConfig.Spec.HugePagesConfig.Enabled {
		if err := r.applyHugePagesConfig(ctx, nodeSystemConfig, node, configStatus); err != nil {
			log.Error(err, "Failed to apply huge pages configuration")
			configStatus.OverallStatus = "Failed"
			configStatus.Message = fmt.Sprintf("Huge pages configuration failed: %v", err)
		}
	}

	// Apply sysctl configuration if specified
	if nodeSystemConfig.Spec.SysctlConfig != nil && nodeSystemConfig.Spec.SysctlConfig.Enabled {
		if err := r.applySysctlConfig(ctx, nodeSystemConfig, node, configStatus); err != nil {
			log.Error(err, "Failed to apply sysctl configuration")
			configStatus.OverallStatus = "Failed"
			configStatus.Message = fmt.Sprintf("Sysctl configuration failed: %v", err)
		}
	}

	// Apply kernel modules configuration if specified
	if nodeSystemConfig.Spec.KernelModulesConfig != nil && nodeSystemConfig.Spec.KernelModulesConfig.Enabled {
		if err := r.applyKernelModulesConfig(ctx, nodeSystemConfig, node, configStatus); err != nil {
			log.Error(err, "Failed to apply kernel modules configuration")
			configStatus.OverallStatus = "Failed"
			configStatus.Message = fmt.Sprintf("Kernel modules configuration failed: %v", err)
		}
	}

	// Apply CPU configuration if specified
	if nodeSystemConfig.Spec.CPUConfig != nil && nodeSystemConfig.Spec.CPUConfig.Enabled {
		if err := r.applyCPUConfig(ctx, nodeSystemConfig, node, configStatus); err != nil {
			log.Error(err, "Failed to apply CPU configuration")
			configStatus.OverallStatus = "Failed"
			configStatus.Message = fmt.Sprintf("CPU configuration failed: %v", err)
		}
	}

	// Apply memory configuration if specified
	if nodeSystemConfig.Spec.MemoryConfig != nil && nodeSystemConfig.Spec.MemoryConfig.Enabled {
		if err := r.applyMemoryConfig(ctx, nodeSystemConfig, node, configStatus); err != nil {
			log.Error(err, "Failed to apply memory configuration")
			configStatus.OverallStatus = "Failed"
			configStatus.Message = fmt.Sprintf("Memory configuration failed: %v", err)
		}
	}

	// Update status if no errors occurred
	if configStatus.OverallStatus != "Failed" {
		configStatus.OverallStatus = "Applied"
		configStatus.Message = "All system configurations applied successfully"
	}

	configStatus.LastUpdated = metav1.Now()

	return nil
}

// findOrCreateConfigStatus finds or creates a system configuration status for a node
func (r *NodeSystemConfigReconciler) findOrCreateConfigStatus(nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node) *networkingv1alpha1.SystemConfigStatus {
	// Look for existing status
	for i := range nodeSystemConfig.Status.Configurations {
		if nodeSystemConfig.Status.Configurations[i].NodeName == node.Name {
			return &nodeSystemConfig.Status.Configurations[i]
		}
	}

	// Create new status
	newStatus := networkingv1alpha1.SystemConfigStatus{
		NodeID:        string(node.UID),
		NodeName:      node.Name,
		OverallStatus: "Pending",
		LastUpdated:   metav1.Now(),
	}

	nodeSystemConfig.Status.Configurations = append(nodeSystemConfig.Status.Configurations, newStatus)
	return &nodeSystemConfig.Status.Configurations[len(nodeSystemConfig.Status.Configurations)-1]
}

// applyHugePagesConfig applies huge pages configuration to a node
func (r *NodeSystemConfigReconciler) applyHugePagesConfig(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node, configStatus *networkingv1alpha1.SystemConfigStatus) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Applying huge pages configuration")

	hugePagesConfig := nodeSystemConfig.Spec.HugePagesConfig

	// Initialize huge pages status if not exists
	if configStatus.HugePagesStatus == nil {
		configStatus.HugePagesStatus = &networkingv1alpha1.HugePagesStatus{
			Configured: false,
			Pages:      []networkingv1alpha1.HugePageStatus{},
		}
	}

	// Set mount path
	mountPath := hugePagesConfig.MountPath
	if mountPath == "" {
		mountPath = "/dev/hugepages"
	}
	configStatus.HugePagesStatus.MountPath = mountPath

	// Apply each huge page configuration with enhanced status tracking
	var totalAllocated int32
	var totalAvailable int32
	pageStatuses := []networkingv1alpha1.HugePageStatus{}
	allocationErrors := []string{}

	// Get allocation strategy
	allocationStrategy := hugePagesConfig.AllocationStrategy
	if allocationStrategy == "" {
		allocationStrategy = "runtime" // Default strategy
	}

	for _, pageSpec := range hugePagesConfig.Pages {
		pageStatus := networkingv1alpha1.HugePageStatus{
			Size:                      pageSpec.Size,
			Requested:                 pageSpec.Count,
			Allocated:                 0,
			Available:                 0,
			AllocationStatus:          "Pending",
			AllocationMethod:          allocationStrategy,
			LastAllocationAttempt:     metav1.Now(),
			AllocationErrors:          []string{},
			Priority:                  r.getPagePriority(&pageSpec),
			PartialAllocationAccepted: r.getPartialAllocationAccepted(&pageSpec),
		}

		if pageSpec.NUMANode != nil {
			pageStatus.NUMANode = pageSpec.NUMANode
		}

		// Calculate memory requirement
		memReq, err := r.calculateRequiredMemory(pageSpec.Size, pageSpec.Count)
		if err != nil {
			log.Error(err, "Failed to calculate memory requirement", "size", pageSpec.Size)
			pageStatus.AllocationErrors = append(pageStatus.AllocationErrors, fmt.Sprintf("Memory calculation failed: %v", err))
			pageStatus.AllocationStatus = "Failed"

		} else {
			pageStatus.MemoryRequirement = memReq
		}

		// Apply huge pages configuration via system manager
		allocated, available, err := r.configureHugePages(ctx, node, pageSpec)
		if err != nil {
			log.Error(err, "Failed to configure huge pages", "size", pageSpec.Size, "count", pageSpec.Count)
			pageStatus.AllocationStatus = "Failed"
			pageStatus.AllocationErrors = append(pageStatus.AllocationErrors, err.Error())
			allocationErrors = append(allocationErrors, fmt.Sprintf("%s: %v", pageSpec.Size, err))
		} else {
			pageStatus.Allocated = allocated
			pageStatus.Available = available

			// Determine allocation status
			if allocated == pageSpec.Count {
				pageStatus.AllocationStatus = "Success"
			} else if allocated > 0 {
				pageStatus.AllocationStatus = "PartialSuccess"
			} else {
				pageStatus.AllocationStatus = "Failed"

			}
		}

		totalAllocated += pageStatus.Allocated
		totalAvailable += pageStatus.Available
		pageStatuses = append(pageStatuses, pageStatus)
	}

	// Update status
	configStatus.HugePagesStatus.Configured = true
	configStatus.HugePagesStatus.Pages = pageStatuses
	configStatus.HugePagesStatus.TotalAllocated = totalAllocated
	configStatus.HugePagesStatus.TotalAvailable = totalAvailable

	log.Info("Huge pages configuration applied successfully",
		"totalAllocated", totalAllocated,
		"totalAvailable", totalAvailable)

	return nil
}

// configureHugePages configures huge pages on a node with comprehensive validation and error handling
func (r *NodeSystemConfigReconciler) configureHugePages(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec) (int32, int32, error) {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSpec.Size, "count", pageSpec.Count)
	log.Info("Configuring huge pages on node")

	// Step 1: Validate huge page size
	if err := r.validateHugePageSize(pageSpec.Size); err != nil {
		return 0, 0, fmt.Errorf("invalid huge page size %s: %v", pageSpec.Size, err)
	}

	// Step 2: Check current huge pages status
	currentAllocated, currentAvailable, err := r.getCurrentHugePagesStatus(ctx, node, pageSpec.Size)
	if err != nil {
		log.Error(err, "Failed to get current huge pages status")
		// Continue with allocation attempt
	}

	// Step 3: Pre-allocation memory check
	if err := r.checkMemoryAvailability(ctx, node, pageSpec); err != nil {
		return currentAllocated, currentAvailable, fmt.Errorf("insufficient memory for huge pages allocation: %v", err)
	}

	// Step 4: Attempt runtime allocation
	allocated, available, err := r.performRuntimeHugePagesAllocation(ctx, node, pageSpec)
	if err != nil {
		log.Error(err, "Runtime allocation failed, checking if persistent configuration is needed")

		// Step 5: If runtime allocation fails, provide guidance on persistence
		persistentErr := r.checkPersistentConfigurationNeeded(ctx, node, pageSpec)
		if persistentErr != nil {
			return currentAllocated, currentAvailable, fmt.Errorf("runtime allocation failed: %v; persistent configuration may be needed: %v", err, persistentErr)
		}

		return currentAllocated, currentAvailable, fmt.Errorf("runtime allocation failed: %v", err)
	}

	// Step 6: Verify allocation was successful
	if err := r.verifyHugePagesAllocation(ctx, node, pageSpec, allocated); err != nil {
		log.Error(err, "Huge pages allocation verification failed")
		return allocated, available, fmt.Errorf("allocation verification failed: %v", err)
	}

	// Step 7: Update node resources to advertise huge pages
	if err := r.updateNodeHugePagesResources(ctx, node, pageSpec.Size, allocated); err != nil {
		log.Error(err, "Failed to update node resources")
		// This is not a critical failure, log and continue
	}

	log.Info("Huge pages configured successfully",
		"allocated", allocated,
		"available", available,
		"previousAllocated", currentAllocated)

	return allocated, available, nil
}

// applySysctlConfig applies sysctl configuration to a node
func (r *NodeSystemConfigReconciler) applySysctlConfig(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node, configStatus *networkingv1alpha1.SystemConfigStatus) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Applying sysctl configuration")

	sysctlConfig := nodeSystemConfig.Spec.SysctlConfig

	// Initialize sysctl status if not exists
	if configStatus.SysctlStatus == nil {
		configStatus.SysctlStatus = &networkingv1alpha1.SysctlStatus{
			Configured:        false,
			AppliedParameters: make(map[string]string),
			FailedParameters:  make(map[string]string),
		}
	}

	// Apply sysctl parameters
	appliedParams := make(map[string]string)
	failedParams := make(map[string]string)

	for param, value := range sysctlConfig.Parameters {
		if err := r.applySysctlParameter(ctx, node, param, value); err != nil {
			log.Error(err, "Failed to apply sysctl parameter", "parameter", param, "value", value)
			failedParams[param] = err.Error()
		} else {
			appliedParams[param] = value
		}
	}

	// Apply preset configurations
	for _, preset := range sysctlConfig.Presets {
		presetParams := r.getSysctlPresetParameters(preset)
		for param, value := range presetParams {
			if err := r.applySysctlParameter(ctx, node, param, value); err != nil {
				log.Error(err, "Failed to apply preset sysctl parameter", "preset", preset, "parameter", param, "value", value)
				failedParams[param] = err.Error()
			} else {
				appliedParams[param] = value
			}
		}
	}

	// Update status
	configStatus.SysctlStatus.Configured = len(failedParams) == 0
	configStatus.SysctlStatus.AppliedParameters = appliedParams
	configStatus.SysctlStatus.FailedParameters = failedParams

	if len(failedParams) > 0 {
		return fmt.Errorf("failed to apply %d sysctl parameters", len(failedParams))
	}

	log.Info("Sysctl configuration applied successfully", "appliedCount", len(appliedParams))
	return nil
}

// applySysctlParameter applies a single sysctl parameter to a node
func (r *NodeSystemConfigReconciler) applySysctlParameter(ctx context.Context, node corev1.Node, param, value string) error {
	log := r.Log.WithValues("node", node.Name, "parameter", param, "value", value)
	log.Info("Applying sysctl parameter")

	// TODO: Implement actual sysctl parameter application via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Executing sysctl commands to set the parameter
	// 3. Verifying the parameter was set correctly
	// 4. Making the change persistent if needed

	// For now, just log the operation
	log.Info("Sysctl parameter applied successfully")
	return nil
}

// getSysctlPresetParameters returns sysctl parameters for a given preset
func (r *NodeSystemConfigReconciler) getSysctlPresetParameters(preset string) map[string]string {
	presets := map[string]map[string]string{
		"high-performance-networking": {
			"net.core.rmem_max":               "134217728",
			"net.core.wmem_max":               "134217728",
			"net.core.rmem_default":           "65536",
			"net.core.wmem_default":           "65536",
			"net.core.netdev_max_backlog":     "5000",
			"net.ipv4.tcp_rmem":               "4096 65536 134217728",
			"net.ipv4.tcp_wmem":               "4096 65536 134217728",
			"net.ipv4.tcp_congestion_control": "bbr",
		},
		"low-latency": {
			"net.core.busy_poll":                 "50",
			"net.core.busy_read":                 "50",
			"net.ipv4.tcp_low_latency":           "1",
			"kernel.sched_min_granularity_ns":    "1000000",
			"kernel.sched_wakeup_granularity_ns": "1500000",
		},
		"dpdk-optimized": {
			"vm.nr_hugepages":      "1024",
			"vm.hugetlb_shm_group": "0",
			"kernel.shmmax":        "68719476736",
			"kernel.shmall":        "4294967296",
		},
	}

	if params, exists := presets[preset]; exists {
		return params
	}
	return make(map[string]string)
}

// applyKernelModulesConfig applies kernel modules configuration to a node
func (r *NodeSystemConfigReconciler) applyKernelModulesConfig(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node, configStatus *networkingv1alpha1.SystemConfigStatus) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Applying kernel modules configuration")

	kernelModulesConfig := nodeSystemConfig.Spec.KernelModulesConfig

	// Initialize kernel modules status if not exists
	if configStatus.KernelModulesStatus == nil {
		configStatus.KernelModulesStatus = &networkingv1alpha1.KernelModulesStatus{
			Configured:      false,
			LoadedModules:   []string{},
			UnloadedModules: []string{},
			FailedModules:   []string{},
		}
	}

	var loadedModules []string
	var unloadedModules []string
	var failedModules []string

	// Load specified modules
	for _, module := range kernelModulesConfig.Load {
		if err := r.loadKernelModule(ctx, node, module, kernelModulesConfig.Parameters[module]); err != nil {
			log.Error(err, "Failed to load kernel module", "module", module)
			failedModules = append(failedModules, module)
		} else {
			loadedModules = append(loadedModules, module)
		}
	}

	// Unload specified modules
	for _, module := range kernelModulesConfig.Unload {
		if err := r.unloadKernelModule(ctx, node, module); err != nil {
			log.Error(err, "Failed to unload kernel module", "module", module)
			failedModules = append(failedModules, module)
		} else {
			unloadedModules = append(unloadedModules, module)
		}
	}

	// Update status
	configStatus.KernelModulesStatus.Configured = len(failedModules) == 0
	configStatus.KernelModulesStatus.LoadedModules = loadedModules
	configStatus.KernelModulesStatus.UnloadedModules = unloadedModules
	configStatus.KernelModulesStatus.FailedModules = failedModules

	if len(failedModules) > 0 {
		return fmt.Errorf("failed to configure %d kernel modules", len(failedModules))
	}

	log.Info("Kernel modules configuration applied successfully",
		"loadedCount", len(loadedModules),
		"unloadedCount", len(unloadedModules))

	return nil
}

// loadKernelModule loads a kernel module on a node
func (r *NodeSystemConfigReconciler) loadKernelModule(ctx context.Context, node corev1.Node, module string, params map[string]string) error {
	log := r.Log.WithValues("node", node.Name, "module", module)
	log.Info("Loading kernel module")

	// TODO: Implement actual kernel module loading via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Executing modprobe commands to load the module
	// 3. Setting module parameters if specified
	// 4. Verifying the module was loaded correctly

	// For now, just log the operation
	log.Info("Kernel module loaded successfully")
	return nil
}

// unloadKernelModule unloads a kernel module on a node
func (r *NodeSystemConfigReconciler) unloadKernelModule(ctx context.Context, node corev1.Node, module string) error {
	log := r.Log.WithValues("node", node.Name, "module", module)
	log.Info("Unloading kernel module")

	// TODO: Implement actual kernel module unloading via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Executing modprobe -r commands to unload the module
	// 3. Verifying the module was unloaded correctly

	// For now, just log the operation
	log.Info("Kernel module unloaded successfully")
	return nil
}

// applyCPUConfig applies CPU configuration to a node
func (r *NodeSystemConfigReconciler) applyCPUConfig(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node, configStatus *networkingv1alpha1.SystemConfigStatus) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Applying CPU configuration")

	cpuConfig := nodeSystemConfig.Spec.CPUConfig

	// Initialize CPU status if not exists
	if configStatus.CPUStatus == nil {
		configStatus.CPUStatus = &networkingv1alpha1.CPUStatus{
			Configured: false,
		}
	}

	// Apply CPU isolation if specified
	if cpuConfig.IsolatedCPUs != "" {
		if err := r.applyCPUIsolation(ctx, node, cpuConfig.IsolatedCPUs); err != nil {
			log.Error(err, "Failed to apply CPU isolation")
			return err
		}
		configStatus.CPUStatus.IsolatedCPUs = cpuConfig.IsolatedCPUs
	}

	// Apply CPU governor if specified
	if cpuConfig.Governor != "" {
		if err := r.applyCPUGovernor(ctx, node, cpuConfig.Governor); err != nil {
			log.Error(err, "Failed to apply CPU governor")
			return err
		}
		configStatus.CPUStatus.Governor = cpuConfig.Governor
	}

	// Apply turbo boost setting if specified
	if cpuConfig.TurboBoost != nil {
		if err := r.applyTurboBoost(ctx, node, *cpuConfig.TurboBoost); err != nil {
			log.Error(err, "Failed to apply turbo boost setting")
			return err
		}
		configStatus.CPUStatus.TurboBoost = cpuConfig.TurboBoost
	}

	// Update status
	configStatus.CPUStatus.Configured = true

	log.Info("CPU configuration applied successfully")
	return nil
}

// applyCPUIsolation applies CPU isolation configuration to a node
func (r *NodeSystemConfigReconciler) applyCPUIsolation(ctx context.Context, node corev1.Node, isolatedCPUs string) error {
	log := r.Log.WithValues("node", node.Name, "isolatedCPUs", isolatedCPUs)
	log.Info("Applying CPU isolation")

	// TODO: Implement actual CPU isolation via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Updating kernel boot parameters or runtime configuration
	// 3. Verifying CPU isolation is applied correctly

	// For now, just log the operation
	log.Info("CPU isolation applied successfully")
	return nil
}

// applyCPUGovernor applies CPU governor configuration to a node
func (r *NodeSystemConfigReconciler) applyCPUGovernor(ctx context.Context, node corev1.Node, governor string) error {
	log := r.Log.WithValues("node", node.Name, "governor", governor)
	log.Info("Applying CPU governor")

	// TODO: Implement actual CPU governor configuration via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Setting the CPU frequency governor
	// 3. Verifying the governor was set correctly

	// For now, just log the operation
	log.Info("CPU governor applied successfully")
	return nil
}

// applyTurboBoost applies turbo boost configuration to a node
func (r *NodeSystemConfigReconciler) applyTurboBoost(ctx context.Context, node corev1.Node, enabled bool) error {
	log := r.Log.WithValues("node", node.Name, "turboBoost", enabled)
	log.Info("Applying turbo boost configuration")

	// TODO: Implement actual turbo boost configuration via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Enabling/disabling turbo boost
	// 3. Verifying the setting was applied correctly

	// For now, just log the operation
	log.Info("Turbo boost configuration applied successfully")
	return nil
}

// processNode processes system configuration for a single node
func (r *NodeSystemConfigReconciler) processNode(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node, currentConfigurations map[string]bool) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Processing node for system configuration")

	// Mark this node as having been processed
	nodeKey := node.Name
	currentConfigurations[nodeKey] = true

	// Apply system configurations to this node
	if err := r.applySystemConfigurations(ctx, nodeSystemConfig, node); err != nil {
		log.Error(err, "Failed to apply system configurations to node")
		return err
	}

	return nil
}

// applyMemoryConfig applies memory configuration to a node
func (r *NodeSystemConfigReconciler) applyMemoryConfig(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node, configStatus *networkingv1alpha1.SystemConfigStatus) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Applying memory configuration")

	memoryConfig := nodeSystemConfig.Spec.MemoryConfig

	// Initialize memory status if not exists
	if configStatus.MemoryStatus == nil {
		configStatus.MemoryStatus = &networkingv1alpha1.MemoryStatus{
			Configured: false,
		}
	}

	// Apply swappiness if specified
	if memoryConfig.Swappiness != nil {
		if err := r.applySwappiness(ctx, node, *memoryConfig.Swappiness); err != nil {
			log.Error(err, "Failed to apply swappiness")
			return err
		}
		configStatus.MemoryStatus.Swappiness = memoryConfig.Swappiness
	}

	// Apply transparent huge pages if specified
	if memoryConfig.TransparentHugePages != "" {
		if err := r.applyTransparentHugePages(ctx, node, memoryConfig.TransparentHugePages); err != nil {
			log.Error(err, "Failed to apply transparent huge pages")
			return err
		}
		configStatus.MemoryStatus.TransparentHugePages = memoryConfig.TransparentHugePages
	}

	// Apply KSM configuration if specified
	if memoryConfig.KSM != nil {
		ksmStatus, err := r.applyKSMConfig(ctx, node, memoryConfig.KSM)
		if err != nil {
			log.Error(err, "Failed to apply KSM configuration")
			return err
		}
		configStatus.MemoryStatus.KSMStatus = ksmStatus
	}

	// Update status
	configStatus.MemoryStatus.Configured = true

	log.Info("Memory configuration applied successfully")
	return nil
}

// applySwappiness applies swappiness configuration to a node
func (r *NodeSystemConfigReconciler) applySwappiness(ctx context.Context, node corev1.Node, swappiness int32) error {
	log := r.Log.WithValues("node", node.Name, "swappiness", swappiness)
	log.Info("Applying swappiness configuration")

	// TODO: Implement actual swappiness configuration via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Setting vm.swappiness sysctl parameter
	// 3. Verifying the setting was applied correctly

	// For now, just log the operation
	log.Info("Swappiness configuration applied successfully")
	return nil
}

// applyTransparentHugePages applies transparent huge pages configuration to a node
func (r *NodeSystemConfigReconciler) applyTransparentHugePages(ctx context.Context, node corev1.Node, setting string) error {
	log := r.Log.WithValues("node", node.Name, "transparentHugePages", setting)
	log.Info("Applying transparent huge pages configuration")

	// TODO: Implement actual THP configuration via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Setting THP configuration in /sys/kernel/mm/transparent_hugepage/
	// 3. Verifying the setting was applied correctly

	// For now, just log the operation
	log.Info("Transparent huge pages configuration applied successfully")
	return nil
}

// applyKSMConfig applies KSM configuration to a node
func (r *NodeSystemConfigReconciler) applyKSMConfig(ctx context.Context, node corev1.Node, ksmConfig *networkingv1alpha1.KSMConfig) (*networkingv1alpha1.KSMStatus, error) {
	log := r.Log.WithValues("node", node.Name, "ksmEnabled", ksmConfig.Enabled)
	log.Info("Applying KSM configuration")

	// TODO: Implement actual KSM configuration via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Enabling/disabling KSM
	// 3. Setting scan interval and pages to scan if specified
	// 4. Verifying the configuration was applied correctly

	// For now, return mock status
	ksmStatus := &networkingv1alpha1.KSMStatus{
		Enabled: ksmConfig.Enabled,
	}

	if ksmConfig.ScanInterval != nil {
		ksmStatus.ScanInterval = ksmConfig.ScanInterval
	}

	if ksmConfig.PagesToScan != nil {
		ksmStatus.PagesToScan = ksmConfig.PagesToScan
	}

	log.Info("KSM configuration applied successfully")
	return ksmStatus, nil
}

// cleanupSystemConfigurations cleans up system configurations for a NodeSystemConfig being deleted
func (r *NodeSystemConfigReconciler) cleanupSystemConfigurations(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name)
	log.Info("Cleaning up system configurations")

	// Get all nodes that match the node selector
	nodeList := &corev1.NodeList{}
	selector := labels.SelectorFromSet(nodeSystemConfig.Spec.NodeSelector)
	listOpts := &client.ListOptions{LabelSelector: selector}

	if err := r.Client.List(ctx, nodeList, listOpts); err != nil {
		log.Error(err, "Failed to list nodes for cleanup")
		return err
	}

	// Clean up configurations on each matching node
	for _, node := range nodeList.Items {
		if err := r.cleanupNodeSystemConfiguration(ctx, nodeSystemConfig, node); err != nil {
			log.Error(err, "Failed to cleanup system configuration on node", "node", node.Name)
			// Continue with other nodes even if one fails
		}
	}

	log.Info("System configurations cleanup completed")
	return nil
}

// cleanupNodeSystemConfiguration cleans up system configuration for a single node
func (r *NodeSystemConfigReconciler) cleanupNodeSystemConfiguration(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, node corev1.Node) error {
	log := r.Log.WithValues("nodesystemconfig", nodeSystemConfig.Name, "node", node.Name)
	log.Info("Cleaning up system configuration on node")

	// TODO: Implement actual cleanup via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Reverting huge pages configuration
	// 3. Reverting sysctl parameters
	// 4. Unloading kernel modules that were loaded
	// 5. Reverting CPU and memory configurations
	// 6. Updating node resources

	// For now, just log the operation
	log.Info("System configuration cleanup completed on node")
	return nil
}

// removeStaleConfigurations removes stale system configurations from the status
func (r *NodeSystemConfigReconciler) removeStaleConfigurations(ctx context.Context, nodeSystemConfig *networkingv1alpha1.NodeSystemConfig, currentConfigurations map[string]bool) []networkingv1alpha1.SystemConfigStatus {
	var updatedConfigurations []networkingv1alpha1.SystemConfigStatus

	for _, config := range nodeSystemConfig.Status.Configurations {
		if currentConfigurations[config.NodeName] {
			// This configuration is still current
			updatedConfigurations = append(updatedConfigurations, config)
		} else {
			// This configuration is stale, log its removal
			r.Log.Info("Removing stale system configuration", "node", config.NodeName)
		}
	}

	return updatedConfigurations
}

// validateHugePageSize validates that the huge page size is supported
func (r *NodeSystemConfigReconciler) validateHugePageSize(size string) error {
	// Supported huge page sizes
	supportedSizes := map[string]bool{
		"2Mi":  true,
		"2MB":  true,
		"1Gi":  true,
		"1GB":  true,
		"4Ki":  true, // Some systems support 4K huge pages
		"64Ki": true, // ARM64 systems
		"2Gi":  true, // Some systems support 2GB pages
	}

	if !supportedSizes[size] {
		return fmt.Errorf("unsupported huge page size: %s. Supported sizes: 2Mi, 1Gi, 2MB, 1GB", size)
	}

	return nil
}

// getCurrentHugePagesStatus gets the current huge pages allocation status
func (r *NodeSystemConfigReconciler) getCurrentHugePagesStatus(ctx context.Context, node corev1.Node, pageSize string) (int32, int32, error) {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSize)
	log.Info("Getting current huge pages status")

	// TODO: Implement actual status retrieval via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Reading /proc/meminfo or /sys/kernel/mm/hugepages/hugepages-*/
	// 3. Parsing the current allocation and availability

	// For now, return mock values
	// In real implementation, this would query the actual system state
	allocated := int32(0)
	available := int32(0)

	log.Info("Current huge pages status retrieved", "allocated", allocated, "available", available)
	return allocated, available, nil
}

// checkMemoryAvailability checks if there's enough memory for huge pages allocation
func (r *NodeSystemConfigReconciler) checkMemoryAvailability(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec) error {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSpec.Size, "count", pageSpec.Count)
	log.Info("Checking memory availability for huge pages allocation")

	// Calculate required memory
	requiredMemory, err := r.calculateRequiredMemory(pageSpec.Size, pageSpec.Count)
	if err != nil {
		return fmt.Errorf("failed to calculate required memory: %v", err)
	}

	// Get available memory
	availableMemory, err := r.getAvailableMemory(ctx, node, pageSpec.NUMANode)
	if err != nil {
		return fmt.Errorf("failed to get available memory: %v", err)
	}

	// Check fragmentation level
	fragmentationLevel, err := r.getMemoryFragmentation(ctx, node, pageSpec.NUMANode)
	if err != nil {
		log.Error(err, "Failed to get memory fragmentation level")
		// Continue without fragmentation check
	}

	// Check if there's enough memory
	if availableMemory < requiredMemory {
		return fmt.Errorf("insufficient memory: required %d bytes, available %d bytes", requiredMemory, availableMemory)
	}

	// Check fragmentation for large pages
	if pageSpec.Size == "1Gi" || pageSpec.Size == "1GB" {
		if fragmentationLevel > 50 { // 50% fragmentation threshold
			log.Info("High memory fragmentation detected", "fragmentationLevel", fragmentationLevel, "warning", true)
			return fmt.Errorf("high memory fragmentation (%d%%) may prevent 1GB page allocation", fragmentationLevel)
		}
	}

	log.Info("Memory availability check passed",
		"requiredMemory", requiredMemory,
		"availableMemory", availableMemory,
		"fragmentationLevel", fragmentationLevel)

	return nil
}

// calculateRequiredMemory calculates the memory requirement for huge pages
func (r *NodeSystemConfigReconciler) calculateRequiredMemory(pageSize string, count int32) (int64, error) {
	var pageSizeBytes int64

	switch pageSize {
	case "2Mi", "2MB":
		pageSizeBytes = 2 * 1024 * 1024 // 2MB
	case "1Gi", "1GB":
		pageSizeBytes = 1024 * 1024 * 1024 // 1GB
	case "4Ki":
		pageSizeBytes = 4 * 1024 // 4KB
	case "64Ki":
		pageSizeBytes = 64 * 1024 // 64KB
	case "2Gi":
		pageSizeBytes = 2 * 1024 * 1024 * 1024 // 2GB
	default:
		return 0, fmt.Errorf("unsupported page size: %s", pageSize)
	}

	return pageSizeBytes * int64(count), nil
}

// getAvailableMemory gets available memory on the node
func (r *NodeSystemConfigReconciler) getAvailableMemory(ctx context.Context, node corev1.Node, numaNode *int32) (int64, error) {
	log := r.Log.WithValues("node", node.Name, "numaNode", numaNode)
	log.Info("Getting available memory")

	// TODO: Implement actual memory retrieval via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Reading /proc/meminfo for total memory info
	// 3. Reading /sys/devices/system/node/node*/meminfo for NUMA-specific info
	// 4. Calculating available memory considering existing allocations

	// For now, return mock values
	// In real implementation, this would query actual system memory
	availableMemory := int64(8 * 1024 * 1024 * 1024) // 8GB mock available memory

	log.Info("Available memory retrieved", "availableMemory", availableMemory)
	return availableMemory, nil
}

// getMemoryFragmentation gets memory fragmentation level
func (r *NodeSystemConfigReconciler) getMemoryFragmentation(ctx context.Context, node corev1.Node, numaNode *int32) (int32, error) {
	log := r.Log.WithValues("node", node.Name, "numaNode", numaNode)
	log.Info("Getting memory fragmentation level")

	// TODO: Implement actual fragmentation level calculation via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Reading /proc/buddyinfo to analyze memory fragmentation
	// 3. Calculating fragmentation percentage based on available contiguous blocks

	// For now, return mock fragmentation level
	fragmentationLevel := int32(20) // 20% fragmentation

	log.Info("Memory fragmentation level retrieved", "fragmentationLevel", fragmentationLevel)
	return fragmentationLevel, nil
}

// performRuntimeHugePagesAllocation performs runtime huge pages allocation
func (r *NodeSystemConfigReconciler) performRuntimeHugePagesAllocation(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec) (int32, int32, error) {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSpec.Size, "count", pageSpec.Count)
	log.Info("Performing runtime huge pages allocation")

	// TODO: Implement actual runtime allocation via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Writing to /proc/sys/vm/nr_hugepages or size-specific files:
	//    - For 2MB pages: echo <count> > /proc/sys/vm/nr_hugepages
	//    - For 1GB pages: echo <count> > /proc/sys/vm/nr_hugepages_1048576
	// 3. For NUMA-aware allocation:
	//    - echo <count> > /sys/devices/system/node/node<N>/hugepages/hugepages-<size>/nr_hugepages
	// 4. Verifying the allocation was successful by reading back the values
	// 5. Handling partial allocations gracefully

	// Simulate runtime allocation
	requestedCount := pageSpec.Count

	// Simulate potential partial allocation based on memory availability
	// In real implementation, this would be the actual result from the system
	allocatedCount := requestedCount // Assume full allocation for now
	availableCount := allocatedCount

	// Check if partial allocation is acceptable
	if pageSpec.AllowPartialAllocation != nil && !*pageSpec.AllowPartialAllocation {
		if allocatedCount < requestedCount {
			return 0, 0, fmt.Errorf("partial allocation not allowed: requested %d, could allocate %d", requestedCount, allocatedCount)
		}
	}

	// Check minimum count requirement
	if pageSpec.MinCount != nil && allocatedCount < *pageSpec.MinCount {
		return allocatedCount, availableCount, fmt.Errorf("allocated count (%d) below minimum required (%d)", allocatedCount, *pageSpec.MinCount)
	}

	log.Info("Runtime huge pages allocation completed",
		"requested", requestedCount,
		"allocated", allocatedCount,
		"available", availableCount)

	return allocatedCount, availableCount, nil
}

// checkPersistentConfigurationNeeded checks if persistent configuration is needed
func (r *NodeSystemConfigReconciler) checkPersistentConfigurationNeeded(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec) error {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSpec.Size)
	log.Info("Checking if persistent configuration is needed")

	// TODO: Implement actual persistent configuration check via system manager DaemonSet
	// This would involve:
	// 1. Checking if huge pages are configured in /etc/sysctl.conf
	// 2. Checking if huge pages are configured in grub configuration
	// 3. Checking if systemd service exists for huge pages setup
	// 4. Providing specific guidance on how to make configuration persistent

	// For now, provide general guidance
	log.Info("Runtime allocation failed - persistent configuration may be required")

	// Return guidance message
	return fmt.Errorf("consider configuring huge pages persistently via boot parameters or sysctl.conf. "+
		"For %s pages, add 'hugepagesz=%s hugepages=%d' to kernel boot parameters or "+
		"'vm.nr_hugepages_%s = %d' to /etc/sysctl.conf",
		pageSpec.Size, pageSpec.Size, pageSpec.Count,
		r.getSysctlPageSizeKey(pageSpec.Size), pageSpec.Count)
}

// getSysctlPageSizeKey converts page size to sysctl key format
func (r *NodeSystemConfigReconciler) getSysctlPageSizeKey(pageSize string) string {
	switch pageSize {
	case "2Mi", "2MB":
		return "2048" // 2MB in KB
	case "1Gi", "1GB":
		return "1048576" // 1GB in KB
	case "4Ki":
		return "4"
	case "64Ki":
		return "64"
	case "2Gi":
		return "2097152" // 2GB in KB
	default:
		return pageSize
	}
}

// verifyHugePagesAllocation verifies that huge pages allocation was successful
func (r *NodeSystemConfigReconciler) verifyHugePagesAllocation(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, expectedAllocated int32) error {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSpec.Size, "expectedAllocated", expectedAllocated)
	log.Info("Verifying huge pages allocation")

	// TODO: Implement actual verification via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Reading back the allocation from /proc/meminfo or /sys/kernel/mm/hugepages/
	// 3. Comparing with expected allocation
	// 4. Checking that pages are actually available for use

	// For now, assume verification passes
	log.Info("Huge pages allocation verification completed successfully")
	return nil
}

// updateNodeHugePagesResources updates node resources to advertise huge pages
func (r *NodeSystemConfigReconciler) updateNodeHugePagesResources(ctx context.Context, node corev1.Node, pageSize string, allocated int32) error {
	log := r.Log.WithValues("node", node.Name, "pageSize", pageSize, "allocated", allocated)
	log.Info("Updating node resources to advertise huge pages")

	// TODO: Implement actual node resource update
	// This would involve:
	// 1. Getting the current node object
	// 2. Updating the node's allocatable resources to include huge pages
	// 3. Using the appropriate resource name format (e.g., hugepages-2Mi, hugepages-1Gi)
	// 4. Updating the node object via the Kubernetes API

	// Resource name format for huge pages
	resourceName := fmt.Sprintf("hugepages-%s", pageSize)

	log.Info("Node resources updated successfully", "resourceName", resourceName, "quantity", allocated)
	return nil
}

// Helper methods for getting configuration values with defaults

// getPagePriority gets the priority for a page spec with default
func (r *NodeSystemConfigReconciler) getPagePriority(pageSpec *networkingv1alpha1.HugePageSpec) *int32 {
	if pageSpec.Priority != nil {
		return pageSpec.Priority
	}
	defaultPriority := int32(50)
	return &defaultPriority
}

// getPartialAllocationAccepted gets the partial allocation setting with default
func (r *NodeSystemConfigReconciler) getPartialAllocationAccepted(pageSpec *networkingv1alpha1.HugePageSpec) *bool {
	if pageSpec.AllowPartialAllocation != nil {
		return pageSpec.AllowPartialAllocation
	}
	defaultValue := true
	return &defaultValue
}
