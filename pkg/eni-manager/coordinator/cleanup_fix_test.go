package coordinator

import (
	"testing"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/sriov"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/test"
)

// TestSRIOVResourceTracking tests that SR-IOV resources are properly tracked and cleaned up
func TestSRIOVResourceTracking(t *testing.T) {
	// Skip if no Kubernetes cluster available
	test.SkipIfNoKubernetesCluster(t)

	// Create a test manager
	cfg := &config.ENIManagerConfig{
		NodeName:          "test-node",
		SRIOVDPConfigPath: "/tmp/test-cleanup-config.json",
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

	// Test tracking resources
	nodeENIName := "test-nodeeni-1"

	// Track some resources
	resource1 := SRIOVResourceInfo{
		PCIAddress:     "0000:00:07.0",
		ResourceName:   "sriov_kernel_1",
		ResourcePrefix: "intel.com",
		Driver:         "ena",
		NodeENIName:    nodeENIName,
	}

	resource2 := SRIOVResourceInfo{
		PCIAddress:     "0000:00:08.0",
		ResourceName:   "sriov_kernel_2",
		ResourcePrefix: "intel.com",
		Driver:         "ena",
		NodeENIName:    nodeENIName,
	}

	// Track the resources
	manager.trackSRIOVResource(nodeENIName, resource1)
	manager.trackSRIOVResource(nodeENIName, resource2)

	// Verify resources are tracked
	manager.mutex.RLock()
	trackedResources := manager.nodeENISRIOVResources[nodeENIName]
	manager.mutex.RUnlock()

	if len(trackedResources) != 2 {
		t.Errorf("Expected 2 tracked resources, got %d", len(trackedResources))
	}

	// Test finding resources for cleanup
	pciAddresses, err := manager.findSRIOVResourcesForNodeENI(nodeENIName)
	if err != nil {
		t.Errorf("Failed to find SR-IOV resources: %v", err)
	}

	if len(pciAddresses) != 2 {
		t.Errorf("Expected 2 PCI addresses for cleanup, got %d", len(pciAddresses))
	}

	expectedPCIs := map[string]bool{
		"0000:00:07.0": true,
		"0000:00:08.0": true,
	}

	for _, pci := range pciAddresses {
		if !expectedPCIs[pci] {
			t.Errorf("Unexpected PCI address in cleanup list: %s", pci)
		}
	}

	t.Logf("Successfully tracked and found %d SR-IOV resources for cleanup", len(pciAddresses))
}

// TestSRIOVResourceCleanup tests the actual cleanup process
func TestSRIOVResourceCleanup(t *testing.T) {
	// Skip if no Kubernetes cluster available
	test.SkipIfNoKubernetesCluster(t)

	// Create a test manager with SR-IOV manager
	cfg := &config.ENIManagerConfig{
		NodeName:          "test-node",
		SRIOVDPConfigPath: "/tmp/test-cleanup-config.json",
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

	nodeENIName := "test-nodeeni-cleanup"

	// Track a resource
	resource := SRIOVResourceInfo{
		PCIAddress:     "0000:00:09.0",
		ResourceName:   "sriov_test",
		ResourcePrefix: "intel.com",
		Driver:         "ena",
		NodeENIName:    nodeENIName,
	}

	manager.trackSRIOVResource(nodeENIName, resource)

	// Verify resource is tracked
	manager.mutex.RLock()
	trackedCount := len(manager.nodeENISRIOVResources[nodeENIName])
	manager.mutex.RUnlock()

	if trackedCount != 1 {
		t.Errorf("Expected 1 tracked resource before cleanup, got %d", trackedCount)
	}

	// First, add the resource to SR-IOV configuration
	update := sriov.ResourceUpdate{
		PCIAddress:     resource.PCIAddress,
		Driver:         resource.Driver,
		ResourceName:   resource.ResourceName,
		ResourcePrefix: resource.ResourcePrefix,
		Action:         "add",
	}

	err = manager.sriovManager.AddResource(update)
	if err != nil {
		t.Errorf("Failed to add resource to SR-IOV config: %v", err)
	}

	// Test cleanup
	configModified := manager.cleanupNonDPDKSRIOVResources(nodeENIName)
	if !configModified {
		t.Error("Expected cleanup to modify configuration")
	}

	// Verify resource tracking is cleaned up
	manager.mutex.RLock()
	_, exists := manager.nodeENISRIOVResources[nodeENIName]
	manager.mutex.RUnlock()

	if exists {
		t.Error("Expected resource tracking to be removed after cleanup")
	}

	t.Log("Successfully cleaned up SR-IOV resources and tracking")
}

// TestResourceTrackingDuplicates tests that duplicate resources are handled correctly
func TestResourceTrackingDuplicates(t *testing.T) {
	// Skip if no Kubernetes cluster available
	test.SkipIfNoKubernetesCluster(t)

	cfg := &config.ENIManagerConfig{
		NodeName: "test-node",
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

	nodeENIName := "test-nodeeni-duplicates"

	resource := SRIOVResourceInfo{
		PCIAddress:     "0000:00:0a.0",
		ResourceName:   "sriov_dup_test",
		ResourcePrefix: "intel.com",
		Driver:         "ena",
		NodeENIName:    nodeENIName,
	}

	// Track the same resource twice
	manager.trackSRIOVResource(nodeENIName, resource)
	manager.trackSRIOVResource(nodeENIName, resource)

	// Should only have one resource tracked
	manager.mutex.RLock()
	trackedCount := len(manager.nodeENISRIOVResources[nodeENIName])
	manager.mutex.RUnlock()

	if trackedCount != 1 {
		t.Errorf("Expected 1 tracked resource (no duplicates), got %d", trackedCount)
	}

	t.Log("Successfully handled duplicate resource tracking")
}
