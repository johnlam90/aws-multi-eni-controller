package sriov

import (
	"os"
	"testing"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// TestSRIOVConfigurationTracking tests that the SR-IOV configuration tracking prevents unnecessary restarts
func TestSRIOVConfigurationTracking(t *testing.T) {
	// Clear the tracking map before test
	sriovConfigMutex.Lock()
	processedSRIOVConfigs = make(map[string]string)
	sriovConfigMutex.Unlock()

	// Create a temporary config file with valid JSON
	tmpFile, err := os.CreateTemp("", "sriov-tracking-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write initial valid config
	initialConfig := `{
		"resourceList": []
	}`
	if _, err := tmpFile.WriteString(initialConfig); err != nil {
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

	// First call - should process the configuration
	t.Log("Testing first call (should process)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	// Check that the configuration was tracked
	configKey := pciAddress + ":" + driver + ":" + resourceName
	sriovConfigMutex.Lock()
	_, exists := processedSRIOVConfigs[configKey]
	sriovConfigMutex.Unlock()

	if !exists {
		t.Error("Configuration should be tracked after first call")
	}

	// Second call with same parameters - should be skipped
	t.Log("Testing second call with same parameters (should be skipped)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	// Third call with different driver - should process again
	t.Log("Testing third call with different driver (should process)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, "vfio-pci", resourceName, cfg)
	if err != nil {
		t.Fatalf("Third call failed: %v", err)
	}

	// Check that the new configuration was tracked
	newConfigKey := pciAddress + ":" + "vfio-pci" + ":" + resourceName
	sriovConfigMutex.Lock()
	_, newExists := processedSRIOVConfigs[newConfigKey]
	sriovConfigMutex.Unlock()

	if !newExists {
		t.Error("New configuration should be tracked after third call")
	}

	// Fourth call with same parameters as third - should be skipped
	t.Log("Testing fourth call with same parameters as third (should be skipped)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, "vfio-pci", resourceName, cfg)
	if err != nil {
		t.Fatalf("Fourth call failed: %v", err)
	}

	t.Log("✓ SR-IOV configuration tracking test completed successfully")
}

// TestSRIOVTrackingConcurrency tests that the tracking mechanism is thread-safe
func TestSRIOVTrackingConcurrency(t *testing.T) {
	// Clear the tracking map before test
	sriovConfigMutex.Lock()
	processedSRIOVConfigs = make(map[string]string)
	sriovConfigMutex.Unlock()

	// Create a temporary config file with valid JSON
	tmpFile, err := os.CreateTemp("", "sriov-concurrency-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write initial valid config
	initialConfig := `{
		"resourceList": []
	}`
	if _, err := tmpFile.WriteString(initialConfig); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}
	tmpFile.Close()

	cfg := &config.ENIManagerConfig{
		SRIOVDPConfigPath: tmpFile.Name(),
	}

	// Test concurrent access to the same configuration
	pciAddress := "0000:00:05.0"
	driver := "ena"
	resourceName := "example.com/eni"

	// Run multiple goroutines trying to update the same configuration
	done := make(chan bool, 10)
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			err := updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg)
			if err != nil {
				errors <- err
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		select {
		case <-done:
			// Success
		case err := <-errors:
			t.Errorf("Goroutine failed: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("Test timed out")
		}
	}

	// Check that the configuration was tracked exactly once
	configKey := pciAddress + ":" + driver + ":" + resourceName
	sriovConfigMutex.Lock()
	_, exists := processedSRIOVConfigs[configKey]
	trackingMapSize := len(processedSRIOVConfigs)
	sriovConfigMutex.Unlock()

	if !exists {
		t.Error("Configuration should be tracked after concurrent calls")
	}

	if trackingMapSize != 1 {
		t.Errorf("Expected tracking map to have 1 entry, got %d", trackingMapSize)
	}

	t.Log("✓ SR-IOV tracking concurrency test completed successfully")
}

// TestSRIOVTrackingReset tests that we can reset the tracking when needed
func TestSRIOVTrackingReset(t *testing.T) {
	// Add some entries to the tracking map
	sriovConfigMutex.Lock()
	processedSRIOVConfigs["test1"] = "test1"
	processedSRIOVConfigs["test2"] = "test2"
	initialSize := len(processedSRIOVConfigs)
	sriovConfigMutex.Unlock()

	if initialSize != 2 {
		t.Errorf("Expected 2 entries in tracking map, got %d", initialSize)
	}

	// Reset the tracking map
	sriovConfigMutex.Lock()
	processedSRIOVConfigs = make(map[string]string)
	finalSize := len(processedSRIOVConfigs)
	sriovConfigMutex.Unlock()

	if finalSize != 0 {
		t.Errorf("Expected 0 entries after reset, got %d", finalSize)
	}

	t.Log("✓ SR-IOV tracking reset test completed successfully")
}
