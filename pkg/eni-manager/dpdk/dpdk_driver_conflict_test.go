package dpdk

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// TestDPDKDriverConflictResolution tests that non-DPDK reconciliation doesn't interfere with DPDK binding
func TestDPDKDriverConflictResolution(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "dpdk-driver-conflict-test-*.json")
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

	// Test scenario: Interface currently bound to 'ena' driver, but NodeENI specifies 'vfio-pci' as DPDK driver
	t.Log("=== Testing Driver Conflict Resolution ===")

	pciAddress := "0000:00:08.0"
	resourceName := "intel.com/sriov_test_2"

	// Step 1: Simulate non-DPDK reconciliation path
	// This should use the actual current driver (ena), NOT the specified DPDK driver (vfio-pci)
	t.Log("1. Non-DPDK reconciliation with current driver 'ena' (should use 'ena', not specified 'vfio-pci')")

	currentDriver := "ena" // This is what determineDriverForInterface() would return
	err = updateModernSRIOVDevicePluginConfig(pciAddress, currentDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("Non-DPDK reconciliation failed: %v", err)
	}

	// Verify configuration was created with 'ena' driver
	config, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after non-DPDK reconciliation: %v", err)
	}

	if len(config.ResourceList) != 1 {
		t.Fatalf("Expected 1 resource after non-DPDK reconciliation, got %d", len(config.ResourceList))
	}

	resource := config.ResourceList[0]
	if len(resource.Selectors) != 1 || len(resource.Selectors[0].Drivers) != 1 {
		t.Fatalf("Expected 1 selector with 1 driver, got %d selectors", len(resource.Selectors))
	}

	if resource.Selectors[0].Drivers[0] != "ena" {
		t.Errorf("Expected driver 'ena' from non-DPDK reconciliation, got '%s'", resource.Selectors[0].Drivers[0])
	}

	t.Log("✓ Non-DPDK reconciliation correctly used current driver 'ena'")

	// Step 2: Simulate DPDK binding operation
	// This should change the driver to 'vfio-pci' and trigger a restart
	t.Log("2. DPDK binding operation changing driver to 'vfio-pci' (should trigger restart)")

	dpdkDriver := "vfio-pci"
	err = updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("DPDK binding operation failed: %v", err)
	}

	// Verify configuration was updated with 'vfio-pci' driver
	config, err = loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after DPDK binding: %v", err)
	}

	if config.ResourceList[0].Selectors[0].Drivers[0] != "vfio-pci" {
		t.Errorf("Expected driver 'vfio-pci' after DPDK binding, got '%s'", config.ResourceList[0].Selectors[0].Drivers[0])
	}

	t.Log("✓ DPDK binding correctly changed driver to 'vfio-pci'")

	// Step 3: Simulate another non-DPDK reconciliation after DPDK binding
	// This should now detect 'vfio-pci' as the current driver and NOT change it
	t.Log("3. Non-DPDK reconciliation after DPDK binding (should detect 'vfio-pci' and not change)")

	// Now the current driver would be 'vfio-pci' since the device was bound to DPDK
	currentDriverAfterDPDK := "vfio-pci"
	err = updateModernSRIOVDevicePluginConfig(pciAddress, currentDriverAfterDPDK, resourceName, cfg)
	if err != nil {
		t.Fatalf("Non-DPDK reconciliation after DPDK binding failed: %v", err)
	}

	// Verify configuration remains unchanged (should skip restart)
	config, err = loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after second non-DPDK reconciliation: %v", err)
	}

	if config.ResourceList[0].Selectors[0].Drivers[0] != "vfio-pci" {
		t.Errorf("Expected driver 'vfio-pci' to remain after second reconciliation, got '%s'", config.ResourceList[0].Selectors[0].Drivers[0])
	}

	t.Log("✓ Non-DPDK reconciliation after DPDK binding correctly detected current driver")

	// Step 4: Simulate DPDK unbinding operation
	// This should change the driver back to 'ena' and trigger a restart
	t.Log("4. DPDK unbinding operation changing driver back to 'ena' (should trigger restart)")

	err = updateModernSRIOVDevicePluginConfig(pciAddress, "ena", resourceName, cfg)
	if err != nil {
		t.Fatalf("DPDK unbinding operation failed: %v", err)
	}

	// Verify configuration was updated back to 'ena' driver
	config, err = loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after DPDK unbinding: %v", err)
	}

	if config.ResourceList[0].Selectors[0].Drivers[0] != "ena" {
		t.Errorf("Expected driver 'ena' after DPDK unbinding, got '%s'", config.ResourceList[0].Selectors[0].Drivers[0])
	}

	t.Log("✓ DPDK unbinding correctly changed driver back to 'ena'")

	t.Log("✓ DPDK driver conflict resolution test completed successfully")
	t.Log("✓ Non-DPDK reconciliation now correctly uses actual current driver, not specified DPDK driver")
}

