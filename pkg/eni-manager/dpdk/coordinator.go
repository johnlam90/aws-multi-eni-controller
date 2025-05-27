package dpdk

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/sriov"
	vnetlink "github.com/vishvananda/netlink"
)

// Coordinator orchestrates DPDK binding operations for NodeENI resources
type Coordinator struct {
	manager      *Manager
	sriovManager *sriov.Manager
	config       *config.ENIManagerConfig
	sriovMutex   sync.Mutex

	// State tracking for change detection
	lastNodeENIStates map[string]string // Track NodeENI resource versions
	stateMutex        sync.RWMutex
}

// NewCoordinator creates a new DPDK coordinator
func NewCoordinator(cfg *config.ENIManagerConfig) *Coordinator {
	return &Coordinator{
		manager:           NewManager(cfg),
		sriovManager:      sriov.NewManager(cfg.SRIOVDPConfigPath),
		config:            cfg,
		lastNodeENIStates: make(map[string]string),
	}
}

// SRIOVUpdate represents an SR-IOV configuration update for DPDK
type SRIOVUpdate struct {
	PCIAddress     string
	Driver         string
	ResourceName   string
	ResourcePrefix string
}

// ProcessNodeENIBindings processes DPDK bindings for all NodeENI resources
func (c *Coordinator) ProcessNodeENIBindings(ctx context.Context, nodeName string, nodeENIs []networkingv1alpha1.NodeENI) error {
	log.Printf("Processing DPDK bindings for node %s with %d NodeENI resources", nodeName, len(nodeENIs))

	// Check if any NodeENI has actually changed to avoid unnecessary processing
	if !c.hasNodeENIChanges(nodeENIs) {
		log.Printf("No NodeENI changes detected for DPDK processing, skipping")
		return nil
	}

	log.Printf("NodeENI changes detected, processing DPDK bindings")

	// Collect all DPDK SR-IOV updates
	dpdkSriovUpdates := make(map[string]SRIOVUpdate)

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIs {
		if err := c.processNodeENIBinding(ctx, nodeENI, nodeName, dpdkSriovUpdates); err != nil {
			log.Printf("Warning: Failed to process DPDK binding for NodeENI %s: %v", nodeENI.Name, err)
			continue
		}
	}

	// Apply batched SR-IOV updates
	if len(dpdkSriovUpdates) > 0 {
		if err := c.applyBatchedSRIOVUpdates(dpdkSriovUpdates); err != nil {
			return fmt.Errorf("failed to apply batched DPDK SR-IOV updates: %v", err)
		}
	}

	// Update the NodeENI state tracking
	c.updateNodeENIStates(nodeENIs)

	return nil
}

// processNodeENIBinding processes DPDK binding for a single NodeENI
func (c *Coordinator) processNodeENIBinding(ctx context.Context, nodeENI networkingv1alpha1.NodeENI, nodeName string, dpdkSriovUpdates map[string]SRIOVUpdate) error {
	// Skip if DPDK is not enabled for this NodeENI
	if !nodeENI.Spec.EnableDPDK {
		return nil
	}

	log.Printf("Processing DPDK binding for NodeENI %s", nodeENI.Name)

	// Get DPDK driver
	dpdkDriver := c.getDPDKDriver(nodeENI)
	if dpdkDriver == "" {
		return fmt.Errorf("DPDK driver not specified for NodeENI %s", nodeENI.Name)
	}

	// Process each attachment
	for _, attachment := range nodeENI.Status.Attachments {
		if err := c.processAttachmentBinding(ctx, attachment, nodeENI, dpdkDriver, dpdkSriovUpdates); err != nil {
			log.Printf("Warning: Failed to process DPDK binding for attachment %s: %v", attachment.ENIID, err)
			continue
		}
	}

	return nil
}

