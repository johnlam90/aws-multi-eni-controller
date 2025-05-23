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
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
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
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	AWS      awsutil.EC2Interface
	Config   *config.ControllerConfig
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

	return &NodeENIReconciler{
		Client:   mgr.GetClient(),
		Log:      log,
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("nodeeni-controller"),
		AWS:      awsClient,
		Config:   cfg,
	}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *NodeENIReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
		controllerutil.RemoveFinalizer(nodeENI, NodeENIFinalizer)
		if err := r.Client.Update(ctx, nodeENI); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Stop reconciliation as the item is being deleted
	return ctrl.Result{}, nil
}

// unbindInterfaceFromDPDK unbinds an interface from DPDK driver
// This is called during cleanup to ensure interfaces are properly unbound before detachment
func (r *NodeENIReconciler) unbindInterfaceFromDPDK(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Attempting to unbind interface from DPDK driver")

	// Create a Kubernetes client to communicate with the ENI Manager
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	// Find the ENI Manager pod on the node
	pods, err := clientset.CoreV1().Pods("eni-controller-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=eni-manager",
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", attachment.NodeID),
	})
	if err != nil {
		return fmt.Errorf("failed to list ENI Manager pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no ENI Manager pod found on node %s", attachment.NodeID)
	}

	// Get the interface name for this ENI
	// We'll try to infer it from the device index
	ifaceName := fmt.Sprintf("eth%d", attachment.DeviceIndex)
	log.Info("Using interface name based on device index", "ifaceName", ifaceName, "deviceIndex", attachment.DeviceIndex)

	// Create a command to unbind the interface
	// We'll use the dpdk-devbind.py script in the ENI Manager pod
	cmd := []string{
		"bash", "-c",
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
  # The primary interface is usually at 0000:00:03.0, and secondary interfaces
  # are at 0000:00:04.0, 0000:00:05.0, etc.
  potential_addr="0000:00:%02d.0"
  if [ -d "/sys/bus/pci/devices/$potential_addr" ]; then
    pci_address="$potential_addr"
  fi
fi

if [ -z "$pci_address" ]; then
  echo "Could not find PCI address for interface %s"
  exit 1
fi

echo "Found PCI address $pci_address for interface %s"

# Check if the device is bound to a DPDK driver
driver=$(basename $(readlink -f /sys/bus/pci/devices/$pci_address/driver 2>/dev/null) 2>/dev/null)
if [ "$driver" != "vfio-pci" ] && [ "$driver" != "igb_uio" ]; then
  echo "Interface %s is not bound to a DPDK driver (current driver: $driver), skipping unbind"
  exit 0
fi

echo "Unbinding interface %s (PCI: $pci_address) from DPDK driver $driver"

# First unbind from the current driver
echo $pci_address > /sys/bus/pci/drivers/$driver/unbind

# Now bind to the original driver (ena for AWS instances)
echo "ena" > /sys/bus/pci/devices/$pci_address/driver_override
echo $pci_address > /sys/bus/pci/drivers/ena/bind
echo "" > /sys/bus/pci/devices/$pci_address/driver_override

echo "Successfully unbound interface %s (PCI: $pci_address) from DPDK driver and bound to ena driver"
`, ifaceName, ifaceName, attachment.DeviceIndex+3, ifaceName, ifaceName, ifaceName, ifaceName, ifaceName),
	}

	// Execute the command in the ENI Manager pod
	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pods.Items[0].Name).
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
		return fmt.Errorf("failed to create executor: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.Stream(remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	log.Info("Unbind command output", "stdout", stdout.String(), "stderr", stderr.String())

	if err != nil {
		return fmt.Errorf("failed to execute unbind command: %v, stderr: %s", err, stderr.String())
	}

	log.Info("Successfully executed unbind command")
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
	if err := r.Client.Update(ctx, nodeENI); err != nil {
		return ctrl.Result{}, err
	}
	// Return here to avoid processing the rest of the reconciliation
	// The update will trigger another reconciliation
	return ctrl.Result{}, nil
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

	// Update the NodeENI status
	if err := r.Client.Status().Update(ctx, nodeENI); err != nil {
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

// removeStaleAttachments removes stale attachments from a NodeENI resource
func (r *NodeENIReconciler) removeStaleAttachments(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, currentAttachments map[string]bool) []networkingv1alpha1.ENIAttachment {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	var updatedAttachments []networkingv1alpha1.ENIAttachment
	var staleAttachments []networkingv1alpha1.ENIAttachment

	// Separate current and stale attachments
	for _, attachment := range nodeENI.Status.Attachments {
		if currentAttachments[attachment.NodeID] {
			updatedAttachments = append(updatedAttachments, attachment)
		} else {
			staleAttachments = append(staleAttachments, attachment)
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

	// Update the NodeENI status
	if err := r.Client.Status().Update(ctx, nodeENI); err != nil {
		log.Error(err, "Failed to update NodeENI status with verified attachments")
	} else {
		log.Info("Successfully updated NodeENI status with verified attachments")
	}
}
