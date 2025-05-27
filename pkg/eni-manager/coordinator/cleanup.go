// Package coordinator provides cleanup functionality for NodeENI deletions
package coordinator

import (
	"fmt"
	"log"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/sriov"
)

// gatherCleanupResources collects DPDK and non-DPDK resources that need cleanup
func (m *Manager) gatherCleanupResources(nodeENIName string) ([]string, []string) {
	var dpdkInterfacesToCleanup []string
	for pciAddr, boundInterface := range m.config.DPDKBoundInterfaces {
		if boundInterface.NodeENIName == nodeENIName {
			dpdkInterfacesToCleanup = append(dpdkInterfacesToCleanup, pciAddr)
		}
	}

	var nonDpdkResourcesToCleanup []string
	if m.config.SRIOVDPConfigPath != "" {
		if resources, err := m.findSRIOVResourcesForNodeENI(nodeENIName); err == nil {
			nonDpdkResourcesToCleanup = resources
		}
	}

	return dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup
}

// shouldSkipCleanup determines if cleanup should be skipped
func (m *Manager) shouldSkipCleanup(totalResourcesToCleanup int, nodeENIName string) bool {
	if totalResourcesToCleanup == 0 {
		log.Printf("No resources to cleanup for deleted NodeENI: %s", nodeENIName)
		return true
	}

	if m.config.SRIOVDPConfigPath == "" {
		log.Printf("SR-IOV device plugin config path not configured, skipping cleanup for NodeENI: %s", nodeENIName)
		return true
	}

	return false
}

// logCleanupSummary logs a summary of what will be cleaned up
func (m *Manager) logCleanupSummary(dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup []string, totalResourcesToCleanup int, nodeENIName string) {
	log.Printf("Cleanup summary for NodeENI %s:", nodeENIName)
	log.Printf("  - DPDK interfaces to cleanup: %d (%v)", len(dpdkInterfacesToCleanup), dpdkInterfacesToCleanup)
	log.Printf("  - Non-DPDK resources to cleanup: %d (%v)", len(nonDpdkResourcesToCleanup), nonDpdkResourcesToCleanup)
	log.Printf("  - Total resources to cleanup: %d", totalResourcesToCleanup)
}

// performSRIOVCleanup performs the actual SR-IOV configuration cleanup
func (m *Manager) performSRIOVCleanup(dpdkInterfacesToCleanup []string, nodeENIName string) bool {
	configModified := false
	for _, pciAddr := range dpdkInterfacesToCleanup {
		if m.cleanupSingleDPDKInterface(pciAddr, nodeENIName) {
			configModified = true
		}
	}

	// Ensure the SR-IOV configuration has a minimal valid structure after cleanup
	if err := m.ensureMinimalSRIOVConfig(); err != nil {
		log.Printf("Warning: Failed to ensure minimal SR-IOV config after cleanup: %v", err)
	}

	return configModified
}

// cleanupSingleDPDKInterface cleans up a single DPDK interface
func (m *Manager) cleanupSingleDPDKInterface(pciAddr, nodeENIName string) bool {
	boundInterface, exists := m.config.DPDKBoundInterfaces[pciAddr]
	if !exists {
		log.Printf("PCI address %s not found in DPDK bound interfaces for NodeENI %s", pciAddr, nodeENIName)
		return false
	}

	log.Printf("Cleaning up DPDK interface %s (PCI: %s) for NodeENI %s", boundInterface.IfaceName, pciAddr, nodeENIName)

	// Remove from tracking map
	delete(m.config.DPDKBoundInterfaces, pciAddr)

	// Try modern format first, then fall back to legacy format
	if err := m.removeModernSRIOVDeviceConfig(pciAddr); err != nil {
		log.Printf("Modern SR-IOV removal failed for PCI %s, trying legacy format: %v", pciAddr, err)
		// Fall back to legacy format
		if legacyErr := m.removeSRIOVDevicePluginConfig(boundInterface.IfaceName, pciAddr); legacyErr != nil {
			log.Printf("Warning: Both modern and legacy SR-IOV config removal failed for PCI %s: modern=%v, legacy=%v",
				pciAddr, err, legacyErr)
			return false
		}
		log.Printf("Removed SR-IOV config for PCI %s using legacy format (NodeENI: %s)", pciAddr, nodeENIName)
	} else {
		log.Printf("Removed SR-IOV config for PCI %s using modern format (NodeENI: %s)", pciAddr, nodeENIName)
	}

	return true
}

