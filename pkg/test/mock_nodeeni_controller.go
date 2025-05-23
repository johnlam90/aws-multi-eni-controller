package test

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// MockNodeENIController is a mock implementation of the NodeENI controller for testing
type MockNodeENIController struct {
	Client   client.Client
	AWS      *awsutil.MockEC2Client
	Log      logr.Logger
	Recorder *MockEventRecorder
	Config   *config.ControllerConfig
}

// NewMockNodeENIController creates a new mock NodeENI controller
func NewMockNodeENIController(client client.Client, aws *awsutil.MockEC2Client, log logr.Logger, recorder *MockEventRecorder) *MockNodeENIController {
	return &MockNodeENIController{
		Client:   client,
		AWS:      aws,
		Log:      log,
		Recorder: recorder,
		Config: &config.ControllerConfig{
			AWSRegion:                  "us-east-1",
			ReconcilePeriod:            5 * time.Minute,
			DetachmentTimeout:          15 * time.Second,
			DefaultDeviceIndex:         1,
			MaxConcurrentReconciles:    5,
			DefaultDeleteOnTermination: true,
		},
	}
}

// Reconcile reconciles a NodeENI resource
func (r *MockNodeENIController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := r.Log.WithValues("nodeeni", req.Name)
	log.Info("Reconciling NodeENI")

	// Fetch the NodeENI instance
	nodeENI := &networkingv1alpha1.NodeENI{}
	err := r.Client.Get(ctx, req.NamespacedName, nodeENI)
	if err != nil {
		log.Error(err, "Failed to get NodeENI")
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Process the NodeENI
	if err := r.processNodeENI(ctx, nodeENI); err != nil {
		log.Error(err, "Failed to process NodeENI")
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: r.Config.ReconcilePeriod}, nil
}

// processNodeENI processes a NodeENI resource
func (r *MockNodeENIController) processNodeENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) error {
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
	updatedAttachments := r.removeStaleAttachments(nodeENI, currentAttachments)
	nodeENI.Status.Attachments = updatedAttachments

	// Update the NodeENI status
	if err := r.Client.Status().Update(ctx, nodeENI); err != nil {
		log.Error(err, "Failed to update NodeENI status")
		return err
	}

	return nil
}

// processNode processes a single node for a NodeENI resource
func (r *MockNodeENIController) processNode(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, currentAttachments map[string]bool) error {
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

	// Get all subnet IDs we need to create ENIs in
	subnetIDs, err := r.getAllSubnetIDs(ctx, nodeENI)
	if err != nil {
		log.Error(err, "Failed to determine subnet IDs")
		return err
	}

	// Build maps for tracking existing attachments and device indices
	existingSubnets, usedDeviceIndices, subnetToDeviceIndex := r.buildAttachmentMaps(nodeENI, node.Name)

	// Create ENIs for any subnets that don't already have one
	for i, subnetID := range subnetIDs {
		// Skip if we already have an ENI in this subnet
		if existingSubnets[subnetID] {
			log.Info("Node already has an ENI in this subnet", "subnetID", subnetID)
			continue
		}

		// Determine the device index to use for this subnet
		deviceIndex := r.determineDeviceIndex(nodeENI, subnetID, i, subnetToDeviceIndex, usedDeviceIndices)

		// Create and attach a new ENI for this subnet
		if err := r.createAndAttachENIForSubnet(ctx, nodeENI, node, instanceID, subnetID, deviceIndex); err != nil {
			log.Error(err, "Failed to create and attach ENI for subnet", "subnetID", subnetID)
			// Continue with other subnets even if one fails
			continue
		}
	}

	return nil
}

// buildAttachmentMaps builds maps for tracking existing attachments and device indices
func (r *MockNodeENIController) buildAttachmentMaps(nodeENI *networkingv1alpha1.NodeENI, nodeName string) (
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
		// and which device indices are already in use
		if attachment.NodeID == nodeName {
			if attachment.SubnetID != "" {
				existingSubnets[attachment.SubnetID] = true
			}
			if attachment.DeviceIndex > 0 {
				usedDeviceIndices[attachment.DeviceIndex] = true
			}
		}
	}

	return existingSubnets, usedDeviceIndices, subnetToDeviceIndex
}

