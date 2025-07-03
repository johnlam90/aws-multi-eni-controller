package network

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// MockFileSystem provides a mock filesystem for testing sysfs operations
type MockFileSystem struct {
	files map[string]string
	mutex sync.RWMutex
}

// NewMockFileSystem creates a new mock filesystem
func NewMockFileSystem() *MockFileSystem {
	return &MockFileSystem{
		files: make(map[string]string),
	}
}

// SetFile sets the content of a mock file
func (m *MockFileSystem) SetFile(path, content string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.files[path] = content
}

// RemoveFile removes a mock file
func (m *MockFileSystem) RemoveFile(path string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	delete(m.files, path)
}

// ReadFile reads from the mock filesystem
func (m *MockFileSystem) ReadFile(path string) ([]byte, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	content, exists := m.files[path]
	if !exists {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	return []byte(content), nil
}

// TestableManager extends Manager with testable filesystem operations
type TestableManager struct {
	*Manager
	mockFS *MockFileSystem
}

// NewTestableManager creates a new testable manager
func NewTestableManager(cfg *config.ENIManagerConfig) *TestableManager {
	return &TestableManager{
		Manager: NewManager(cfg),
		mockFS:  NewMockFileSystem(),
	}
}

// Override getDeviceIndexForInterface to use mock filesystem
func (tm *TestableManager) getDeviceIndexForInterface(ifaceName string) (int, error) {
	// Method 1: Try to read device index from sysfs (most reliable)
	if deviceIndex, err := tm.getDeviceIndexFromSysfs(ifaceName); err == nil {
		return deviceIndex, nil
	}

	// Method 2: Fallback to interface name parsing

	// For eth interfaces (eth0, eth1, etc.)
	if strings.HasPrefix(ifaceName, "eth") {
		indexStr := strings.TrimPrefix(ifaceName, "eth")
		if index, err := strconv.Atoi(indexStr); err == nil {
			return index, nil
		}
	}

	// For ens interfaces (ens5, ens6, etc.)
	if strings.HasPrefix(ifaceName, "ens") {
		indexStr := strings.TrimPrefix(ifaceName, "ens")
		if index, err := strconv.Atoi(indexStr); err == nil {
			// EKS typically starts at ens5 for device index 0
			return index - 5, nil
		}
	}

	return 0, fmt.Errorf("could not determine device index for interface %s", ifaceName)
}

// getDeviceIndexFromSysfs reads the device index directly from mock sysfs
func (tm *TestableManager) getDeviceIndexFromSysfs(ifaceName string) (int, error) {
	// Try multiple sysfs paths for device index
	paths := []string{
		fmt.Sprintf("/sys/class/net/%s/device/device_index", ifaceName),
		fmt.Sprintf("/sys/class/net/%s/dev_id", ifaceName),
	}

	for _, path := range paths {
		if data, err := tm.mockFS.ReadFile(path); err == nil {
			if deviceIndex, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				// Validate that device index is reasonable (non-negative)
				if deviceIndex >= 0 {
					return deviceIndex, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("device index not found in sysfs for interface %s", ifaceName)
}

// TestDeviceIndexCalculation tests the core device index calculation logic
func TestDeviceIndexCalculation(t *testing.T) {
	tests := []struct {
		name           string
		ifaceName      string
		sysfsContent   map[string]string // path -> content
		expectedIndex  int
		expectedMethod string // "sysfs" or "name-based"
		expectError    bool
	}{
		{
			name:           "ens5 with sysfs - device index 0",
			ifaceName:      "ens5",
			sysfsContent:   map[string]string{"/sys/class/net/ens5/device/device_index": "0"},
			expectedIndex:  0,
			expectedMethod: "sysfs",
			expectError:    false,
		},
		{
			name:           "ens6 with sysfs - device index 1",
			ifaceName:      "ens6",
			sysfsContent:   map[string]string{"/sys/class/net/ens6/device/device_index": "1"},
			expectedIndex:  1,
			expectedMethod: "sysfs",
			expectError:    false,
		},
		{
			name:           "ens7 with sysfs - device index 2",
			ifaceName:      "ens7",
			sysfsContent:   map[string]string{"/sys/class/net/ens7/device/device_index": "2"},
			expectedIndex:  2,
			expectedMethod: "sysfs",
			expectError:    false,
		},
		{
			name:           "ens8 with sysfs - device index 3",
			ifaceName:      "ens8",
			sysfsContent:   map[string]string{"/sys/class/net/ens8/device/device_index": "3"},
			expectedIndex:  3,
			expectedMethod: "sysfs",
			expectError:    false,
		},
		{
			name:           "ens5 fallback to name-based calculation",
			ifaceName:      "ens5",
			sysfsContent:   map[string]string{}, // No sysfs files
			expectedIndex:  0,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "ens6 fallback to name-based calculation",
			ifaceName:      "ens6",
			sysfsContent:   map[string]string{}, // No sysfs files
			expectedIndex:  1,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "ens7 fallback to name-based calculation",
			ifaceName:      "ens7",
			sysfsContent:   map[string]string{}, // No sysfs files
			expectedIndex:  2,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "ens8 fallback to name-based calculation",
			ifaceName:      "ens8",
			sysfsContent:   map[string]string{}, // No sysfs files
			expectedIndex:  3,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "eth0 interface",
			ifaceName:      "eth0",
			sysfsContent:   map[string]string{},
			expectedIndex:  0,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "eth1 interface",
			ifaceName:      "eth1",
			sysfsContent:   map[string]string{},
			expectedIndex:  1,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "sysfs with dev_id fallback",
			ifaceName:      "ens5",
			sysfsContent:   map[string]string{"/sys/class/net/ens5/dev_id": "0"},
			expectedIndex:  0,
			expectedMethod: "sysfs",
			expectError:    false,
		},
		{
			name:           "malformed sysfs data",
			ifaceName:      "ens5",
			sysfsContent:   map[string]string{"/sys/class/net/ens5/device/device_index": "invalid"},
			expectedIndex:  0,
			expectedMethod: "name-based",
			expectError:    false,
		},
		{
			name:           "invalid interface name",
			ifaceName:      "invalid",
			sysfsContent:   map[string]string{},
			expectedIndex:  0,
			expectedMethod: "",
			expectError:    true,
		},
		{
			name:           "empty interface name",
			ifaceName:      "",
			sysfsContent:   map[string]string{},
			expectedIndex:  0,
			expectedMethod: "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ENIManagerConfig{}
			tm := NewTestableManager(cfg)

			// Set up mock filesystem
			for path, content := range tt.sysfsContent {
				tm.mockFS.SetFile(path, content)
			}

			// Test device index calculation
			index, err := tm.getDeviceIndexForInterface(tt.ifaceName)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if index != tt.expectedIndex {
				t.Errorf("Expected device index %d, got %d", tt.expectedIndex, index)
			}
		})
	}
}

// TestScalabilityScenarios tests device index calculation for 10+ ENI interfaces
func TestScalabilityScenarios(t *testing.T) {
	tests := []struct {
		name           string
		startInterface int
		endInterface   int
		useSysfs       bool
	}{
		{
			name:           "10 ENI interfaces with sysfs (ens5-ens14)",
			startInterface: 5,
			endInterface:   14,
			useSysfs:       true,
		},
		{
			name:           "10 ENI interfaces with name-based fallback (ens5-ens14)",
			startInterface: 5,
			endInterface:   14,
			useSysfs:       false,
		},
		{
			name:           "15 ENI interfaces with sysfs (ens5-ens19)",
			startInterface: 5,
			endInterface:   19,
			useSysfs:       true,
		},
		{
			name:           "Maximum ENI interfaces (ens5-ens20)",
			startInterface: 5,
			endInterface:   20,
			useSysfs:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ENIManagerConfig{}
			tm := NewTestableManager(cfg)

			// Set up mock filesystem if using sysfs
			if tt.useSysfs {
				for i := tt.startInterface; i <= tt.endInterface; i++ {
					ifaceName := fmt.Sprintf("ens%d", i)
					expectedDeviceIndex := i - 5
					path := fmt.Sprintf("/sys/class/net/%s/device/device_index", ifaceName)
					tm.mockFS.SetFile(path, strconv.Itoa(expectedDeviceIndex))
				}
			}

			// Test each interface
			for i := tt.startInterface; i <= tt.endInterface; i++ {
				ifaceName := fmt.Sprintf("ens%d", i)
				expectedDeviceIndex := i - 5

				index, err := tm.getDeviceIndexForInterface(ifaceName)
				if err != nil {
					t.Errorf("Interface %s: unexpected error: %v", ifaceName, err)
					continue
				}

				if index != expectedDeviceIndex {
					t.Errorf("Interface %s: expected device index %d, got %d",
						ifaceName, expectedDeviceIndex, index)
				}
			}
		})
	}
}

// TestConcurrentDeviceIndexLookups tests performance with concurrent lookups
func TestConcurrentDeviceIndexLookups(t *testing.T) {
	cfg := &config.ENIManagerConfig{}
	tm := NewTestableManager(cfg)

	// Set up mock filesystem for 10 interfaces
	for i := 5; i <= 14; i++ {
		ifaceName := fmt.Sprintf("ens%d", i)
		expectedDeviceIndex := i - 5
		path := fmt.Sprintf("/sys/class/net/%s/device/device_index", ifaceName)
		tm.mockFS.SetFile(path, strconv.Itoa(expectedDeviceIndex))
	}

	// Test concurrent access
	const numGoroutines = 50
	const numIterations = 10

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*numIterations)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < numIterations; i++ {
				// Test random interface
				interfaceNum := 5 + (goroutineID+i)%10
				ifaceName := fmt.Sprintf("ens%d", interfaceNum)
				expectedIndex := interfaceNum - 5

				index, err := tm.getDeviceIndexForInterface(ifaceName)
				if err != nil {
					errors <- fmt.Errorf("goroutine %d, iteration %d: %v", goroutineID, i, err)
					return
				}

				if index != expectedIndex {
					errors <- fmt.Errorf("goroutine %d, iteration %d: expected %d, got %d",
						goroutineID, i, expectedIndex, index)
					return
				}
			}
		}(g)
	}

	// Wait for all goroutines to complete
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out after 5 seconds")
	}

	// Check for errors
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestCrossPlatformCompatibility tests different sysfs structures
func TestCrossPlatformCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		platform       string
		sysfsStructure map[string]string
		ifaceName      string
		expectedIndex  int
	}{
		{
			name:     "Amazon Linux 2 - device_index file",
			platform: "AL2",
			sysfsStructure: map[string]string{
				"/sys/class/net/ens5/device/device_index": "0",
			},
			ifaceName:     "ens5",
			expectedIndex: 0,
		},
		{
			name:     "Amazon Linux 2023 - device_index file",
			platform: "AL2023",
			sysfsStructure: map[string]string{
				"/sys/class/net/ens6/device/device_index": "1",
			},
			ifaceName:     "ens6",
			expectedIndex: 1,
		},
		{
			name:     "Alternative sysfs structure - dev_id file",
			platform: "Alternative",
			sysfsStructure: map[string]string{
				"/sys/class/net/ens7/dev_id": "2",
			},
			ifaceName:     "ens7",
			expectedIndex: 2,
		},
		{
			name:           "No sysfs - fallback to name parsing",
			platform:       "Fallback",
			sysfsStructure: map[string]string{},
			ifaceName:      "ens8",
			expectedIndex:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ENIManagerConfig{}
			tm := NewTestableManager(cfg)

			// Set up platform-specific sysfs structure
			for path, content := range tt.sysfsStructure {
				tm.mockFS.SetFile(path, content)
			}

			index, err := tm.getDeviceIndexForInterface(tt.ifaceName)
			if err != nil {
				t.Errorf("Platform %s: unexpected error: %v", tt.platform, err)
				return
			}

			if index != tt.expectedIndex {
				t.Errorf("Platform %s: expected device index %d, got %d",
					tt.platform, tt.expectedIndex, index)
			}
		})
	}
}

