package integration

import (
	"fmt"
	"os"
	"testing"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/coordinator"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/sriov"
)

// TestSRIOVResourceNameParsing tests that SR-IOV resource names are correctly parsed
// from NodeENI specifications for non-DPDK interfaces
func TestSRIOVResourceNameParsing(t *testing.T) {
	// Skip if not in integration test mode
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tests := []struct {
		name                 string
		nodeENI              networkingv1alpha1.NodeENI
		expectedResourceName string
		expectedPrefix       string
	}{
		{
			name: "Intel SR-IOV kernel resource",
			nodeENI: networkingv1alpha1.NodeENI{
				Spec: networkingv1alpha1.NodeENISpec{
					EnableDPDK:       false,
					DPDKResourceName: "intel.com/sriov_kernel_1",
					DPDKPCIAddress:   "0000:00:07.0",
				},
			},
			expectedResourceName: "sriov_kernel_1",
			expectedPrefix:       "intel.com",
		},
		{
			name: "Amazon ENA kernel resource",
			nodeENI: networkingv1alpha1.NodeENI{
				Spec: networkingv1alpha1.NodeENISpec{
					EnableDPDK:       false,
					DPDKResourceName: "amazon.com/ena_kernel_test",
					DPDKPCIAddress:   "0000:00:08.0",
				},
			},
			expectedResourceName: "ena_kernel_test",
			expectedPrefix:       "amazon.com",
		},
		{
			name: "Resource name without prefix",
			nodeENI: networkingv1alpha1.NodeENI{
				Spec: networkingv1alpha1.NodeENISpec{
					EnableDPDK:       false,
					DPDKResourceName: "sriov_test",
					DPDKPCIAddress:   "0000:00:09.0",
				},
			},
			expectedResourceName: "sriov_test",
			expectedPrefix:       "intel.com",
		},
		{
			name: "Empty resource name",
			nodeENI: networkingv1alpha1.NodeENI{
				Spec: networkingv1alpha1.NodeENISpec{
					EnableDPDK:       false,
					DPDKResourceName: "",
					DPDKPCIAddress:   "0000:00:0a.0",
				},
			},
			expectedResourceName: "sriov_net",
			expectedPrefix:       "intel.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal config for testing
			cfg := &config.ENIManagerConfig{
				NodeName:          "test-node",
				SRIOVDPConfigPath: "/tmp/test-sriov-config.json",
				DPDKBoundInterfaces: make(map[string]struct {
					PCIAddress  string
					Driver      string
					NodeENIName string
					ENIID       string
					IfaceName   string
				}),
			}

			// Create coordinator manager
			manager, err := coordinator.NewManager(cfg)
			if err != nil {
				t.Fatalf("Failed to create coordinator manager: %v", err)
			}

			// Test the parseResourceName function directly
			prefix, name := manager.ParseResourceNameForTest(tt.nodeENI.Spec.DPDKResourceName)

			if prefix != tt.expectedPrefix {
				t.Errorf("Expected prefix %q, got %q", tt.expectedPrefix, prefix)
			}

			if name != tt.expectedResourceName {
				t.Errorf("Expected resource name %q, got %q", tt.expectedResourceName, name)
			}

			t.Logf("Successfully parsed resource name %q -> prefix: %q, name: %q",
				tt.nodeENI.Spec.DPDKResourceName, prefix, name)
		})
	}
}

// TestSRIOVConfigurationGeneration tests that SR-IOV configuration is correctly generated
// for non-DPDK interfaces with the right resource names
func TestSRIOVConfigurationGeneration(t *testing.T) {
	// Skip if not in integration test mode
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create a test SR-IOV manager with a unique config file
	tempConfigPath := "/tmp/test-sriov-config-generation-" + fmt.Sprintf("%d", time.Now().UnixNano()) + ".json"
	defer os.Remove(tempConfigPath) // Clean up after test
	sriovManager := sriov.NewManager(tempConfigPath)

	// Test resource updates with different resource names
	updates := []sriov.ResourceUpdate{
		{
			PCIAddress:     "0000:00:07.0",
			Driver:         "ena",
			ResourceName:   "sriov_kernel_1",
			ResourcePrefix: "intel.com",
			Action:         "add",
		},
		{
			PCIAddress:     "0000:00:08.0",
			Driver:         "ena",
			ResourceName:   "sriov_kernel_2",
			ResourcePrefix: "intel.com",
			Action:         "add",
		},
	}

	// Apply the updates
	configChanged, err := sriovManager.ApplyBatchUpdatesWithChangeDetection(updates)
	if err != nil {
		t.Fatalf("Failed to apply SR-IOV updates: %v", err)
	}

	if !configChanged {
		t.Error("Expected configuration to change, but it didn't")
	}

	// Load the configuration and verify it contains the correct resources
	config, err := sriovManager.LoadOrCreateConfig()
	if err != nil {
		t.Fatalf("Failed to load SR-IOV config: %v", err)
	}

	// Verify we have the expected resources
	expectedResources := map[string]string{
		"intel.com/sriov_kernel_1": "0000:00:07.0",
		"intel.com/sriov_kernel_2": "0000:00:08.0",
	}

	for _, resource := range config.ResourceList {
		fullResourceName := resource.ResourcePrefix + "/" + resource.ResourceName
		expectedPCI, exists := expectedResources[fullResourceName]

		if !exists {
			t.Errorf("Unexpected resource in config: %s", fullResourceName)
			continue
		}

		// Check if the PCI address is in the selectors
		found := false
		for _, selector := range resource.Selectors {
			for _, pci := range selector.PCIAddresses {
				if pci == expectedPCI {
					found = true
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			t.Errorf("Expected PCI address %s not found in resource %s", expectedPCI, fullResourceName)
		}

		t.Logf("Verified resource %s with PCI %s", fullResourceName, expectedPCI)
		delete(expectedResources, fullResourceName)
	}

	// Check if all expected resources were found
	if len(expectedResources) > 0 {
		t.Errorf("Missing expected resources: %v", expectedResources)
	}

	t.Log("SR-IOV configuration generation test completed successfully")
}