// processAttachmentBinding processes DPDK binding for a single attachment
func (c *Coordinator) processAttachmentBinding(ctx context.Context, attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI, dpdkDriver string, dpdkSriovUpdates map[string]SRIOVUpdate) error {
	// Skip if already bound
	if c.shouldSkipBinding(attachment, nodeENI) {
		return nil
	}

	// Try explicit PCI address first
	if nodeENI.Spec.DPDKPCIAddress != "" {
		return c.bindWithExplicitPCIAddress(nodeENI.Spec.DPDKPCIAddress, dpdkDriver, nodeENI, attachment, dpdkSriovUpdates)
	}

	// Fall back to interface name binding
	return c.bindByInterfaceName(attachment, nodeENI, dpdkDriver, dpdkSriovUpdates)
}

// bindWithExplicitPCIAddress binds using an explicit PCI address
func (c *Coordinator) bindWithExplicitPCIAddress(pciAddress, dpdkDriver string, nodeENI networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment, dpdkSriovUpdates map[string]SRIOVUpdate) error {
	log.Printf("Binding with explicit PCI address: %s", pciAddress)

	// Validate PCI address format
	if !c.isPCIAddressFormat(pciAddress) {
		return fmt.Errorf("invalid PCI address format: %s", pciAddress)
	}

	// Collect SR-IOV update if resource name is specified
	if nodeENI.Spec.DPDKResourceName != "" {
		c.collectSRIOVUpdate(pciAddress, nodeENI.Spec.DPDKResourceName, dpdkDriver, dpdkSriovUpdates)
	}

	// Bind the PCI device
	if err := c.manager.BindPCIDeviceToDPDK(pciAddress, dpdkDriver); err != nil {
		return fmt.Errorf("failed to bind PCI device %s to DPDK: %v", pciAddress, err)
	}

	// Update bound interfaces map
	c.updateBoundInterfacesMap(pciAddress, dpdkDriver, nodeENI.Name, attachment.ENIID, "")

	// Update NodeENI status
	return c.updateNodeENIStatus(attachment.ENIID, nodeENI.Name, dpdkDriver, true, pciAddress, nodeENI.Spec.DPDKResourceName)
}

// bindByInterfaceName binds using interface name
func (c *Coordinator) bindByInterfaceName(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI, dpdkDriver string, dpdkSriovUpdates map[string]SRIOVUpdate) error {
	// Determine interface name
	ifaceName := c.getInterfaceNameForAttachment(attachment)
	if ifaceName == "" {
		return fmt.Errorf("could not determine interface name for attachment %s", attachment.ENIID)
	}

	log.Printf("Binding interface %s to DPDK driver %s", ifaceName, dpdkDriver)

	// Get the network link
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get link for interface %s: %v", ifaceName, err)
	}

	// Get PCI address
	pciAddress, err := c.manager.getPCIAddressForInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get PCI address for interface %s: %v", ifaceName, err)
	}

	// Collect SR-IOV update if resource name is specified
	if nodeENI.Spec.DPDKResourceName != "" {
		c.collectSRIOVUpdate(pciAddress, nodeENI.Spec.DPDKResourceName, dpdkDriver, dpdkSriovUpdates)
	}

	// Bind the interface
	if err := c.manager.BindInterfaceToDPDK(link, dpdkDriver); err != nil {
		return fmt.Errorf("failed to bind interface %s to DPDK: %v", ifaceName, err)
	}

	// Update bound interfaces map
	c.updateBoundInterfacesMap(pciAddress, dpdkDriver, nodeENI.Name, attachment.ENIID, ifaceName)

	// Update NodeENI status
	return c.updateNodeENIStatus(attachment.ENIID, nodeENI.Name, dpdkDriver, true, pciAddress, nodeENI.Spec.DPDKResourceName)
}

// CheckForUnbinding checks for interfaces that need to be unbound from DPDK
func (c *Coordinator) CheckForUnbinding(ctx context.Context, nodeName string, nodeENIs []networkingv1alpha1.NodeENI) error {
	log.Printf("Checking for DPDK unbinding on node %s", nodeName)

	// Get currently bound interfaces
	boundInterfaces := c.manager.GetBoundInterfaces()

	// Check each bound interface to see if it should still be bound
	for pciAddr, boundInterface := range boundInterfaces {
		shouldRemain := false

		// Check if this interface is still required by any NodeENI
		for _, nodeENI := range nodeENIs {
			if !nodeENI.Spec.EnableDPDK {
				continue
			}

			// Check if this NodeENI references this interface
			if c.nodeENIReferencesInterface(nodeENI, pciAddr, boundInterface.ENIID) {
				shouldRemain = true
				break
			}
		}

		// If interface should not remain bound, unbind it
		if !shouldRemain {
			log.Printf("Unbinding interface %s (PCI: %s) as it's no longer required",
				boundInterface.InterfaceName, pciAddr)

			if err := c.manager.UnbindInterfaceFromDPDK(boundInterface.InterfaceName); err != nil {
				log.Printf("Warning: Failed to unbind interface %s: %v", boundInterface.InterfaceName, err)
			}
		}
	}

	return nil
}

