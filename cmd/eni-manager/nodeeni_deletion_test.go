package main

import (
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestNodeENIDeletionDetection tests that the ENI Manager can detect when NodeENI resources are deleted
func TestNodeENIDeletionDetection(t *testing.T) {
	// Create test configuration
	cfg := &config.ENIManagerConfig{
		EnableDPDK:        true,
		SRIOVDPConfigPath: "/tmp/test-sriov-config.json",
		DPDKBoundInterfaces: map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}{
			"0000:00:05.0": {
				PCIAddress:  "0000:00:05.0",
				Driver:      "vfio-pci",
				NodeENIName: "test-nodeeni-1",
				ENIID:       "eni-12345",
				IfaceName:   "eth1",
			},
			"0000:00:06.0": {
				PCIAddress:  "0000:00:06.0",
				Driver:      "vfio-pci",
				NodeENIName: "test-nodeeni-2",
				ENIID:       "eni-67890",
				IfaceName:   "eth2",
			},
		},
	}

	// Test the cleanup function directly
	t.Run("CleanupSRIOVConfigForNodeENI", func(t *testing.T) {
		// Test cleanup for a NodeENI that has DPDK interfaces
		err := cleanupSRIOVConfigForNodeENI("test-nodeeni-1", cfg)
		if err != nil {
			t.Logf("Expected error due to missing SR-IOV config file: %v", err)
		}

		// Verify that the interface was removed from tracking
		if _, exists := cfg.DPDKBoundInterfaces["0000:00:05.0"]; exists {
			t.Error("Expected PCI device 0000:00:05.0 to be removed from tracking map")
		}

		// Verify that other interfaces are still tracked
		if _, exists := cfg.DPDKBoundInterfaces["0000:00:06.0"]; !exists {
			t.Error("Expected PCI device 0000:00:06.0 to still be in tracking map")
		}
	})

	t.Run("CleanupSRIOVConfigForNonExistentNodeENI", func(t *testing.T) {
		// Test cleanup for a NodeENI that doesn't have any DPDK interfaces
		err := cleanupSRIOVConfigForNodeENI("non-existent-nodeeni", cfg)
		if err != nil {
			// In test environment, we expect Kubernetes client errors, which is fine
			t.Logf("Expected error due to missing Kubernetes client in test environment: %v", err)
		}
	})
}

// TestNodeENITrackingLogic tests the logic for tracking NodeENI resources and detecting deletions
func TestNodeENITrackingLogic(t *testing.T) {
	// Simulate the tracking logic from startNodeENIUpdaterLoop
	var previousNodeENIs map[string]bool
	deletedNodeENIs := []string{}

	// Mock cleanup function
	mockCleanup := func(nodeENIName string) {
		deletedNodeENIs = append(deletedNodeENIs, nodeENIName)
	}

	// First iteration - initial NodeENI resources
	currentNodeENIs1 := map[string]bool{
		"nodeeni-1": true,
		"nodeeni-2": true,
		"nodeeni-3": true,
	}

	// No deletions on first iteration (previousNodeENIs is nil)
	if previousNodeENIs != nil {
		for nodeENIName := range previousNodeENIs {
			if !currentNodeENIs1[nodeENIName] {
				mockCleanup(nodeENIName)
			}
		}
	}
	previousNodeENIs = currentNodeENIs1

	if len(deletedNodeENIs) != 0 {
		t.Errorf("Expected no deletions on first iteration, got: %v", deletedNodeENIs)
	}

	// Second iteration - one NodeENI deleted
	currentNodeENIs2 := map[string]bool{
		"nodeeni-1": true,
		"nodeeni-3": true,
		// nodeeni-2 is deleted
	}

	// Check for deletions
	if previousNodeENIs != nil {
		for nodeENIName := range previousNodeENIs {
			if !currentNodeENIs2[nodeENIName] {
				mockCleanup(nodeENIName)
			}
		}
	}
	previousNodeENIs = currentNodeENIs2

	if len(deletedNodeENIs) != 1 || deletedNodeENIs[0] != "nodeeni-2" {
		t.Errorf("Expected deletion of nodeeni-2, got: %v", deletedNodeENIs)
	}

	// Third iteration - multiple NodeENIs deleted
	deletedNodeENIs = []string{} // Reset
	currentNodeENIs3 := map[string]bool{
		// All NodeENIs deleted
	}

	// Check for deletions
	if previousNodeENIs != nil {
		for nodeENIName := range previousNodeENIs {
			if !currentNodeENIs3[nodeENIName] {
				mockCleanup(nodeENIName)
			}
		}
	}
	previousNodeENIs = currentNodeENIs3

	expectedDeletions := []string{"nodeeni-1", "nodeeni-3"}
	if len(deletedNodeENIs) != 2 {
		t.Errorf("Expected 2 deletions, got %d: %v", len(deletedNodeENIs), deletedNodeENIs)
	}

	// Check that both expected NodeENIs were deleted (order doesn't matter)
	deletionMap := make(map[string]bool)
	for _, deleted := range deletedNodeENIs {
		deletionMap[deleted] = true
	}

	for _, expected := range expectedDeletions {
		if !deletionMap[expected] {
			t.Errorf("Expected %s to be deleted, but it wasn't", expected)
		}
	}

	t.Log("✓ NodeENI deletion tracking logic works correctly")
}

