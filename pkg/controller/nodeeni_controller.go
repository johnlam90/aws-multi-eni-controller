// Package controller implements the Kubernetes controller for managing
// AWS Elastic Network Interfaces (ENIs) for nodes.
//
// The NodeENI controller watches NodeENI custom resources and automatically
// creates, attaches, and manages ENIs for nodes that match the specified selectors.
// It supports multiple subnets and security groups, and handles the lifecycle
// of ENIs including creation, attachment, detachment, and deletion.
package controller

import (
	"context"
	"fmt"
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

		// Clean up all ENI attachments
		for _, attachment := range nodeENI.Status.Attachments {
			r.cleanupENIAttachment(ctx, nodeENI, attachment)
		}

		// Remove our finalizer from the list and update it
		controllerutil.RemoveFinalizer(nodeENI, NodeENIFinalizer)
		if err := r.Client.Update(ctx, nodeENI); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Stop reconciliation as the item is being deleted
	return ctrl.Result{}, nil
}

// cleanupENIAttachment detaches and deletes an ENI attachment
func (r *NodeENIReconciler) cleanupENIAttachment(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Detaching and deleting ENI")

	// Detach the ENI if it's attached
	if attachment.AttachmentID != "" {
		if err := r.AWS.DetachENI(ctx, attachment.AttachmentID, true); err != nil {
			log.Error(err, "Failed to detach ENI", "attachmentID", attachment.AttachmentID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDetachmentFailed",
				"Failed to detach ENI %s from node %s: %v", attachment.ENIID, attachment.NodeID, err)
			// Continue with deletion attempt
		}
	}

	// Wait for the detachment to complete
	if attachment.ENIID != "" {
		// Try to wait for detachment, but continue even if it fails
		_ = r.AWS.WaitForENIDetachment(ctx, attachment.ENIID, r.Config.DetachmentTimeout)

		// Delete the ENI
		r.deleteENIIfExists(ctx, nodeENI, attachment)
	}
}

// deleteENIIfExists checks if an ENI exists and deletes it if it does
func (r *NodeENIReconciler) deleteENIIfExists(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "eniID", attachment.ENIID)

	// Try to describe the ENI to check its status
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		log.Error(err, "Failed to describe ENI")
		return
	}

	if eni == nil {
		log.Info("ENI no longer exists")
		return
	}

	// Check if the ENI is still attached
	if eni.Attachment != nil && eni.Status != awsutil.EC2v2NetworkInterfaceStatusAvailable {
		log.Info("ENI is still attached, waiting longer", "status", eni.Status)
		time.Sleep(r.Config.DetachmentTimeout)
	}

	// Delete the ENI
	if err := r.AWS.DeleteENI(ctx, attachment.ENIID); err != nil {
		log.Error(err, "Failed to delete ENI")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDeletionFailed",
			"Failed to delete ENI %s: %v", attachment.ENIID, err)
	} else {
		log.Info("Successfully deleted ENI")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDeleted",
			"Successfully deleted ENI %s", attachment.ENIID)
	}
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

	currentAttachments[node.Name] = true

	// Check if we already have an attachment for this node
	for _, attachment := range nodeENI.Status.Attachments {
		if attachment.NodeID == node.Name {
			// TODO: Verify the attachment is still valid
			return nil
		}
	}

	// Create and attach a new ENI
	return r.createAndAttachENI(ctx, nodeENI, node, instanceID)
}

// createAndAttachENI creates and attaches a new ENI to a node
func (r *NodeENIReconciler) createAndAttachENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, instanceID string) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	log.Info("Creating and attaching ENI for node", "node", node.Name, "instanceID", instanceID)

	// Create the ENI
	eniID, err := r.createENI(ctx, nodeENI, node)
	if err != nil {
		log.Error(err, "Failed to create ENI", "node", node.Name)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENICreationFailed",
			"Failed to create ENI for node %s: %v", node.Name, err)
		return err
	}

	// Attach the ENI
	attachmentID, err := r.attachENI(ctx, eniID, instanceID, nodeENI.Spec.DeviceIndex)
	if err != nil {
		log.Error(err, "Failed to attach ENI", "node", node.Name, "eniID", eniID)
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIAttachmentFailed",
			"Failed to attach ENI %s to node %s: %v", eniID, node.Name, err)
		return err
	}

	// Add the attachment to the status
	nodeENI.Status.Attachments = append(nodeENI.Status.Attachments, networkingv1alpha1.ENIAttachment{
		NodeID:       node.Name,
		InstanceID:   instanceID,
		ENIID:        eniID,
		AttachmentID: attachmentID,
		Status:       "attached",
		LastUpdated:  metav1.Now(),
	})

	r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAttached",
		"Successfully attached ENI %s to node %s", eniID, node.Name)

	return nil
}

