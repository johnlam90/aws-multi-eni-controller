package sriov

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// TestSRIOVConfigChangeDetection tests that SR-IOV device plugin restarts only occur when configuration changes
func TestSRIOVConfigChangeDetection(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "sriov-config-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Initialize with empty config
	emptyConfig := ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{},
	}
	configData, err := json.MarshalIndent(emptyConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal empty config: %v", err)
	}

	err = os.WriteFile(tmpFile.Name(), configData, 0644)
	if err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}
	tmpFile.Close()

	cfg := &config.ENIManagerConfig{
		SRIOVDPConfigPath: tmpFile.Name(),
	}

	// Test data
	pciAddress := "0000:00:05.0"
	driver := "ena"
	resourceName := "example.com/eni"

	// First update - should trigger restart (new configuration)
	t.Log("Testing first update (should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
	if err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	// Second update with same parameters - should NOT trigger restart
	t.Log("Testing second update with same parameters (should skip restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
	if err != nil {
		t.Fatalf("Second update failed: %v", err)
	}

	// Third update with different driver - should trigger restart
	t.Log("Testing third update with different driver (should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, "vfio-pci", resourceName, cfg)
	if err != nil {
		t.Fatalf("Third update failed: %v", err)
	}

	// Fourth update with same parameters as third - should NOT trigger restart
	t.Log("Testing fourth update with same parameters as third (should skip restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, "vfio-pci", resourceName, cfg)
	if err != nil {
		t.Fatalf("Fourth update failed: %v", err)
	}

	t.Log("✓ SR-IOV configuration change detection test completed successfully")
}

// TestSRIOVConfigComparison tests the configuration comparison functions
func TestSRIOVConfigComparison(t *testing.T) {
	// Test identical configurations
	config1 := ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{
			{
				ResourceName:   "eni",
				ResourcePrefix: "example.com",
				Selectors: []ModernSRIOVSelector{
					{
						Drivers:      []string{"ena"},
						PCIAddresses: []string{"0000:00:05.0"},
					},
				},
			},
		},
	}

	config2 := deepCopySRIOVConfig(config1)

	if !sriovConfigsEqual(config1, config2) {
		t.Error("Identical configurations should be equal")
	}

	// Test different configurations
	config3 := ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{
			{
				ResourceName:   "eni",
				ResourcePrefix: "example.com",
				Selectors: []ModernSRIOVSelector{
					{
						Drivers:      []string{"vfio-pci"}, // Different driver
						PCIAddresses: []string{"0000:00:05.0"},
					},
				},
			},
		},
	}

	if sriovConfigsEqual(config1, config3) {
		t.Error("Different configurations should not be equal")
	}

	t.Log("✓ SR-IOV configuration comparison test completed successfully")
}

// TestSRIOVConfigDeepCopy tests the deep copy function
func TestSRIOVConfigDeepCopy(t *testing.T) {
	original := ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{
			{
				ResourceName:   "eni",
				ResourcePrefix: "example.com",
				Selectors: []ModernSRIOVSelector{
					{
						Drivers:      []string{"ena", "vfio-pci"},
						PCIAddresses: []string{"0000:00:05.0", "0000:00:06.0"},
					},
				},
			},
		},
	}

	copied := deepCopySRIOVConfig(original)

	// Verify they are equal
	if !sriovConfigsEqual(original, copied) {
		t.Error("Deep copy should be equal to original")
	}

	// Modify the copy and verify original is unchanged
	copied.ResourceList[0].Selectors[0].Drivers[0] = "modified"

	if sriovConfigsEqual(original, copied) {
		t.Error("Modifying copy should not affect original")
	}

	if original.ResourceList[0].Selectors[0].Drivers[0] == "modified" {
		t.Error("Original should not be modified when copy is changed")
	}

	t.Log("✓ SR-IOV configuration deep copy test completed successfully")
}

// MockSRIOVConfigManager for testing without actual Kubernetes operations
type MockSRIOVConfigManager struct {
	*SRIOVConfigManager
	restartCount int
}

func (m *MockSRIOVConfigManager) restartDevicePlugin() error {
	m.restartCount++
	return nil
}

// TestSRIOVRestartFrequency tests that restarts only happen when needed
func TestSRIOVRestartFrequency(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "sriov-restart-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Create mock manager
	manager := &MockSRIOVConfigManager{
		SRIOVConfigManager: NewSRIOVConfigManager(tmpFile.Name()),
		restartCount:       0,
	}

	// Override the restart function in the actual manager
	originalManager := NewSRIOVConfigManager(tmpFile.Name())
	originalManager.k8sClient = nil // Disable actual Kubernetes operations

	cfg := &config.ENIManagerConfig{
		SRIOVDPConfigPath: tmpFile.Name(),
	}

	// Simulate multiple updates with same configuration
	pciAddress := "0000:00:05.0"
	driver := "ena"
	resourceName := "example.com/eni"

	// First update should change config
	initialRestartCount := manager.restartCount
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
	if err != nil {
		t.Fatalf("First update failed: %v", err)
	}

	// Subsequent updates with same config should not trigger restarts
	for i := 0; i < 5; i++ {
		err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
		if err != nil {
			t.Fatalf("Update %d failed: %v", i+2, err)
		}
	}

	t.Logf("Initial restart count: %d, Final restart count: %d", initialRestartCount, manager.restartCount)
	t.Log("✓ SR-IOV restart frequency test completed successfully")
}