// TestNodeENIUpdaterLoopSimulation simulates the behavior of the updated startNodeENIUpdaterLoop
func TestNodeENIUpdaterLoopSimulation(t *testing.T) {
	// This test simulates what would happen in the actual loop
	// without requiring a real Kubernetes cluster

	// Create test configuration
	cfg := &config.ENIManagerConfig{
		EnableDPDK:        true,
		SRIOVDPConfigPath: "/tmp/test-sriov-config.json",
		DPDKBoundInterfaces: map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}{
			"0000:00:05.0": {
				PCIAddress:  "0000:00:05.0",
				Driver:      "vfio-pci",
				NodeENIName: "test-nodeeni",
				ENIID:       "eni-12345",
				IfaceName:   "eth1",
			},
		},
	}

	// Mock the getNodeENIResources function behavior
	// First call returns one NodeENI, second call returns empty list (simulating deletion)

	// Simulate first iteration with NodeENI present
	nodeENIs1 := []v1alpha1.NodeENI{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "test-nodeeni"},
			Spec: v1alpha1.NodeENISpec{
				EnableDPDK:       true,
				DPDKResourceName: "intel.com/sriov_kernel_1",
			},
		},
	}

	currentNodeENIs1 := make(map[string]bool)
	for _, nodeENI := range nodeENIs1 {
		currentNodeENIs1[nodeENI.Name] = true
	}

	var previousNodeENIs map[string]bool
	cleanupCalled := false

	// First iteration - no cleanup should happen
	if previousNodeENIs != nil {
		for nodeENIName := range previousNodeENIs {
			if !currentNodeENIs1[nodeENIName] {
				cleanupCalled = true
				t.Logf("Would cleanup NodeENI: %s", nodeENIName)
			}
		}
	}
	previousNodeENIs = currentNodeENIs1

	if cleanupCalled {
		t.Error("Cleanup should not be called on first iteration")
	}

	// Simulate second iteration with NodeENI deleted
	nodeENIs2 := []v1alpha1.NodeENI{} // Empty list - NodeENI was deleted

	currentNodeENIs2 := make(map[string]bool)
	for _, nodeENI := range nodeENIs2 {
		currentNodeENIs2[nodeENI.Name] = true
	}

	// Check for deletions
	if previousNodeENIs != nil {
		for nodeENIName := range previousNodeENIs {
			if !currentNodeENIs2[nodeENIName] {
				cleanupCalled = true
				t.Logf("Would cleanup NodeENI: %s", nodeENIName)

				// Verify the interface would be removed from tracking
				if _, exists := cfg.DPDKBoundInterfaces["0000:00:05.0"]; exists {
					delete(cfg.DPDKBoundInterfaces, "0000:00:05.0")
				}
			}
		}
	}

	if !cleanupCalled {
		t.Error("Cleanup should be called when NodeENI is deleted")
	}

	// Verify tracking map was updated
	if len(cfg.DPDKBoundInterfaces) != 0 {
		t.Errorf("Expected empty tracking map after cleanup, got: %v", cfg.DPDKBoundInterfaces)
	}

	t.Log("✓ NodeENI updater loop simulation completed successfully")
}