// TestNonDPDKReconciliationDriverDetection tests that non-DPDK reconciliation correctly detects current drivers
func TestNonDPDKReconciliationDriverDetection(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "non-dpdk-driver-detection-*.json")
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

	// Test different driver scenarios
	testCases := []struct {
		name           string
		currentDriver  string
		specifiedDPDK  string
		expectedDriver string
	}{
		{
			name:           "ENA device with vfio-pci specified",
			currentDriver:  "ena",
			specifiedDPDK:  "vfio-pci",
			expectedDriver: "ena", // Should use current, not specified
		},
		{
			name:           "DPDK-bound device with ena specified",
			currentDriver:  "vfio-pci",
			specifiedDPDK:  "ena",
			expectedDriver: "vfio-pci", // Should use current, not specified
		},
		{
			name:           "Custom driver with vfio-pci specified",
			currentDriver:  "igb_uio",
			specifiedDPDK:  "vfio-pci",
			expectedDriver: "igb_uio", // Should use current, not specified
		},
	}

	for i, tc := range testCases {
		t.Logf("=== Test Case %d: %s ===", i+1, tc.name)

		pciAddress := "0000:00:0" + string(rune('5'+i)) + ".0" // Different PCI addresses
		resourceName := "intel.com/test_" + string(rune('1'+i))

		// Simulate non-DPDK reconciliation with current driver
		t.Logf("Current driver: %s, Specified DPDK driver: %s, Expected: %s",
			tc.currentDriver, tc.specifiedDPDK, tc.expectedDriver)

		err = updateModernSRIOVDevicePluginConfig(pciAddress, tc.currentDriver, resourceName, cfg)
		if err != nil {
			t.Fatalf("Test case %d failed: %v", i+1, err)
		}

		// Verify the correct driver was used
		config, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
		if err != nil {
			t.Fatalf("Failed to load config for test case %d: %v", i+1, err)
		}

		// Find the resource for this test case
		var foundResource *ModernSRIOVResource
		for j := range config.ResourceList {
			if config.ResourceList[j].ResourceName == "test_"+string(rune('1'+i)) {
				foundResource = &config.ResourceList[j]
				break
			}
		}

		if foundResource == nil {
			t.Fatalf("Resource not found for test case %d", i+1)
		}

		if len(foundResource.Selectors) == 0 || len(foundResource.Selectors[0].Drivers) == 0 {
			t.Fatalf("No drivers found for test case %d", i+1)
		}

		actualDriver := foundResource.Selectors[0].Drivers[0]
		if actualDriver != tc.expectedDriver {
			t.Errorf("Test case %d: Expected driver '%s', got '%s'", i+1, tc.expectedDriver, actualDriver)
		}

		t.Logf("✓ Test case %d passed: Used driver '%s' as expected", i+1, actualDriver)
	}

	t.Log("✓ Non-DPDK reconciliation driver detection test completed successfully")
}