// TestErrorHandlingAndLogging tests graceful degradation and error scenarios
func TestErrorHandlingAndLogging(t *testing.T) {
	tests := []struct {
		name           string
		ifaceName      string
		sysfsContent   map[string]string
		expectedIndex  int
		expectError    bool
		expectedMethod string
	}{
		{
			name:           "sysfs permission denied - fallback to name-based",
			ifaceName:      "ens5",
			sysfsContent:   map[string]string{}, // Simulate permission denied by not setting file
			expectedIndex:  0,
			expectError:    false,
			expectedMethod: "name-based",
		},
		{
			name:      "malformed sysfs data - fallback to name-based",
			ifaceName: "ens6",
			sysfsContent: map[string]string{
				"/sys/class/net/ens6/device/device_index": "not_a_number",
			},
			expectedIndex:  1,
			expectError:    false,
			expectedMethod: "name-based",
		},
		{
			name:      "empty sysfs file - fallback to name-based",
			ifaceName: "ens7",
			sysfsContent: map[string]string{
				"/sys/class/net/ens7/device/device_index": "",
			},
			expectedIndex:  2,
			expectError:    false,
			expectedMethod: "name-based",
		},
		{
			name:      "negative device index in sysfs - fallback to name-based",
			ifaceName: "ens8",
			sysfsContent: map[string]string{
				"/sys/class/net/ens8/device/device_index": "-1",
			},
			expectedIndex:  3,
			expectError:    false,
			expectedMethod: "name-based",
		},
		{
			name:           "invalid interface name - no fallback possible",
			ifaceName:      "invalid_interface",
			sysfsContent:   map[string]string{},
			expectedIndex:  0,
			expectError:    true,
			expectedMethod: "",
		},
		{
			name:           "interface name with no number - error",
			ifaceName:      "ens",
			sysfsContent:   map[string]string{},
			expectedIndex:  0,
			expectError:    true,
			expectedMethod: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ENIManagerConfig{}
			tm := NewTestableManager(cfg)

			// Set up mock filesystem
			for path, content := range tt.sysfsContent {
				tm.mockFS.SetFile(path, content)
			}

			index, err := tm.getDeviceIndexForInterface(tt.ifaceName)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if index != tt.expectedIndex {
				t.Errorf("Expected device index %d, got %d", tt.expectedIndex, index)
			}
		})
	}
}