// removeStaleAttachments removes stale attachments from a NodeENI resource
func (r *NodeENIReconciler) removeStaleAttachments(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, currentAttachments map[string]bool) []networkingv1alpha1.ENIAttachment {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	var updatedAttachments []networkingv1alpha1.ENIAttachment

	for _, attachment := range nodeENI.Status.Attachments {
		if currentAttachments[attachment.NodeID] {
			updatedAttachments = append(updatedAttachments, attachment)
		} else {
			// Handle stale attachment
			if r.handleStaleAttachment(ctx, nodeENI, attachment) {
				// Keep the attachment if cleanup failed
				updatedAttachments = append(updatedAttachments, attachment)
			} else {
				log.Info("Successfully removed stale attachment", "node", attachment.NodeID, "eniID", attachment.ENIID)
				r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
					"Successfully detached and deleted ENI %s from node %s", attachment.ENIID, attachment.NodeID)
			}
		}
	}

	return updatedAttachments
}

// handleStaleAttachment handles a stale attachment
// Returns true if the attachment should be kept (cleanup failed), false otherwise
func (r *NodeENIReconciler) handleStaleAttachment(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", attachment.NodeID, "eniID", attachment.ENIID)
	log.Info("Detaching and deleting stale ENI")

	// Detach the ENI if it's attached
	if attachment.AttachmentID != "" {
		if err := r.AWS.DetachENI(ctx, attachment.AttachmentID, true); err != nil {
			log.Error(err, "Failed to detach ENI", "attachmentID", attachment.AttachmentID)
			r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDetachmentFailed",
				"Failed to detach ENI %s from node %s: %v", attachment.ENIID, attachment.NodeID, err)
			return true // Keep the attachment
		}
	}

	// Wait for the detachment to complete
	log.Info("Waiting for ENI detachment to complete")
	time.Sleep(r.Config.DetachmentTimeout)

	if attachment.ENIID == "" {
		return false // No ENI ID, nothing to delete
	}

	// Delete the ENI
	return r.deleteStaleENI(ctx, nodeENI, attachment)
}

// deleteStaleENI deletes a stale ENI
// Returns true if the attachment should be kept (cleanup failed), false otherwise
func (r *NodeENIReconciler) deleteStaleENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "eniID", attachment.ENIID)

	// Try to describe the ENI to check its status
	eni, err := r.AWS.DescribeENI(ctx, attachment.ENIID)
	if err != nil {
		log.Error(err, "Failed to describe ENI")
		return true // Keep the attachment
	}

	if eni == nil {
		log.Info("ENI no longer exists")
		return false // ENI doesn't exist, don't keep the attachment
	}

	// Check if the ENI is still attached
	if eni.Attachment != nil && eni.Status != awsutil.EC2v2NetworkInterfaceStatusAvailable {
		log.Info("ENI is still attached, waiting longer", "status", eni.Status)
		time.Sleep(r.Config.DetachmentTimeout)
	}

	// Delete the ENI
	if err := r.AWS.DeleteENI(ctx, attachment.ENIID); err != nil {
		log.Error(err, "Failed to delete ENI")
		r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDeletionFailed",
			"Failed to delete ENI %s: %v", attachment.ENIID, err)
		return true // Keep the attachment
	}

	return false // Successfully deleted, don't keep the attachment
}

// Helper functions for AWS operations

// createENI creates a new ENI in AWS
func (r *NodeENIReconciler) createENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node) (string, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name)

	description := nodeENI.Spec.Description
	if description == "" {
		description = fmt.Sprintf("ENI created by nodeeni-controller for node %s", node.Name)
	}

	// Determine the subnet ID to use
	subnetID := nodeENI.Spec.SubnetID
	if subnetID == "" && nodeENI.Spec.SubnetName != "" {
		// Look up subnet ID by name
		var err error
		subnetID, err = r.AWS.GetSubnetIDByName(ctx, nodeENI.Spec.SubnetName)
		if err != nil {
			return "", fmt.Errorf("failed to get subnet ID from name %s: %v", nodeENI.Spec.SubnetName, err)
		}
		log.Info("Resolved subnet name to ID", "subnetName", nodeENI.Spec.SubnetName, "subnetID", subnetID)
	}

	if subnetID == "" {
		return "", fmt.Errorf("neither subnetID nor subnetName provided")
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
		"Name":                             fmt.Sprintf("nodeeni-%s-%s", nodeENI.Name, node.Name),
		"NodeENI":                          nodeENI.Name,
		"Node":                             node.Name,
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
