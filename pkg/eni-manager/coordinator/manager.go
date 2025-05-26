// Package coordinator provides the main orchestration logic
// for the AWS Multi-ENI Controller ENI Manager.
package coordinator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/dpdk"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/kubernetes"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/network"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/sriov"
)

// Manager coordinates all ENI management operations
type Manager struct {
	config           *config.ENIManagerConfig
	kubernetesClient *kubernetes.Client
	networkManager   *network.Manager
	dpdkCoordinator  *dpdk.Coordinator
	sriovManager     *sriov.Manager
	
	// State management
	lastProcessedNodeENIs map[string]time.Time
	mutex                 sync.RWMutex
}

// NewManager creates a new coordinator manager
func NewManager(cfg *config.ENIManagerConfig) (*Manager, error) {
	// Initialize Kubernetes client
	k8sClient, err := kubernetes.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	// Initialize network manager
	networkMgr := network.NewManager(cfg)

	// Initialize DPDK coordinator
	dpdkCoord := dpdk.NewCoordinator(cfg)

	// Initialize SR-IOV manager
	sriovMgr := sriov.NewManager(cfg.SRIOVDPConfigPath)

	return &Manager{
		config:                cfg,
		kubernetesClient:      k8sClient,
		networkManager:        networkMgr,
		dpdkCoordinator:       dpdkCoord,
		sriovManager:          sriovMgr,
		lastProcessedNodeENIs: make(map[string]time.Time),
	}, nil
}

// Start starts the ENI manager coordinator
func (m *Manager) Start(ctx context.Context) error {
	log.Printf("Starting ENI Manager Coordinator for node %s", m.config.NodeName)

	// Start the main processing loop
	go m.processLoop(ctx)

	// Start watching for NodeENI changes
	go m.watchNodeENIChanges(ctx)

	// Wait for context cancellation
	<-ctx.Done()
	log.Printf("ENI Manager Coordinator shutting down")
	return nil
}

// processLoop is the main processing loop
func (m *Manager) processLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.processNodeENIs(ctx); err != nil {
				log.Printf("Error processing NodeENIs: %v", err)
			}
		}
	}
}

// watchNodeENIChanges watches for changes to NodeENI resources
func (m *Manager) watchNodeENIChanges(ctx context.Context) {
	callback := func(nodeENIs []networkingv1alpha1.NodeENI) {
		if err := m.handleNodeENIChanges(ctx, nodeENIs); err != nil {
			log.Printf("Error handling NodeENI changes: %v", err)
		}
	}

	if err := m.kubernetesClient.WatchNodeENIResources(ctx, m.config.NodeName, callback); err != nil {
		log.Printf("Error watching NodeENI resources: %v", err)
	}
}

// processNodeENIs processes all NodeENI resources for this node
func (m *Manager) processNodeENIs(ctx context.Context) error {
	// Get NodeENI resources for this node
	nodeENIs, err := m.kubernetesClient.GetNodeENIResources(ctx, m.config.NodeName)
	if err != nil {
		return fmt.Errorf("failed to get NodeENI resources: %v", err)
	}

	if len(nodeENIs) == 0 {
		log.Printf("No NodeENI resources found for node %s", m.config.NodeName)
		return nil
	}

	log.Printf("Processing %d NodeENI resources for node %s", len(nodeENIs), m.config.NodeName)

	// Process network interfaces
	if err := m.processNetworkInterfaces(ctx, nodeENIs); err != nil {
		log.Printf("Error processing network interfaces: %v", err)
	}

	// Process DPDK bindings
	if err := m.processDPDKBindings(ctx, nodeENIs); err != nil {
		log.Printf("Error processing DPDK bindings: %v", err)
	}

	// Check for DPDK unbinding
	if err := m.dpdkCoordinator.CheckForUnbinding(ctx, m.config.NodeName, nodeENIs); err != nil {
		log.Printf("Error checking DPDK unbinding: %v", err)
	}

	// Update SR-IOV configuration
	if err := m.updateSRIOVConfiguration(ctx, nodeENIs); err != nil {
		log.Printf("Error updating SR-IOV configuration: %v", err)
	}

	return nil
}

// handleNodeENIChanges handles changes to NodeENI resources
func (m *Manager) handleNodeENIChanges(ctx context.Context, nodeENIs []networkingv1alpha1.NodeENI) error {
	log.Printf("Handling changes to %d NodeENI resources", len(nodeENIs))

	// Check if any NodeENI has been updated since last processing
	hasChanges := false
	m.mutex.RLock()
	for _, nodeENI := range nodeENIs {
		lastProcessed, exists := m.lastProcessedNodeENIs[nodeENI.Name]
		if !exists || nodeENI.ObjectMeta.ResourceVersion != "" {
			// For simplicity, we'll process if we haven't seen this NodeENI before
			// In production, you'd compare resource versions or generation
			hasChanges = true
			break
		}
		_ = lastProcessed // Use the variable to avoid compiler warning
	}
	m.mutex.RUnlock()

	if hasChanges {
		log.Printf("Detected changes in NodeENI resources, triggering immediate processing")
		return m.processNodeENIs(ctx)
	}

	return nil
}