// Helper methods

func (c *Coordinator) shouldSkipBinding(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI) bool {
	// Skip if already bound to DPDK
	if attachment.DPDKBound {
		log.Printf("Attachment %s is already bound to DPDK, skipping", attachment.ENIID)
		return true
	}

	// Skip if attachment is not ready
	if attachment.Status != "attached" {
		log.Printf("Attachment %s is not in attached state (%s), skipping DPDK binding",
			attachment.ENIID, attachment.Status)
		return true
	}

	return false
}

func (c *Coordinator) getDPDKDriver(nodeENI networkingv1alpha1.NodeENI) string {
	if nodeENI.Spec.DPDKDriver != "" {
		return nodeENI.Spec.DPDKDriver
	}

	// Default to vfio-pci
	return "vfio-pci"
}

func (c *Coordinator) isPCIAddressFormat(addr string) bool {
	// Simple PCI address format validation (e.g., 0000:00:06.0)
	return strings.Contains(addr, ":") && strings.Contains(addr, ".")
}

func (c *Coordinator) getInterfaceNameForAttachment(attachment networkingv1alpha1.ENIAttachment) string {
	// Try to determine interface name from device index
	// This is a simplified version - in practice, this would use the mapping logic
	return fmt.Sprintf("eth%d", attachment.DeviceIndex)
}

func (c *Coordinator) collectSRIOVUpdate(pciAddress, resourceName, driver string, dpdkSriovUpdates map[string]SRIOVUpdate) {
	log.Printf("Collecting DPDK SR-IOV update for PCI %s with resource %s", pciAddress, resourceName)

	// Parse resource name to get prefix and name
	resourcePrefix := ""
	if strings.Contains(resourceName, "/") {
		parts := strings.SplitN(resourceName, "/", 2)
		resourcePrefix = parts[0]
		resourceName = parts[1]
	}

	dpdkSriovUpdates[resourceName] = SRIOVUpdate{
		PCIAddress:     pciAddress,
		Driver:         driver,
		ResourceName:   resourceName,
		ResourcePrefix: resourcePrefix,
	}
}

func (c *Coordinator) updateBoundInterfacesMap(pciAddress, driver, nodeENIName, eniID, ifaceName string) {
	if c.config.DPDKBoundInterfaces == nil {
		c.config.DPDKBoundInterfaces = make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		})
	}

	boundInterface := c.config.DPDKBoundInterfaces[pciAddress]
	boundInterface.NodeENIName = nodeENIName
	boundInterface.ENIID = eniID
	boundInterface.Driver = driver
	boundInterface.PCIAddress = pciAddress
	if ifaceName != "" {
		boundInterface.IfaceName = ifaceName
	}

	c.config.DPDKBoundInterfaces[pciAddress] = boundInterface
	log.Printf("Updated DPDKBoundInterfaces map for PCI %s with NodeENI %s and ENI ID %s",
		pciAddress, nodeENIName, eniID)
}

func (c *Coordinator) nodeENIReferencesInterface(nodeENI networkingv1alpha1.NodeENI, pciAddress, eniID string) bool {
	// Check if NodeENI references this interface by ENI ID or PCI address
	for _, attachment := range nodeENI.Status.Attachments {
		if attachment.ENIID == eniID {
			return true
		}
	}

	// Check by PCI address if specified
	if nodeENI.Spec.DPDKPCIAddress == pciAddress {
		return true
	}

	return false
}

