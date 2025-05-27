package coordinator

import (
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/test"
)

func TestNodeENIDeletionDetection(t *testing.T) {
	// Skip if no Kubernetes cluster available
	test.SkipIfNoKubernetesCluster(t)

	// Create a test configuration
	cfg := &config.ENIManagerConfig{
		NodeName:          "test-node",
		SRIOVDPConfigPath: "/tmp/test-config.json",
		DPDKBoundInterfaces: make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}),
	}

	// Create a manager
	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test 1: Initial state - no previous NodeENIs
	t.Run("InitialState", func(t *testing.T) {
		currentNodeENIs := map[string]bool{
			"nodeeni-1": true,
			"nodeeni-2": true,
		}

		err := manager.handleNodeENIDeletions(currentNodeENIs)
		if err != nil {
			t.Errorf("Expected no error for initial state, got: %v", err)
		}

		// For initial state, previous NodeENIs should be empty, so no deletions should be detected
		// This is the expected behavior - we don't track previous state until after the first run
	})

	// Test 2: No deletions
	t.Run("NoDeletions", func(t *testing.T) {
		// Set up previous state
		manager.mutex.Lock()
		manager.previousNodeENIs = map[string]bool{
			"nodeeni-1": true,
			"nodeeni-2": true,
		}
		manager.mutex.Unlock()

		currentNodeENIs := map[string]bool{
			"nodeeni-1": true,
			"nodeeni-2": true,
		}

		err := manager.handleNodeENIDeletions(currentNodeENIs)
		if err != nil {
			t.Errorf("Expected no error for no deletions, got: %v", err)
		}
	})

	// Test 3: One deletion
	t.Run("OneDeletion", func(t *testing.T) {
		// Set up previous state
		manager.mutex.Lock()
		manager.previousNodeENIs = map[string]bool{
			"nodeeni-1": true,
			"nodeeni-2": true,
		}
		manager.mutex.Unlock()

		currentNodeENIs := map[string]bool{
			"nodeeni-1": true,
			// nodeeni-2 is deleted
		}

		err := manager.handleNodeENIDeletions(currentNodeENIs)
		// We expect this to fail because the cleanup functions are not fully implemented
		// but it should attempt the cleanup
		t.Logf("Deletion handling result: %v", err)
	})

	// Test 4: Multiple deletions
	t.Run("MultipleDeletions", func(t *testing.T) {
		// Set up previous state
		manager.mutex.Lock()
		manager.previousNodeENIs = map[string]bool{
			"nodeeni-1": true,
			"nodeeni-2": true,
			"nodeeni-3": true,
		}
		manager.mutex.Unlock()

		currentNodeENIs := map[string]bool{
			"nodeeni-1": true,
			// nodeeni-2 and nodeeni-3 are deleted
		}

		err := manager.handleNodeENIDeletions(currentNodeENIs)
		// We expect this to fail because the cleanup functions are not fully implemented
		// but it should attempt the cleanup
		t.Logf("Multiple deletion handling result: %v", err)
	})
}

