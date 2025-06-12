// Package controller implements the Kubernetes controller for managing
// AWS Elastic Network Interfaces (ENIs) for nodes.
//
// The NodeENI controller watches NodeENI custom resources and automatically
// creates, attaches, and manages ENIs for nodes that match the specified selectors.
// It supports multiple subnets and security groups, and handles the lifecycle
// of ENIs including creation, attachment, detachment, and deletion.
package controller

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/aws/retry"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/observability"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/remotecommand"
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
	// NodeENIFinalizer is the finalizer added to NodeENI resources
	NodeENIFinalizer = "nodeeni.networking.k8s.aws/finalizer"
)

// NodeENIReconciler reconciles a NodeENI object
type NodeENIReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	AWS            awsutil.EC2Interface
	Config         *config.ControllerConfig
	CircuitBreaker *retry.CircuitBreaker
	Coordinator    *CoordinationManager
	Metrics        *observability.Metrics
	StructuredLog  *observability.StructuredLogger
}

// NewNodeENIReconciler creates a new NodeENI controller
func NewNodeENIReconciler(mgr manager.Manager) (*NodeENIReconciler, error) {
	// Load configuration from environment variables
	cfg, err := config.LoadControllerConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load controller configuration: %v", err)
	}

	// Create logger
	log := ctrl.Log.WithName("controllers").WithName("NodeENI")

	// Log configuration
	log.Info("Controller configuration loaded",
		"awsRegion", cfg.AWSRegion,
		"reconcilePeriod", cfg.ReconcilePeriod,
		"detachmentTimeout", cfg.DetachmentTimeout,
		"maxConcurrentReconciles", cfg.MaxConcurrentReconciles)

	// Create AWS EC2 client
	ctx := context.Background()
	awsClient, err := awsutil.CreateEC2Client(ctx, cfg.AWSRegion, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS EC2 client: %v", err)
	}

	// Configure IMDS hop limit for container compatibility
	if ec2Client, ok := awsClient.(*awsutil.EC2Client); ok {
		if err := ec2Client.ConfigureIMDSHopLimit(ctx); err != nil {
			log.Error(err, "Failed to configure IMDS hop limit, continuing with default configuration")
			// Don't fail controller startup if IMDS configuration fails
		}
	}

	// Create circuit breaker if enabled
	var circuitBreaker *retry.CircuitBreaker
	if cfg.CircuitBreakerEnabled {
		cbConfig := &retry.CircuitBreakerConfig{
			FailureThreshold:      cfg.CircuitBreakerFailureThreshold,
			SuccessThreshold:      cfg.CircuitBreakerSuccessThreshold,
			Timeout:               cfg.CircuitBreakerTimeout,
			MaxConcurrentRequests: 1, // Conservative default
		}
		circuitBreaker = retry.NewCircuitBreaker(cbConfig, log)
		log.Info("Circuit breaker enabled for AWS operations",
			"failureThreshold", cbConfig.FailureThreshold,
			"successThreshold", cbConfig.SuccessThreshold,
			"timeout", cbConfig.Timeout)
	} else {
		log.Info("Circuit breaker disabled")
	}

	// Create coordination manager
	coordinator := NewCoordinationManager(log)

	// Create observability components
	metrics := observability.NewMetrics()
	structuredLog := observability.NewStructuredLogger(log, metrics)

	return &NodeENIReconciler{
		Client:         mgr.GetClient(),
		Log:            log,
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("nodeeni-controller"),
		AWS:            awsClient,
		Config:         cfg,
		CircuitBreaker: circuitBreaker,
		Coordinator:    coordinator,
		Metrics:        metrics,
		StructuredLog:  structuredLog,
	}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *NodeENIReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Start IMDS configuration retry in the background
	ctx := context.Background()
	r.startIMDSConfigurationRetry(ctx)

	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.NodeENI{}).
		Watches(
			&source.Kind{Type: &corev1.Node{}},
			handler.EnqueueRequestsFromMapFunc(r.findNodeENIsForNode),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles}).
		Complete(r)
}

// findNodeENIsForNode maps a Node to NodeENI resources that match its labels
func (r *NodeENIReconciler) findNodeENIsForNode(obj client.Object) []reconcile.Request {
	nodeENIList := &networkingv1alpha1.NodeENIList{}
	err := r.Client.List(context.Background(), nodeENIList)
	if err != nil {
		r.Log.Error(err, "Failed to list NodeENIs")
		return nil
	}

	node, ok := obj.(*corev1.Node)
	if !ok {
		r.Log.Error(nil, "Failed to convert to Node", "object", obj)
		return nil
	}

	var requests []reconcile.Request
	for _, nodeENI := range nodeENIList.Items {
		selector := labels.SelectorFromSet(nodeENI.Spec.NodeSelector)
		if selector.Matches(labels.Set(node.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeENI.Name,
				},
			})
		}
	}

	return requests
}

// Reconcile handles NodeENI resources
// +kubebuilder:rbac:groups=networking.k8s.aws,resources=nodeenis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.aws,resources=nodeenis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
func (r *NodeENIReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the NodeENI instance
	nodeENI := &networkingv1alpha1.NodeENI{}
	err := r.Client.Get(ctx, req.NamespacedName, nodeENI)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		return ctrl.Result{}, err
	}

	// Handle deletion if needed
	if !nodeENI.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, nodeENI)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(nodeENI, NodeENIFinalizer) {
		return r.addFinalizer(ctx, nodeENI)
	}

	// Process the NodeENI resource
	if err := r.processNodeENI(ctx, nodeENI); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to periodically check the status
	return ctrl.Result{RequeueAfter: r.Config.ReconcilePeriod}, nil
}

// handleDeletion handles the deletion of a NodeENI resource
func (r *NodeENIReconciler) handleDeletion(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) (ctrl.Result, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	// If our finalizer is present, clean up resources
	if controllerutil.ContainsFinalizer(nodeENI, NodeENIFinalizer) {
		log.Info("Cleaning up resources for NodeENI being deleted", "name", nodeENI.Name)

		// Check if cleanup has been running too long
		if r.isCleanupTimedOut(nodeENI) {
			log.Error(nil, "Cleanup has timed out, forcing finalizer removal", "nodeeni", nodeENI.Name)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "CleanupTimeout",
				"Cleanup timed out after maximum duration, forcing finalizer removal")

			// Force remove finalizer to prevent resource from being stuck forever
			controllerutil.RemoveFinalizer(nodeENI, NodeENIFinalizer)
			return r.updateNodeENIWithRetry(ctx, nodeENI)
		}

		// Set cleanup start time if not already set
		if err := r.setCleanupStartTime(ctx, nodeENI); err != nil {
			log.Error(err, "Failed to set cleanup start time")
			return ctrl.Result{RequeueAfter: r.Config.DetachmentTimeout}, nil
		}

		// Clean up all ENI attachments in parallel
		cleanupSucceeded := r.cleanupENIAttachmentsInParallel(ctx, nodeENI)

		// Only remove the finalizer if all cleanup operations succeeded
		if !cleanupSucceeded {
			log.Info("Some cleanup operations failed, will retry later", "name", nodeENI.Name)
			// Requeue with a backoff to retry the cleanup
			return ctrl.Result{RequeueAfter: r.Config.DetachmentTimeout}, nil
		}

		// All cleanup operations succeeded, remove the finalizer
		log.Info("All cleanup operations succeeded, removing finalizer", "name", nodeENI.Name)
		return r.removeFinalizer(ctx, nodeENI)
	}

	// Stop reconciliation as the item is being deleted
	return ctrl.Result{}, nil
}

// Constants for cleanup tracking annotations
const (
	CleanupStartTimeAnnotation = "aws-multi-eni-controller/cleanup-start-time"
)

// isCleanupTimedOut checks if cleanup has been running longer than the maximum allowed duration
func (r *NodeENIReconciler) isCleanupTimedOut(nodeENI *networkingv1alpha1.NodeENI) bool {
	if nodeENI.Annotations == nil {
		return false
	}

	startTimeStr, exists := nodeENI.Annotations[CleanupStartTimeAnnotation]
	if !exists {
		return false
	}

	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		// If we can't parse the time, assume it's not timed out
		return false
	}

	elapsed := time.Since(startTime)
	return elapsed > r.Config.MaxCleanupDuration
}

// setCleanupStartTime sets the cleanup start time annotation if not already set
func (r *NodeENIReconciler) setCleanupStartTime(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) error {
	if nodeENI.Annotations == nil {
		nodeENI.Annotations = make(map[string]string)
	}

	// Only set if not already set
	if _, exists := nodeENI.Annotations[CleanupStartTimeAnnotation]; !exists {
		nodeENI.Annotations[CleanupStartTimeAnnotation] = time.Now().Format(time.RFC3339)

		// Use retry logic for resource version conflicts
		_, err := r.updateNodeENIWithRetry(ctx, nodeENI)
		return err
	}

	return nil
}

// removeFinalizer removes the finalizer from a NodeENI resource with retry
func (r *NodeENIReconciler) removeFinalizer(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(nodeENI, NodeENIFinalizer)

	// Remove cleanup tracking annotation since cleanup is complete
	if nodeENI.Annotations != nil {
		delete(nodeENI.Annotations, CleanupStartTimeAnnotation)
	}

	return r.updateNodeENIWithRetry(ctx, nodeENI)
}