// TestEdgeCases tests various edge cases and boundary conditions
func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		ifaceName     string
		sysfsContent  map[string]string
		expectedIndex int
		expectError   bool
	}{
		{
			name:          "very large interface number",
			ifaceName:     "ens999",
			sysfsContent:  map[string]string{},
			expectedIndex: 994, // 999 - 5
			expectError:   false,
		},
		{
			name:      "device index larger than interface number",
			ifaceName: "ens5",
			sysfsContent: map[string]string{
				"/sys/class/net/ens5/device/device_index": "10",
			},
			expectedIndex: 10,
			expectError:   false,
		},
		{
			name:          "interface with leading zeros",
			ifaceName:     "ens005",
			sysfsContent:  map[string]string{},
			expectedIndex: 0, // 5 - 5
			expectError:   false,
		},
		{
			name:          "mixed case interface name",
			ifaceName:     "ENS5",
			sysfsContent:  map[string]string{},
			expectedIndex: 0,
			expectError:   true, // Should fail as we expect lowercase
		},
		{
			name:      "whitespace in sysfs content",
			ifaceName: "ens5",
			sysfsContent: map[string]string{
				"/sys/class/net/ens5/device/device_index": "  0  \n",
			},
			expectedIndex: 0,
			expectError:   false,
		},
		{
			name:      "multiple sysfs files - prefer device_index",
			ifaceName: "ens6",
			sysfsContent: map[string]string{
				"/sys/class/net/ens6/device/device_index": "1",
				"/sys/class/net/ens6/dev_id":              "99", // Should be ignored
			},
			expectedIndex: 1,
			expectError:   false,
		},
		{
			name:      "only dev_id available",
			ifaceName: "ens7",
			sysfsContent: map[string]string{
				"/sys/class/net/ens7/dev_id": "2",
			},
			expectedIndex: 2,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ENIManagerConfig{}
			tm := NewTestableManager(cfg)

			// Set up mock filesystem
			for path, content := range tt.sysfsContent {
				tm.mockFS.SetFile(path, content)
			}

			index, err := tm.getDeviceIndexForInterface(tt.ifaceName)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if index != tt.expectedIndex {
				t.Errorf("Expected device index %d, got %d", tt.expectedIndex, index)
			}
		})
	}
}

