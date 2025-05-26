// Package sriov provides SR-IOV device plugin configuration management
// for the AWS Multi-ENI Controller.
package sriov

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Manager handles SR-IOV device plugin configuration
type Manager struct {
	configPath string
	mutex      sync.RWMutex
}

// NewManager creates a new SR-IOV configuration manager
func NewManager(configPath string) *Manager {
	return &Manager{
		configPath: configPath,
	}
}

// ModernSRIOVDPConfig represents the modern SR-IOV device plugin configuration
type ModernSRIOVDPConfig struct {
	ResourceList []ModernSRIOVResource `json:"resourceList"`
}

// ModernSRIOVResource represents a single SR-IOV resource
type ModernSRIOVResource struct {
	ResourcePrefix string                `json:"resourcePrefix"`
	ResourceName   string                `json:"resourceName"`
	Selectors      []ModernSRIOVSelector `json:"selectors"`
}

// ModernSRIOVSelector represents a device selector for SR-IOV resources
type ModernSRIOVSelector struct {
	Drivers      []string `json:"drivers,omitempty"`
	PCIAddresses []string `json:"pciAddresses,omitempty"`
	Vendors      []string `json:"vendors,omitempty"`
	Devices      []string `json:"devices,omitempty"`
}

// ResourceUpdate represents an SR-IOV resource update
type ResourceUpdate struct {
	PCIAddress     string
	Driver         string
	ResourceName   string
	ResourcePrefix string
	Action         string // "add" or "remove"
}

// LoadOrCreateConfig loads existing SR-IOV configuration or creates a new one
func (m *Manager) LoadOrCreateConfig() (*ModernSRIOVDPConfig, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Check if config file exists
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		log.Printf("SR-IOV config file does not exist, creating minimal configuration")
		return m.createMinimalConfig(), nil
	}

	// Read existing configuration
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SR-IOV config file: %v", err)
	}

	var config ModernSRIOVDPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		log.Printf("Failed to parse existing SR-IOV config, creating new one: %v", err)
		return m.createMinimalConfig(), nil
	}

	log.Printf("Loaded SR-IOV configuration with %d resources", len(config.ResourceList))
	return &config, nil
}

// SaveConfig saves the SR-IOV configuration to file
func (m *Manager) SaveConfig(config *ModernSRIOVDPConfig) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %v", err)
	}

	// Marshal configuration to JSON
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SR-IOV config: %v", err)
	}

	// Write to file
	if err := os.WriteFile(m.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write SR-IOV config file: %v", err)
	}

	log.Printf("Saved SR-IOV configuration with %d resources to %s", len(config.ResourceList), m.configPath)
	return nil
}

// AddResource adds a new SR-IOV resource or updates an existing one
func (m *Manager) AddResource(update ResourceUpdate) error {
	config, err := m.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	// Find existing resource or create new one
	resourceFound := false
	for i := range config.ResourceList {
		resource := &config.ResourceList[i]
		if m.matchesResource(resource, update) {
			m.addPCIAddressToResource(resource, update)
			resourceFound = true
			break
		}
	}

	// Create new resource if not found
	if !resourceFound {
		newResource := m.createResource(update)
		config.ResourceList = append(config.ResourceList, newResource)
		log.Printf("Created new SR-IOV resource: %s/%s", update.ResourcePrefix, update.ResourceName)
	}

	// Save updated configuration
	return m.SaveConfig(config)
}

// RemoveResource removes a PCI address from SR-IOV resources
func (m *Manager) RemoveResource(pciAddress string) error {
	config, err := m.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	configModified := false

	// Remove PCI address from all resources
	var newResourceList []ModernSRIOVResource
	for _, resource := range config.ResourceList {
		// Remove PCI address from selectors
		var newSelectors []ModernSRIOVSelector
		for _, selector := range resource.Selectors {
			var newPCIAddresses []string
			for _, addr := range selector.PCIAddresses {
				if addr != pciAddress {
					newPCIAddresses = append(newPCIAddresses, addr)
				} else {
					configModified = true
					log.Printf("Removing PCI address %s from resource %s/%s",
						pciAddress, resource.ResourcePrefix, resource.ResourceName)
				}
			}

			// Keep selector if it still has PCI addresses
			if len(newPCIAddresses) > 0 {
				selector.PCIAddresses = newPCIAddresses
				newSelectors = append(newSelectors, selector)
			}
		}

		// Keep resource if it still has selectors
		if len(newSelectors) > 0 {
			resource.Selectors = newSelectors
			newResourceList = append(newResourceList, resource)
		} else {
			configModified = true
			log.Printf("Removing empty resource %s/%s", resource.ResourcePrefix, resource.ResourceName)
		}
	}

	config.ResourceList = newResourceList

	// Save if modified
	if configModified {
		return m.SaveConfig(config)
	}

	log.Printf("PCI address %s not found in SR-IOV configuration", pciAddress)
	return nil
}

