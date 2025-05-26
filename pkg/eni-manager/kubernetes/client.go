// Package kubernetes provides Kubernetes API interactions
// for the AWS Multi-ENI Controller.
package kubernetes

import (
	"context"
	"fmt"
	"log"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// Client handles Kubernetes API operations
type Client struct {
	client client.Client
}

// NewClient creates a new Kubernetes client
func NewClient() (*Client, error) {
	// Get the Kubernetes configuration
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes config: %v", err)
	}

	// Create a new scheme and add our custom types
	scheme := runtime.NewScheme()
	if err := networkingv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add networking scheme: %v", err)
	}

	// Create the client
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	return &Client{
		client: k8sClient,
	}, nil
}

// GetNodeENIResources retrieves all NodeENI resources for a specific node
func (c *Client) GetNodeENIResources(ctx context.Context, nodeName string) ([]networkingv1alpha1.NodeENI, error) {
	log.Printf("Fetching NodeENI resources for node %s", nodeName)

	var nodeENIList networkingv1alpha1.NodeENIList
	if err := c.client.List(ctx, &nodeENIList); err != nil {
		return nil, fmt.Errorf("failed to list NodeENI resources: %v", err)
	}

	// Filter by node name using NodeSelector
	var nodeENIs []networkingv1alpha1.NodeENI
	for _, nodeENI := range nodeENIList.Items {
		// Check if this NodeENI applies to the specified node
		// For simplicity, we'll check if the node name matches any value in NodeSelector
		// In a real implementation, you'd want more sophisticated node selection logic
		if c.nodeMatchesSelector(nodeName, nodeENI.Spec.NodeSelector) {
			nodeENIs = append(nodeENIs, nodeENI)
		}
	}

	log.Printf("Found %d NodeENI resources for node %s", len(nodeENIs), nodeName)
	return nodeENIs, nil
}

// UpdateNodeENIStatus updates the status of a NodeENI resource
func (c *Client) UpdateNodeENIStatus(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) error {
	log.Printf("Updating status for NodeENI %s", nodeENI.Name)

	if err := c.client.Status().Update(ctx, nodeENI); err != nil {
		return fmt.Errorf("failed to update NodeENI status: %v", err)
	}

	log.Printf("Successfully updated status for NodeENI %s", nodeENI.Name)
	return nil
}

// UpdateAttachmentDPDKStatus updates the DPDK status for a specific ENI attachment
func (c *Client) UpdateAttachmentDPDKStatus(ctx context.Context, eniID, nodeENIName, dpdkDriver string, dpdkBound bool, pciAddress, resourceName string) error {
	log.Printf("Updating DPDK status for ENI %s in NodeENI %s", eniID, nodeENIName)

	// Get the NodeENI resource
	var nodeENI networkingv1alpha1.NodeENI
	if err := c.client.Get(ctx, types.NamespacedName{Name: nodeENIName}, &nodeENI); err != nil {
		return fmt.Errorf("failed to get NodeENI %s: %v", nodeENIName, err)
	}

	// Find and update the attachment
	attachmentFound := false
	for i := range nodeENI.Status.Attachments {
		attachment := &nodeENI.Status.Attachments[i]
		if attachment.ENIID == eniID {
			attachment.DPDKBound = dpdkBound
			attachment.DPDKDriver = dpdkDriver
			attachment.DPDKPCIAddress = pciAddress
			attachment.DPDKResourceName = resourceName
			attachment.LastUpdated = metav1.Now()
			attachmentFound = true
			break
		}
	}

	if !attachmentFound {
		return fmt.Errorf("attachment with ENI ID %s not found in NodeENI %s", eniID, nodeENIName)
	}

	// Update the status
	if err := c.UpdateNodeENIStatus(ctx, &nodeENI); err != nil {
		return fmt.Errorf("failed to update NodeENI status: %v", err)
	}

	log.Printf("Successfully updated DPDK status for ENI %s: bound=%t, driver=%s, PCI=%s, resource=%s",
		eniID, dpdkBound, dpdkDriver, pciAddress, resourceName)
	return nil
}

// UpdateAttachmentStatus updates the general status of an ENI attachment
func (c *Client) UpdateAttachmentStatus(ctx context.Context, eniID, nodeENIName, status string) error {
	log.Printf("Updating status for ENI %s in NodeENI %s to %s", eniID, nodeENIName, status)

	// Get the NodeENI resource
	var nodeENI networkingv1alpha1.NodeENI
	if err := c.client.Get(ctx, types.NamespacedName{Name: nodeENIName}, &nodeENI); err != nil {
		return fmt.Errorf("failed to get NodeENI %s: %v", nodeENIName, err)
	}

	// Find and update the attachment
	attachmentFound := false
	for i := range nodeENI.Status.Attachments {
		attachment := &nodeENI.Status.Attachments[i]
		if attachment.ENIID == eniID {
			attachment.Status = status
			attachment.LastUpdated = metav1.Now()
			attachmentFound = true
			break
		}
	}

	if !attachmentFound {
		return fmt.Errorf("attachment with ENI ID %s not found in NodeENI %s", eniID, nodeENIName)
	}

	// Update the status
	if err := c.UpdateNodeENIStatus(ctx, &nodeENI); err != nil {
		return fmt.Errorf("failed to update NodeENI status: %v", err)
	}

	log.Printf("Successfully updated status for ENI %s to %s", eniID, status)
	return nil
}

