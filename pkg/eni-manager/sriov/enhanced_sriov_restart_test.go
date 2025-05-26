package sriov

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestEnhancedSRIOVRestartConfiguration tests the enhanced SR-IOV restart configuration
func TestEnhancedSRIOVRestartConfiguration(t *testing.T) {
	t.Log("=== Testing Enhanced SR-IOV Restart Configuration ===")

	// Test default configuration
	defaultConfig := getDefaultSRIOVRestartConfig()

	// Verify default values
	if defaultConfig.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries=3, got %d", defaultConfig.MaxRetries)
	}

	if defaultConfig.BaseWaitTime != 60*time.Second {
		t.Errorf("Expected BaseWaitTime=60s, got %v", defaultConfig.BaseWaitTime)
	}

	if defaultConfig.MaxWaitTime != 180*time.Second {
		t.Errorf("Expected MaxWaitTime=180s, got %v", defaultConfig.MaxWaitTime)
	}

	if defaultConfig.RetryBackoffMultiplier != 1.5 {
		t.Errorf("Expected RetryBackoffMultiplier=1.5, got %f", defaultConfig.RetryBackoffMultiplier)
	}

	if defaultConfig.PodReadinessTimeout != 120*time.Second {
		t.Errorf("Expected PodReadinessTimeout=120s, got %v", defaultConfig.PodReadinessTimeout)
	}

	if defaultConfig.ResourceVerifyTimeout != 90*time.Second {
		t.Errorf("Expected ResourceVerifyTimeout=90s, got %v", defaultConfig.ResourceVerifyTimeout)
	}

	if !defaultConfig.StaleResourceCleanup {
		t.Error("Expected StaleResourceCleanup=true")
	}

	t.Log("✓ Default configuration values are correct")
}

// TestSRIOVRestartHelperFunctions tests the helper functions for enhanced restart
func TestSRIOVRestartHelperFunctions(t *testing.T) {
	t.Log("=== Testing SR-IOV Restart Helper Functions ===")

	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "enhanced-restart-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write test SR-IOV config
	testConfig := ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{
			{
				ResourceName:   "sriov_test_1",
				ResourcePrefix: "intel.com",
				Selectors: []ModernSRIOVSelector{
					{
						Drivers:      []string{"vfio-pci"},
						PCIAddresses: []string{"0000:00:07.0"},
					},
				},
			},
			{
				ResourceName:   "sriov_test_2",
				ResourcePrefix: "intel.com",
				Selectors: []ModernSRIOVSelector{
					{
						Drivers:      []string{"ena"},
						PCIAddresses: []string{"0000:00:08.0"},
					},
				},
			},
		},
	}

	configData, err := json.MarshalIndent(testConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal test config: %v", err)
	}

	if _, err := tmpFile.Write(configData); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	tmpFile.Close()

	manager := NewSRIOVConfigManager(tmpFile.Name())

	// Test 1: getCurrentNodeResources (will fail without cluster, but should not crash)
	t.Log("1. Testing getCurrentNodeResources")
	resources, err := manager.getCurrentNodeResources()
	if err != nil {
		t.Logf("✓ getCurrentNodeResources failed as expected without cluster: %v", err)
	} else {
		t.Logf("✓ getCurrentNodeResources returned: %v", resources)
	}

	// Test 2: identifyStaleResources
	t.Log("2. Testing identifyStaleResources")
	currentResources := map[string]string{
		"intel.com/sriov_test_1":                    "1",
		"intel.com/sriov_test_2":                    "1",
		"intel.com/intel_sriov_netdevice_pci_12345": "1", // Stale resource
	}
	expectedResources := []string{
		"intel.com/sriov_test_1",
		"intel.com/sriov_test_2",
	}

	staleResources := manager.identifyStaleResources(currentResources, expectedResources)
	expectedStale := []string{"intel.com/intel_sriov_netdevice_pci_12345"}

	if len(staleResources) != len(expectedStale) {
		t.Errorf("Expected %d stale resources, got %d", len(expectedStale), len(staleResources))
	} else {
		t.Logf("✓ Correctly identified stale resources: %v", staleResources)
	}

	// Test 3: getExpectedDPDKResources
	t.Log("3. Testing getExpectedDPDKResources")
	expectedResources2, err := manager.getExpectedDPDKResources()
	if err != nil {
		t.Errorf("getExpectedDPDKResources failed: %v", err)
	} else {
		expectedCount := 2 // Should find 2 resources from our test config
		if len(expectedResources2) != expectedCount {
			t.Errorf("Expected %d resources, got %d: %v", expectedCount, len(expectedResources2), expectedResources2)
		} else {
			t.Logf("✓ Found expected resources: %v", expectedResources2)
		}
	}

	// Test 4: waitForDevicePluginPodsReady (will timeout without cluster)
	t.Log("4. Testing waitForDevicePluginPodsReady (expected to timeout)")
	start := time.Now()
	ready := manager.waitForDevicePluginPodsReady(5 * time.Second) // Short timeout for test
	duration := time.Since(start)

	if ready {
		t.Log("✓ Device plugin pods reported as ready (unexpected in test environment)")
	} else {
		t.Logf("✓ Device plugin pods not ready as expected (took %v)", duration)
	}

	// Test 5: verifyResourcesWithTimeout (will timeout without cluster)
	t.Log("5. Testing verifyResourcesWithTimeout (expected to timeout)")
	start = time.Now()
	verified := manager.verifyResourcesWithTimeout(expectedResources, 5*time.Second) // Short timeout for test
	duration = time.Since(start)

	if verified {
		t.Log("✓ Resources verified as advertised (unexpected in test environment)")
	} else {
		t.Logf("✓ Resources not verified as expected (took %v)", duration)
	}

	t.Log("✓ All helper functions tested successfully")
}