// determineDeviceIndex determines the device index to use for a subnet
func (r *MockNodeENIController) determineDeviceIndex(
	nodeENI *networkingv1alpha1.NodeENI,
	subnetID string,
	subnetIndex int,
	subnetToDeviceIndex map[string]int,
	usedDeviceIndices map[int]bool,
) int {
	// First, check if we already have a mapping for this subnet
	if existingIndex, exists := subnetToDeviceIndex[subnetID]; exists {
		// Use the existing mapping for consistency across nodes
		return r.findAvailableDeviceIndex(existingIndex, usedDeviceIndices)
	}

	// No existing mapping, calculate a new one
	baseDeviceIndex := nodeENI.Spec.DeviceIndex
	if baseDeviceIndex <= 0 {
		baseDeviceIndex = r.Config.DefaultDeviceIndex
	}

	// Start with the base device index plus the subnet index
	// This ensures a deterministic mapping between subnet and device index
	deviceIndex := baseDeviceIndex + subnetIndex

	// Store this mapping for future reference
	subnetToDeviceIndex[subnetID] = deviceIndex

	return r.findAvailableDeviceIndex(deviceIndex, usedDeviceIndices)
}

// findAvailableDeviceIndex finds an available device index starting from the given index
func (r *MockNodeENIController) findAvailableDeviceIndex(
	startIndex int,
	usedDeviceIndices map[int]bool,
) int {
	deviceIndex := startIndex

	// If this device index is already in use, find the next available one
	for usedDeviceIndices[deviceIndex] {
		deviceIndex++
	}

	return deviceIndex
}

// createAndAttachENIForSubnet creates and attaches a new ENI to a node for a specific subnet
func (r *MockNodeENIController) createAndAttachENIForSubnet(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node, instanceID string, subnetID string, deviceIndex int) error {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "node", node.Name, "subnetID", subnetID)
	log.Info("Creating and attaching ENI for subnet", "instanceID", instanceID, "deviceIndex", deviceIndex)

	// Create the ENI in the specified subnet
	description := nodeENI.Spec.Description
	if description == "" {
		description = fmt.Sprintf("ENI created by nodeeni-controller for node %s", node.Name)
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
	eniID, err := r.AWS.CreateENI(ctx, subnetID, nodeENI.Spec.SecurityGroupIDs, description, tags)
	if err != nil {
		r.Recorder.Eventf(nodeENI, "Warning", "ENICreationFailed",
			"Failed to create ENI for node %s in subnet %s: %v", node.Name, subnetID, err)
		return fmt.Errorf("failed to create ENI: %v", err)
	}

	// Attach the ENI
	attachmentID, err := r.AWS.AttachENI(ctx, eniID, instanceID, deviceIndex, nodeENI.Spec.DeleteOnTermination)
	if err != nil {
		r.Recorder.Eventf(nodeENI, "Warning", "ENIAttachmentFailed",
			"Failed to attach ENI %s to node %s: %v", eniID, node.Name, err)
		return fmt.Errorf("failed to attach ENI: %v", err)
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

	r.Recorder.Eventf(nodeENI, "Normal", "ENIAttached",
		"Attached ENI %s to node %s", eniID, node.Name)

	return nil
}

// getAllSubnetIDs returns all subnet IDs that should be used for creating ENIs
func (r *MockNodeENIController) getAllSubnetIDs(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) ([]string, error) {
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

	return subnetIDs, nil
}

// removeStaleAttachments removes attachments for nodes that no longer match the selector
func (r *MockNodeENIController) removeStaleAttachments(nodeENI *networkingv1alpha1.NodeENI, currentAttachments map[string]bool) []networkingv1alpha1.ENIAttachment {
	var updatedAttachments []networkingv1alpha1.ENIAttachment

	for _, attachment := range nodeENI.Status.Attachments {
		// Keep the attachment if the node is still valid
		if currentAttachments[attachment.NodeID] {
			updatedAttachments = append(updatedAttachments, attachment)
		}
	}

	return updatedAttachments
}
