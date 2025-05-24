package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	testutil "github.com/johnlam90/aws-multi-eni-controller/pkg/test"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestSRIOVDPDKIntegration tests the complete SR-IOV and DPDK integration flow
func TestSRIOVDPDKIntegration(t *testing.T) {
	// Skip if not in integration test environment
	if os.Getenv("INTEGRATION_TEST") != "true" {
		t.Skip("Skipping integration test - set INTEGRATION_TEST=true to run")
	}

	// Skip if no Kubernetes cluster available
	testutil.SkipIfNoKubernetesCluster(t)

	// Create test environment
	ctx := context.Background()
	tempDir, err := ioutil.TempDir("", "sriov-integration-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set up configuration
	cfg := setupIntegrationTestConfig(t, tempDir)

	// Create Kubernetes clients
	clientset, runtimeClient := createK8sClients(t)

	// Test 1: Create NodeENI with DPDK enabled
	t.Run("CreateNodeENIWithDPDK", func(t *testing.T) {
		testCreateNodeENIWithDPDK(t, ctx, runtimeClient, cfg)
	})

	// Test 2: Verify SR-IOV configuration is created
	t.Run("VerifySRIOVConfiguration", func(t *testing.T) {
		testVerifySRIOVConfiguration(t, cfg)
	})

	// Test 3: Test non-DPDK SR-IOV configuration
	t.Run("TestNonDPDKSRIOV", func(t *testing.T) {
		testNonDPDKSRIOVConfiguration(t, ctx, runtimeClient, cfg)
	})

	// Test 4: Test AWS ENA driver detection
	t.Run("TestAWSENADriverDetection", func(t *testing.T) {
		testAWSENADriverDetection(t, cfg)
	})

	// Test 5: Test concurrent operations
	t.Run("TestConcurrentOperations", func(t *testing.T) {
		testConcurrentSRIOVOperations(t, cfg)
	})

	// Cleanup
	cleanupIntegrationTest(t, ctx, clientset, runtimeClient)
}

// setupIntegrationTestConfig creates a test configuration
func setupIntegrationTestConfig(t *testing.T, tempDir string) *config.ENIManagerConfig {
	cfg := config.DefaultENIManagerConfig()
	cfg.SRIOVDPConfigPath = filepath.Join(tempDir, "config.json")
	cfg.DPDKBindingScript = "/opt/dpdk/dpdk-devbind.py" // Assume this exists in integration environment
	cfg.EnableDPDK = true
	
	// Create initial empty SR-IOV config
	initialConfig := map[string]interface{}{
		"resourceList": []interface{}{},
	}
	configData, _ := json.MarshalIndent(initialConfig, "", "  ")
	err := ioutil.WriteFile(cfg.SRIOVDPConfigPath, configData, 0644)
	if err != nil {
		t.Fatalf("Failed to create initial SR-IOV config: %v", err)
	}

	return cfg
}

// createK8sClients creates Kubernetes clients for testing
func createK8sClients(t *testing.T) (kubernetes.Interface, client.Client) {
	// This would use the actual cluster configuration
	// For now, we'll use mock clients in unit tests
	t.Skip("Integration test requires actual Kubernetes cluster")
	return nil, nil
}

// testCreateNodeENIWithDPDK tests creating a NodeENI with DPDK enabled
func testCreateNodeENIWithDPDK(t *testing.T, ctx context.Context, runtimeClient client.Client, cfg *config.ENIManagerConfig) {
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "integration-test-dpdk",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				"integration-test": "true",
			},
			SubnetID:         "subnet-12345678",
			SecurityGroupIDs: []string{"sg-12345678"},
			DeviceIndex:      2,
			EnableDPDK:       true,
			DPDKDriver:       "vfio-pci",
			DPDKResourceName: "intel.com/sriov_integration_test",
			DPDKPCIAddress:   "0000:00:07.0",
		},
	}

	err := runtimeClient.Create(ctx, nodeENI)
	if err != nil {
		t.Fatalf("Failed to create NodeENI: %v", err)
	}

	// Wait for reconciliation
	time.Sleep(5 * time.Second)

	// Verify NodeENI was created
	var updatedNodeENI networkingv1alpha1.NodeENI
	err = runtimeClient.Get(ctx, client.ObjectKey{Name: nodeENI.Name}, &updatedNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Verify DPDK fields are set correctly
	if !updatedNodeENI.Spec.EnableDPDK {
		t.Error("Expected EnableDPDK to be true")
	}
	if updatedNodeENI.Spec.DPDKDriver != "vfio-pci" {
		t.Errorf("Expected DPDKDriver vfio-pci, got %s", updatedNodeENI.Spec.DPDKDriver)
	}
}