// updateNodeENIWithRetry updates a NodeENI resource with retry logic for resource version conflicts
func (r *NodeENIReconciler) updateNodeENIWithRetry(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) (ctrl.Result, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	// Use exponential backoff for resource version conflicts
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 100 * time.Millisecond,
		Factor:   2.0,
		Jitter:   0.1,
	}

	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		updateErr := r.Client.Update(ctx, nodeENI)
		if updateErr != nil {
			// Check if this is a resource version conflict
			if errors.IsConflict(updateErr) {
				log.V(1).Info("Resource version conflict detected, retrying update", "error", updateErr.Error())

				// Fetch the latest version of the resource
				latest := &networkingv1alpha1.NodeENI{}
				if getErr := r.Client.Get(ctx, client.ObjectKeyFromObject(nodeENI), latest); getErr != nil {
					log.Error(getErr, "Failed to fetch latest NodeENI for retry")
					lastErr = getErr
					return false, getErr
				}

				// Preserve the changes we want to make
				preserveFinalizers := nodeENI.Finalizers
				preserveAnnotations := nodeENI.Annotations

				// Update the latest version with our changes
				latest.Finalizers = preserveFinalizers
				if preserveAnnotations != nil {
					if latest.Annotations == nil {
						latest.Annotations = make(map[string]string)
					}
					for key, value := range preserveAnnotations {
						latest.Annotations[key] = value
					}
					// Remove cleanup annotation if it was deleted
					if _, exists := preserveAnnotations[CleanupStartTimeAnnotation]; !exists {
						delete(latest.Annotations, CleanupStartTimeAnnotation)
					}
				}

				// Update nodeENI to point to the latest version for the next retry
				*nodeENI = *latest
				lastErr = updateErr
				return false, nil // Continue retrying
			}

			// For other errors, fail immediately
			lastErr = updateErr
			return false, updateErr
		}
		return true, nil
	})

	if err != nil {
		log.Error(lastErr, "Failed to update NodeENI after retries")
		return ctrl.Result{}, lastErr
	}

	log.V(1).Info("Successfully updated NodeENI")
	return ctrl.Result{}, nil
}

// updateNodeENIStatusWithRetry updates a NodeENI status with retry logic for resource version conflicts
func (r *NodeENIReconciler) updateNodeENIStatusWithRetry(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	// Use exponential backoff for resource version conflicts
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 100 * time.Millisecond,
		Factor:   2.0,
		Jitter:   0.1,
	}

	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		updateErr := r.Client.Status().Update(ctx, nodeENI)
		if updateErr != nil {
			// Check if this is a resource version conflict
			if errors.IsConflict(updateErr) {
				log.V(1).Info("Resource version conflict detected during status update, retrying", "error", updateErr.Error())

				// Fetch the latest version of the resource
				latest := &networkingv1alpha1.NodeENI{}
				if getErr := r.Client.Get(ctx, client.ObjectKeyFromObject(nodeENI), latest); getErr != nil {
					log.Error(getErr, "Failed to fetch latest NodeENI for status retry")
					lastErr = getErr
					return false, getErr
				}

				// Preserve the status changes we want to make
				preserveStatus := nodeENI.Status

				// Update the latest version with our status changes
				latest.Status = preserveStatus

				// Update nodeENI to point to the latest version for the next retry
				*nodeENI = *latest
				lastErr = updateErr
				return false, nil // Continue retrying
			}

			// For other errors, fail immediately
			lastErr = updateErr
			return false, updateErr
		}
		return true, nil
	})

	if err != nil {
		log.Error(lastErr, "Failed to update NodeENI status after retries")
		return lastErr
	}

	log.V(1).Info("Successfully updated NodeENI status")
	return nil
}

// executeWithCircuitBreaker executes an AWS operation with circuit breaker protection
func (r *NodeENIReconciler) executeWithCircuitBreaker(ctx context.Context, operation string, fn func() error) error {
	// If circuit breaker is disabled, execute directly
	if r.CircuitBreaker == nil {
		return fn()
	}

	// Check if circuit breaker allows the operation
	if !r.CircuitBreaker.IsOperationAllowed() {
		r.Log.Info("Circuit breaker is open, skipping AWS operation", "operation", operation)
		return fmt.Errorf("circuit breaker is open for operation: %s", operation)
	}

	// Execute with circuit breaker protection
	return r.CircuitBreaker.Execute(ctx, operation, fn)
}

// executeWithEnhancedRetry executes an AWS operation with enhanced error categorization and retry logic
// TODO: This function is currently unused but may be needed for future enhancements
func (r *NodeENIReconciler) executeWithEnhancedRetry(ctx context.Context, operation string, fn func() error) error {
	// Wrap the function to make it compatible with retry.RetryableFunc
	retryableFunc := func() (bool, error) {
		err := fn()
		if err != nil {
			return false, err // Not done, has error
		}
		return true, nil // Done, no error
	}

	// Use categorized retry with circuit breaker protection
	if r.CircuitBreaker != nil {
		// If circuit breaker is enabled, check if operation is allowed
		if !r.CircuitBreaker.IsOperationAllowed() {
			r.Log.Info("Circuit breaker is open, skipping AWS operation", "operation", operation)
			return fmt.Errorf("circuit breaker is open for operation: %s", operation)
		}

		// Execute with both circuit breaker and categorized retry
		return r.CircuitBreaker.Execute(ctx, operation, func() error {
			return retry.WithCategorizedRetry(ctx, r.Log, operation, retryableFunc)
		})
	}

	// Execute with categorized retry only
	return retry.WithCategorizedRetry(ctx, r.Log, operation, retryableFunc)
}

// cleanupENIAttachmentCoordinated performs coordinated cleanup of an ENI attachment
func (r *NodeENIReconciler) cleanupENIAttachmentCoordinated(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Create operation ID for coordination
	operationID := fmt.Sprintf("cleanup-%s-%s-%s", nodeENI.Name, attachment.NodeID, attachment.ENIID)

	// Define resource IDs with granular locking strategy
	// Only lock the specific ENI resource to allow parallel cleanup of different ENIs
	// on the same node/instance
	resourceIDs := []string{
		attachment.ENIID, // Only lock the specific ENI being cleaned up
	}

	// For operations that might conflict at the instance level (like instance termination),
	// we use a more specific resource ID that includes the ENI ID
	if attachment.InstanceID != "" {
		// Create instance-specific resource ID that includes ENI to avoid conflicts
		// between different ENI cleanup operations on the same instance
		instanceResourceID := fmt.Sprintf("instance-%s-eni-%s", attachment.InstanceID, attachment.ENIID)
		resourceIDs = append(resourceIDs, instanceResourceID)
	}

	log.V(1).Info("Acquiring locks for ENI cleanup",
		"operationID", operationID,
		"resourceIDs", resourceIDs,
		"eniID", attachment.ENIID,
		"instanceID", attachment.InstanceID,
		"nodeID", attachment.NodeID)

	// Create coordinated operation
	operation := &CoordinatedOperation{
		ID:          operationID,
		Type:        "eni-cleanup",
		ResourceIDs: resourceIDs,
		Priority:    1,
		DependsOn:   []string{}, // No dependencies for cleanup operations
		Timeout:     r.Config.DetachmentTimeout,
		Execute: func(ctx context.Context) error {
			// Execute the actual cleanup
			success := r.cleanupENIAttachment(ctx, nodeENI, attachment)
			if !success {
				return fmt.Errorf("cleanup failed for ENI %s", attachment.ENIID)
			}
			return nil
		},
	}

	// Execute with coordination
	err := r.Coordinator.ExecuteCoordinated(ctx, operation)
	if err != nil {
		log.Error(err, "Coordinated cleanup failed",
			"operationID", operationID,
			"resourceIDs", resourceIDs)
		return false
	}

	log.Info("Coordinated cleanup succeeded", "operationID", operationID)
	return true
}

// cleanupENIAttachmentWithNodeCoordination performs ENI cleanup with node-level coordination
// This is used when operations need to be serialized at the node level (e.g., for DPDK operations)
func (r *NodeENIReconciler) cleanupENIAttachmentWithNodeCoordination(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Create operation ID for coordination
	operationID := fmt.Sprintf("node-cleanup-%s-%s-%s", nodeENI.Name, attachment.NodeID, attachment.ENIID)

	// Define resource IDs with node-level locking for operations that require serialization
	resourceIDs := []string{
		attachment.ENIID, // ENI resource
		fmt.Sprintf("node-%s", attachment.NodeID), // Node resource for serialization
	}

	// Add instance-level coordination if needed
	if attachment.InstanceID != "" {
		resourceIDs = append(resourceIDs, attachment.InstanceID)
	}

	log.V(1).Info("Acquiring node-level locks for ENI cleanup",
		"operationID", operationID,
		"resourceIDs", resourceIDs,
		"reason", "node-level coordination required")

	// Create coordinated operation
	operation := &CoordinatedOperation{
		ID:          operationID,
		Type:        "eni-node-cleanup",
		ResourceIDs: resourceIDs,
		Priority:    2, // Higher priority for node-level operations
		DependsOn:   []string{},
		Timeout:     r.Config.DetachmentTimeout,
		Execute: func(ctx context.Context) error {
			// Execute the actual cleanup
			success := r.cleanupENIAttachment(ctx, nodeENI, attachment)
			if !success {
				return fmt.Errorf("node-coordinated cleanup failed for ENI %s", attachment.ENIID)
			}
			return nil
		},
	}

	// Execute with coordination
	err := r.Coordinator.ExecuteCoordinated(ctx, operation)
	if err != nil {
		log.Error(err, "Node-coordinated cleanup failed",
			"operationID", operationID,
			"resourceIDs", resourceIDs)
		return false
	}

	log.Info("Node-coordinated cleanup succeeded", "operationID", operationID)
	return true
}

// shouldUseNodeLevelCoordination determines if node-level coordination is needed
func (r *NodeENIReconciler) shouldUseNodeLevelCoordination(nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	// Use node-level coordination for DPDK-enabled ENIs
	if nodeENI.Spec.EnableDPDK {
		return true
	}

	// Use node-level coordination for SR-IOV enabled ENIs (indicated by PCI address or resource name)
	if nodeENI.Spec.DPDKPCIAddress != "" || nodeENI.Spec.DPDKResourceName != "" {
		return true
	}

	// For standard ENIs (Case 1), use granular coordination (no node-level locking)
	return false
}

// InterfaceState represents the current state of a network interface
type InterfaceState struct {
	PCIAddress    string
	CurrentDriver string
	IfaceName     string
	IsBoundToDPDK bool
}