// ApplyBatchUpdates applies multiple resource updates in a single operation
func (m *Manager) ApplyBatchUpdates(updates []ResourceUpdate) error {
	if len(updates) == 0 {
		return nil
	}

	log.Printf("Applying %d batched SR-IOV updates", len(updates))

	config, err := m.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	// Apply all updates
	for _, update := range updates {
		switch update.Action {
		case "add":
			m.applyAddUpdate(config, update)
		case "remove":
			m.applyRemoveUpdate(config, update)
		default:
			log.Printf("Unknown update action: %s", update.Action)
		}
	}

	// Cleanup and validate configuration
	m.cleanupConfig(config)

	// Save updated configuration
	return m.SaveConfig(config)
}

// RestartDevicePlugin restarts the SR-IOV device plugin
func (m *Manager) RestartDevicePlugin() error {
	log.Printf("Restarting SR-IOV device plugin")

	// This would implement the actual restart logic
	// For now, just log the action
	log.Printf("SR-IOV device plugin restart requested")

	return nil
}

// Helper methods

func (m *Manager) createMinimalConfig() *ModernSRIOVDPConfig {
	return &ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{
			{
				ResourcePrefix: "intel.com",
				ResourceName:   "sriov_placeholder",
				Selectors: []ModernSRIOVSelector{
					{
						Drivers:      []string{"placeholder"},
						PCIAddresses: []string{"0000:00:00.0"},
					},
				},
			},
		},
	}
}

func (m *Manager) matchesResource(resource *ModernSRIOVResource, update ResourceUpdate) bool {
	expectedPrefix := update.ResourcePrefix
	expectedName := update.ResourceName

	return resource.ResourcePrefix == expectedPrefix && resource.ResourceName == expectedName
}

func (m *Manager) addPCIAddressToResource(resource *ModernSRIOVResource, update ResourceUpdate) {
	// Check if PCI address already exists
	for _, selector := range resource.Selectors {
		for _, pciAddr := range selector.PCIAddresses {
			if pciAddr == update.PCIAddress {
				log.Printf("PCI address %s already exists in resource %s/%s",
					update.PCIAddress, resource.ResourcePrefix, resource.ResourceName)
				return
			}
		}
	}

	// Add to first selector or create new one
	if len(resource.Selectors) == 0 {
		resource.Selectors = []ModernSRIOVSelector{
			{
				Drivers:      []string{update.Driver},
				PCIAddresses: []string{update.PCIAddress},
			},
		}
	} else {
		resource.Selectors[0].PCIAddresses = append(resource.Selectors[0].PCIAddresses, update.PCIAddress)
		// Ensure driver is in the list
		if !m.containsString(resource.Selectors[0].Drivers, update.Driver) {
			resource.Selectors[0].Drivers = append(resource.Selectors[0].Drivers, update.Driver)
		}
	}

	log.Printf("Added PCI address %s to resource %s/%s",
		update.PCIAddress, resource.ResourcePrefix, resource.ResourceName)
}

func (m *Manager) createResource(update ResourceUpdate) ModernSRIOVResource {
	return ModernSRIOVResource{
		ResourcePrefix: update.ResourcePrefix,
		ResourceName:   update.ResourceName,
		Selectors: []ModernSRIOVSelector{
			{
				Drivers:      []string{update.Driver},
				PCIAddresses: []string{update.PCIAddress},
			},
		},
	}
}

func (m *Manager) applyAddUpdate(config *ModernSRIOVDPConfig, update ResourceUpdate) {
	// Find existing resource or create new one
	resourceFound := false
	for i := range config.ResourceList {
		resource := &config.ResourceList[i]
		if m.matchesResource(resource, update) {
			m.addPCIAddressToResource(resource, update)
			resourceFound = true
			break
		}
	}

	if !resourceFound {
		newResource := m.createResource(update)
		config.ResourceList = append(config.ResourceList, newResource)
	}
}

func (m *Manager) applyRemoveUpdate(config *ModernSRIOVDPConfig, update ResourceUpdate) {
	// Remove PCI address from matching resource
	for i := range config.ResourceList {
		resource := &config.ResourceList[i]
		if m.matchesResource(resource, update) {
			m.removePCIAddressFromResource(resource, update.PCIAddress)
			break
		}
	}
}

func (m *Manager) removePCIAddressFromResource(resource *ModernSRIOVResource, pciAddress string) {
	for i := range resource.Selectors {
		selector := &resource.Selectors[i]
		var newPCIAddresses []string
		for _, addr := range selector.PCIAddresses {
			if addr != pciAddress {
				newPCIAddresses = append(newPCIAddresses, addr)
			}
		}
		selector.PCIAddresses = newPCIAddresses
	}
}

func (m *Manager) cleanupConfig(config *ModernSRIOVDPConfig) {
	// Remove empty selectors and resources
	var newResourceList []ModernSRIOVResource
	for _, resource := range config.ResourceList {
		var newSelectors []ModernSRIOVSelector
		for _, selector := range resource.Selectors {
			if len(selector.PCIAddresses) > 0 {
				newSelectors = append(newSelectors, selector)
			}
		}

		if len(newSelectors) > 0 {
			resource.Selectors = newSelectors
			newResourceList = append(newResourceList, resource)
		}
	}
	config.ResourceList = newResourceList
}

func (m *Manager) containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