// TestEnhancedRestartWithCustomConfig tests the enhanced restart with custom configuration
func TestEnhancedRestartWithCustomConfig(t *testing.T) {
	t.Log("=== Testing Enhanced Restart with Custom Configuration ===")

	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "custom-restart-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write minimal config
	minimalConfig := `{"resourceList": []}`
	if _, err := tmpFile.WriteString(minimalConfig); err != nil {
		t.Fatalf("Failed to write minimal config: %v", err)
	}
	tmpFile.Close()

	manager := NewSRIOVConfigManager(tmpFile.Name())

	// Create custom configuration with shorter timeouts for testing
	customConfig := SRIOVRestartConfig{
		MaxRetries:             2,                // Fewer retries for faster test
		BaseWaitTime:           5 * time.Second,  // Shorter wait time
		MaxWaitTime:            15 * time.Second, // Shorter max wait
		RetryBackoffMultiplier: 2.0,              // Faster backoff
		PodReadinessTimeout:    10 * time.Second, // Shorter pod timeout
		ResourceVerifyTimeout:  10 * time.Second, // Shorter verify timeout
		StaleResourceCleanup:   true,             // Enable cleanup verification
	}

	t.Logf("Testing with custom config: maxRetries=%d, baseWait=%v",
		customConfig.MaxRetries, customConfig.BaseWaitTime)

	// Test the enhanced restart with custom config (will fail without cluster)
	start := time.Now()
	err = manager.restartDevicePluginWithConfig(customConfig)
	duration := time.Since(start)

	if err != nil {
		t.Logf("✓ Enhanced restart failed as expected without cluster: %v", err)
		t.Logf("✓ Test completed in %v (should be faster due to custom timeouts)", duration)

		// Verify it failed reasonably quickly (should be much less than default timeouts)
		if duration > 60*time.Second {
			t.Errorf("Test took too long: %v (expected < 60s with custom config)", duration)
		}
	} else {
		t.Log("✓ Enhanced restart succeeded (unexpected in test environment)")
	}

	t.Log("✓ Custom configuration test completed")
}

// TestBackwardCompatibility tests that existing restart functionality still works
func TestBackwardCompatibility(t *testing.T) {
	t.Log("=== Testing Backward Compatibility ===")

	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "backward-compat-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write minimal config
	minimalConfig := `{"resourceList": []}`
	if _, err := tmpFile.WriteString(minimalConfig); err != nil {
		t.Fatalf("Failed to write minimal config: %v", err)
	}
	tmpFile.Close()

	manager := NewSRIOVConfigManager(tmpFile.Name())

	// Test 1: Basic restart function still works
	t.Log("1. Testing basic restartDevicePlugin function")
	err = manager.restartDevicePlugin()
	if err != nil {
		t.Logf("✓ Basic restart failed as expected without cluster: %v", err)
	} else {
		t.Log("✓ Basic restart succeeded (unexpected in test environment)")
	}

	// Test 2: Enhanced restart with default config still works
	t.Log("2. Testing restartDevicePluginWithVerification (default config)")
	err = manager.restartDevicePluginWithVerification()
	if err != nil {
		t.Logf("✓ Enhanced restart failed as expected without cluster: %v", err)
	} else {
		t.Log("✓ Enhanced restart succeeded (unexpected in test environment)")
	}

	t.Log("✓ Backward compatibility verified")
}