// unbindInterfaceFromDPDK unbinds an interface from DPDK driver with rollback capability
// This is called during cleanup to ensure interfaces are properly unbound before detachment
func (r *NodeENIReconciler) unbindInterfaceFromDPDK(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Attempting to unbind interface from DPDK driver with rollback capability")

	// Step 1: Capture current state for rollback
	currentState, err := r.captureInterfaceState(ctx, nodeENI, attachment)
	if err != nil {
		return fmt.Errorf("failed to capture interface state: %v", err)
	}

	// If interface is not bound to DPDK, nothing to do
	if !currentState.IsBoundToDPDK {
		log.Info("Interface is not bound to DPDK driver, skipping unbind", "currentDriver", currentState.CurrentDriver)
		return nil
	}

	// Step 2: Perform atomic DPDK unbinding with rollback on failure
	if err := r.atomicDPDKUnbind(ctx, nodeENI, attachment, currentState); err != nil {
		// Step 3: Attempt rollback on failure
		if rollbackErr := r.rollbackInterfaceState(ctx, nodeENI, attachment, currentState); rollbackErr != nil {
			return fmt.Errorf("unbind failed and rollback failed: unbind=%v, rollback=%v", err, rollbackErr)
		}
		return fmt.Errorf("unbind failed but rollback succeeded: %v", err)
	}

	// Step 4: Verify unbinding succeeded
	if err := r.verifyDPDKUnbind(ctx, nodeENI, attachment); err != nil {
		return fmt.Errorf("unbind appeared to succeed but verification failed: %v", err)
	}

	log.Info("Successfully unbound interface from DPDK driver")
	return nil
}

// captureInterfaceState captures the current state of an interface for rollback purposes
func (r *NodeENIReconciler) captureInterfaceState(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) (*InterfaceState, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Create a Kubernetes client to communicate with the ENI Manager
	clientset, err := r.createKubernetesClient()
	if err != nil {
		return nil, err
	}

	// Find the ENI Manager pod on the node
	pods, err := r.findENIManagerPod(ctx, clientset, attachment.NodeID)
	if err != nil {
		return nil, err
	}

	// Get the interface name for this ENI
	ifaceName := fmt.Sprintf("eth%d", attachment.DeviceIndex)
	log.Info("Capturing interface state", "ifaceName", ifaceName, "deviceIndex", attachment.DeviceIndex)

	// Create a command to capture interface state
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf(`
# Try to find the PCI address for this interface
pci_address=""
for addr in $(ls -1 /sys/bus/pci/devices/); do
  if [ -d "/sys/bus/pci/devices/$addr/net/%s" ] || grep -q "%s" /sys/bus/pci/devices/$addr/uevent 2>/dev/null; then
    pci_address="$addr"
    break
  fi
done

# If we couldn't find it by interface name, try to find it by device index pattern
if [ -z "$pci_address" ]; then
  # For AWS instances, the PCI addresses typically follow a pattern
  potential_addr="0000:00:%02d.0"
  if [ -d "/sys/bus/pci/devices/$potential_addr" ]; then
    pci_address="$potential_addr"
  fi
fi

if [ -z "$pci_address" ]; then
  echo "ERROR: Could not find PCI address for interface %s"
  exit 1
fi

# Get current driver
driver=$(basename $(readlink -f /sys/bus/pci/devices/$pci_address/driver 2>/dev/null) 2>/dev/null)
if [ -z "$driver" ]; then
  driver="none"
fi

# Check if bound to DPDK
is_dpdk="false"
if [ "$driver" = "vfio-pci" ] || [ "$driver" = "igb_uio" ]; then
  is_dpdk="true"
fi

# Output state in parseable format
echo "PCI_ADDRESS=$pci_address"
echo "CURRENT_DRIVER=$driver"
echo "INTERFACE_NAME=%s"
echo "IS_BOUND_TO_DPDK=$is_dpdk"
`, ifaceName, ifaceName, attachment.DeviceIndex+3, ifaceName, ifaceName),
	}

	// Execute the command to capture state
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pods[0].Name).
		Namespace("eni-controller-system").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: cmd,
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(k8sConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("failed to create executor: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to execute state capture command: %v, stderr: %s", err, stderr.String())
	}

	// Parse the output to create InterfaceState
	state, err := r.parseInterfaceState(stdout.String(), ifaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to parse interface state: %v", err)
	}

	log.Info("Captured interface state", "pciAddress", state.PCIAddress, "currentDriver", state.CurrentDriver, "isBoundToDPDK", state.IsBoundToDPDK)
	return state, nil
}

// createKubernetesClient creates a Kubernetes client for communicating with ENI Manager pods
func (r *NodeENIReconciler) createKubernetesClient() (*kubernetes.Clientset, error) {
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	return clientset, nil
}

// findENIManagerPod finds the ENI Manager pod on a specific node
func (r *NodeENIReconciler) findENIManagerPod(ctx context.Context, clientset *kubernetes.Clientset, nodeID string) ([]corev1.Pod, error) {
	pods, err := clientset.CoreV1().Pods("eni-controller-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=eni-manager",
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list ENI Manager pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no ENI Manager pod found on node %s", nodeID)
	}

	return pods.Items, nil
}

// parseInterfaceState parses the output from the state capture command
func (r *NodeENIReconciler) parseInterfaceState(output, ifaceName string) (*InterfaceState, error) {
	state := &InterfaceState{
		IfaceName: ifaceName,
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "PCI_ADDRESS=") {
			state.PCIAddress = strings.TrimPrefix(line, "PCI_ADDRESS=")
		} else if strings.HasPrefix(line, "CURRENT_DRIVER=") {
			state.CurrentDriver = strings.TrimPrefix(line, "CURRENT_DRIVER=")
		} else if strings.HasPrefix(line, "IS_BOUND_TO_DPDK=") {
			isDPDKStr := strings.TrimPrefix(line, "IS_BOUND_TO_DPDK=")
			state.IsBoundToDPDK = isDPDKStr == "true"
		}
	}

	if state.PCIAddress == "" {
		return nil, fmt.Errorf("could not determine PCI address from output: %s", output)
	}

	return state, nil
}

// atomicDPDKUnbind performs atomic DPDK unbinding operations
func (r *NodeENIReconciler) atomicDPDKUnbind(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment, state *InterfaceState) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Performing atomic DPDK unbind", "pciAddress", state.PCIAddress, "currentDriver", state.CurrentDriver)

	clientset, err := r.createKubernetesClient()
	if err != nil {
		return err
	}

	pods, err := r.findENIManagerPod(ctx, clientset, attachment.NodeID)
	if err != nil {
		return err
	}

	// Create atomic unbind command with error checking
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf(`
set -e  # Exit on any error

pci_address="%s"
current_driver="%s"

echo "Starting atomic DPDK unbind for PCI address $pci_address"

# Step 1: Verify current state
actual_driver=$(basename $(readlink -f /sys/bus/pci/devices/$pci_address/driver 2>/dev/null) 2>/dev/null || echo "none")
if [ "$actual_driver" != "$current_driver" ]; then
  echo "ERROR: Driver state changed unexpectedly. Expected: $current_driver, Actual: $actual_driver"
  exit 1
fi

# Step 2: Unbind from current DPDK driver
echo "Unbinding from driver: $current_driver"
echo $pci_address > /sys/bus/pci/drivers/$current_driver/unbind || {
  echo "ERROR: Failed to unbind from $current_driver"
  exit 1
}

# Step 3: Set driver override to ena
echo "Setting driver override to ena"
echo "ena" > /sys/bus/pci/devices/$pci_address/driver_override || {
  echo "ERROR: Failed to set driver override"
  # Try to rebind to original driver
  echo $pci_address > /sys/bus/pci/drivers/$current_driver/bind 2>/dev/null || true
  exit 1
}

# Step 4: Bind to ena driver
echo "Binding to ena driver"
echo $pci_address > /sys/bus/pci/drivers/ena/bind || {
  echo "ERROR: Failed to bind to ena driver"
  # Try to clear override and rebind to original driver
  echo "" > /sys/bus/pci/devices/$pci_address/driver_override 2>/dev/null || true
  echo $pci_address > /sys/bus/pci/drivers/$current_driver/bind 2>/dev/null || true
  exit 1
}

# Step 5: Clear driver override
echo "Clearing driver override"
echo "" > /sys/bus/pci/devices/$pci_address/driver_override || {
  echo "WARNING: Failed to clear driver override, but binding succeeded"
}

echo "Successfully completed atomic DPDK unbind"
`, state.PCIAddress, state.CurrentDriver),
	}

	return r.executeCommand(ctx, clientset, pods[0].Name, cmd, "atomic DPDK unbind")
}

// rollbackInterfaceState attempts to restore the interface to its previous state
func (r *NodeENIReconciler) rollbackInterfaceState(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment, state *InterfaceState) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Attempting to rollback interface state", "pciAddress", state.PCIAddress, "targetDriver", state.CurrentDriver)

	clientset, err := r.createKubernetesClient()
	if err != nil {
		return err
	}

	pods, err := r.findENIManagerPod(ctx, clientset, attachment.NodeID)
	if err != nil {
		return err
	}

	// Create rollback command
	cmd := []string{
		"sh", "-c",
		fmt.Sprintf(`
set -e  # Exit on any error

pci_address="%s"
target_driver="%s"

echo "Starting rollback for PCI address $pci_address to driver $target_driver"

# Get current driver
current_driver=$(basename $(readlink -f /sys/bus/pci/devices/$pci_address/driver 2>/dev/null) 2>/dev/null || echo "none")

# If already bound to target driver, nothing to do
if [ "$current_driver" = "$target_driver" ]; then
  echo "Interface already bound to target driver $target_driver"
  exit 0
fi

# Unbind from current driver if bound
if [ "$current_driver" != "none" ]; then
  echo "Unbinding from current driver: $current_driver"
  echo $pci_address > /sys/bus/pci/drivers/$current_driver/unbind || {
    echo "WARNING: Failed to unbind from $current_driver"
  }
fi

# Set driver override if needed
if [ "$target_driver" != "ena" ]; then
  echo "Setting driver override to $target_driver"
  echo "$target_driver" > /sys/bus/pci/devices/$pci_address/driver_override || {
    echo "ERROR: Failed to set driver override to $target_driver"
    exit 1
  }
fi

# Bind to target driver
echo "Binding to target driver: $target_driver"
echo $pci_address > /sys/bus/pci/drivers/$target_driver/bind || {
  echo "ERROR: Failed to bind to $target_driver"
  exit 1
}

# Clear driver override if we set it
if [ "$target_driver" != "ena" ]; then
  echo "Clearing driver override"
  echo "" > /sys/bus/pci/devices/$pci_address/driver_override || {
    echo "WARNING: Failed to clear driver override"
  }
fi

echo "Successfully completed rollback to $target_driver"
`, state.PCIAddress, state.CurrentDriver),
	}

	return r.executeCommand(ctx, clientset, pods[0].Name, cmd, "rollback interface state")
}