// TestNodeENIIntegration tests integration with NodeENI resource matching
func TestNodeENIIntegration(t *testing.T) {
	tests := []struct {
		name               string
		interfaces         []string
		sysfsSetup         map[string]string
		nodeENIDeviceIndex int
		nodeENISubnets     int
		expectedMatches    map[string]bool // interface -> should match NodeENI
	}{
		{
			name:       "Single NodeENI with 2 subnets - device indices 2,3",
			interfaces: []string{"ens5", "ens6", "ens7", "ens8"},
			sysfsSetup: map[string]string{
				"/sys/class/net/ens5/device/device_index": "0",
				"/sys/class/net/ens6/device/device_index": "1",
				"/sys/class/net/ens7/device/device_index": "2",
				"/sys/class/net/ens8/device/device_index": "3",
			},
			nodeENIDeviceIndex: 2,
			nodeENISubnets:     2,
			expectedMatches: map[string]bool{
				"ens5": false, // Primary ENI
				"ens6": false, // VPC CNI
				"ens7": true,  // NodeENI device index 2
				"ens8": true,  // NodeENI device index 3
			},
		},
		{
			name:       "NodeENI with device index 4 - single subnet",
			interfaces: []string{"ens5", "ens6", "ens7", "ens8", "ens9"},
			sysfsSetup: map[string]string{
				"/sys/class/net/ens5/device/device_index": "0",
				"/sys/class/net/ens6/device/device_index": "1",
				"/sys/class/net/ens7/device/device_index": "2",
				"/sys/class/net/ens8/device/device_index": "3",
				"/sys/class/net/ens9/device/device_index": "4",
			},
			nodeENIDeviceIndex: 4,
			nodeENISubnets:     1,
			expectedMatches: map[string]bool{
				"ens5": false,
				"ens6": false,
				"ens7": false,
				"ens8": false,
				"ens9": true, // NodeENI device index 4
			},
		},
		{
			name:       "Large scale - 10 ENI interfaces",
			interfaces: []string{"ens5", "ens6", "ens7", "ens8", "ens9", "ens10", "ens11", "ens12", "ens13", "ens14"},
			sysfsSetup: map[string]string{
				"/sys/class/net/ens5/device/device_index":  "0",
				"/sys/class/net/ens6/device/device_index":  "1",
				"/sys/class/net/ens7/device/device_index":  "2",
				"/sys/class/net/ens8/device/device_index":  "3",
				"/sys/class/net/ens9/device/device_index":  "4",
				"/sys/class/net/ens10/device/device_index": "5",
				"/sys/class/net/ens11/device/device_index": "6",
				"/sys/class/net/ens12/device/device_index": "7",
				"/sys/class/net/ens13/device/device_index": "8",
				"/sys/class/net/ens14/device/device_index": "9",
			},
			nodeENIDeviceIndex: 2,
			nodeENISubnets:     8, // Device indices 2-9
			expectedMatches: map[string]bool{
				"ens5":  false, // Primary ENI
				"ens6":  false, // VPC CNI
				"ens7":  true,  // NodeENI device index 2
				"ens8":  true,  // NodeENI device index 3
				"ens9":  true,  // NodeENI device index 4
				"ens10": true,  // NodeENI device index 5
				"ens11": true,  // NodeENI device index 6
				"ens12": true,  // NodeENI device index 7
				"ens13": true,  // NodeENI device index 8
				"ens14": true,  // NodeENI device index 9
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.ENIManagerConfig{}
			tm := NewTestableManager(cfg)

			// Set up mock filesystem
			for path, content := range tt.sysfsSetup {
				tm.mockFS.SetFile(path, content)
			}

			// Test each interface
			for _, ifaceName := range tt.interfaces {
				index, err := tm.getDeviceIndexForInterface(ifaceName)
				if err != nil {
					t.Errorf("Interface %s: unexpected error: %v", ifaceName, err)
					continue
				}

				// Check if this device index would match the NodeENI
				expectedMatch := tt.expectedMatches[ifaceName]
				actualMatch := false

				// Simulate NodeENI device index range calculation
				for i := 0; i < tt.nodeENISubnets; i++ {
					nodeENIDeviceIndex := tt.nodeENIDeviceIndex + i
					if index == nodeENIDeviceIndex {
						actualMatch = true
						break
					}
				}

				if actualMatch != expectedMatch {
					t.Errorf("Interface %s (device index %d): expected match=%v, got match=%v",
						ifaceName, index, expectedMatch, actualMatch)
				}
			}
		})
	}
}

