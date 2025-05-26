package dpdk

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// TestDPDKOperationsConcurrency tests concurrent DPDK operations
func TestDPDKOperationsConcurrency(t *testing.T) {
	// Reset global state
	dpdkOperations = make(map[string]bool)

	const numGoroutines = 10
	const pciAddress = "0000:00:01.0"

	var wg sync.WaitGroup
	successCount := 0
	var successMutex sync.Mutex

	// Test that operations are properly serialized for the same PCI address
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			releaseLock, err := acquireDPDKLock(pciAddress)
			if err != nil {
				// This should not happen with the new queuing mechanism
				t.Errorf("Unexpected error acquiring lock: %v", err)
				return
			}

			// Simulate some work
			time.Sleep(10 * time.Millisecond)

			successMutex.Lock()
			successCount++
			successMutex.Unlock()

			releaseLock()
		}(i)
	}

	wg.Wait()

	// All operations should have succeeded (serialized)
	if successCount != numGoroutines {
		t.Errorf("Expected %d successful operations (serialized), got %d", numGoroutines, successCount)
	}

	// Map should be empty after all operations complete
	dpdkOpsMutex.Lock()
	mapSize := len(dpdkOperations)
	dpdkOpsMutex.Unlock()

	if mapSize != 0 {
		t.Errorf("Expected empty operations map, got size %d", mapSize)
	}
}

// TestDPDKOperationsMemoryLeak tests for memory leaks in DPDK operations tracking
func TestDPDKOperationsMemoryLeak(t *testing.T) {
	// Reset global state
	dpdkOperations = make(map[string]bool)

	// Simulate many operations
	for i := 0; i < 50; i++ {
		pciAddress := fmt.Sprintf("0000:00:%02d.0", i)

		releaseLock, err := acquireDPDKLock(pciAddress)
		if err != nil {
			t.Fatalf("Failed to acquire lock for %s: %v", pciAddress, err)
		}

		// Immediately release
		releaseLock()
	}

	// Map should be empty
	dpdkOpsMutex.Lock()
	mapSize := len(dpdkOperations)
	dpdkOpsMutex.Unlock()

	if mapSize != 0 {
		t.Errorf("Expected empty operations map after cleanup, got size %d", mapSize)
	}
}

// TestValidateSRIOVResourceNameEdgeCases tests edge cases for resource name validation
func TestValidateSRIOVResourceNameEdgeCases(t *testing.T) {
	testCases := []struct {
		name          string
		resourceName  string
		expectedError bool
		errorContains string
	}{
		{
			name:          "empty domain",
			resourceName:  "/test",
			expectedError: true,
			errorContains: "domain part cannot be empty",
		},
		{
			name:          "empty resource",
			resourceName:  "intel.com/",
			expectedError: true,
			errorContains: "resource part cannot be empty",
		},
		{
			name:          "multiple slashes",
			resourceName:  "intel.com/sriov/test/extra",
			expectedError: true,
			errorContains: "must be in format 'domain/resource'",
		},
		{
			name:          "kubernetes.io prefix",
			resourceName:  "kubernetes.io/test",
			expectedError: true,
			errorContains: "reserved domain prefix",
		},
		{
			name:          "k8s.io prefix",
			resourceName:  "k8s.io/test",
			expectedError: true,
			errorContains: "reserved domain prefix",
		},
		{
			name:          "valid long name",
			resourceName:  "very.long.domain.name.com/very_long_resource_name_with_underscores",
			expectedError: false,
		},
		{
			name:          "valid with numbers",
			resourceName:  "intel.com/sriov_dpdk_1",
			expectedError: false,
		},
		{
			name:          "valid amazon resource",
			resourceName:  "amazon.com/ena_dpdk",
			expectedError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSRIOVResourceName(tc.resourceName)

			if tc.expectedError {
				if err == nil {
					t.Errorf("Expected validation error but got none")
				} else if tc.errorContains != "" && !containsString(err.Error(), tc.errorContains) {
					t.Errorf("Expected error containing '%s', got: %v", tc.errorContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no validation error but got: %v", err)
				}
			}
		})
	}
}

// TestPCIAddressValidation tests PCI address format validation
func TestPCIAddressValidation(t *testing.T) {
	testCases := []struct {
		name     string
		address  string
		expected bool
	}{
		{
			name:     "valid address",
			address:  "0000:00:01.0",
			expected: true,
		},
		{
			name:     "valid address with hex",
			address:  "0000:0a:1f.7",
			expected: true,
		},
		{
			name:     "too short",
			address:  "0000:00:01",
			expected: false,
		},
		{
			name:     "too long",
			address:  "0000:00:01.0.extra",
			expected: false,
		},
		{
			name:     "missing colon",
			address:  "000000:01.0",
			expected: false,
		},
		{
			name:     "missing dot",
			address:  "0000:00:010",
			expected: false,
		},
		{
			name:     "wrong domain length",
			address:  "000:00:01.0",
			expected: false,
		},
		{
			name:     "wrong bus length",
			address:  "0000:0:01.0",
			expected: false,
		},
		{
			name:     "wrong device.function format",
			address:  "0000:00:1.0",
			expected: false,
		},
		{
			name:     "empty string",
			address:  "",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isValidPCIAddress(tc.address)
			if result != tc.expected {
				t.Errorf("Expected %v for address '%s', got %v", tc.expected, tc.address, result)
			}
		})
	}
}

// TestDPDKBoundInterfacesManagement tests the management of DPDK bound interfaces map
func TestDPDKBoundInterfacesManagement(t *testing.T) {
	cfg := config.DefaultENIManagerConfig()

	// Test adding interface
	pciAddress := "0000:00:01.0"
	driver := "vfio-pci"
	nodeENIName := "test-nodeeni"
	eniID := "eni-12345"
	ifaceName := "eth1"

	updateDPDKBoundInterfacesMap(pciAddress, driver, nodeENIName, eniID, ifaceName, cfg)

	// Verify the interface was added
	boundInterface, exists := cfg.DPDKBoundInterfaces[pciAddress]
	if !exists {
		t.Fatalf("Expected interface to be added to DPDK bound interfaces map")
	}

	if boundInterface.PCIAddress != pciAddress {
		t.Errorf("Expected PCI address %s, got %s", pciAddress, boundInterface.PCIAddress)
	}

	if boundInterface.Driver != driver {
		t.Errorf("Expected driver %s, got %s", driver, boundInterface.Driver)
	}

	if boundInterface.NodeENIName != nodeENIName {
		t.Errorf("Expected NodeENI name %s, got %s", nodeENIName, boundInterface.NodeENIName)
	}

	if boundInterface.ENIID != eniID {
		t.Errorf("Expected ENI ID %s, got %s", eniID, boundInterface.ENIID)
	}

	if boundInterface.IfaceName != ifaceName {
		t.Errorf("Expected interface name %s, got %s", ifaceName, boundInterface.IfaceName)
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