// verifyDPDKUnbind verifies that the DPDK unbinding was successful
func (r *NodeENIReconciler) verifyDPDKUnbind(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Capture current state to verify unbinding
	currentState, err := r.captureInterfaceState(ctx, nodeENI, attachment)
	if err != nil {
		return fmt.Errorf("failed to capture state for verification: %v", err)
	}

	// Verify that interface is no longer bound to DPDK
	if currentState.IsBoundToDPDK {
		return fmt.Errorf("verification failed: interface is still bound to DPDK driver %s", currentState.CurrentDriver)
	}

	// Verify that interface is bound to ena driver
	if currentState.CurrentDriver != "ena" {
		return fmt.Errorf("verification failed: interface is bound to %s instead of ena", currentState.CurrentDriver)
	}

	log.Info("DPDK unbind verification successful", "currentDriver", currentState.CurrentDriver)
	return nil
}

// executeCommand executes a command in an ENI Manager pod
func (r *NodeENIReconciler) executeCommand(ctx context.Context, clientset *kubernetes.Clientset, podName string, cmd []string, operation string) error {
	// TODO: Use ctx for timeout and cancellation in future enhancements
	_ = ctx
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace("eni-controller-system").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: cmd,
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(k8sConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("failed to create executor for %s: %v", operation, err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	r.Log.Info("Command execution result", "operation", operation, "stdout", stdout.String(), "stderr", stderr.String())

	if err != nil {
		return fmt.Errorf("failed to execute %s: %v, stderr: %s", operation, err, stderr.String())
	}

	return nil
}

// handleDPDKUnbinding attempts to unbind an interface from DPDK if needed
func (r *NodeENIReconciler) handleDPDKUnbinding(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Check if this ENI is bound to DPDK
	if !nodeENI.Spec.EnableDPDK || !attachment.DPDKBound {
		return
	}

	// Try to unbind the interface from DPDK
	// This is a best-effort operation - we'll continue with detachment even if it fails
	if err := r.unbindInterfaceFromDPDK(ctx, nodeENI, attachment); err != nil {
		log.Error(err, "Failed to unbind interface from DPDK driver, continuing with detachment")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "DPDKUnbindFailed",
			"Failed to unbind ENI %s from DPDK driver: %v", attachment.ENIID, err)
	} else {
		log.Info("Successfully unbound interface from DPDK driver")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "DPDKUnbound",
			"Successfully unbound ENI %s from DPDK driver", attachment.ENIID)
	}
}

// checkENIExists checks if an ENI still exists in AWS
// Returns true if the ENI exists, false if it doesn't, and an error for other issues
func (r *NodeENIReconciler) checkENIExists(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) (bool, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Check if the ENI still exists
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		// Check if the error indicates the ENI doesn't exist
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			log.V(1).Info("ENI no longer exists (not found in AWS), considering cleanup successful")
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAlreadyDeleted",
				"ENI %s was already deleted (possibly manually)", attachment.ENIID)
			return false, nil
		}

		// For other errors, log but continue with cleanup attempt
		log.Error(err, "Failed to describe ENI, will still attempt cleanup")
		return true, err
	}

	// If ENI doesn't exist, cleanup is already done
	if eni == nil {
		log.V(1).Info("ENI no longer exists, considering cleanup successful")
		return false, nil
	}

	return true, nil
}

// detachENIWithRetry attempts to detach an ENI with retry logic
// Returns true if detachment was successful or not needed, false otherwise
func (r *NodeENIReconciler) detachENIWithRetry(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// If there's no attachment ID, nothing to detach
	if attachment.AttachmentID == "" {
		return true
	}

	// Use exponential backoff for detachment to handle rate limiting
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}

	detachErr := wait.ExponentialBackoff(backoff, func() (bool, error) {
		if err := r.AWS.DetachENI(ctx, attachment.AttachmentID, true); err != nil {
			// Check if the error indicates the attachment doesn't exist
			if strings.Contains(err.Error(), "InvalidAttachmentID.NotFound") {
				log.V(1).Info("ENI attachment no longer exists, considering detachment successful")
				r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAttachmentAlreadyRemoved",
					"ENI attachment for %s was already removed (possibly manually)", attachment.ENIID)
				return true, nil
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") ||
				strings.Contains(err.Error(), "rate limit") {
				log.Info("AWS API rate limit exceeded when detaching ENI, will retry", "attachmentID", attachment.AttachmentID)
				return false, nil
			}

			// For other errors, fail immediately
			return false, err
		}
		return true, nil
	})

	if detachErr != nil {
		log.Error(detachErr, "Failed to detach ENI after retries", "attachmentID", attachment.AttachmentID)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDetachmentFailed",
			"Failed to detach ENI %s from node %s after retries: %v", attachment.ENIID, attachment.NodeID, detachErr)
		return false
	}

	return true
}

// waitForENIDetachmentWithRetry waits for an ENI to be fully detached with retry logic
// Returns true if waiting was successful or not needed, false otherwise
func (r *NodeENIReconciler) waitForENIDetachmentWithRetry(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// If there's no ENI ID, nothing to wait for
	if attachment.ENIID == "" {
		return true
	}

	// Try to wait for detachment with exponential backoff
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}

	waitErr := wait.ExponentialBackoff(backoff, func() (bool, error) {
		err := r.AWS.WaitForENIDetachment(ctx, attachment.ENIID, r.Config.DetachmentTimeout)
		if err != nil {
			// Check if the error indicates the ENI doesn't exist
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				log.V(1).Info("ENI no longer exists when waiting for detachment, considering detachment successful")
				r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAlreadyDeleted",
					"ENI %s was already deleted (possibly manually) when waiting for detachment", attachment.ENIID)
				return true, nil
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") ||
				strings.Contains(err.Error(), "rate limit") {
				log.Info("Rate limit exceeded when waiting for ENI detachment, will retry", "eniID", attachment.ENIID)
				return false, nil
			}

			// For other errors, fail immediately
			return false, err
		}
		return true, nil
	})

	if waitErr != nil {
		log.Error(waitErr, "Failed to wait for ENI detachment after retries", "eniID", attachment.ENIID)
		return false
	}

	return true
}

// cleanupENIAttachment detaches and deletes an ENI attachment
// Returns true if cleanup was successful, false otherwise
func (r *NodeENIReconciler) cleanupENIAttachment(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.V(1).Info("Detaching and deleting ENI") // Reduced log level for routine operations

	// Track if any operation failed
	success := true

	// Step 1: Handle DPDK unbinding if needed
	r.handleDPDKUnbinding(ctx, nodeENI, attachment)

	// Step 2: Check if the ENI still exists
	eniExists, _ := r.checkENIExists(ctx, nodeENI, attachment)
	if !eniExists {
		return true // ENI doesn't exist, nothing more to do
	}

	// Step 3: Detach the ENI if it's attached
	if !r.detachENIWithRetry(ctx, nodeENI, attachment) {
		success = false
		// Continue with deletion attempt regardless
	}

	// Step 4: Wait for the detachment to complete
	if !r.waitForENIDetachmentWithRetry(ctx, nodeENI, attachment) {
		success = false
	}

	// Step 5: Delete the ENI
	if !r.deleteENIIfExists(ctx, nodeENI, attachment) {
		success = false
	}

	return success
}

// deleteENIIfExists checks if an ENI exists and deletes it if it does
// Returns true if deletion was successful or ENI doesn't exist, false otherwise
func (r *NodeENIReconciler) deleteENIIfExists(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "eniID", attachment.ENIID)

	// Try to describe the ENI to check its status
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		// Check if the error indicates the ENI doesn't exist
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			log.V(1).Info("ENI no longer exists (not found in AWS)")
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAlreadyDeleted",
				"ENI %s was already deleted (possibly manually)", attachment.ENIID)
			return true
		}

		log.Error(err, "Failed to describe ENI")
		return false
	}

	if eni == nil {
		log.V(1).Info("ENI no longer exists")
		return true
	}

	// Check if the ENI is still attached
	if eni.Attachment != nil && eni.Status != awsutil.EC2v2NetworkInterfaceStatusAvailable {
		log.V(1).Info("ENI is still attached, waiting longer", "status", eni.Status)
		time.Sleep(r.Config.DetachmentTimeout)

		// Check again after waiting
		eni, err = r.AWS.DescribeENI(ctx, attachment.ENIID)
		if err != nil {
			// Check if the error indicates the ENI doesn't exist
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				log.V(1).Info("ENI no longer exists after waiting (not found in AWS)")
				r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAlreadyDeleted",
					"ENI %s was already deleted (possibly manually) after waiting", attachment.ENIID)
				return true
			}

			log.Error(err, "Failed to describe ENI after waiting")
			return false
		}

		if eni == nil {
			log.V(1).Info("ENI no longer exists after waiting")
			return true
		}
	}

	// Delete the ENI
	if err := r.AWS.DeleteENI(ctx, attachment.ENIID); err != nil {
		// Check if the error indicates the ENI doesn't exist
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			log.V(1).Info("ENI was already deleted when attempting to delete it")
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAlreadyDeleted",
				"ENI %s was already deleted (possibly manually) when attempting to delete it", attachment.ENIID)
			return true
		}

		log.Error(err, "Failed to delete ENI")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDeletionFailed",
			"Failed to delete ENI %s: %v", attachment.ENIID, err)
		return false
	}

	log.Info("Successfully deleted ENI")
	r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDeleted",
		"Successfully deleted ENI %s", attachment.ENIID)
	return true
}

// addFinalizer adds a finalizer to a NodeENI resource
func (r *NodeENIReconciler) addFinalizer(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) (ctrl.Result, error) {
	controllerutil.AddFinalizer(nodeENI, NodeENIFinalizer)

	// Use retry logic for resource version conflicts
	result, err := r.updateNodeENIWithRetry(ctx, nodeENI)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Return here to avoid processing the rest of the reconciliation
	// The update will trigger another reconciliation
	return result, nil
}