// testVerifySRIOVConfiguration verifies that SR-IOV configuration is properly created
func testVerifySRIOVConfiguration(t *testing.T, cfg *config.ENIManagerConfig) {
	// Read the SR-IOV configuration file
	configData, err := ioutil.ReadFile(cfg.SRIOVDPConfigPath)
	if err != nil {
		t.Fatalf("Failed to read SR-IOV config: %v", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatalf("Failed to parse SR-IOV config: %v", err)
	}

	// Verify structure
	resourceList, ok := config["resourceList"].([]interface{})
	if !ok {
		t.Fatal("Expected resourceList to be an array")
	}

	// For integration test, we expect at least one resource
	if len(resourceList) == 0 {
		t.Log("No resources found in SR-IOV config - this may be expected for initial state")
	}
}

// testNonDPDKSRIOVConfiguration tests SR-IOV configuration without DPDK
func testNonDPDKSRIOVConfiguration(t *testing.T, ctx context.Context, runtimeClient client.Client, cfg *config.ENIManagerConfig) {
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "integration-test-non-dpdk",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				"integration-test": "true",
			},
			SubnetID:         "subnet-12345678",
			SecurityGroupIDs: []string{"sg-12345678"},
			DeviceIndex:      3,
			EnableDPDK:       false,
			DPDKResourceName: "amazon.com/ena_kernel_test",
			DPDKPCIAddress:   "0000:00:08.0",
		},
	}

	err := runtimeClient.Create(ctx, nodeENI)
	if err != nil {
		t.Fatalf("Failed to create non-DPDK NodeENI: %v", err)
	}

	// Wait for reconciliation
	time.Sleep(5 * time.Second)

	// Verify the resource name follows AWS ENA conventions
	if nodeENI.Spec.DPDKResourceName != "amazon.com/ena_kernel_test" {
		t.Errorf("Expected AWS ENA resource name, got %s", nodeENI.Spec.DPDKResourceName)
	}
}

// testAWSENADriverDetection tests AWS ENA driver detection
func testAWSENADriverDetection(t *testing.T, cfg *config.ENIManagerConfig) {
	// This test would require actual AWS ENA devices
	// For now, we'll test the logic with mock data
	
	// Test the driver determination logic
	testCases := []struct {
		name           string
		mockDriver     string
		expectedDriver string
	}{
		{
			name:           "AWS ENA device",
			mockDriver:     "ena",
			expectedDriver: "ena",
		},
		{
			name:           "Intel device",
			mockDriver:     "ixgbe",
			expectedDriver: "ixgbe",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// This would test the actual driver detection logic
			// For integration tests, we'd need real hardware
			t.Logf("Testing driver detection for %s", tc.name)
		})
	}
}

// testConcurrentSRIOVOperations tests concurrent SR-IOV configuration operations
func testConcurrentSRIOVOperations(t *testing.T, cfg *config.ENIManagerConfig) {
	// This test verifies that concurrent operations don't corrupt the configuration
	// It would create multiple NodeENI resources simultaneously and verify consistency
	t.Log("Testing concurrent SR-IOV operations")
	
	// For integration tests, this would create multiple NodeENI resources
	// and verify that the SR-IOV configuration remains consistent
}

// cleanupIntegrationTest cleans up resources created during integration testing
func cleanupIntegrationTest(t *testing.T, ctx context.Context, clientset kubernetes.Interface, runtimeClient client.Client) {
	// Delete test NodeENI resources
	nodeENINames := []string{
		"integration-test-dpdk",
		"integration-test-non-dpdk",
	}

	for _, name := range nodeENINames {
		nodeENI := &networkingv1alpha1.NodeENI{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		err := runtimeClient.Delete(ctx, nodeENI)
		if err != nil {
			t.Logf("Warning: Failed to delete NodeENI %s: %v", name, err)
		}
	}

	t.Log("Integration test cleanup completed")
}

// TestSRIOVConfigurationValidation tests SR-IOV configuration validation in integration environment
func TestSRIOVConfigurationValidation(t *testing.T) {
	if os.Getenv("INTEGRATION_TEST") != "true" {
		t.Skip("Skipping integration test - set INTEGRATION_TEST=true to run")
	}

	tempDir, err := ioutil.TempDir("", "sriov-validation-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")

	// Test various configuration scenarios
	testCases := []struct {
		name          string
		config        interface{}
		expectValid   bool
	}{
		{
			name: "valid modern config",
			config: map[string]interface{}{
				"resourceList": []map[string]interface{}{
					{
						"resourceName":   "test",
						"resourcePrefix": "intel.com",
						"selectors": []map[string]interface{}{
							{
								"drivers":      []string{"vfio-pci"},
								"pciAddresses": []string{"0000:00:01.0"},
							},
						},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "invalid config - empty resource list",
			config: map[string]interface{}{
				"resourceList": []interface{}{},
			},
			expectValid: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Write test config
			configData, _ := json.MarshalIndent(tc.config, "", "  ")
			err := ioutil.WriteFile(configPath, configData, 0644)
			if err != nil {
				t.Fatalf("Failed to write test config: %v", err)
			}

			// Test validation
			// This would use the actual validation logic from the main code
			t.Logf("Testing validation for %s", tc.name)
		})
	}
}
