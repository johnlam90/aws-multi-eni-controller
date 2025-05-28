// Package coordinator provides the main orchestration logic
// for the AWS Multi-ENI Controller ENI Manager.
package coordinator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/dpdk"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/kubernetes"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/network"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/sriov"
)

// SRIOVResourceInfo tracks SR-IOV resource information for cleanup
type SRIOVResourceInfo struct {
	PCIAddress     string
	ResourceName   string
	ResourcePrefix string
	Driver         string
	NodeENIName    string
}

// Manager coordinates all ENI management operations
type Manager struct {
	config           *config.ENIManagerConfig
	kubernetesClient *kubernetes.Client
	networkManager   *network.Manager
	dpdkCoordinator  *dpdk.Coordinator
	sriovManager     *sriov.Manager

	// State management
	lastProcessedNodeENIs map[string]time.Time
	previousNodeENIs      map[string]bool   // Track previous NodeENI names for deletion detection
	lastNodeENIStates     map[string]string // Track NodeENI resource versions to detect changes
	lastSRIOVConfigHash   string            // Track SR-IOV configuration hash to detect changes

	// Resource tracking for cleanup
	nodeENISRIOVResources map[string][]SRIOVResourceInfo // Track SR-IOV resources by NodeENI name

	mutex sync.RWMutex
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
		previousNodeENIs:      make(map[string]bool),
		lastNodeENIStates:     make(map[string]string),
		lastSRIOVConfigHash:   "",
		nodeENISRIOVResources: make(map[string][]SRIOVResourceInfo),
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

	// Create current NodeENI map for deletion detection
	currentNodeENIs := make(map[string]bool)
	for _, nodeENI := range nodeENIs {
		currentNodeENIs[nodeENI.Name] = true
	}

	// Handle NodeENI deletions
	if err := m.handleNodeENIDeletions(currentNodeENIs); err != nil {
		log.Printf("Error handling NodeENI deletions: %v", err)
	}

	// Update previous NodeENI tracking
	m.mutex.Lock()
	m.previousNodeENIs = currentNodeENIs
	m.mutex.Unlock()

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
	// Check if NodeENI resources have actually changed
	if !m.hasNodeENIChanges(nodeENIs) {
		log.Printf("No NodeENI changes detected, skipping SR-IOV configuration update")
		return nil
	}

	log.Printf("NodeENI changes detected, updating SR-IOV configuration for %d NodeENI resources", len(nodeENIs))

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

		// Only process interfaces where dpdkPCIAddress is explicitly specified
		// This covers Case 2 (SR-IOV without DPDK) and Case 3 (SR-IOV with DPDK)
		// Case 1 (regular ENI) should not generate SR-IOV configuration
		if nodeENI.Spec.DPDKPCIAddress == "" {
			log.Printf("Skipping SR-IOV configuration for interface %s: no dpdkPCIAddress specified (regular ENI)", iface.Name)
			continue
		}

		// Skip if DPDK is enabled (DPDK coordinator handles SR-IOV for DPDK interfaces)
		if nodeENI.Spec.EnableDPDK {
			log.Printf("Skipping SR-IOV configuration for interface %s: DPDK enabled (handled by DPDK coordinator)", iface.Name)
			continue
		}

		// Create SR-IOV update for non-DPDK interface (Case 2: SR-IOV without DPDK)
		if iface.PCIAddress != "" {
			// Parse resource name from NodeENI spec or use default
			resourcePrefix, resourceName := m.parseResourceName(nodeENI.Spec.DPDKResourceName)

			update := sriov.ResourceUpdate{
				PCIAddress:     iface.PCIAddress,
				Driver:         "ena", // Default driver for AWS ENA devices
				ResourceName:   resourceName,
				ResourcePrefix: resourcePrefix,
				Action:         "add",
			}
			updates = append(updates, update)

			// Track this resource for cleanup purposes
			m.trackSRIOVResource(nodeENI.Name, SRIOVResourceInfo{
				PCIAddress:     iface.PCIAddress,
				ResourceName:   resourceName,
				ResourcePrefix: resourcePrefix,
				Driver:         "ena",
				NodeENIName:    nodeENI.Name,
			})

			log.Printf("Creating SR-IOV update for non-DPDK interface %s: %s/%s at PCI %s (Case 2: SR-IOV without DPDK)",
				iface.Name, resourcePrefix, resourceName, iface.PCIAddress)
		}
	}

	// Apply batched updates
	if len(updates) > 0 {
		log.Printf("Applying %d SR-IOV configuration updates", len(updates))

		// Check if updates will actually change the configuration
		configChanged, err := m.sriovManager.ApplyBatchUpdatesWithChangeDetection(updates)
		if err != nil {
			return fmt.Errorf("failed to apply SR-IOV updates: %v", err)
		}

		// Only restart device plugin if configuration actually changed
		if configChanged {
			log.Printf("SR-IOV configuration changed, restarting device plugin")
			if err := m.sriovManager.RestartDevicePlugin(); err != nil {
				log.Printf("Warning: Failed to restart SR-IOV device plugin: %v", err)
			}
		} else {
			log.Printf("SR-IOV configuration unchanged, no device plugin restart needed")
		}
	} else {
		log.Printf("No SR-IOV configuration updates needed")
	}

	// Update the NodeENI state tracking
	m.updateNodeENIStates(nodeENIs)

	return nil
}

