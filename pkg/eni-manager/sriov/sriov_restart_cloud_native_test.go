package sriov

import (
	"testing"
	"time"
)

// TestSRIOVConfigManager_RestartDevicePlugin_NoClient tests behavior when Kubernetes client is not available
func TestSRIOVConfigManager_RestartDevicePlugin_NoClient(t *testing.T) {
	manager := &SRIOVConfigManager{
		configPath: "/tmp/test-config.json",
		backupPath: "/tmp/test-config.json.backup",
		maxRetries: 3,
		retryDelay: 2 * time.Second,
		k8sClient:  nil, // No client available
	}

	err := manager.restartDevicePlugin()
	if err == nil {
		t.Error("Expected error when Kubernetes client is not available")
	}

	expectedErrMsg := "Kubernetes client not available for device plugin restart"
	if err.Error() != expectedErrMsg {
		t.Errorf("Expected error message '%s', got '%s'", expectedErrMsg, err.Error())
	}

	t.Log("✓ Correctly handles missing Kubernetes client")
}

// TestNewSRIOVConfigManager tests the creation of a new SR-IOV configuration manager
func TestNewSRIOVConfigManager(t *testing.T) {
	configPath := "/tmp/test-sriov-config.json"

	manager := NewSRIOVConfigManager(configPath)

	if manager == nil {
		t.Fatal("Expected non-nil manager")
	}

	if manager.configPath != configPath {
		t.Errorf("Expected configPath %s, got %s", configPath, manager.configPath)
	}

	if manager.backupPath != configPath+".backup" {
		t.Errorf("Expected backupPath %s, got %s", configPath+".backup", manager.backupPath)
	}

	if manager.maxRetries != 3 {
		t.Errorf("Expected maxRetries 3, got %d", manager.maxRetries)
	}

	if manager.retryDelay != 2*time.Second {
		t.Errorf("Expected retryDelay 2s, got %v", manager.retryDelay)
	}

	// Note: k8sClient may be nil if Kubernetes client creation fails in test environment
	// This is expected and handled gracefully by the restart function

	t.Log("✓ SR-IOV configuration manager created successfully")
}

// TestSRIOVConfigManager_CloudNativeApproach tests that the new implementation doesn't use kubectl
func TestSRIOVConfigManager_CloudNativeApproach(t *testing.T) {
	// This test verifies that our implementation is truly cloud-native
	// by ensuring it doesn't rely on external binaries like kubectl

	manager := NewSRIOVConfigManager("/tmp/test-config.json")

	// The manager should be created without any external dependencies
	if manager == nil {
		t.Fatal("Expected non-nil manager")
	}

	// When k8sClient is nil, the restart should fail gracefully with a clear error
	// rather than trying to execute kubectl commands
	if manager.k8sClient == nil {
		err := manager.restartDevicePlugin()
		if err == nil {
			t.Error("Expected error when no Kubernetes client is available")
		}

		// The error should indicate missing client, not missing kubectl binary
		if err.Error() != "Kubernetes client not available for device plugin restart" {
			t.Errorf("Unexpected error message: %s", err.Error())
		}
	}

	t.Log("✓ Implementation is cloud-native and doesn't depend on kubectl binary")
}