// processNodeENI processes a NodeENI resource
func (r *NodeENIReconciler) processNodeENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	// List all nodes that match the selector
	nodeList := &corev1.NodeList{}
	selector := labels.SelectorFromSet(nodeENI.Spec.NodeSelector)
	if err := r.Client.List(ctx, nodeList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		log.Error(err, "Failed to list nodes")
		return err
	}

	// Initialize status if it's nil
	if nodeENI.Status.Attachments == nil {
		nodeENI.Status.Attachments = []networkingv1alpha1.ENIAttachment{}
	}

	// Track current attachments to detect stale ones
	currentAttachments := make(map[string]bool)

	// Process each matching node
	for _, node := range nodeList.Items {
		if err := r.processNode(ctx, nodeENI, node, currentAttachments); err != nil {
			log.Error(err, "Error processing node", "node", node.Name)
			// Continue with other nodes
		}
	}

	// Remove stale attachments
	updatedAttachments := r.removeStaleAttachments(ctx, nodeENI, currentAttachments)
	nodeENI.Status.Attachments = updatedAttachments

	// Update the NodeENI status with retry logic
	if err := r.updateNodeENIStatusWithRetry(ctx, nodeENI); err != nil {
		log.Error(err, "Failed to update NodeENI status")
		return err
	}

	return nil
}

// processNode processes a single node for a NodeENI resource
func (r *NodeENIReconciler) processNode(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, currentAttachments map[string]bool) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name)

	// Skip nodes that don't have the provider ID (not ready yet)
	if node.Spec.ProviderID == "" {
		log.Info("Node doesn't have provider ID yet, skipping")
		return nil
	}

	// Extract EC2 instance ID from provider ID
	instanceID := util.GetInstanceIDFromProviderID(node.Spec.ProviderID)
	if instanceID == "" {
		log.Error(nil, "Failed to extract instance ID from provider ID", "providerID", node.Spec.ProviderID)
		return nil
	}

	// Mark this node as having been processed
	nodeKey := node.Name
	currentAttachments[nodeKey] = true

	// Verify existing ENI attachments for this node
	r.verifyENIAttachments(ctx, nodeENI, node.Name)

	// Get all subnet IDs we need to create ENIs in
	subnetIDs, err := r.getAllSubnetIDs(ctx, nodeENI)
	if err != nil {
		log.Error(err, "Failed to determine subnet IDs")
		return err
	}

	// Build maps for tracking existing attachments and device indices
	existingSubnets, usedDeviceIndices, subnetToDeviceIndex := r.buildAttachmentMaps(nodeENI, node.Name)

	// Create ENIs for any subnets that don't already have one
	r.createMissingENIs(ctx, nodeENI, node, instanceID, subnetIDs, existingSubnets, usedDeviceIndices, subnetToDeviceIndex)

	return nil
}

// buildAttachmentMaps builds maps for tracking existing attachments and device indices
func (r *NodeENIReconciler) buildAttachmentMaps(nodeENI *networkingv1alpha1.NodeENI, nodeName string) (
	existingSubnets map[string]bool,
	usedDeviceIndices map[int]bool,
	subnetToDeviceIndex map[string]int,
) {
	existingSubnets = make(map[string]bool)
	usedDeviceIndices = make(map[int]bool)
	subnetToDeviceIndex = make(map[string]int)

	// First pass: build the subnet to device index mapping from existing attachments
	for _, attachment := range nodeENI.Status.Attachments {
		// We want to build a global mapping across all nodes
		if attachment.SubnetID != "" && attachment.DeviceIndex > 0 {
			// If we haven't seen this subnet before, or if this device index is lower
			// than what we've seen before (prefer lower indices), update the mapping
			if existingIndex, exists := subnetToDeviceIndex[attachment.SubnetID]; !exists || attachment.DeviceIndex < existingIndex {
				subnetToDeviceIndex[attachment.SubnetID] = attachment.DeviceIndex
			}
		}

		// For this specific node, track which subnets already have ENIs
		if attachment.NodeID == nodeName && attachment.SubnetID != "" {
			existingSubnets[attachment.SubnetID] = true
		}

		// For this specific node, track which device indices are already in use
		if attachment.NodeID == nodeName && attachment.DeviceIndex > 0 {
			usedDeviceIndices[attachment.DeviceIndex] = true
		}
	}

	return existingSubnets, usedDeviceIndices, subnetToDeviceIndex
}

// createMissingENIs creates ENIs for any subnets that don't already have one
func (r *NodeENIReconciler) createMissingENIs(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	node corev1.Node,
	instanceID string,
	subnetIDs []string,
	existingSubnets map[string]bool,
	usedDeviceIndices map[int]bool,
	subnetToDeviceIndex map[string]int,
) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name)

	for i, subnetID := range subnetIDs {
		// Skip if we already have an ENI in this subnet
		if existingSubnets[subnetID] {
			log.Info("Node already has an ENI in this subnet", "subnetID", subnetID)
			continue
		}

		// Determine the device index to use for this subnet
		deviceIndex := r.determineDeviceIndex(nodeENI, subnetID, i, subnetToDeviceIndex, usedDeviceIndices, log)

		// Create and attach a new ENI for this subnet
		if err := r.createAndAttachENIForSubnet(ctx, nodeENI, node, instanceID, subnetID, deviceIndex); err != nil {
			log.Error(err, "Failed to create and attach ENI for subnet", "subnetID", subnetID)
			// Continue with other subnets even if one fails
			continue
		}
	}
}

// determineDeviceIndex determines the device index to use for a subnet
func (r *NodeENIReconciler) determineDeviceIndex(
	nodeENI *networkingv1alpha1.NodeENI,
	subnetID string,
	subnetIndex int,
	subnetToDeviceIndex map[string]int,
	usedDeviceIndices map[int]bool,
	log logr.Logger,
) int {
	// First, check if we already have a mapping for this subnet
	if existingIndex, exists := subnetToDeviceIndex[subnetID]; exists {
		// Use the existing mapping for consistency across nodes
		log.Info("Using existing device index mapping for subnet", "subnetID", subnetID, "deviceIndex", existingIndex)
		return r.findAvailableDeviceIndex(existingIndex, usedDeviceIndices, subnetID, log)
	}

	// No existing mapping, calculate a new one
	baseDeviceIndex := nodeENI.Spec.DeviceIndex
	if baseDeviceIndex <= 0 {
		baseDeviceIndex = r.Config.DefaultDeviceIndex
	}

	// Start with the base device index plus the subnet index
	// This ensures a deterministic mapping between subnet and device index
	deviceIndex := baseDeviceIndex + subnetIndex
	log.Info("Calculated new device index for subnet", "subnetID", subnetID, "deviceIndex", deviceIndex)

	// Store this mapping for future reference
	subnetToDeviceIndex[subnetID] = deviceIndex

	return r.findAvailableDeviceIndex(deviceIndex, usedDeviceIndices, subnetID, log)
}

// findAvailableDeviceIndex finds an available device index starting from the given index
func (r *NodeENIReconciler) findAvailableDeviceIndex(
	startIndex int,
	usedDeviceIndices map[int]bool,
	subnetID string,
	log logr.Logger,
) int {
	deviceIndex := startIndex
	originalIndex := startIndex

	// If this device index is already in use, find the next available one
	for usedDeviceIndices[deviceIndex] {
		deviceIndex++
		log.Info("Device index already in use on this node, incrementing",
			"subnetID", subnetID,
			"originalIndex", originalIndex,
			"newIndex", deviceIndex)
	}

	return deviceIndex
}

// createAndAttachENI creates and attaches a new ENI to a node
// This is kept for backward compatibility
func (r *NodeENIReconciler) createAndAttachENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, instanceID string) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	log.Info("Creating and attaching ENI for node", "node", node.Name, "instanceID", instanceID)

	// Get the subnet ID using the old method (for backward compatibility)
	subnetID, err := r.determineSubnetID(ctx, nodeENI, log)
	if err != nil {
		return err
	}

	// Use the new method with the determined subnet ID
	return r.createAndAttachENIForSubnet(ctx, nodeENI, node, instanceID, subnetID, nodeENI.Spec.DeviceIndex)
}

// createAndAttachENIForSubnet creates and attaches a new ENI to a node for a specific subnet
func (r *NodeENIReconciler) createAndAttachENIForSubnet(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, instanceID string, subnetID string, deviceIndex int) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name, "subnetID", subnetID)
	log.Info("Creating and attaching ENI for subnet", "instanceID", instanceID, "deviceIndex", deviceIndex)

	// Create the ENI in the specified subnet
	eniID, err := r.createENIInSubnet(ctx, nodeENI, node, subnetID)
	if err != nil {
		log.Error(err, "Failed to create ENI in subnet")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENICreationFailed",
			"Failed to create ENI for node %s in subnet %s: %v", node.Name, subnetID, err)
		return err
	}

	// Attach the ENI
	attachmentID, err := r.attachENI(ctx, eniID, instanceID, deviceIndex)
	if err != nil {
		log.Error(err, "Failed to attach ENI", "eniID", eniID)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIAttachmentFailed",
			"Failed to attach ENI %s to node %s: %v", eniID, node.Name, err)

		// Clean up the created ENI to avoid resource leaks
		log.Info("Cleaning up unattached ENI", "eniID", eniID)
		if deleteErr := r.AWS.DeleteENI(ctx, eniID); deleteErr != nil {
			log.Error(deleteErr, "Failed to delete unattached ENI", "eniID", eniID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDeletionFailed",
				"Failed to delete unattached ENI %s: %v", eniID, deleteErr)
		} else {
			log.Info("Successfully deleted unattached ENI", "eniID", eniID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDeleted",
				"Successfully deleted unattached ENI %s", eniID)
		}

		return err
	}

	// Get the subnet CIDR
	subnetCIDR, err := r.AWS.GetSubnetCIDRByID(ctx, subnetID)
	if err != nil {
		log.Error(err, "Failed to get subnet CIDR", "subnetID", subnetID)
		// Continue without the CIDR, it's not critical
		subnetCIDR = ""
	}

	// Add the attachment to the status
	nodeENI.Status.Attachments = append(nodeENI.Status.Attachments, networkingv1alpha1.ENIAttachment{
		NodeID:       node.Name,
		InstanceID:   instanceID,
		ENIID:        eniID,
		AttachmentID: attachmentID,
		SubnetID:     subnetID,
		SubnetCIDR:   subnetCIDR,
		MTU:          nodeENI.Spec.MTU,
		DeviceIndex:  deviceIndex,
		Status:       "attached",
		LastUpdated:  metav1.Now(),
	})

	r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAttached",
		"Successfully attached ENI %s to node %s in subnet %s", eniID, node.Name, subnetID)

	return nil
}

