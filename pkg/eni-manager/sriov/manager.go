// Package sriov provides SR-IOV device plugin configuration management
// for the AWS Multi-ENI Controller.
package sriov

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
	changed, err := m.ApplyBatchUpdatesWithChangeDetection(updates)
	_ = changed // Ignore the change detection result for backward compatibility
	return err
}

// ApplyBatchUpdatesWithChangeDetection applies multiple resource updates and returns whether the config changed
func (m *Manager) ApplyBatchUpdatesWithChangeDetection(updates []ResourceUpdate) (bool, error) {
	if len(updates) == 0 {
		return false, nil
	}

	log.Printf("Applying %d batched SR-IOV updates with change detection", len(updates))

	config, err := m.LoadOrCreateConfig()
	if err != nil {
		return false, fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	// Create a deep copy of the original config for comparison
	originalConfig := m.deepCopyConfig(config)

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

	// Check if configuration actually changed
	configChanged := !m.configsEqual(originalConfig, config)

	if configChanged {
		log.Printf("SR-IOV configuration changed, saving updated config")
		// Save updated configuration
		if err := m.SaveConfig(config); err != nil {
			return false, err
		}
	} else {
		log.Printf("SR-IOV configuration unchanged, no save needed")
	}

	return configChanged, nil
}

// RestartDevicePlugin restarts the SR-IOV device plugin
func (m *Manager) RestartDevicePlugin() error {
	log.Printf("Restarting SR-IOV device plugin")

	// Check NODE_NAME first before creating Kubernetes client
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return fmt.Errorf("NODE_NAME environment variable not set - cannot determine which node's SR-IOV device plugin to restart")
	}

	// Create a Kubernetes client for device plugin restart
	k8sClient, err := m.createKubernetesClient()
	if err != nil {
		log.Printf("Warning: Failed to create Kubernetes client for device plugin restart: %v", err)
		return err
	}

	// Use the restart logic from the old implementation
	return m.restartDevicePluginPods(k8sClient)
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
	hasRealResources := false

	for _, resource := range config.ResourceList {
		var newSelectors []ModernSRIOVSelector
		for _, selector := range resource.Selectors {
			if len(selector.PCIAddresses) > 0 {
				newSelectors = append(newSelectors, selector)
			}
		}

		if len(newSelectors) > 0 {
			// Check if this is a placeholder resource
			isPlaceholder := resource.ResourceName == "sriov_placeholder" &&
				len(newSelectors) == 1 &&
				len(newSelectors[0].PCIAddresses) == 1 &&
				newSelectors[0].PCIAddresses[0] == "0000:00:00.0"

			if !isPlaceholder {
				hasRealResources = true
			}

			resource.Selectors = newSelectors
			newResourceList = append(newResourceList, resource)
		}
	}

	// If we have real resources, remove placeholder resources
	if hasRealResources {
		var finalResourceList []ModernSRIOVResource
		for _, resource := range newResourceList {
			isPlaceholder := resource.ResourceName == "sriov_placeholder" &&
				len(resource.Selectors) == 1 &&
				len(resource.Selectors[0].PCIAddresses) == 1 &&
				resource.Selectors[0].PCIAddresses[0] == "0000:00:00.0"

			if !isPlaceholder {
				finalResourceList = append(finalResourceList, resource)
			}
		}
		config.ResourceList = finalResourceList
	} else {
		config.ResourceList = newResourceList
	}
}

func (m *Manager) containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// createKubernetesClient creates a Kubernetes client for device plugin operations
func (m *Manager) createKubernetesClient() (kubernetes.Interface, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	return clientset, nil
}

// restartDevicePluginPods restarts the SR-IOV device plugin pods
func (m *Manager) restartDevicePluginPods(k8sClient kubernetes.Interface) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get the current node name for node-specific pod filtering
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return fmt.Errorf("NODE_NAME environment variable not set - cannot determine which node's SR-IOV device plugin to restart")
	}

	// Try different label selectors for SR-IOV device plugin pods
	labelSelectors := []string{
		"app=sriov-device-plugin",
		"app=kube-sriov-device-plugin",
		"name=sriov-device-plugin",
		"component=sriov-device-plugin",
	}

	// Try different namespaces where SR-IOV device plugin might be deployed
	namespaces := []string{
		"kube-system",
		"sriov-network-operator",
		"openshift-sriov-network-operator",
		"intel-device-plugins-operator",
		"default",
	}

	deletedPods := 0
	var lastErr error

	for _, namespace := range namespaces {
		for _, labelSelector := range labelSelectors {
			listOptions := metav1.ListOptions{
				LabelSelector: labelSelector,
				FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
			}

			pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, listOptions)
			if err != nil {
				log.Printf("Warning: Failed to list pods with selector %s in namespace %s: %v", labelSelector, namespace, err)
				lastErr = err
				continue
			}

			if len(pods.Items) > 0 {
				log.Printf("Found %d SR-IOV device plugin pods on node %s with selector %s in namespace %s", len(pods.Items), nodeName, labelSelector, namespace)

				// Delete each pod to trigger restart
				for _, pod := range pods.Items {
					// Double-check node affinity as additional safety measure
					if pod.Spec.NodeName != nodeName {
						log.Printf("Warning: Pod %s is not on expected node %s (actual: %s), skipping", pod.Name, nodeName, pod.Spec.NodeName)
						continue
					}

					log.Printf("Deleting SR-IOV device plugin pod %s on node %s in namespace %s", pod.Name, nodeName, namespace)

					err := k8sClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
						GracePeriodSeconds: &[]int64{0}[0], // Force immediate deletion
					})
					if err != nil {
						log.Printf("Warning: Failed to delete pod %s on node %s: %v", pod.Name, nodeName, err)
						lastErr = err
					} else {
						log.Printf("Successfully deleted SR-IOV device plugin pod %s on node %s in namespace %s", pod.Name, nodeName, namespace)
						deletedPods++
					}
				}

				// If we found and processed pods with this selector and namespace, break out of label selector loop
				if len(pods.Items) > 0 {
					break
				}
			}
		}

		// If we found pods in this namespace, break out of namespace loop
		if deletedPods > 0 {
			break
		}
	}

	if deletedPods == 0 {
		if lastErr != nil {
			return fmt.Errorf("failed to restart any SR-IOV device plugin pods: %v", lastErr)
		}
		return fmt.Errorf("no SR-IOV device plugin pods found in any namespace")
	}

	log.Printf("Successfully initiated restart of %d SR-IOV device plugin pods", deletedPods)
	return nil
}