// handleDevicePluginRestart handles the device plugin restart logic
func (m *Manager) handleDevicePluginRestart(configModified bool, dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup []string, totalResourcesToCleanup int, nodeENIName string) error {
	// Only restart if there were actual changes made to the configuration
	shouldRestart := configModified && (len(dpdkInterfacesToCleanup) > 0 || len(nonDpdkResourcesToCleanup) > 0)

	if !shouldRestart {
		if configModified {
			log.Printf("SR-IOV config was modified but no resources were cleaned up for NodeENI %s - no restart needed", nodeENIName)
		} else {
			log.Printf("No SR-IOV configuration changes for NodeENI %s - no restart needed", nodeENIName)
		}
		return nil
	}

	m.logRestartReason(configModified, dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup, nodeENIName)
	return m.executeDevicePluginRestart(nodeENIName)
}

// logRestartReason logs why the device plugin restart is needed
func (m *Manager) logRestartReason(configModified bool, dpdkInterfacesToCleanup, nonDpdkResourcesToCleanup []string, nodeENIName string) {
	log.Printf("SR-IOV device plugin restart needed for NodeENI %s:", nodeENIName)
	if configModified {
		log.Printf("  - SR-IOV configuration was modified")
	}
	if len(dpdkInterfacesToCleanup) > 0 {
		log.Printf("  - DPDK interfaces were cleaned up: %v", dpdkInterfacesToCleanup)
	}
	if len(nonDpdkResourcesToCleanup) > 0 {
		log.Printf("  - Non-DPDK resources were cleaned up: %v", nonDpdkResourcesToCleanup)
	}
}

// executeDevicePluginRestart executes the device plugin restart with fallback
func (m *Manager) executeDevicePluginRestart(nodeENIName string) error {
	// Use the SR-IOV manager to restart the device plugin
	if err := m.sriovManager.RestartDevicePlugin(); err != nil {
		log.Printf("Warning: Failed to restart SR-IOV device plugin: %v", err)
		return err
	}

	log.Printf("Successfully restarted SR-IOV device plugin after NodeENI %s cleanup", nodeENIName)
	return nil
}

// Helper methods for SR-IOV configuration management

// findSRIOVResourcesForNodeENI finds SR-IOV resources associated with a NodeENI
func (m *Manager) findSRIOVResourcesForNodeENI(nodeENIName string) ([]string, error) {
	// This is a simplified implementation
	// In a real scenario, you'd need to track which SR-IOV resources belong to which NodeENI
	// For now, we'll return an empty list
	return []string{}, nil
}

// removeModernSRIOVDeviceConfig removes a device from modern SR-IOV configuration
func (m *Manager) removeModernSRIOVDeviceConfig(pciAddr string) error {
	return m.sriovManager.RemoveResource(pciAddr)
}

// removeSRIOVDevicePluginConfig removes a device from legacy SR-IOV configuration
func (m *Manager) removeSRIOVDevicePluginConfig(ifaceName, pciAddr string) error {
	// Legacy format removal - this would need to be implemented based on the legacy format
	log.Printf("Legacy SR-IOV config removal not implemented for interface %s (PCI: %s)", ifaceName, pciAddr)
	return fmt.Errorf("legacy SR-IOV config removal not implemented")
}

// ensureMinimalSRIOVConfig ensures the SR-IOV configuration has a minimal valid structure
func (m *Manager) ensureMinimalSRIOVConfig() error {
	// Load the current configuration
	config, err := m.sriovManager.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	// If no resources remain, create a minimal placeholder configuration
	if len(config.ResourceList) == 0 {
		log.Printf("No SR-IOV resources remain, creating minimal placeholder configuration")

		// Create a minimal placeholder resource
		placeholderResource := sriov.ModernSRIOVResource{
			ResourceName:   "placeholder",
			ResourcePrefix: "intel.com",
			Selectors: []sriov.ModernSRIOVSelector{
				{
					Vendors:      []string{"8086"}, // Intel vendor ID
					Devices:      []string{"0000"}, // Placeholder device ID
					Drivers:      []string{"vfio-pci"},
					PCIAddresses: []string{}, // Empty PCI addresses
				},
			},
		}

		config.ResourceList = []sriov.ModernSRIOVResource{placeholderResource}

		// Save the minimal configuration
		if err := m.sriovManager.SaveConfig(config); err != nil {
			return fmt.Errorf("failed to save minimal SR-IOV config: %v", err)
		}

		log.Printf("Created minimal placeholder SR-IOV configuration")
	}

	return nil
}