// removeStaleAttachments removes stale attachments from a NodeENI resource with comprehensive verification
func (r *NodeENIReconciler) removeStaleAttachments(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, currentAttachments map[string]bool) []networkingv1alpha1.ENIAttachment {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	var updatedAttachments []networkingv1alpha1.ENIAttachment
	var staleAttachments []networkingv1alpha1.ENIAttachment

	// Separate current and stale attachments with comprehensive verification
	for _, attachment := range nodeENI.Status.Attachments {
		if currentAttachments[attachment.NodeID] {
			// Node exists in Kubernetes, but verify AWS instance state
			if r.isAttachmentStaleInAWS(ctx, nodeENI, attachment) {
				log.Info("Attachment is stale in AWS despite node existing in Kubernetes",
					"nodeID", attachment.NodeID, "eniID", attachment.ENIID, "instanceID", attachment.InstanceID)
				staleAttachments = append(staleAttachments, attachment)
			} else {
				updatedAttachments = append(updatedAttachments, attachment)
			}
		} else {
			// Node doesn't exist in Kubernetes, perform comprehensive stale detection
			if r.isAttachmentComprehensivelyStale(ctx, nodeENI, attachment) {
				staleAttachments = append(staleAttachments, attachment)
			} else {
				// Keep attachment if comprehensive verification suggests it's still valid
				log.Info("Keeping attachment despite node not matching selector - comprehensive verification failed to confirm it's stale",
					"nodeID", attachment.NodeID, "eniID", attachment.ENIID)
				updatedAttachments = append(updatedAttachments, attachment)
			}
		}
	}

	// If there are no stale attachments, return early
	if len(staleAttachments) == 0 {
		return updatedAttachments
	}

	// If there's only one stale attachment, handle it directly
	if len(staleAttachments) == 1 {
		if r.handleStaleAttachment(ctx, nodeENI, staleAttachments[0]) {
			// Keep the attachment if cleanup failed
			updatedAttachments = append(updatedAttachments, staleAttachments[0])
		} else {
			log.Info("Successfully removed stale attachment", "node", staleAttachments[0].NodeID, "eniID", staleAttachments[0].ENIID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
				"Successfully detached and deleted ENI %s from node %s", staleAttachments[0].ENIID, staleAttachments[0].NodeID)
		}
		return updatedAttachments
	}

	// Handle multiple stale attachments in parallel
	log.Info("Cleaning up stale attachments in parallel", "count", len(staleAttachments))

	// Use the specific cleanup function for the stale attachments
	cleanupSucceeded := r.cleanupSpecificENIAttachmentsInParallel(ctx, nodeENI, staleAttachments)

	// If all cleanups succeeded, we're done
	if cleanupSucceeded {
		log.Info("Successfully removed all stale attachments in parallel")
		// No need to add any stale attachments to updatedAttachments
		return updatedAttachments
	}

	// If some cleanups failed, we need to check each one individually
	log.Info("Some stale attachment cleanups failed, checking each one")
	for _, attachment := range staleAttachments {
		// Check if this attachment still exists
		eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
		if err != nil {
			// Check if the error indicates the ENI doesn't exist
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				// ENI was successfully deleted or doesn't exist
				log.Info("Successfully removed stale attachment (ENI not found)", "node", attachment.NodeID, "eniID", attachment.ENIID)
				r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
					"Successfully detached and deleted ENI %s from node %s (or it was already deleted)", attachment.ENIID, attachment.NodeID)
			} else {
				// For other errors, keep the attachment
				log.Info("Keeping stale attachment due to error checking ENI", "node", attachment.NodeID, "eniID", attachment.ENIID, "error", err.Error())
				updatedAttachments = append(updatedAttachments, attachment)
			}
		} else if eni != nil {
			// ENI still exists, keep the attachment
			log.Info("Keeping stale attachment because ENI still exists", "node", attachment.NodeID, "eniID", attachment.ENIID)
			updatedAttachments = append(updatedAttachments, attachment)
		} else {
			// ENI was successfully deleted
			log.Info("Successfully removed stale attachment", "node", attachment.NodeID, "eniID", attachment.ENIID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
				"Successfully detached and deleted ENI %s from node %s", attachment.ENIID, attachment.NodeID)
		}
	}

	return updatedAttachments
}

// isAttachmentStaleInAWS checks if an attachment is stale in AWS despite the node existing in Kubernetes
func (r *NodeENIReconciler) isAttachmentStaleInAWS(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "nodeID", attachment.NodeID, "eniID", attachment.ENIID)

	// Step 1: Verify AWS instance exists and is running
	if !r.verifyInstanceExists(ctx, attachment.InstanceID) {
		log.Info("AWS instance does not exist or is terminated", "instanceID", attachment.InstanceID)
		return true // Attachment is stale
	}

	// Step 2: Verify ENI attachment state in AWS
	if !r.verifyENIAttachmentState(ctx, attachment) {
		log.Info("ENI attachment state is invalid in AWS", "eniID", attachment.ENIID, "instanceID", attachment.InstanceID)
		return true // Attachment is stale
	}

	// Step 3: Cross-verify ENI is attached to the correct instance
	if !r.verifyENIInstanceMapping(ctx, attachment) {
		log.Info("ENI is not attached to the expected instance", "eniID", attachment.ENIID, "expectedInstance", attachment.InstanceID)
		return true // Attachment is stale
	}

	return false // Attachment is valid
}

// isAttachmentComprehensivelyStale performs comprehensive stale detection for attachments where the node is missing
func (r *NodeENIReconciler) isAttachmentComprehensivelyStale(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "nodeID", attachment.NodeID, "eniID", attachment.ENIID)

	// Step 1: Check if AWS instance exists (most definitive check)
	if !r.verifyInstanceExists(ctx, attachment.InstanceID) {
		log.Info("AWS instance does not exist, attachment is definitely stale", "instanceID", attachment.InstanceID)
		return true // Definitely stale
	}

	// Step 2: Check ENI existence and attachment state
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			log.Info("ENI does not exist in AWS, attachment is stale", "eniID", attachment.ENIID)
			return true // Definitely stale
		}
		// For other errors, be conservative and keep the attachment
		log.Error(err, "Failed to describe ENI, keeping attachment to be safe")
		return false
	}

	if eni == nil {
		log.Info("ENI does not exist, attachment is stale")
		return true // Definitely stale
	}

	// Step 3: Check if ENI is attached to the expected instance
	if eni.Attachment == nil {
		log.Info("ENI is not attached to any instance, attachment is stale")
		return true // Stale
	}

	if eni.Attachment.InstanceID != attachment.InstanceID {
		log.Info("ENI is attached to a different instance",
			"expectedInstance", attachment.InstanceID,
			"actualInstance", eni.Attachment.InstanceID)
		return true // Stale
	}

	// Step 4: Instance exists and ENI is properly attached, but node no longer matches NodeENI selector
	// This means the user intentionally removed the node from the selector (e.g., removed labels)
	// We should clean up the ENI as the user expects it to be removed
	log.Info("Instance and ENI exist and are properly attached, but node no longer matches NodeENI selector - marking as stale for cleanup")
	return true
}

// verifyInstanceExists verifies that an AWS instance exists and is in a valid state
func (r *NodeENIReconciler) verifyInstanceExists(ctx context.Context, instanceID string) bool {
	log := r.Log.WithValues("instanceID", instanceID)

	// Use AWS API to check instance state
	instance, err := r.AWS.DescribeInstance(ctx, instanceID)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidInstanceID.NotFound") {
			log.Info("AWS instance not found")
			return false
		}
		// For other errors, be conservative and assume instance exists
		log.Error(err, "Failed to describe instance, assuming it exists")
		return true
	}

	if instance == nil {
		log.Info("AWS instance does not exist")
		return false
	}

	// Check instance state - consider running, pending, stopping as valid
	// terminated, shutting-down as invalid
	validStates := []string{"running", "pending", "stopping", "stopped"}
	for _, validState := range validStates {
		if instance.State == validState {
			log.Info("AWS instance exists and is in valid state", "state", instance.State)
			return true
		}
	}

	log.Info("AWS instance exists but is in invalid state", "state", instance.State)
	return false
}

// verifyENIAttachmentState verifies the ENI attachment state in AWS
func (r *NodeENIReconciler) verifyENIAttachmentState(ctx context.Context, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("eniID", attachment.ENIID, "instanceID", attachment.InstanceID)

	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			log.Info("ENI not found in AWS")
			return false
		}
		// For other errors, be conservative
		log.Error(err, "Failed to describe ENI, assuming attachment is valid")
		return true
	}

	if eni == nil {
		log.Info("ENI does not exist")
		return false
	}

	// Check if ENI is attached
	if eni.Attachment == nil {
		log.Info("ENI is not attached to any instance")
		return false
	}

	// Check attachment status
	if eni.Status != awsutil.EC2v2NetworkInterfaceStatusInUse {
		log.Info("ENI is not in 'in-use' status", "status", eni.Status)
		return false
	}

	log.Info("ENI attachment state is valid", "status", eni.Status, "attachedTo", eni.Attachment.InstanceID)
	return true
}

// verifyENIInstanceMapping verifies that the ENI is attached to the expected instance
func (r *NodeENIReconciler) verifyENIInstanceMapping(ctx context.Context, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("eniID", attachment.ENIID, "expectedInstance", attachment.InstanceID)

	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		// For errors, be conservative
		log.Error(err, "Failed to describe ENI for instance mapping verification")
		return true
	}

	if eni == nil || eni.Attachment == nil {
		log.Info("ENI or attachment does not exist")
		return false
	}

	if eni.Attachment.InstanceID != attachment.InstanceID {
		log.Info("ENI is attached to different instance",
			"expectedInstance", attachment.InstanceID,
			"actualInstance", eni.Attachment.InstanceID)
		return false
	}

	log.Info("ENI instance mapping is correct")
	return true
}

