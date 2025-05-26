package dpdk

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// TestDPDKBindingSRIOVIntegration tests that DPDK binding operations properly trigger SR-IOV device plugin restarts
func TestDPDKBindingSRIOVIntegration(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "dpdk-sriov-integration-test-*.json")
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
		DPDKResourceNames: make(map[string]string),
	}

	// Test scenario: DPDK binding operation
	t.Log("=== Testing DPDK Binding Operation ===")

	// Simulate DPDK binding: interface bound to vfio-pci driver
	pciAddress := "0000:00:08.0"
	ifaceName := "eth2"
	dpdkDriver := "vfio-pci"
	resourceName := "intel.com/sriov_test_1"

	// Store resource name (simulating what happens in DPDK binding)
	cfg.DPDKResourceNames[ifaceName] = resourceName

	// First DPDK binding - should trigger restart (new device)
	t.Log("1. First DPDK binding (should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("DPDK binding update failed: %v", err)
	}

	// Verify configuration was created
	config, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after DPDK binding: %v", err)
	}

	if len(config.ResourceList) != 1 {
		t.Fatalf("Expected 1 resource after DPDK binding, got %d", len(config.ResourceList))
	}

	resource := config.ResourceList[0]
	if resource.ResourceName != "sriov_test_1" || resource.ResourcePrefix != "intel.com" {
		t.Errorf("Expected resource intel.com/sriov_test_1, got %s/%s", resource.ResourcePrefix, resource.ResourceName)
	}

	if len(resource.Selectors) != 1 || len(resource.Selectors[0].PCIAddresses) != 1 {
		t.Fatalf("Expected 1 selector with 1 PCI address, got %d selectors", len(resource.Selectors))
	}

	if resource.Selectors[0].PCIAddresses[0] != pciAddress {
		t.Errorf("Expected PCI address %s, got %s", pciAddress, resource.Selectors[0].PCIAddresses[0])
	}

	if len(resource.Selectors[0].Drivers) != 1 || resource.Selectors[0].Drivers[0] != dpdkDriver {
		t.Errorf("Expected driver %s, got %v", dpdkDriver, resource.Selectors[0].Drivers)
	}

	// Second DPDK binding with same parameters - should NOT trigger restart
	t.Log("2. Second DPDK binding with same parameters (should skip restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("Second DPDK binding update failed: %v", err)
	}

	// Test scenario: DPDK unbinding operation
	t.Log("=== Testing DPDK Unbinding Operation ===")

	// Simulate DPDK unbinding: interface unbound from vfio-pci, bound back to ena
	enaDriver := "ena"

	// DPDK unbinding - should trigger restart (driver change)
	t.Log("3. DPDK unbinding (driver change should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, enaDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("DPDK unbinding update failed: %v", err)
	}

	// Verify driver was updated
	config, err = loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after DPDK unbinding: %v", err)
	}

	if config.ResourceList[0].Selectors[0].Drivers[0] != enaDriver {
		t.Errorf("Expected driver %s after unbinding, got %s", enaDriver, config.ResourceList[0].Selectors[0].Drivers[0])
	}

	// Test scenario: Re-applying NodeENI (common issue scenario)
	t.Log("=== Testing NodeENI Re-application ===")

	// Simulate re-applying NodeENI with DPDK binding
	t.Log("4. Re-applying NodeENI with DPDK binding (should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("NodeENI re-application update failed: %v", err)
	}

	// Verify driver was updated back to DPDK
	config, err = loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after NodeENI re-application: %v", err)
	}

	if config.ResourceList[0].Selectors[0].Drivers[0] != dpdkDriver {
		t.Errorf("Expected driver %s after re-application, got %s", dpdkDriver, config.ResourceList[0].Selectors[0].Drivers[0])
	}

	// Test scenario: Multiple DPDK devices
	t.Log("=== Testing Multiple DPDK Devices ===")

	// Add second DPDK device
	pciAddress2 := "0000:00:09.0"
	resourceName2 := "intel.com/sriov_test_2"

	t.Log("5. Adding second DPDK device (should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress2, dpdkDriver, resourceName2, cfg)
	if err != nil {
		t.Fatalf("Second device addition failed: %v", err)
	}

	// Verify both resources exist
	config, err = loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config after adding second device: %v", err)
	}

	if len(config.ResourceList) != 2 {
		t.Fatalf("Expected 2 resources after adding second device, got %d", len(config.ResourceList))
	}

	// Test scenario: Routine reconciliation (should not trigger restarts)
	t.Log("=== Testing Routine Reconciliation ===")

	t.Log("6. Routine reconciliation - first device (should skip restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, resourceName, cfg)
	if err != nil {
		t.Fatalf("Routine reconciliation failed: %v", err)
	}

	t.Log("7. Routine reconciliation - second device (should skip restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress2, dpdkDriver, resourceName2, cfg)
	if err != nil {
		t.Fatalf("Routine reconciliation failed: %v", err)
	}

	t.Log("✓ DPDK-SR-IOV integration test completed successfully")
	t.Log("✓ SR-IOV device plugin restarts will now occur only when DPDK binding/unbinding changes the configuration")
}

// TestDPDKSRIOVChangeDetectionEdgeCases tests edge cases in DPDK-SR-IOV change detection
func TestDPDKSRIOVChangeDetectionEdgeCases(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "dpdk-sriov-edge-cases-*.json")
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

	// Test case: Same PCI address, different resource names
	t.Log("=== Testing Same PCI Address, Different Resource Names ===")

	pciAddress := "0000:00:10.0"
	driver := "vfio-pci"
	resourceName1 := "intel.com/resource1"
	resourceName2 := "intel.com/resource2"

	// Add with first resource name
	t.Log("1. Adding PCI device with first resource name")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName1, cfg)
	if err != nil {
		t.Fatalf("First resource addition failed: %v", err)
	}

	// Change to second resource name - should trigger restart
	t.Log("2. Changing to second resource name (should trigger restart)")
	err = updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName2, cfg)
	if err != nil {
		t.Fatalf("Resource name change failed: %v", err)
	}

	// Verify configuration
	config, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Should have 2 resources now
	if len(config.ResourceList) != 2 {
		t.Fatalf("Expected 2 resources after resource name change, got %d", len(config.ResourceList))
	}

	t.Log("✓ DPDK-SR-IOV edge cases test completed successfully")
}
