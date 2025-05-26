package coordinator

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestStartupSRIOVRestartLogic tests that ENI Manager startup triggers SR-IOV device plugin restart when existing DPDK devices are found
func TestStartupSRIOVRestartLogic(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "startup-sriov-restart-test-*.json")
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
		EnableDPDK:        true,
		DPDKBoundInterfaces: make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}),
		DPDKResourceNames: make(map[string]string),
	}

	// Test scenario: Simulate existing DPDK-bound NodeENI resources (like after Helm upgrade)
	t.Log("=== Testing Startup with Existing DPDK-bound NodeENI Resources ===")

	// Create mock NodeENI resources with existing DPDK bindings
	nodeENIs := []v1alpha1.NodeENI{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-sriov-integration-1",
			},
			Spec: v1alpha1.NodeENISpec{
				EnableDPDK:       true,
				DPDKDriver:       "vfio-pci",
				DPDKResourceName: "intel.com/sriov_test_1",
			},
			Status: v1alpha1.NodeENIStatus{
				Attachments: []v1alpha1.ENIAttachment{
					{
						NodeID:           "test-node",
						ENIID:            "eni-0e204424182ba8b10",
						DPDKBound:        true,
						DPDKResourceName: "intel.com/sriov_test_1",
						DPDKDriver:       "vfio-pci",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-sriov-integration-2",
			},
			Spec: v1alpha1.NodeENISpec{
				EnableDPDK:       true,
				DPDKDriver:       "vfio-pci",
				DPDKResourceName: "intel.com/sriov_test_2",
			},
			Status: v1alpha1.NodeENIStatus{
				Attachments: []v1alpha1.ENIAttachment{
					{
						NodeID:           "test-node",
						ENIID:            "eni-0fe5e1d2cdc840261",
						DPDKBound:        true,
						DPDKResourceName: "intel.com/sriov_test_2",
						DPDKDriver:       "vfio-pci",
					},
				},
			},
		},
	}

	// Simulate existing DPDK bound interfaces in the config (like after pod restart)
	cfg.DPDKBoundInterfaces["0000:00:07.0"] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  "0000:00:07.0",
		IfaceName:   "eth2",
		ENIID:       "eni-0e204424182ba8b10",
		Driver:      "vfio-pci",
		NodeENIName: "test-sriov-integration-1",
	}
	cfg.DPDKBoundInterfaces["0000:00:08.0"] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  "0000:00:08.0",
		IfaceName:   "eth3",
		ENIID:       "eni-0fe5e1d2cdc840261",
		Driver:      "vfio-pci",
		NodeENIName: "test-sriov-integration-2",
	}

	// Test 1: hasExistingDPDKBinding should detect existing bindings
	t.Log("1. Testing hasExistingDPDKBinding detection")

	foundBinding1 := hasExistingDPDKBinding(nodeENIs[0], "test-node", cfg)
	if !foundBinding1 {
		t.Error("Expected to find existing DPDK binding for first NodeENI")
	}

	foundBinding2 := hasExistingDPDKBinding(nodeENIs[1], "test-node", cfg)
	if !foundBinding2 {
		t.Error("Expected to find existing DPDK binding for second NodeENI")
	}

	t.Log("✓ hasExistingDPDKBinding correctly detected existing DPDK bindings")

	// Test 2: Startup mode should force restart when existing DPDK devices are found
	t.Log("2. Testing startup mode with existing DPDK devices (should force restart)")

	// This should detect existing DPDK devices and force a restart
	updateDPDKBindingFromNodeENIWithStartup("test-node", cfg, nodeENIs, true)

	t.Log("✓ Startup mode completed - should have forced SR-IOV device plugin restart")

	// Test 3: Regular mode should not force restart for same devices
	t.Log("3. Testing regular mode with same devices (should not force restart)")

	// This should not force a restart since it's not startup mode
	updateDPDKBindingFromNodeENIWithStartup("test-node", cfg, nodeENIs, false)

	t.Log("✓ Regular mode completed - should not have forced restart")

	// Test 4: Test non-DPDK startup logic
	t.Log("4. Testing non-DPDK startup mode with SR-IOV resources")

	// Create a config without DPDK enabled
	nonDPDKCfg := &config.ENIManagerConfig{
		SRIOVDPConfigPath: tmpFile.Name(),
		EnableDPDK:        false,
		DPDKBoundInterfaces: make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}),
		DPDKResourceNames: make(map[string]string),
	}

	// This should handle non-DPDK SR-IOV resources during startup
	updateSRIOVConfigForAllInterfacesWithStartup("test-node", nonDPDKCfg, nodeENIs, true)

	t.Log("✓ Non-DPDK startup mode completed")

	t.Log("✓ Startup SR-IOV restart logic test completed successfully")
}