// handleStaleAttachment handles a stale attachment
// Returns true if the attachment should be kept (cleanup failed), false otherwise
func (r *NodeENIReconciler) handleStaleAttachment(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Detaching and deleting stale ENI")

	// First check if the ENI still exists
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		// Check if the error indicates the ENI doesn't exist
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			log.Info("Stale ENI no longer exists (not found in AWS), considering cleanup successful", "error", err.Error())
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAlreadyDeleted",
				"Stale ENI %s was already deleted (possibly manually)", attachment.ENIID)
			return false // Don't keep the attachment
		}

		// For other errors, log but continue with cleanup attempt
		log.Error(err, "Failed to describe stale ENI, will still attempt cleanup")
	} else if eni == nil {
		log.Info("Stale ENI no longer exists, considering cleanup successful")
		return false // Don't keep the attachment
	}

	// Use the same cleanup logic as for finalizers
	return !r.cleanupENIAttachment(ctx, nodeENI, attachment)
}

// Helper functions for AWS operations

// createENI creates a new ENI in AWS (kept for backward compatibility)
func (r *NodeENIReconciler) createENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node) (string, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name)

	// Determine the subnet ID to use with the old method
	subnetID, err := r.determineSubnetID(ctx, nodeENI, log)
	if err != nil {
		return "", err
	}

	// Use the new method with the determined subnet ID
	return r.createENIInSubnet(ctx, nodeENI, node, subnetID)
}

// createENIInSubnet creates a new ENI in AWS in a specific subnet
func (r *NodeENIReconciler) createENIInSubnet(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, subnetID string) (string, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name, "subnetID", subnetID)

	description := nodeENI.Spec.Description
	if description == "" {
		description = fmt.Sprintf("ENI created by nodeeni-controller for node %s", node.Name)
	}

	// Determine the security group IDs to use
	var securityGroupIDs []string

	// First, use any explicitly provided security group IDs
	if len(nodeENI.Spec.SecurityGroupIDs) > 0 {
		securityGroupIDs = append(securityGroupIDs, nodeENI.Spec.SecurityGroupIDs...)
	}

	// Then, look up any security group names and add those IDs
	if len(nodeENI.Spec.SecurityGroupNames) > 0 {
		for _, sgName := range nodeENI.Spec.SecurityGroupNames {
			sgID, err := r.AWS.GetSecurityGroupIDByName(ctx, sgName)
			if err != nil {
				return "", fmt.Errorf("failed to get security group ID from name %s: %v", sgName, err)
			}
			log.Info("Resolved security group name to ID", "securityGroupName", sgName, "securityGroupID", sgID)

			// Check if this ID is already in the list (to avoid duplicates)
			if !util.ContainsString(securityGroupIDs, sgID) {
				securityGroupIDs = append(securityGroupIDs, sgID)
			}
		}
	}

	if len(securityGroupIDs) == 0 {
		return "", fmt.Errorf("neither securityGroupIDs nor securityGroupNames provided, or all lookups failed")
	}

	// Create tags for the ENI
	tags := map[string]string{
		"Name":                             fmt.Sprintf("nodeeni-%s-%s-%s", nodeENI.Name, node.Name, subnetID[len(subnetID)-8:]),
		"NodeENI":                          nodeENI.Name,
		"Node":                             node.Name,
		"SubnetID":                         subnetID,
		"kubernetes.io/cluster/managed-by": "nodeeni-controller",
		"node.k8s.amazonaws.com/no_manage": "true",
	}

	// Create the ENI
	eniID, err := r.AWS.CreateENI(ctx, subnetID, securityGroupIDs, description, tags)
	if err != nil {
		return "", fmt.Errorf("failed to create ENI: %v", err)
	}

	return eniID, nil
}

// determineSubnetID determines which subnet ID to use for creating an ENI
// This is kept for backward compatibility with the old single-subnet approach
func (r *NodeENIReconciler) determineSubnetID(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, log logr.Logger) (string, error) {
	// Priority order:
	// 1. Single SubnetID if specified
	// 2. Multiple SubnetIDs if specified (use first)
	// 3. Single SubnetName if specified
	// 4. Multiple SubnetNames if specified (use first)

	// Check for single SubnetID (backward compatibility)
	if nodeENI.Spec.SubnetID != "" {
		return nodeENI.Spec.SubnetID, nil
	}

	// Check for multiple SubnetIDs
	if len(nodeENI.Spec.SubnetIDs) > 0 {
		// Simply use the first subnet in the list for backward compatibility
		subnetID := nodeENI.Spec.SubnetIDs[0]
		log.Info("Selected subnet ID from list", "subnetID", subnetID, "totalSubnets", len(nodeENI.Spec.SubnetIDs))
		return subnetID, nil
	}

	// Check for single SubnetName (backward compatibility)
	if nodeENI.Spec.SubnetName != "" {
		subnetID, err := r.AWS.GetSubnetIDByName(ctx, nodeENI.Spec.SubnetName)
		if err != nil {
			return "", fmt.Errorf("failed to get subnet ID from name %s: %v", nodeENI.Spec.SubnetName, err)
		}
		log.Info("Resolved subnet name to ID", "subnetName", nodeENI.Spec.SubnetName, "subnetID", subnetID)
		return subnetID, nil
	}

	// Check for multiple SubnetNames
	if len(nodeENI.Spec.SubnetNames) > 0 {
		// Simply use the first subnet name in the list for backward compatibility
		subnetName := nodeENI.Spec.SubnetNames[0]
		subnetID, err := r.AWS.GetSubnetIDByName(ctx, subnetName)
		if err != nil {
			return "", fmt.Errorf("failed to get subnet ID from name %s: %v", subnetName, err)
		}
		log.Info("Resolved subnet name to ID from list", "subnetName", subnetName, "subnetID", subnetID, "totalSubnets", len(nodeENI.Spec.SubnetNames))
		return subnetID, nil
	}

	// No subnet information provided
	return "", fmt.Errorf("no subnet information provided (subnetID, subnetIDs, subnetName, or subnetNames)")
}

// getAllSubnetIDs returns all subnet IDs that should be used for creating ENIs
func (r *NodeENIReconciler) getAllSubnetIDs(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) ([]string, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	var subnetIDs []string

	// Check for single SubnetID (backward compatibility)
	if nodeENI.Spec.SubnetID != "" {
		subnetIDs = append(subnetIDs, nodeENI.Spec.SubnetID)
	}

	// Add all SubnetIDs from the list
	if len(nodeENI.Spec.SubnetIDs) > 0 {
		subnetIDs = append(subnetIDs, nodeENI.Spec.SubnetIDs...)
	}

	// Check for single SubnetName (backward compatibility)
	if nodeENI.Spec.SubnetName != "" {
		subnetID, err := r.AWS.GetSubnetIDByName(ctx, nodeENI.Spec.SubnetName)
		if err != nil {
			return nil, fmt.Errorf("failed to get subnet ID from name %s: %v", nodeENI.Spec.SubnetName, err)
		}
		log.Info("Resolved subnet name to ID", "subnetName", nodeENI.Spec.SubnetName, "subnetID", subnetID)

		// Only add if not already in the list
		if !util.ContainsString(subnetIDs, subnetID) {
			subnetIDs = append(subnetIDs, subnetID)
		}
	}

	// Add all SubnetNames from the list
	if len(nodeENI.Spec.SubnetNames) > 0 {
		for _, subnetName := range nodeENI.Spec.SubnetNames {
			subnetID, err := r.AWS.GetSubnetIDByName(ctx, subnetName)
			if err != nil {
				return nil, fmt.Errorf("failed to get subnet ID from name %s: %v", subnetName, err)
			}
			log.Info("Resolved subnet name to ID", "subnetName", subnetName, "subnetID", subnetID)

			// Only add if not already in the list
			if !util.ContainsString(subnetIDs, subnetID) {
				subnetIDs = append(subnetIDs, subnetID)
			}
		}
	}

	// Check if we have any subnet IDs
	if len(subnetIDs) == 0 {
		return nil, fmt.Errorf("no subnet information provided (subnetID, subnetIDs, subnetName, or subnetNames)")
	}

	log.Info("Determined subnet IDs for ENI creation", "count", len(subnetIDs), "subnetIDs", subnetIDs)
	return subnetIDs, nil
}

// attachENI attaches an ENI to an EC2 instance
func (r *NodeENIReconciler) attachENI(ctx context.Context, eniID, instanceID string, deviceIndex int) (string, error) {
	// Use default device index if not specified
	if deviceIndex <= 0 {
		deviceIndex = r.Config.DefaultDeviceIndex
	}

	// Attach the ENI with delete on termination set to the configured default
	attachmentID, err := r.AWS.AttachENI(ctx, eniID, instanceID, deviceIndex, r.Config.DefaultDeleteOnTermination)
	if err != nil {
		return "", fmt.Errorf("failed to attach ENI: %v", err)
	}

	return attachmentID, nil
}

// verifyENIAttachments verifies the actual state of ENIs in AWS and updates the NodeENI resource status accordingly
func (r *NodeENIReconciler) verifyENIAttachments(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, nodeName string) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", nodeName)
	log.Info("Verifying ENI attachments for node", "attachmentCount", len(nodeENI.Status.Attachments))

	// Create a new list of attachments
	var updatedAttachments []networkingv1alpha1.ENIAttachment

	// Check each attachment for this node
	for _, attachment := range nodeENI.Status.Attachments {
		log.Info("Checking attachment", "eniID", attachment.ENIID, "nodeID", attachment.NodeID, "attachmentID", attachment.AttachmentID)

		if attachment.NodeID != nodeName {
			// Keep attachments for other nodes as is
			log.Info("Keeping attachment for different node", "eniID", attachment.ENIID, "nodeID", attachment.NodeID)
			updatedAttachments = append(updatedAttachments, attachment)
			continue
		}

		// Verify this attachment and add to updatedAttachments if it's still valid
		if r.verifyAndUpdateAttachment(ctx, nodeENI, attachment, &updatedAttachments) {
			log.Info("Attachment verified and updated", "eniID", attachment.ENIID)
		} else {
			log.Info("Attachment removed from status", "eniID", attachment.ENIID)
		}
	}

	// Update the NodeENI status with the verified attachments
	r.updateNodeENIStatus(ctx, nodeENI, updatedAttachments)
}

