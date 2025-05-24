package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestSRIOVConfigManager_ConcurrentAccess tests concurrent access to SR-IOV configuration
func TestSRIOVConfigManager_ConcurrentAccess(t *testing.T) {
	// Create a temporary directory for test files
	tempDir, err := ioutil.TempDir("", "sriov-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")
	manager := NewSRIOVConfigManager(configPath)

	// Create initial config
	initialConfig := SRIOVDPConfig{
		ResourceList: []SRIOVDeviceConfig{},
	}
	configData, _ := json.MarshalIndent(initialConfig, "", "  ")
	err = ioutil.WriteFile(configPath, configData, 0644)
	if err != nil {
		t.Fatalf("Failed to create initial config: %v", err)
	}

	// Test concurrent updates
	const numGoroutines = 10
	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			ifaceName := fmt.Sprintf("eth%d", id)
			pciAddress := fmt.Sprintf("0000:00:%02d.0", id)
			driver := "vfio-pci"
			resourceName := fmt.Sprintf("intel.com/sriov_test_%d", id)

			err := manager.updateConfig(ifaceName, pciAddress, driver, resourceName)
			if err != nil {
				errors <- fmt.Errorf("goroutine %d failed: %v", id, err)
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent access error: %v", err)
	}

	// Verify final configuration
	if err := manager.validateConfig(); err != nil {
		t.Errorf("Final configuration validation failed: %v", err)
	}
}

// TestSRIOVConfigManager_BackupRestore tests backup and restore functionality
func TestSRIOVConfigManager_BackupRestore(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "sriov-backup-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")
	manager := NewSRIOVConfigManager(configPath)

	// Create initial config
	originalConfig := SRIOVDPConfig{
		ResourceList: []SRIOVDeviceConfig{
			{
				ResourceName: "intel.com/original",
				DeviceType:   "netdevice",
				Devices: []PCIDeviceInfo{
					{
						PCIAddress: "0000:00:01.0",
						Driver:     "vfio-pci",
					},
				},
			},
		},
	}

	configData, _ := json.MarshalIndent(originalConfig, "", "  ")
	err = ioutil.WriteFile(configPath, configData, 0644)
	if err != nil {
		t.Fatalf("Failed to create initial config: %v", err)
	}

	// Create backup
	if err := manager.createBackup(); err != nil {
		t.Fatalf("Failed to create backup: %v", err)
	}

	// Modify config
	err = manager.updateConfig("eth1", "0000:00:02.0", "vfio-pci", "intel.com/modified")
	if err != nil {
		t.Fatalf("Failed to update config: %v", err)
	}

	// Verify modification - read as SRIOVDPConfig
	modConfigData, err := ioutil.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read modified config: %v", err)
	}

	var config SRIOVDPConfig
	if err := json.Unmarshal(modConfigData, &config); err != nil {
		t.Fatalf("Failed to parse modified config: %v", err)
	}

	if len(config.ResourceList) != 2 {
		t.Errorf("Expected 2 resources after modification, got %d", len(config.ResourceList))
	}

	// Restore backup
	if err := manager.restoreBackup(); err != nil {
		t.Fatalf("Failed to restore backup: %v", err)
	}

	// Verify restoration
	restoredConfigData, err := ioutil.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read restored config: %v", err)
	}

	var restoredConfig SRIOVDPConfig
	if err := json.Unmarshal(restoredConfigData, &restoredConfig); err != nil {
		t.Fatalf("Failed to parse restored config: %v", err)
	}

	if len(restoredConfig.ResourceList) != 1 {
		t.Errorf("Expected 1 resource after restoration, got %d", len(restoredConfig.ResourceList))
	}

	if restoredConfig.ResourceList[0].ResourceName != "intel.com/original" {
		t.Errorf("Expected original resource name, got %s", restoredConfig.ResourceList[0].ResourceName)
	}
}

// TestSRIOVConfigManager_ValidationErrors tests configuration validation
func TestSRIOVConfigManager_ValidationErrors(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "sriov-validation-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.json")
	manager := NewSRIOVConfigManager(configPath)

	testCases := []struct {
		name          string
		config        interface{}
		expectedError bool
		errorContains string
	}{
		{
			name: "empty resource list",
			config: SRIOVDPConfig{
				ResourceList: []SRIOVDeviceConfig{},
			},
			expectedError: true,
			errorContains: "no resources defined",
		},
		{
			name: "empty resource name",
			config: SRIOVDPConfig{
				ResourceList: []SRIOVDeviceConfig{
					{
						ResourceName: "",
						DeviceType:   "netdevice",
						Devices: []PCIDeviceInfo{
							{
								PCIAddress: "0000:00:01.0",
								Driver:     "vfio-pci",
							},
						},
					},
				},
			},
			expectedError: true,
			errorContains: "empty resource name",
		},
		{
			name: "invalid PCI address",
			config: SRIOVDPConfig{
				ResourceList: []SRIOVDeviceConfig{
					{
						ResourceName: "intel.com/test",
						DeviceType:   "netdevice",
						Devices: []PCIDeviceInfo{
							{
								PCIAddress: "invalid-pci",
								Driver:     "vfio-pci",
							},
						},
					},
				},
			},
			expectedError: true,
			errorContains: "invalid PCI address format",
		},
		{
			name: "valid configuration",
			config: SRIOVDPConfig{
				ResourceList: []SRIOVDeviceConfig{
					{
						ResourceName: "intel.com/test",
						DeviceType:   "netdevice",
						Devices: []PCIDeviceInfo{
							{
								PCIAddress: "0000:00:01.0",
								Driver:     "vfio-pci",
							},
						},
					},
				},
			},
			expectedError: false,
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

			// Validate
			err = manager.validateConfig()

			if tc.expectedError {
				if err == nil {
					t.Errorf("Expected validation error but got none")
				} else if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
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

// TestValidateSRIOVResourceName tests resource name validation
func TestValidateSRIOVResourceName(t *testing.T) {
	testCases := []struct {
		name          string
		resourceName  string
		expectedError bool
		errorContains string
	}{
		{
			name:          "empty name",
			resourceName:  "",
			expectedError: true,
			errorContains: "cannot be empty",
		},
		{
			name:          "invalid format - no slash",
			resourceName:  "intel.com",
			expectedError: true,
			errorContains: "must be in format 'domain/resource'",
		},
		{
			name:          "invalid format - multiple slashes",
			resourceName:  "intel.com/sriov/test",
			expectedError: true,
			errorContains: "must be in format 'domain/resource'",
		},
		{
			name:          "reserved prefix kubernetes.io",
			resourceName:  "kubernetes.io/test",
			expectedError: true,
			errorContains: "reserved domain prefix",
		},
		{
			name:          "reserved prefix k8s.io",
			resourceName:  "k8s.io/test",
			expectedError: true,
			errorContains: "reserved domain prefix",
		},
		{
			name:          "valid intel resource",
			resourceName:  "intel.com/sriov_dpdk",
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
				} else if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
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