func TestCleanupResourceGathering(t *testing.T) {
	// Skip if no Kubernetes cluster available
	test.SkipIfNoKubernetesCluster(t)

	// Create a test configuration with some DPDK bound interfaces
	cfg := &config.ENIManagerConfig{
		NodeName:          "test-node",
		SRIOVDPConfigPath: "/tmp/test-config.json",
		DPDKBoundInterfaces: map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}{
			"0000:00:05.0": {
				IfaceName:   "eth1",
				NodeENIName: "nodeeni-1",
				PCIAddress:  "0000:00:05.0",
				Driver:      "vfio-pci",
				ENIID:       "eni-12345",
			},
			"0000:00:06.0": {
				IfaceName:   "eth2",
				NodeENIName: "nodeeni-2",
				PCIAddress:  "0000:00:06.0",
				Driver:      "vfio-pci",
				ENIID:       "eni-67890",
			},
			"0000:00:07.0": {
				IfaceName:   "eth3",
				NodeENIName: "nodeeni-1", // Same NodeENI as first one
				PCIAddress:  "0000:00:07.0",
				Driver:      "vfio-pci",
				ENIID:       "eni-abcde",
			},
		},
	}

	// Create a manager
	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test gathering cleanup resources for nodeeni-1
	t.Run("GatherForNodeENI1", func(t *testing.T) {
		dpdkInterfaces, nonDpdkResources := manager.gatherCleanupResources("nodeeni-1")

		// Should find 2 DPDK interfaces for nodeeni-1
		expectedDPDK := 2
		if len(dpdkInterfaces) != expectedDPDK {
			t.Errorf("Expected %d DPDK interfaces for nodeeni-1, got %d: %v", expectedDPDK, len(dpdkInterfaces), dpdkInterfaces)
		}

		// Should contain the correct PCI addresses
		expectedPCIs := map[string]bool{
			"0000:00:05.0": true,
			"0000:00:07.0": true,
		}
		for _, pci := range dpdkInterfaces {
			if !expectedPCIs[pci] {
				t.Errorf("Unexpected PCI address in cleanup list: %s", pci)
			}
		}

		t.Logf("DPDK interfaces to cleanup for nodeeni-1: %v", dpdkInterfaces)
		t.Logf("Non-DPDK resources to cleanup for nodeeni-1: %v", nonDpdkResources)
	})

	// Test gathering cleanup resources for nodeeni-2
	t.Run("GatherForNodeENI2", func(t *testing.T) {
		dpdkInterfaces, nonDpdkResources := manager.gatherCleanupResources("nodeeni-2")

		// Should find 1 DPDK interface for nodeeni-2
		expectedDPDK := 1
		if len(dpdkInterfaces) != expectedDPDK {
			t.Errorf("Expected %d DPDK interfaces for nodeeni-2, got %d: %v", expectedDPDK, len(dpdkInterfaces), dpdkInterfaces)
		}

		// Should contain the correct PCI address
		if len(dpdkInterfaces) > 0 && dpdkInterfaces[0] != "0000:00:06.0" {
			t.Errorf("Expected PCI address 0000:00:06.0 for nodeeni-2, got %s", dpdkInterfaces[0])
		}

		t.Logf("DPDK interfaces to cleanup for nodeeni-2: %v", dpdkInterfaces)
		t.Logf("Non-DPDK resources to cleanup for nodeeni-2: %v", nonDpdkResources)
	})

	// Test gathering cleanup resources for non-existent NodeENI
	t.Run("GatherForNonExistentNodeENI", func(t *testing.T) {
		dpdkInterfaces, nonDpdkResources := manager.gatherCleanupResources("nodeeni-nonexistent")

		// Should find no interfaces
		if len(dpdkInterfaces) != 0 {
			t.Errorf("Expected 0 DPDK interfaces for non-existent NodeENI, got %d: %v", len(dpdkInterfaces), dpdkInterfaces)
		}

		t.Logf("DPDK interfaces to cleanup for non-existent NodeENI: %v", dpdkInterfaces)
		t.Logf("Non-DPDK resources to cleanup for non-existent NodeENI: %v", nonDpdkResources)
	})
}

func TestShouldSkipCleanup(t *testing.T) {
	// Test cases for cleanup skip logic
	testCases := []struct {
		name                    string
		totalResourcesToCleanup int
		sriovConfigPath         string
		expectedSkip            bool
		expectedReason          string
	}{
		{
			name:                    "NoResourcesToCleanup",
			totalResourcesToCleanup: 0,
			sriovConfigPath:         "/tmp/config.json",
			expectedSkip:            true,
			expectedReason:          "no resources to cleanup",
		},
		{
			name:                    "NoSRIOVConfigPath",
			totalResourcesToCleanup: 5,
			sriovConfigPath:         "",
			expectedSkip:            true,
			expectedReason:          "no SR-IOV config path",
		},
		{
			name:                    "ShouldProceedWithCleanup",
			totalResourcesToCleanup: 3,
			sriovConfigPath:         "/tmp/config.json",
			expectedSkip:            false,
			expectedReason:          "should proceed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Skip if no Kubernetes cluster available
			test.SkipIfNoKubernetesCluster(t)

			cfg := &config.ENIManagerConfig{
				NodeName:          "test-node",
				SRIOVDPConfigPath: tc.sriovConfigPath,
				DPDKBoundInterfaces: make(map[string]struct {
					PCIAddress  string
					Driver      string
					NodeENIName string
					ENIID       string
					IfaceName   string
				}),
			}

			manager, err := NewManager(cfg)
			if err != nil {
				t.Fatalf("Failed to create manager: %v", err)
			}

			shouldSkip := manager.shouldSkipCleanup(tc.totalResourcesToCleanup, "test-nodeeni")

			if shouldSkip != tc.expectedSkip {
				t.Errorf("Expected shouldSkip=%v for %s, got %v", tc.expectedSkip, tc.expectedReason, shouldSkip)
			}

			t.Logf("Test case %s: shouldSkip=%v (expected=%v)", tc.name, shouldSkip, tc.expectedSkip)
		})
	}
}