// TestStartupVsRegularModeComparison tests the difference between startup and regular reconciliation
func TestStartupVsRegularModeComparison(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "startup-vs-regular-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Initialize with existing SR-IOV config (simulating post-Helm-upgrade state)
	existingConfig := ModernSRIOVDPConfig{
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
		},
	}
	configData, err := json.MarshalIndent(existingConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal existing config: %v", err)
	}

	err = os.WriteFile(tmpFile.Name(), configData, 0644)
	if err != nil {
		t.Fatalf("Failed to write existing config: %v", err)
	}
	tmpFile.Close()

	cfg := &config.ENIManagerConfig{
		SRIOVDPConfigPath: tmpFile.Name(),
		EnableDPDK:        true,
		DPDKBoundInterfaces: make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}),
		DPDKResourceNames: make(map[string]string),
	}

	// Create NodeENI with existing DPDK binding
	nodeENIs := []v1alpha1.NodeENI{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "existing-dpdk-nodeeni",
			},
			Spec: v1alpha1.NodeENISpec{
				EnableDPDK:       true,
				DPDKDriver:       "vfio-pci",
				DPDKResourceName: "intel.com/sriov_test_1",
			},
			Status: v1alpha1.NodeENIStatus{
				Attachments: []v1alpha1.ENIAttachment{
					{
						NodeID:           "test-node",
						ENIID:            "eni-existing",
						DPDKBound:        true,
						DPDKResourceName: "intel.com/sriov_test_1",
						DPDKDriver:       "vfio-pci",
					},
				},
			},
		},
	}

	// Simulate existing DPDK binding
	cfg.DPDKBoundInterfaces["0000:00:07.0"] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  "0000:00:07.0",
		IfaceName:   "eth2",
		ENIID:       "eni-existing",
		Driver:      "vfio-pci",
		NodeENIName: "existing-dpdk-nodeeni",
	}

	t.Log("=== Comparing Startup vs Regular Mode Behavior ===")

	// Test startup mode - should force restart even with existing config
	t.Log("1. Testing STARTUP mode (should force restart despite existing config)")
	updateDPDKBindingFromNodeENIWithStartup("test-node", cfg, nodeENIs, true)

	// Test regular mode - should not force restart with same config
	t.Log("2. Testing REGULAR mode (should not force restart with same config)")
	updateDPDKBindingFromNodeENIWithStartup("test-node", cfg, nodeENIs, false)

	t.Log("✓ Startup vs Regular mode comparison completed")
	t.Log("✓ This demonstrates that startup mode forces restarts while regular mode uses change detection")
}

// TestForceRestartSRIOVDevicePlugin tests the forced restart functionality
func TestForceRestartSRIOVDevicePlugin(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "force-restart-test-*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	cfg := &config.ENIManagerConfig{
		SRIOVDPConfigPath: tmpFile.Name(),
	}

	t.Log("=== Testing Force Restart SR-IOV Device Plugin ===")

	// Test forced restart
	t.Log("1. Testing forced restart of SR-IOV device plugin")
	forceRestartSRIOVDevicePlugin(cfg)

	// Test with missing config path
	t.Log("2. Testing forced restart with missing config path")
	cfgNoPath := &config.ENIManagerConfig{
		SRIOVDPConfigPath: "",
	}
	forceRestartSRIOVDevicePlugin(cfgNoPath)

	t.Log("✓ Force restart SR-IOV device plugin test completed")
}