// applyBatchedSRIOVUpdates applies SR-IOV updates in batch
func (c *Coordinator) applyBatchedSRIOVUpdates(dpdkSriovUpdates map[string]SRIOVUpdate) error {
	c.sriovMutex.Lock()
	defer c.sriovMutex.Unlock()

	log.Printf("Applying %d batched DPDK SR-IOV updates", len(dpdkSriovUpdates))

	if len(dpdkSriovUpdates) == 0 {
		return nil
	}

	// Convert DPDK SR-IOV updates to the format expected by the SR-IOV manager
	var updates []sriov.ResourceUpdate
	for resourceName, update := range dpdkSriovUpdates {
		log.Printf("Applying SR-IOV update: resource=%s, PCI=%s, driver=%s",
			resourceName, update.PCIAddress, update.Driver)

		updates = append(updates, sriov.ResourceUpdate{
			PCIAddress:     update.PCIAddress,
			Driver:         update.Driver,
			ResourceName:   update.ResourceName,
			ResourcePrefix: update.ResourcePrefix,
			Action:         "add",
		})
	}

	// Apply the updates using the SR-IOV manager with change detection
	configChanged, err := c.sriovManager.ApplyBatchUpdatesWithChangeDetection(updates)
	if err != nil {
		return fmt.Errorf("failed to apply SR-IOV updates: %v", err)
	}

	// Only restart the device plugin if configuration actually changed
	if configChanged {
		log.Printf("DPDK SR-IOV configuration changed, restarting device plugin")
		if err := c.sriovManager.RestartDevicePlugin(); err != nil {
			log.Printf("Warning: Failed to restart SR-IOV device plugin: %v", err)
			// Don't fail the operation for restart failures
		} else {
			log.Printf("Successfully restarted SR-IOV device plugin after DPDK updates")
		}
	} else {
		log.Printf("DPDK SR-IOV configuration unchanged, no device plugin restart needed")
	}

	return nil
}

// updateNodeENIStatus updates the NodeENI status with DPDK binding information
func (c *Coordinator) updateNodeENIStatus(eniID, nodeENIName, dpdkDriver string, dpdkBound bool, pciAddress, resourceName string) error {
	// This would update the NodeENI status in Kubernetes
	// For now, just log the update
	log.Printf("Would update NodeENI %s status: ENI=%s, bound=%t, driver=%s, PCI=%s, resource=%s",
		nodeENIName, eniID, dpdkBound, dpdkDriver, pciAddress, resourceName)
	return nil
}

// hasNodeENIChanges checks if NodeENI resources have changed since last processing
func (c *Coordinator) hasNodeENIChanges(nodeENIs []networkingv1alpha1.NodeENI) bool {
	c.stateMutex.RLock()
	defer c.stateMutex.RUnlock()

	// Check if the number of NodeENIs changed
	if len(nodeENIs) != len(c.lastNodeENIStates) {
		log.Printf("DPDK: NodeENI count changed: %d -> %d", len(c.lastNodeENIStates), len(nodeENIs))
		return true
	}

	// Check if any NodeENI resource version changed
	for _, nodeENI := range nodeENIs {
		lastResourceVersion, exists := c.lastNodeENIStates[nodeENI.Name]
		currentResourceVersion := nodeENI.ObjectMeta.ResourceVersion

		if !exists || lastResourceVersion != currentResourceVersion {
			log.Printf("DPDK: NodeENI %s changed: %s -> %s", nodeENI.Name, lastResourceVersion, currentResourceVersion)
			return true
		}
	}

	return false
}

// updateNodeENIStates updates the tracked NodeENI states
func (c *Coordinator) updateNodeENIStates(nodeENIs []networkingv1alpha1.NodeENI) {
	c.stateMutex.Lock()
	defer c.stateMutex.Unlock()

	// Clear old states
	c.lastNodeENIStates = make(map[string]string)

	// Update with current states
	for _, nodeENI := range nodeENIs {
		c.lastNodeENIStates[nodeENI.Name] = nodeENI.ObjectMeta.ResourceVersion
	}

	log.Printf("DPDK: Updated NodeENI state tracking for %d resources", len(nodeENIs))
}