// verifyAndUpdateAttachment verifies a single ENI attachment and updates it if needed
// Returns true if the attachment is still valid and was added to updatedAttachments
func (r *NodeENIReconciler) verifyAndUpdateAttachment(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	attachment networkingv1alpha1.ENIAttachment,
	updatedAttachments *[]networkingv1alpha1.ENIAttachment,
) bool {
	// Check if the attachment still exists
	if !r.isAttachmentValid(ctx, nodeENI, attachment) {
		return false
	}

	// Check if the ENI still exists and is properly attached
	if !r.isENIProperlyAttached(ctx, nodeENI, attachment) {
		return false
	}

	// ENI is still attached, update it and keep it in the list
	r.updateAttachmentInfo(ctx, &attachment, nodeENI)

	// Add the updated attachment to the list
	*updatedAttachments = append(*updatedAttachments, attachment)
	return true
}

// isAttachmentValid checks if the attachment ID is still valid
// Returns true if the attachment is valid, false otherwise
func (r *NodeENIReconciler) isAttachmentValid(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	attachment networkingv1alpha1.ENIAttachment,
) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// If there's no attachment ID, we can't check it directly
	if attachment.AttachmentID == "" {
		return true
	}

	// Try to describe the attachment directly
	// If this fails with InvalidAttachmentID.NotFound, the attachment no longer exists
	err := r.AWS.DetachENI(ctx, attachment.AttachmentID, false)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidAttachmentID.NotFound") {
			log.Info("ENI attachment no longer exists in AWS, removing from status", "attachmentID", attachment.AttachmentID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
				"ENI %s was manually detached from node %s (attachment ID not found)", attachment.ENIID, attachment.NodeID)
			return false
		}
		// If we get a different error, the attachment might still exist
		return true
	}

	// If DetachENI succeeds, it means the attachment existed and we just detached it
	// This shouldn't happen in normal operation, but we'll handle it gracefully
	log.Info("ENI attachment existed but was just detached, removing from status", "attachmentID", attachment.AttachmentID)
	r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
		"ENI %s was detached from node %s during verification", attachment.ENIID, attachment.NodeID)
	return false
}

// isENIProperlyAttached checks if the ENI exists and is properly attached to the correct instance
// Returns true if the ENI is properly attached, false otherwise
func (r *NodeENIReconciler) isENIProperlyAttached(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	attachment networkingv1alpha1.ENIAttachment,
) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Check if the ENI still exists and is attached
	log.Info("Describing ENI in AWS", "eniID", attachment.ENIID)
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)

	// Handle errors from DescribeENI
	if err != nil {
		return r.handleENIDescribeError(ctx, nodeENI, attachment, err)
	}

	// If ENI is nil, it doesn't exist
	if eni == nil {
		log.Info("ENI no longer exists, removing from status", "eniID", attachment.ENIID)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
			"ENI %s was manually detached and deleted from node %s", attachment.ENIID, attachment.NodeID)
		// No need to delete the ENI as it already doesn't exist
		return false
	}

	// Check if the ENI is still attached to the instance
	log.Info("Checking ENI attachment status", "eniID", attachment.ENIID,
		"hasAttachment", eni.Attachment != nil,
		"status", eni.Status)

	if eni.Attachment == nil || eni.Status == awsutil.EC2v2NetworkInterfaceStatusAvailable {
		log.Info("ENI is no longer attached to the instance, removing from status", "eniID", attachment.ENIID)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
			"ENI %s was manually detached from node %s", attachment.ENIID, attachment.NodeID)

		// Delete the manually detached ENI to avoid resource leakage
		log.Info("Deleting manually detached ENI", "eniID", attachment.ENIID)
		if err := r.AWS.DeleteENI(ctx, attachment.ENIID); err != nil {
			// Check if the error indicates the ENI doesn't exist
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				log.Info("Manually detached ENI was already deleted", "eniID", attachment.ENIID)
			} else {
				log.Error(err, "Failed to delete manually detached ENI", "eniID", attachment.ENIID)
				// We still return false to remove it from status, even if deletion failed
			}
		} else {
			log.Info("Successfully deleted manually detached ENI", "eniID", attachment.ENIID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDeleted",
				"Successfully deleted manually detached ENI %s", attachment.ENIID)
		}

		return false
	}

	// Double-check that the ENI is attached to the correct instance
	if eni.Attachment != nil && eni.Attachment.InstanceID != attachment.InstanceID {
		log.Info("ENI is attached to a different instance, removing from status",
			"eniID", attachment.ENIID,
			"expectedInstance", attachment.InstanceID,
			"actualInstance", eni.Attachment.InstanceID)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
			"ENI %s was detached from node %s and attached to a different instance", attachment.ENIID, attachment.NodeID)

		// We don't delete the ENI in this case since it's being used by another instance
		log.Info("Not deleting ENI as it's attached to another instance",
			"eniID", attachment.ENIID,
			"instance", eni.Attachment.InstanceID)

		return false
	}

	// ENI is properly attached
	log.Info("ENI is still attached, keeping in status", "eniID", attachment.ENIID)
	return true
}

// handleENIDescribeError handles errors from DescribeENI
// Returns true if the attachment should be kept, false otherwise
func (r *NodeENIReconciler) handleENIDescribeError(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	attachment networkingv1alpha1.ENIAttachment,
	err error,
) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)

	// Check if the error indicates the ENI doesn't exist
	if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
		log.Info("ENI no longer exists in AWS, removing from status", "eniID", attachment.ENIID, "error", err.Error())
		r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
			"ENI %s was manually detached and deleted from node %s", attachment.ENIID, attachment.NodeID)
		return false
	}

	// For other errors, we need to be careful
	// If we previously determined the attachment exists, keep it
	// Otherwise, assume it's detached to be safe
	if r.isAttachmentValid(ctx, nodeENI, attachment) {
		log.Error(err, "Failed to describe ENI but attachment exists, keeping attachment", "eniID", attachment.ENIID)
		return true
	}

	log.Error(err, "Failed to describe ENI and attachment status unknown, removing from status", "eniID", attachment.ENIID)
	r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
		"ENI %s may have been detached from node %s (status unknown)", attachment.ENIID, attachment.NodeID)
	return false
}

// updateAttachmentInfo updates the attachment information (subnet CIDR, MTU, timestamp)
func (r *NodeENIReconciler) updateAttachmentInfo(
	ctx context.Context,
	attachment *networkingv1alpha1.ENIAttachment,
	nodeENI *networkingv1alpha1.NodeENI,
) {
	log := r.Log.WithValues("eniID", attachment.ENIID, "node", attachment.NodeID)

	// Check if we need to update the subnet CIDR
	if attachment.SubnetCIDR == "" && attachment.SubnetID != "" {
		// Try to get the subnet CIDR
		subnetCIDR, err := r.AWS.GetSubnetCIDRByID(ctx, attachment.SubnetID)
		if err != nil {
			log.Error(err, "Failed to get subnet CIDR for existing attachment", "subnetID", attachment.SubnetID)
			// Continue without the CIDR, it's not critical
		} else {
			// Update the attachment with the CIDR
			attachment.SubnetCIDR = subnetCIDR
			log.Info("Updated subnet CIDR for existing attachment", "subnetID", attachment.SubnetID, "subnetCIDR", subnetCIDR)
		}
	}

	// Check if we need to update the MTU
	if attachment.MTU <= 0 && nodeENI.Spec.MTU > 0 {
		// Update the attachment with the MTU from the NodeENI spec
		attachment.MTU = nodeENI.Spec.MTU
		log.Info("Updated MTU for existing attachment", "eniID", attachment.ENIID, "mtu", attachment.MTU)
	}

	// Check if we need to update the device index
	if attachment.DeviceIndex <= 0 {
		// Try to get the device index from the ENI description
		eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
		if err != nil {
			log.Error(err, "Failed to get device index for existing attachment", "eniID", attachment.ENIID)
			// Continue without the device index, it's not critical
		} else if eni != nil && eni.Attachment != nil {
			// Update the attachment with the device index from the ENI
			attachment.DeviceIndex = int(eni.Attachment.DeviceIndex)
			log.Info("Updated device index for existing attachment", "eniID", attachment.ENIID, "deviceIndex", attachment.DeviceIndex)
		}
	}

	// Update the last updated timestamp
	attachment.LastUpdated = metav1.Now()
}

// updateNodeENIStatus updates the NodeENI status with the verified attachments
func (r *NodeENIReconciler) updateNodeENIStatus(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	updatedAttachments []networkingv1alpha1.ENIAttachment,
) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	// Update the NodeENI status with the verified attachments
	// Always update the status to ensure the LastUpdated timestamps are current
	log.Info("Updating NodeENI status with verified attachments",
		"before", len(nodeENI.Status.Attachments), "after", len(updatedAttachments))
	nodeENI.Status.Attachments = updatedAttachments

	// Update the NodeENI status with retry logic
	if err := r.updateNodeENIStatusWithRetry(ctx, nodeENI); err != nil {
		log.Error(err, "Failed to update NodeENI status with verified attachments")
	} else {
		log.Info("Successfully updated NodeENI status with verified attachments")
	}
}

// startIMDSConfigurationRetry starts a background process to periodically retry IMDS configuration
// This helps handle node replacement scenarios where new instances need IMDS hop limit configuration
func (r *NodeENIReconciler) startIMDSConfigurationRetry(ctx context.Context) {
	if ec2Client, ok := r.AWS.(*awsutil.EC2Client); ok {
		go func() {
			ticker := time.NewTicker(5 * time.Minute) // Retry every 5 minutes
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					r.Log.Info("IMDS configuration retry stopped due to context cancellation")
					return
				case <-ticker.C:
					r.Log.V(1).Info("Retrying IMDS hop limit configuration")
					if err := ec2Client.ConfigureIMDSHopLimit(ctx); err != nil {
						r.Log.V(1).Info("IMDS configuration retry failed", "error", err.Error())
					} else {
						r.Log.V(1).Info("IMDS configuration retry completed successfully")
					}
				}
			}
		}()
	}
}

// ShouldUseNodeLevelCoordinationTest exposes shouldUseNodeLevelCoordination for testing purposes
func (r *NodeENIReconciler) ShouldUseNodeLevelCoordinationTest(nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	return r.shouldUseNodeLevelCoordination(nodeENI, attachment)
}