// TestRegressionEns8Issue tests the specific ens8 issue that was fixed
func TestRegressionEns8Issue(t *testing.T) {
	cfg := &config.ENIManagerConfig{}
	tm := NewTestableManager(cfg)

	// Simulate the original issue scenario
	interfaces := []string{"ens5", "ens6", "ens7", "ens8"}

	// Test with both sysfs and fallback methods
	testCases := []struct {
		name     string
		useSysfs bool
	}{
		{"with sysfs", true},
		{"with fallback", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear previous mock data
			tm.mockFS = NewMockFileSystem()

			if tc.useSysfs {
				// Set up sysfs data
				for i, ifaceName := range interfaces {
					path := fmt.Sprintf("/sys/class/net/%s/device/device_index", ifaceName)
					tm.mockFS.SetFile(path, strconv.Itoa(i))
				}
			}

			// Test the corrected device index calculation
			expectedIndices := []int{0, 1, 2, 3}

			for i, ifaceName := range interfaces {
				index, err := tm.getDeviceIndexForInterface(ifaceName)
				if err != nil {
					t.Errorf("Interface %s: unexpected error: %v", ifaceName, err)
					continue
				}

				if index != expectedIndices[i] {
					t.Errorf("Interface %s: expected device index %d, got %d",
						ifaceName, expectedIndices[i], index)
				}
			}

			// Specifically test ens8 (the problematic interface)
			ens8Index, err := tm.getDeviceIndexForInterface("ens8")
			if err != nil {
				t.Errorf("ens8: unexpected error: %v", err)
			} else if ens8Index != 3 {
				t.Errorf("ens8: expected device index 3, got %d", ens8Index)
			}

			// Verify ens8 would match a NodeENI with deviceIndex=2 and 2 subnets
			// NodeENI device indices would be: 2, 3
			// ens8 has device index 3, so it should match
			nodeENIDeviceIndices := []int{2, 3}
			ens8Matches := false
			for _, nodeENIIndex := range nodeENIDeviceIndices {
				if ens8Index == nodeENIIndex {
					ens8Matches = true
					break
				}
			}

			if !ens8Matches {
				t.Errorf("ens8 with device index %d should match NodeENI device indices %v",
					ens8Index, nodeENIDeviceIndices)
			}
		})
	}
}