// Helper methods

// parseResourceName parses a resource name in the format "prefix/name" or returns defaults
func (m *Manager) parseResourceName(resourceName string) (string, string) {
	if resourceName == "" {
		// Return default values if no resource name is specified
		return "intel.com", "sriov_net"
	}

	// Split by "/" to separate prefix and name
	parts := strings.Split(resourceName, "/")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}

	// If no "/" found, treat the whole string as the resource name with default prefix
	return "intel.com", resourceName
}

// ParseResourceNameForTest exposes parseResourceName for testing purposes
func (m *Manager) ParseResourceNameForTest(resourceName string) (string, string) {
	return m.parseResourceName(resourceName)
}

// trackSRIOVResource tracks an SR-IOV resource for cleanup purposes
func (m *Manager) trackSRIOVResource(nodeENIName string, resourceInfo SRIOVResourceInfo) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Initialize the slice if it doesn't exist
	if m.nodeENISRIOVResources[nodeENIName] == nil {
		m.nodeENISRIOVResources[nodeENIName] = make([]SRIOVResourceInfo, 0)
	}

	// Check if this resource is already tracked to avoid duplicates
	for i, existing := range m.nodeENISRIOVResources[nodeENIName] {
		if existing.PCIAddress == resourceInfo.PCIAddress &&
			existing.ResourceName == resourceInfo.ResourceName &&
			existing.ResourcePrefix == resourceInfo.ResourcePrefix {
			// Resource already tracked, update it
			m.nodeENISRIOVResources[nodeENIName][i].Driver = resourceInfo.Driver
			log.Printf("Updated tracked SR-IOV resource for NodeENI %s: %s/%s at PCI %s",
				nodeENIName, resourceInfo.ResourcePrefix, resourceInfo.ResourceName, resourceInfo.PCIAddress)
			return
		}
	}

	// Add new resource
	m.nodeENISRIOVResources[nodeENIName] = append(m.nodeENISRIOVResources[nodeENIName], resourceInfo)
	log.Printf("Tracking new SR-IOV resource for NodeENI %s: %s/%s at PCI %s",
		nodeENIName, resourceInfo.ResourcePrefix, resourceInfo.ResourceName, resourceInfo.PCIAddress)
}

func (m *Manager) findNodeENIForInterface(iface network.InterfaceInfo, nodeENIs []networkingv1alpha1.NodeENI) *networkingv1alpha1.NodeENI {
	// For SR-IOV configurations, prioritize PCI address matching over device index
	// This ensures correct mapping when dpdkPCIAddress is explicitly specified
	if iface.PCIAddress != "" {
		for _, nodeENI := range nodeENIs {
			if nodeENI.Spec.DPDKPCIAddress == iface.PCIAddress {
				log.Printf("Matched interface %s (PCI: %s) to NodeENI %s by PCI address",
					iface.Name, iface.PCIAddress, nodeENI.Name)
				return &nodeENI
			}
		}
	}

	// Fallback to device index matching for regular ENI configurations
	for _, nodeENI := range nodeENIs {
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.DeviceIndex == iface.DeviceIndex {
				log.Printf("Matched interface %s (device index: %d) to NodeENI %s by device index",
					iface.Name, iface.DeviceIndex, nodeENI.Name)
				return &nodeENI
			}
		}
	}

	log.Printf("No matching NodeENI found for interface %s (PCI: %s, device index: %d)",
		iface.Name, iface.PCIAddress, iface.DeviceIndex)
	return nil
}