// WatchNodeENIResources watches for changes to NodeENI resources
func (c *Client) WatchNodeENIResources(ctx context.Context, nodeName string, callback func([]networkingv1alpha1.NodeENI)) error {
	log.Printf("Starting to watch NodeENI resources for node %s", nodeName)

	// This is a simplified polling implementation
	// In a production system, this would use the Kubernetes watch API
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Stopping NodeENI watch for node %s", nodeName)
			return ctx.Err()
		case <-ticker.C:
			nodeENIs, err := c.GetNodeENIResources(ctx, nodeName)
			if err != nil {
				log.Printf("Error fetching NodeENI resources: %v", err)
				continue
			}
			callback(nodeENIs)
		}
	}
}

// GetNodeENIByName retrieves a specific NodeENI resource by name
func (c *Client) GetNodeENIByName(ctx context.Context, name string) (*networkingv1alpha1.NodeENI, error) {
	var nodeENI networkingv1alpha1.NodeENI
	if err := c.client.Get(ctx, types.NamespacedName{Name: name}, &nodeENI); err != nil {
		return nil, fmt.Errorf("failed to get NodeENI %s: %v", name, err)
	}

	return &nodeENI, nil
}

// ListAllNodeENIResources retrieves all NodeENI resources in the cluster
func (c *Client) ListAllNodeENIResources(ctx context.Context) ([]networkingv1alpha1.NodeENI, error) {
	var nodeENIList networkingv1alpha1.NodeENIList
	if err := c.client.List(ctx, &nodeENIList); err != nil {
		return nil, fmt.Errorf("failed to list NodeENI resources: %v", err)
	}

	return nodeENIList.Items, nil
}

// UpdateNodeENISpec updates the spec of a NodeENI resource
func (c *Client) UpdateNodeENISpec(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) error {
	log.Printf("Updating spec for NodeENI %s", nodeENI.Name)

	if err := c.client.Update(ctx, nodeENI); err != nil {
		return fmt.Errorf("failed to update NodeENI spec: %v", err)
	}

	log.Printf("Successfully updated spec for NodeENI %s", nodeENI.Name)
	return nil
}

// PatchNodeENIStatus patches the status of a NodeENI resource
func (c *Client) PatchNodeENIStatus(ctx context.Context, name string, patch client.Patch) error {
	log.Printf("Patching status for NodeENI %s", name)

	var nodeENI networkingv1alpha1.NodeENI
	nodeENI.Name = name

	if err := c.client.Status().Patch(ctx, &nodeENI, patch); err != nil {
		return fmt.Errorf("failed to patch NodeENI status: %v", err)
	}

	log.Printf("Successfully patched status for NodeENI %s", name)
	return nil
}

// ValidateNodeENIResource validates a NodeENI resource
func (c *Client) ValidateNodeENIResource(nodeENI *networkingv1alpha1.NodeENI) error {
	// Basic validation
	if nodeENI.Name == "" {
		return fmt.Errorf("NodeENI name cannot be empty")
	}

	if len(nodeENI.Spec.NodeSelector) == 0 {
		return fmt.Errorf("NodeENI spec.nodeSelector cannot be empty")
	}

	// Validate DPDK configuration if enabled
	if nodeENI.Spec.EnableDPDK {
		if nodeENI.Spec.DPDKDriver == "" {
			return fmt.Errorf("DPDK driver must be specified when DPDK is enabled")
		}

		// Validate PCI address format if provided
		if nodeENI.Spec.DPDKPCIAddress != "" {
			if !c.isValidPCIAddress(nodeENI.Spec.DPDKPCIAddress) {
				return fmt.Errorf("invalid PCI address format: %s", nodeENI.Spec.DPDKPCIAddress)
			}
		}
	}

	// Validate MTU if specified
	if nodeENI.Spec.MTU > 0 {
		if nodeENI.Spec.MTU < 68 || nodeENI.Spec.MTU > 9000 {
			return fmt.Errorf("MTU must be between 68 and 9000, got %d", nodeENI.Spec.MTU)
		}
	}

	return nil
}

// Helper methods

func (c *Client) isValidPCIAddress(addr string) bool {
	// Simple PCI address format validation (e.g., 0000:00:06.0)
	// This is a basic check - in production, you'd want more robust validation
	return len(addr) >= 12 &&
		addr[4] == ':' &&
		addr[7] == ':' &&
		addr[10] == '.'
}

// nodeMatchesSelector checks if a node matches the given node selector
func (c *Client) nodeMatchesSelector(nodeName string, nodeSelector map[string]string) bool {
	// Simple implementation: check if node name matches any value in the selector
	// In a real implementation, you'd want to fetch the node and check its labels
	for key, value := range nodeSelector {
		if key == "kubernetes.io/hostname" && value == nodeName {
			return true
		}
		// For simplicity, also check if the node name appears as a value
		if value == nodeName {
			return true
		}
	}
	return false
}