// processNetworkInterfaces processes network interface configuration
func (m *Manager) processNetworkInterfaces(ctx context.Context, nodeENIs []networkingv1alpha1.NodeENI) error {
	log.Printf("Processing network interfaces for %d NodeENI resources", len(nodeENIs))

	// Get all network interfaces
	interfaces, err := m.networkManager.GetAllInterfaces()
	if err != nil {
		return fmt.Errorf("failed to get network interfaces: %v", err)
	}

	// Process each interface
	for _, iface := range interfaces {
		if !iface.IsAWSENI {
			continue
		}

		// Find corresponding NodeENI
		nodeENI := m.findNodeENIForInterface(iface, nodeENIs)
		if nodeENI == nil {
			continue
		}

		// Configure the interface
		if err := m.networkManager.ConfigureInterfaceFromNodeENI(iface.Name, *nodeENI); err != nil {
			log.Printf("Error configuring interface %s: %v", iface.Name, err)
			continue
		}

		log.Printf("Successfully configured interface %s", iface.Name)
	}

	return nil
}

// processDPDKBindings processes DPDK binding operations
func (m *Manager) processDPDKBindings(ctx context.Context, nodeENIs []networkingv1alpha1.NodeENI) error {
	log.Printf("Processing DPDK bindings for %d NodeENI resources", len(nodeENIs))

	// Filter NodeENIs that have DPDK enabled
	var dpdkNodeENIs []networkingv1alpha1.NodeENI
	for _, nodeENI := range nodeENIs {
		if nodeENI.Spec.EnableDPDK {
			dpdkNodeENIs = append(dpdkNodeENIs, nodeENI)
		}
	}

	if len(dpdkNodeENIs) == 0 {
		log.Printf("No NodeENI resources with DPDK enabled")
		return nil
	}

	log.Printf("Processing DPDK bindings for %d NodeENI resources with DPDK enabled", len(dpdkNodeENIs))

	// Process DPDK bindings
	return m.dpdkCoordinator.ProcessNodeENIBindings(ctx, m.config.NodeName, dpdkNodeENIs)
}

// updateSRIOVConfiguration updates SR-IOV device plugin configuration
func (m *Manager) updateSRIOVConfiguration(ctx context.Context, nodeENIs []networkingv1alpha1.NodeENI) error {
	log.Printf("Updating SR-IOV configuration for %d NodeENI resources", len(nodeENIs))

	// Collect SR-IOV updates
	var updates []sriov.ResourceUpdate

	// Get all network interfaces
	interfaces, err := m.networkManager.GetAllInterfaces()
	if err != nil {
		return fmt.Errorf("failed to get network interfaces: %v", err)
	}

	// Process each interface for SR-IOV configuration
	for _, iface := range interfaces {
		if !iface.IsAWSENI {
			continue
		}

		// Find corresponding NodeENI
		nodeENI := m.findNodeENIForInterface(iface, nodeENIs)
		if nodeENI == nil {
			continue
		}

		// Skip if DPDK is enabled (DPDK coordinator handles SR-IOV for DPDK interfaces)
		if nodeENI.Spec.EnableDPDK {
			continue
		}

		// Create SR-IOV update for non-DPDK interface
		if iface.PCIAddress != "" {
			update := sriov.ResourceUpdate{
				PCIAddress:     iface.PCIAddress,
				Driver:         "ena", // Default driver for AWS ENA devices
				ResourceName:   "sriov_net",
				ResourcePrefix: "intel.com",
				Action:         "add",
			}
			updates = append(updates, update)
		}
	}

	// Apply batched updates
	if len(updates) > 0 {
		if err := m.sriovManager.ApplyBatchUpdates(updates); err != nil {
			return fmt.Errorf("failed to apply SR-IOV updates: %v", err)
		}

		// Restart device plugin if needed
		if err := m.sriovManager.RestartDevicePlugin(); err != nil {
			log.Printf("Warning: Failed to restart SR-IOV device plugin: %v", err)
		}
	}

	return nil
}

// Helper methods

func (m *Manager) findNodeENIForInterface(iface network.InterfaceInfo, nodeENIs []networkingv1alpha1.NodeENI) *networkingv1alpha1.NodeENI {
	// Try to match by device index first
	for _, nodeENI := range nodeENIs {
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.DeviceIndex == iface.DeviceIndex {
				return &nodeENI
			}
		}
	}

	// Try to match by PCI address if available
	if iface.PCIAddress != "" {
		for _, nodeENI := range nodeENIs {
			if nodeENI.Spec.DPDKPCIAddress == iface.PCIAddress {
				return &nodeENI
			}
		}
	}

	return nil
}

// updateLastProcessedTime updates the last processed time for a NodeENI
func (m *Manager) updateLastProcessedTime(nodeENIName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.lastProcessedNodeENIs[nodeENIName] = time.Now()
}

// GetStatus returns the current status of the coordinator
func (m *Manager) GetStatus() map[string]interface{} {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	status := map[string]interface{}{
		"node_name":              m.config.NodeName,
		"check_interval":         m.config.CheckInterval.String(),
		"last_processed_count":   len(m.lastProcessedNodeENIs),
		"dpdk_bound_interfaces":  len(m.config.DPDKBoundInterfaces),
	}

	return status
}