// FindNodeENIForInterfaceTest exposes findNodeENIForInterface for testing purposes
func (m *Manager) FindNodeENIForInterfaceTest(iface network.InterfaceInfo, nodeENIs []networkingv1alpha1.NodeENI) *networkingv1alpha1.NodeENI {
	return m.findNodeENIForInterface(iface, nodeENIs)
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
		"node_name":             m.config.NodeName,
		"check_interval":        m.config.CheckInterval.String(),
		"last_processed_count":  len(m.lastProcessedNodeENIs),
		"dpdk_bound_interfaces": len(m.config.DPDKBoundInterfaces),
	}

	return status
}

// handleNodeENIDeletions detects and handles deleted NodeENI resources
func (m *Manager) handleNodeENIDeletions(currentNodeENIs map[string]bool) error {
	m.mutex.RLock()
	previousNodeENIs := m.previousNodeENIs
	m.mutex.RUnlock()

	log.Printf("NodeENI deletion check: found %d current NodeENIs, tracking %d previous NodeENIs",
		len(currentNodeENIs), len(previousNodeENIs))

	if previousNodeENIs == nil {
		log.Printf("Previous NodeENI tracking not initialized yet, skipping deletion check")
		return nil
	}

	deletedCount := 0
	for nodeENIName := range previousNodeENIs {
		if !currentNodeENIs[nodeENIName] {
			deletedCount++
			if err := m.handleSingleNodeENIDeletion(nodeENIName); err != nil {
				log.Printf("Warning: Failed to handle deletion of NodeENI %s: %v", nodeENIName, err)
			}
		}
	}

	if deletedCount > 0 {
		log.Printf("Processed %d deleted NodeENI resources", deletedCount)
	}

	return nil
}

// handleSingleNodeENIDeletion handles the deletion of a single NodeENI resource
func (m *Manager) handleSingleNodeENIDeletion(nodeENIName string) error {
	log.Printf("Detected deleted NodeENI: %s, cleaning up SR-IOV configuration", nodeENIName)

	if err := m.cleanupSRIOVConfigForNodeENI(nodeENIName); err != nil {
		return fmt.Errorf("failed to cleanup SR-IOV config for deleted NodeENI %s: %v", nodeENIName, err)
	}

	log.Printf("Successfully cleaned up SR-IOV config for deleted NodeENI: %s", nodeENIName)
	return nil
}

// hasNodeENIChanges checks if NodeENI resources have changed since last processing
func (m *Manager) hasNodeENIChanges(nodeENIs []networkingv1alpha1.NodeENI) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Check if the number of NodeENIs changed
	if len(nodeENIs) != len(m.lastNodeENIStates) {
		log.Printf("NodeENI count changed: %d -> %d", len(m.lastNodeENIStates), len(nodeENIs))
		return true
	}

	// Check if any NodeENI resource version changed
	for _, nodeENI := range nodeENIs {
		lastResourceVersion, exists := m.lastNodeENIStates[nodeENI.Name]
		currentResourceVersion := nodeENI.ObjectMeta.ResourceVersion

		if !exists || lastResourceVersion != currentResourceVersion {
			log.Printf("NodeENI %s changed: %s -> %s", nodeENI.Name, lastResourceVersion, currentResourceVersion)
			return true
		}
	}

	return false
}

// updateNodeENIStates updates the tracked NodeENI states
func (m *Manager) updateNodeENIStates(nodeENIs []networkingv1alpha1.NodeENI) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Clear old states
	m.lastNodeENIStates = make(map[string]string)

	// Update with current states
	for _, nodeENI := range nodeENIs {
		m.lastNodeENIStates[nodeENI.Name] = nodeENI.ObjectMeta.ResourceVersion
	}

	log.Printf("Updated NodeENI state tracking for %d resources", len(nodeENIs))
}

// cleanupSRIOVConfigForNodeENI removes SR-IOV configuration for a deleted NodeENI
func (m *Manager) cleanupSRIOVConfigForNodeENI(nodeENIName string) error {
	log.Printf("Cleaning up SR-IOV configuration for deleted NodeENI: %s", nodeENIName)

	// Gather resources to cleanup
	dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup := m.gatherCleanupResources(nodeENIName)
	totalResourcesToCleanup := len(dpdkInterfacesToCleanup) + len(nonDpdkResourcesToCleanup)

	// Early return if nothing to cleanup
	if m.shouldSkipCleanup(totalResourcesToCleanup, nodeENIName) {
		return nil
	}

	// Log cleanup summary
	m.logCleanupSummary(dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup, totalResourcesToCleanup, nodeENIName)

	// Perform the actual cleanup
	configModified := m.performSRIOVCleanup(dpdkInterfacesToCleanup, nodeENIName)

	// Handle device plugin restart
	return m.handleDevicePluginRestart(configModified, dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup, totalResourcesToCleanup, nodeENIName)
}