// deepCopyConfig creates a deep copy of the SR-IOV configuration
func (m *Manager) deepCopyConfig(config *ModernSRIOVDPConfig) *ModernSRIOVDPConfig {
	if config == nil {
		return nil
	}

	copy := &ModernSRIOVDPConfig{
		ResourceList: make([]ModernSRIOVResource, len(config.ResourceList)),
	}

	for i, resource := range config.ResourceList {
		copy.ResourceList[i] = ModernSRIOVResource{
			ResourceName:   resource.ResourceName,
			ResourcePrefix: resource.ResourcePrefix,
			Selectors:      make([]ModernSRIOVSelector, len(resource.Selectors)),
		}

		for j, selector := range resource.Selectors {
			copy.ResourceList[i].Selectors[j] = ModernSRIOVSelector{
				Drivers:      append([]string(nil), selector.Drivers...),
				PCIAddresses: append([]string(nil), selector.PCIAddresses...),
			}
		}
	}

	return copy
}

// configsEqual compares two SR-IOV configurations for equality
func (m *Manager) configsEqual(config1, config2 *ModernSRIOVDPConfig) bool {
	if config1 == nil && config2 == nil {
		return true
	}
	if config1 == nil || config2 == nil {
		return false
	}

	if len(config1.ResourceList) != len(config2.ResourceList) {
		return false
	}

	// Create maps for easier comparison
	resources1 := make(map[string]ModernSRIOVResource)
	resources2 := make(map[string]ModernSRIOVResource)

	for _, resource := range config1.ResourceList {
		key := resource.ResourcePrefix + "/" + resource.ResourceName
		resources1[key] = resource
	}

	for _, resource := range config2.ResourceList {
		key := resource.ResourcePrefix + "/" + resource.ResourceName
		resources2[key] = resource
	}

	// Compare resources
	for key, resource1 := range resources1 {
		resource2, exists := resources2[key]
		if !exists {
			return false
		}

		if !m.resourcesEqual(resource1, resource2) {
			return false
		}
	}

	return true
}

// resourcesEqual compares two SR-IOV resources for equality
func (m *Manager) resourcesEqual(resource1, resource2 ModernSRIOVResource) bool {
	if resource1.ResourceName != resource2.ResourceName ||
		resource1.ResourcePrefix != resource2.ResourcePrefix {
		return false
	}

	if len(resource1.Selectors) != len(resource2.Selectors) {
		return false
	}

	for i, selector1 := range resource1.Selectors {
		if i >= len(resource2.Selectors) {
			return false
		}
		selector2 := resource2.Selectors[i]

		if !m.selectorsEqual(selector1, selector2) {
			return false
		}
	}

	return true
}

// selectorsEqual compares two SR-IOV selectors for equality
func (m *Manager) selectorsEqual(selector1, selector2 ModernSRIOVSelector) bool {
	// Compare drivers
	if len(selector1.Drivers) != len(selector2.Drivers) {
		return false
	}

	drivers1 := make(map[string]bool)
	drivers2 := make(map[string]bool)

	for _, driver := range selector1.Drivers {
		drivers1[driver] = true
	}
	for _, driver := range selector2.Drivers {
		drivers2[driver] = true
	}

	for driver := range drivers1 {
		if !drivers2[driver] {
			return false
		}
	}

	// Compare PCI addresses
	if len(selector1.PCIAddresses) != len(selector2.PCIAddresses) {
		return false
	}

	pciAddrs1 := make(map[string]bool)
	pciAddrs2 := make(map[string]bool)

	for _, addr := range selector1.PCIAddresses {
		pciAddrs1[addr] = true
	}
	for _, addr := range selector2.PCIAddresses {
		pciAddrs2[addr] = true
	}

	for addr := range pciAddrs1 {
		if !pciAddrs2[addr] {
			return false
		}
	}

	return true
}