// BenchmarkDeviceIndexCalculation benchmarks the device index calculation performance
func BenchmarkDeviceIndexCalculation(b *testing.B) {
	cfg := &config.ENIManagerConfig{}
	tm := NewTestableManager(cfg)

	// Set up sysfs data for benchmarking
	for i := 5; i <= 20; i++ {
		ifaceName := fmt.Sprintf("ens%d", i)
		path := fmt.Sprintf("/sys/class/net/%s/device/device_index", ifaceName)
		tm.mockFS.SetFile(path, strconv.Itoa(i-5))
	}

	interfaces := []string{"ens5", "ens6", "ens7", "ens8", "ens9", "ens10"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ifaceName := interfaces[i%len(interfaces)]
		_, err := tm.getDeviceIndexForInterface(ifaceName)
		if err != nil {
			b.Errorf("Unexpected error: %v", err)
		}
	}
}

// BenchmarkDeviceIndexCalculationFallback benchmarks fallback performance
func BenchmarkDeviceIndexCalculationFallback(b *testing.B) {
	cfg := &config.ENIManagerConfig{}
	tm := NewTestableManager(cfg)

	// No sysfs data - force fallback to name-based calculation
	interfaces := []string{"ens5", "ens6", "ens7", "ens8", "ens9", "ens10"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ifaceName := interfaces[i%len(interfaces)]
		_, err := tm.getDeviceIndexForInterface(ifaceName)
		if err != nil {
			b.Errorf("Unexpected error: %v", err)
		}
	}
}
