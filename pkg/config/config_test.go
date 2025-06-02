package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultControllerConfig(t *testing.T) {
	cfg := DefaultControllerConfig()

	if cfg == nil {
		t.Fatal("Expected non-nil config")
	}

	// Check default values
	if cfg.AWSRegion != "us-east-1" {
		t.Errorf("Expected default AWSRegion to be 'us-east-1', got '%s'", cfg.AWSRegion)
	}

	if cfg.ReconcilePeriod != 5*time.Minute {
		t.Errorf("Expected default ReconcilePeriod to be 5 minutes, got %v", cfg.ReconcilePeriod)
	}

	if cfg.DetachmentTimeout != 15*time.Second {
		t.Errorf("Expected default DetachmentTimeout to be 15 seconds, got %v", cfg.DetachmentTimeout)
	}

	if cfg.MaxConcurrentReconciles != 5 {
		t.Errorf("Expected default MaxConcurrentReconciles to be 5, got %d", cfg.MaxConcurrentReconciles)
	}

	if cfg.DefaultDeviceIndex != 1 {
		t.Errorf("Expected default DefaultDeviceIndex to be 1, got %d", cfg.DefaultDeviceIndex)
	}

	if !cfg.DefaultDeleteOnTermination {
		t.Error("Expected default DefaultDeleteOnTermination to be true")
	}
}

func TestDefaultENIManagerConfig(t *testing.T) {
	cfg := DefaultENIManagerConfig()

	if cfg == nil {
		t.Fatal("Expected non-nil config")
	}

	// Check default values
	if cfg.CheckInterval != 30*time.Second {
		t.Errorf("Expected default CheckInterval to be 30 seconds, got %v", cfg.CheckInterval)
	}

	if cfg.PrimaryInterface != "" {
		t.Errorf("Expected default PrimaryInterface to be empty, got '%s'", cfg.PrimaryInterface)
	}

	if cfg.DebugMode {
		t.Error("Expected default DebugMode to be false")
	}

	if cfg.InterfaceUpTimeout != 2*time.Second {
		t.Errorf("Expected default InterfaceUpTimeout to be 2 seconds, got %v", cfg.InterfaceUpTimeout)
	}
}

func TestLoadControllerConfig(t *testing.T) {
	// Save original environment variables
	origAWSRegion := os.Getenv("AWS_REGION")
	origReconcilePeriod := os.Getenv("RECONCILE_PERIOD")

	// Restore environment variables after test
	defer func() {
		os.Setenv("AWS_REGION", origAWSRegion)
		os.Setenv("RECONCILE_PERIOD", origReconcilePeriod)
	}()

	// Set test environment variables
	os.Setenv("AWS_REGION", "us-west-2")
	os.Setenv("RECONCILE_PERIOD", "10m")

	// Load configuration
	cfg, err := LoadControllerConfig()
	if err != nil {
		t.Fatalf("Failed to load controller config: %v", err)
	}

	// Check if environment variables were applied
	if cfg.AWSRegion != "us-west-2" {
		t.Errorf("Expected AWSRegion to be 'us-west-2', got '%s'", cfg.AWSRegion)
	}

	if cfg.ReconcilePeriod != 10*time.Minute {
		t.Errorf("Expected ReconcilePeriod to be 10 minutes, got %v", cfg.ReconcilePeriod)
	}

	// Test with default values
	os.Unsetenv("AWS_REGION")
	os.Unsetenv("RECONCILE_PERIOD")
	cfgDefault, errDefault := LoadControllerConfig()
	if errDefault != nil {
		t.Fatalf("Failed to load controller config for default values: %v", errDefault)
	}
	defaultExpected := DefaultControllerConfig()
	if cfgDefault.AWSRegion != defaultExpected.AWSRegion {
		t.Errorf("Expected AWSRegion to be default '%s', got '%s'", defaultExpected.AWSRegion, cfgDefault.AWSRegion)
	}
	if cfgDefault.ReconcilePeriod != defaultExpected.ReconcilePeriod {
		t.Errorf("Expected ReconcilePeriod to be default %v, got %v", defaultExpected.ReconcilePeriod, cfgDefault.ReconcilePeriod)
	}

	// Test all other environment variables
	testCases := []struct {
		name          string
		envVars       map[string]string
		expectedCfg   ControllerConfig
		expectedError string
	}{
		{
			name: "all valid env vars",
			envVars: map[string]string{
				"DETACHMENT_TIMEOUT":                 "30s",
				"MAX_CONCURRENT_RECONCILES":          "10",
				"DEFAULT_DEVICE_INDEX":               "2",
				"DEFAULT_DELETE_ON_TERMINATION":    "false",
				"MAX_CONCURRENT_ENI_CLEANUP":       "5",
				"MAX_CLEANUP_DURATION":               "1h",
				"CIRCUIT_BREAKER_ENABLED":          "false",
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD": "3",
				"CIRCUIT_BREAKER_SUCCESS_THRESHOLD": "2",
				"CIRCUIT_BREAKER_TIMEOUT":            "1m",
			},
			expectedCfg: ControllerConfig{
				AWSRegion:                      defaultExpected.AWSRegion, // Should remain default
				ReconcilePeriod:                defaultExpected.ReconcilePeriod, // Should remain default
				DetachmentTimeout:              30 * time.Second,
				MaxConcurrentReconciles:        10,
				DefaultDeviceIndex:             2,
				DefaultDeleteOnTermination:     false,
				MaxConcurrentENICleanup:        5,
				MaxCleanupDuration:             1 * time.Hour,
				CircuitBreakerEnabled:          false,
				CircuitBreakerFailureThreshold: 3,
				CircuitBreakerSuccessThreshold: 2,
				CircuitBreakerTimeout:          1 * time.Minute,
			},
		},
		{
			name: "invalid detachment timeout format",
			envVars: map[string]string{
				"DETACHMENT_TIMEOUT": "invalid",
			},
			expectedError: "invalid DETACHMENT_TIMEOUT",
		},
		{
			name: "invalid max concurrent reconciles format",
			envVars: map[string]string{
				"MAX_CONCURRENT_RECONCILES": "invalid",
			},
			expectedError: "invalid MAX_CONCURRENT_RECONCILES",
		},
		{
			name: "invalid default device index format",
			envVars: map[string]string{
				"DEFAULT_DEVICE_INDEX": "invalid",
			},
			expectedError: "invalid DEFAULT_DEVICE_INDEX",
		},
		{
			name: "invalid default delete on termination format",
			envVars: map[string]string{
				"DEFAULT_DELETE_ON_TERMINATION": "invalid",
			},
			expectedError: "invalid DEFAULT_DELETE_ON_TERMINATION",
		},
		{
			name: "invalid max concurrent eni cleanup format",
			envVars: map[string]string{
				"MAX_CONCURRENT_ENI_CLEANUP": "invalid",
			},
			expectedError: "invalid MAX_CONCURRENT_ENI_CLEANUP",
		},
		{
			name: "invalid max cleanup duration format",
			envVars: map[string]string{
				"MAX_CLEANUP_DURATION": "invalid",
			},
			expectedError: "invalid MAX_CLEANUP_DURATION",
		},
		{
			name: "invalid circuit breaker enabled format",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_ENABLED": "invalid",
			},
			expectedError: "invalid CIRCUIT_BREAKER_ENABLED",
		},
		{
			name: "invalid circuit breaker failure threshold format",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD": "invalid",
			},
			expectedError: "invalid CIRCUIT_BREAKER_FAILURE_THRESHOLD",
		},
		{
			name: "invalid circuit breaker success threshold format",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_SUCCESS_THRESHOLD": "invalid",
			},
			expectedError: "invalid CIRCUIT_BREAKER_SUCCESS_THRESHOLD",
		},
		{
			name: "invalid circuit breaker timeout format",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_TIMEOUT": "invalid",
			},
			expectedError: "invalid CIRCUIT_BREAKER_TIMEOUT",
		},
		{
			name: "empty env vars should use defaults",
			envVars: map[string]string{
				"DETACHMENT_TIMEOUT":                 "",
				"MAX_CONCURRENT_RECONCILES":          "",
				"DEFAULT_DEVICE_INDEX":               "",
				"DEFAULT_DELETE_ON_TERMINATION":    "",
				"MAX_CONCURRENT_ENI_CLEANUP":       "",
				"MAX_CLEANUP_DURATION":               "",
				"CIRCUIT_BREAKER_ENABLED":          "",
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD": "",
				"CIRCUIT_BREAKER_SUCCESS_THRESHOLD": "",
				"CIRCUIT_BREAKER_TIMEOUT":            "",
			},
			expectedCfg: *DefaultControllerConfig(), // All defaults
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set environment variables for the current test case
			for key, value := range tc.envVars {
				origValue := os.Getenv(key)
				os.Setenv(key, value)
				defer os.Setenv(key, origValue)
			}
			// Ensure AWS_REGION and RECONCILE_PERIOD are unset to test defaults for other fields
			// or set them if they are part of the test case (which they are not here for simplicity of all other fields)
			if _, ok := tc.envVars["AWS_REGION"]; !ok {
				origAWSRegion := os.Getenv("AWS_REGION")
				os.Unsetenv("AWS_REGION")
				defer os.Setenv("AWS_REGION", origAWSRegion)
			}
			if _, ok := tc.envVars["RECONCILE_PERIOD"]; !ok {
				origReconcilePeriod := os.Getenv("RECONCILE_PERIOD")
				os.Unsetenv("RECONCILE_PERIOD")
				defer os.Setenv("RECONCILE_PERIOD", origReconcilePeriod)
			}


			loadedCfg, err := LoadControllerConfig()

			if tc.expectedError != "" {
				if err == nil {
					t.Errorf("Expected error containing '%s', got nil", tc.expectedError)
				} else if !strings.Contains(err.Error(), tc.expectedError) {
					t.Errorf("Expected error containing '%s', got '%v'", tc.expectedError, err)
				}
				return // Test case expects an error, no need to check config values
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if loadedCfg.DetachmentTimeout != tc.expectedCfg.DetachmentTimeout {
				t.Errorf("Expected DetachmentTimeout %v, got %v", tc.expectedCfg.DetachmentTimeout, loadedCfg.DetachmentTimeout)
			}
			if loadedCfg.MaxConcurrentReconciles != tc.expectedCfg.MaxConcurrentReconciles {
				t.Errorf("Expected MaxConcurrentReconciles %d, got %d", tc.expectedCfg.MaxConcurrentReconciles, loadedCfg.MaxConcurrentReconciles)
			}
			if loadedCfg.DefaultDeviceIndex != tc.expectedCfg.DefaultDeviceIndex {
				t.Errorf("Expected DefaultDeviceIndex %d, got %d", tc.expectedCfg.DefaultDeviceIndex, loadedCfg.DefaultDeviceIndex)
			}
			if loadedCfg.DefaultDeleteOnTermination != tc.expectedCfg.DefaultDeleteOnTermination {
				t.Errorf("Expected DefaultDeleteOnTermination %v, got %v", tc.expectedCfg.DefaultDeleteOnTermination, loadedCfg.DefaultDeleteOnTermination)
			}
			if loadedCfg.MaxConcurrentENICleanup != tc.expectedCfg.MaxConcurrentENICleanup {
				t.Errorf("Expected MaxConcurrentENICleanup %d, got %d", tc.expectedCfg.MaxConcurrentENICleanup, loadedCfg.MaxConcurrentENICleanup)
			}
			if loadedCfg.MaxCleanupDuration != tc.expectedCfg.MaxCleanupDuration {
				t.Errorf("Expected MaxCleanupDuration %v, got %v", tc.expectedCfg.MaxCleanupDuration, loadedCfg.MaxCleanupDuration)
			}
			if loadedCfg.CircuitBreakerEnabled != tc.expectedCfg.CircuitBreakerEnabled {
				t.Errorf("Expected CircuitBreakerEnabled %v, got %v", tc.expectedCfg.CircuitBreakerEnabled, loadedCfg.CircuitBreakerEnabled)
			}
			// Only check thresholds and timeout if circuit breaker is expected to be enabled in the test case,
			// or if specific values are provided for them (implying they should be set even if CB is disabled by env var in this test run)
			if tc.expectedCfg.CircuitBreakerEnabled || tc.envVars["CIRCUIT_BREAKER_FAILURE_THRESHOLD"] != "" || tc.envVars["CIRCUIT_BREAKER_SUCCESS_THRESHOLD"] != "" || tc.envVars["CIRCUIT_BREAKER_TIMEOUT"] != "" {
				if loadedCfg.CircuitBreakerFailureThreshold != tc.expectedCfg.CircuitBreakerFailureThreshold {
					t.Errorf("Expected CircuitBreakerFailureThreshold %d, got %d", tc.expectedCfg.CircuitBreakerFailureThreshold, loadedCfg.CircuitBreakerFailureThreshold)
				}
				if loadedCfg.CircuitBreakerSuccessThreshold != tc.expectedCfg.CircuitBreakerSuccessThreshold {
					t.Errorf("Expected CircuitBreakerSuccessThreshold %d, got %d", tc.expectedCfg.CircuitBreakerSuccessThreshold, loadedCfg.CircuitBreakerSuccessThreshold)
				}
				if loadedCfg.CircuitBreakerTimeout != tc.expectedCfg.CircuitBreakerTimeout {
					t.Errorf("Expected CircuitBreakerTimeout %v, got %v", tc.expectedCfg.CircuitBreakerTimeout, loadedCfg.CircuitBreakerTimeout)
				}
			}
			// Check if AWSRegion and ReconcilePeriod match defaults if not set by test case
			if _, ok := tc.envVars["AWS_REGION"]; !ok {
				if loadedCfg.AWSRegion != defaultExpected.AWSRegion {
					t.Errorf("Expected AWSRegion to be default '%s', got '%s'", defaultExpected.AWSRegion, loadedCfg.AWSRegion)
				}
			}
			if _, ok := tc.envVars["RECONCILE_PERIOD"]; !ok {
				if loadedCfg.ReconcilePeriod != defaultExpected.ReconcilePeriod {
					t.Errorf("Expected ReconcilePeriod to be default %v, got %v", defaultExpected.ReconcilePeriod, loadedCfg.ReconcilePeriod)
				}
			}
		})
	}
}

func TestENIManagerConfigValidate(t *testing.T) {
	defaultValidConfig := DefaultENIManagerConfig()
	if err := defaultValidConfig.Validate(); err != nil {
		t.Errorf("Default ENIManagerConfig validation failed: %v", err)
	}

	customValidConfig := &ENIManagerConfig{
		NodeName:           "test-node",
		CheckInterval:      10 * time.Second,
		PrimaryInterface:   "eth0",
		DebugMode:          true,
		InterfaceUpTimeout: 5 * time.Second,
		ENIPattern:         "^eni[0-9a-f]+",
		IgnoreInterfaces:   []string{"lo"},
		DefaultMTU:         1500,
		InterfaceMTUs:      map[string]int{"eth1": 9000},
		EnableDPDK:         true,
		DefaultDPDKDriver:  "vfio-pci",
	}
	if err := customValidConfig.Validate(); err != nil {
		t.Errorf("Custom valid ENIManagerConfig validation failed: %v", err)
	}

	testCases := []struct {
		name        string
		cfg         *ENIManagerConfig
		expectedErr string
	}{
		{
			name: "non-positive CheckInterval",
			cfg: &ENIManagerConfig{CheckInterval: 0, InterfaceUpTimeout: 1 * time.Second, ENIPattern: "a"},
			expectedErr: "CheckInterval must be positive",
		},
		{
			name: "non-positive InterfaceUpTimeout",
			cfg: &ENIManagerConfig{CheckInterval: 1 * time.Second, InterfaceUpTimeout: -1 * time.Second, ENIPattern: "a"},
			expectedErr: "InterfaceUpTimeout must be positive",
		},
		{
			name: "empty ENIPattern",
			cfg: &ENIManagerConfig{CheckInterval: 1 * time.Second, InterfaceUpTimeout: 1 * time.Second, ENIPattern: ""},
			expectedErr: "ENIPattern cannot be empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Fill required fields not part of the specific test case with valid defaults
			// to ensure the test focuses on the intended validation rule.
			if tc.cfg.CheckInterval <= 0 && !strings.Contains(tc.expectedErr, "CheckInterval") {
				tc.cfg.CheckInterval = 30 * time.Second
			}
			if tc.cfg.InterfaceUpTimeout <= 0 && !strings.Contains(tc.expectedErr, "InterfaceUpTimeout") {
				tc.cfg.InterfaceUpTimeout = 2 * time.Second
			}
			if tc.cfg.ENIPattern == "" && !strings.Contains(tc.expectedErr, "ENIPattern") {
				tc.cfg.ENIPattern = "^eth[0-9]+"
			}
			// Other fields in ENIManagerConfig do not have validation rules in Validate() yet.

			err := tc.cfg.Validate()
			if tc.expectedErr != "" {
				if err == nil {
					t.Errorf("Expected error containing '%s', got nil", tc.expectedErr)
				} else if !strings.Contains(err.Error(), tc.expectedErr) {
					t.Errorf("Expected error containing '%s', got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestControllerConfigValidate(t *testing.T) {
	defaultValidConfig := DefaultControllerConfig()
	if err := defaultValidConfig.Validate(); err != nil {
		t.Errorf("Default config validation failed: %v", err)
	}

	customValidConfig := &ControllerConfig{
		AWSRegion:                      "us-west-1",
		ReconcilePeriod:                10 * time.Minute,
		DetachmentTimeout:              30 * time.Second,
		MaxConcurrentReconciles:        10,
		DefaultDeviceIndex:             0,
		DefaultDeleteOnTermination:     false,
		MaxConcurrentENICleanup:        5,
		MaxCleanupDuration:             1 * time.Hour,
		CircuitBreakerEnabled:          true,
		CircuitBreakerFailureThreshold: 3,
		CircuitBreakerSuccessThreshold: 2,
		CircuitBreakerTimeout:          45 * time.Second,
	}
	if err := customValidConfig.Validate(); err != nil {
		t.Errorf("Custom valid config validation failed: %v", err)
	}

	customValidConfigCBDisabled := &ControllerConfig{
		AWSRegion:                      "us-west-1",
		ReconcilePeriod:                10 * time.Minute,
		DetachmentTimeout:              30 * time.Second,
		MaxConcurrentReconciles:        10,
		DefaultDeviceIndex:             0,
		DefaultDeleteOnTermination:     false,
		MaxConcurrentENICleanup:        5,
		MaxCleanupDuration:             1 * time.Hour,
		CircuitBreakerEnabled:          false, // CB Disabled, thresholds can be 0 or positive
		CircuitBreakerFailureThreshold: 0,
		CircuitBreakerSuccessThreshold: 0,
		CircuitBreakerTimeout:          0,
	}
	if err := customValidConfigCBDisabled.Validate(); err != nil {
		t.Errorf("Custom valid config with CB disabled validation failed: %v", err)
	}


	testCases := []struct {
		name        string
		cfg         *ControllerConfig
		expectedErr string
	}{
		{
			name: "non-positive MaxConcurrentReconciles",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 0, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: false},
			expectedErr: "MaxConcurrentReconciles must be positive",
		},
		{
			name: "non-positive DetachmentTimeout",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 0, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: false},
			expectedErr: "DetachmentTimeout must be positive",
		},
		{
			name: "non-positive MaxConcurrentENICleanup",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 0, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: false},
			expectedErr: "MaxConcurrentENICleanup must be positive",
		},
		{
			name: "non-positive ReconcilePeriod",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 0, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: false},
			expectedErr: "ReconcilePeriod must be positive",
		},
		{
			name: "non-positive MaxCleanupDuration",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 0, CircuitBreakerEnabled: false},
			expectedErr: "MaxCleanupDuration must be positive",
		},
		{
			name: "CB enabled, non-positive CircuitBreakerFailureThreshold",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: true, CircuitBreakerFailureThreshold: 0, CircuitBreakerSuccessThreshold: 1, CircuitBreakerTimeout: 1 * time.Second},
			expectedErr: "CircuitBreakerFailureThreshold must be positive",
		},
		{
			name: "CB enabled, non-positive CircuitBreakerSuccessThreshold",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: true, CircuitBreakerFailureThreshold: 1, CircuitBreakerSuccessThreshold: 0, CircuitBreakerTimeout: 1 * time.Second},
			expectedErr: "CircuitBreakerSuccessThreshold must be positive",
		},
		{
			name: "CB enabled, non-positive CircuitBreakerTimeout",
			cfg: &ControllerConfig{MaxConcurrentReconciles: 1, ReconcilePeriod: 1 * time.Minute, DetachmentTimeout: 1 * time.Second, MaxConcurrentENICleanup: 1, MaxCleanupDuration: 1 * time.Minute, CircuitBreakerEnabled: true, CircuitBreakerFailureThreshold: 1, CircuitBreakerSuccessThreshold: 1, CircuitBreakerTimeout: 0},
			expectedErr: "CircuitBreakerTimeout must be positive",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Fill required fields not part of the specific test case with valid defaults
			if tc.cfg.AWSRegion == "" { // Should not cause validation error
				tc.cfg.AWSRegion = "us-test-1"
			}
			if tc.cfg.ReconcilePeriod <= 0 && !strings.Contains(tc.expectedErr, "ReconcilePeriod") {
				tc.cfg.ReconcilePeriod = 5 * time.Minute
			}
			if tc.cfg.DetachmentTimeout <= 0 && !strings.Contains(tc.expectedErr, "DetachmentTimeout") {
				tc.cfg.DetachmentTimeout = 15 * time.Second
			}
			if tc.cfg.MaxConcurrentReconciles <= 0 && !strings.Contains(tc.expectedErr, "MaxConcurrentReconciles") {
				tc.cfg.MaxConcurrentReconciles = 5
			}
			if tc.cfg.MaxConcurrentENICleanup <= 0 && !strings.Contains(tc.expectedErr, "MaxConcurrentENICleanup") {
				tc.cfg.MaxConcurrentENICleanup = 3
			}
			if tc.cfg.MaxCleanupDuration <= 0 && !strings.Contains(tc.expectedErr, "MaxCleanupDuration") {
				tc.cfg.MaxCleanupDuration = 30 * time.Minute
			}
			if tc.cfg.CircuitBreakerEnabled {
				if tc.cfg.CircuitBreakerFailureThreshold <= 0 && !strings.Contains(tc.expectedErr, "CircuitBreakerFailureThreshold") {
					tc.cfg.CircuitBreakerFailureThreshold = 5
				}
				if tc.cfg.CircuitBreakerSuccessThreshold <= 0 && !strings.Contains(tc.expectedErr, "CircuitBreakerSuccessThreshold") {
					tc.cfg.CircuitBreakerSuccessThreshold = 3
				}
				if tc.cfg.CircuitBreakerTimeout <= 0 && !strings.Contains(tc.expectedErr, "CircuitBreakerTimeout") {
					tc.cfg.CircuitBreakerTimeout = 30 * time.Second
				}
			}


			err := tc.cfg.Validate()
			if tc.expectedErr != "" {
				if err == nil {
					t.Errorf("Expected error containing '%s', got nil", tc.expectedErr)
				} else if !strings.Contains(err.Error(), tc.expectedErr) {
					t.Errorf("Expected error containing '%s', got '%v'", tc.expectedErr, err)
				}
			} else if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}


func TestLoadENIManagerConfigFromFlags(t *testing.T) {
	// Save original environment variables
	origInterfaceUpTimeout := os.Getenv("INTERFACE_UP_TIMEOUT")
	origENIPattern := os.Getenv("ENI_PATTERN")
	origIgnoreInterfaces := os.Getenv("IGNORE_INTERFACES")

	// Restore environment variables after test
	defer func() {
		os.Setenv("INTERFACE_UP_TIMEOUT", origInterfaceUpTimeout)
		os.Setenv("ENI_PATTERN", origENIPattern)
		os.Setenv("IGNORE_INTERFACES", origIgnoreInterfaces)
	}()

	// Set test environment variables for the first test
	os.Setenv("INTERFACE_UP_TIMEOUT", "5s")
	os.Setenv("ENI_PATTERN", "^test[0-9]+")
	os.Setenv("IGNORE_INTERFACES", "test0,test1,test2")

	// Test with nil flags (should use defaults and env vars)
	cfg := LoadENIManagerConfigFromFlags(nil, nil, nil, nil, nil)

	if cfg.InterfaceUpTimeout != 5*time.Second {
		t.Errorf("Expected InterfaceUpTimeout to be 5 seconds, got %v", cfg.InterfaceUpTimeout)
	}

	// Check ENI pattern from environment variable
	if cfg.ENIPattern != "^test[0-9]+" {
		t.Errorf("Expected ENIPattern from env var to be '^test[0-9]+', got '%s'", cfg.ENIPattern)
	}

	// Check ignore list from environment variable
	if len(cfg.IgnoreInterfaces) != 3 {
		t.Errorf("Expected IgnoreInterfaces from env var to have 3 values, got %d", len(cfg.IgnoreInterfaces))
	}

	if cfg.IgnoreInterfaces[0] != "test0" {
		t.Errorf("Expected first ignore interface to be 'test0', got '%s'", cfg.IgnoreInterfaces[0])
	}

	// Clear environment variables for the second test
	os.Unsetenv("ENI_PATTERN")
	os.Unsetenv("IGNORE_INTERFACES")

	// Test with provided flags
	checkInterval := 1 * time.Minute
	primaryIface := "eth0"
	debugMode := true
	eniPattern := "^eth[0-9]+"
	ignoreList := "lo,dummy0"

	cfg = LoadENIManagerConfigFromFlags(&checkInterval, &primaryIface, &debugMode, &eniPattern, &ignoreList)

	if cfg.CheckInterval != 1*time.Minute {
		t.Errorf("Expected CheckInterval to be 1 minute, got %v", cfg.CheckInterval)
	}

	if cfg.PrimaryInterface != "eth0" {
		t.Errorf("Expected PrimaryInterface to be 'eth0', got '%s'", cfg.PrimaryInterface)
	}

	if !cfg.DebugMode {
		t.Error("Expected DebugMode to be true")
	}

	if cfg.ENIPattern != "^eth[0-9]+" {
		t.Errorf("Expected ENIPattern to be '^eth[0-9]+', got '%s'", cfg.ENIPattern)
	}

	// Check that the custom ignore list is set correctly
	if len(cfg.IgnoreInterfaces) != 2 {
		t.Errorf("Expected IgnoreInterfaces to have 2 values, got %d", len(cfg.IgnoreInterfaces))
	}

	if cfg.IgnoreInterfaces[0] != "lo" {
		t.Errorf("Expected first ignore interface to be 'lo', got '%s'", cfg.IgnoreInterfaces[0])
	}

	if cfg.IgnoreInterfaces[1] != "dummy0" {
		t.Errorf("Expected second ignore interface to be 'dummy0', got '%s'", cfg.IgnoreInterfaces[1])
	}

	if cfg.InterfaceUpTimeout != 5*time.Second {
		t.Errorf("Expected InterfaceUpTimeout to be 5 seconds, got %v", cfg.InterfaceUpTimeout)
	}

	if cfg.ENIPattern != "^eth[0-9]+" {
		t.Errorf("Expected ENIPattern to be '^eth[0-9]+', got '%s'", cfg.ENIPattern)
	}

	// Check that the default ignore list is set
	if len(cfg.IgnoreInterfaces) == 0 {
		t.Error("Expected IgnoreInterfaces to have default values")
	}

	// Test environment variable overrides for other fields
	os.Unsetenv("INTERFACE_UP_TIMEOUT") // Clear previous env vars for this test section
	os.Unsetenv("ENI_PATTERN")
	os.Unsetenv("IGNORE_INTERFACES")

	envVarTestCases := []struct {
		name        string
		envVars     map[string]string
		checkFunc   func(cfg *ENIManagerConfig, t *testing.T)
		expectedErr string // Not expecting errors from LoadENIManagerConfigFromFlags for these env vars, but good practice
	}{
		{
			name: "DEFAULT_MTU valid",
			envVars: map[string]string{"DEFAULT_MTU": "1500"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.DefaultMTU != 1500 {
					t.Errorf("Expected DefaultMTU to be 1500, got %d", cfg.DefaultMTU)
				}
			},
		},
		{
			name: "DEFAULT_MTU invalid", // Should be ignored, default used
			envVars: map[string]string{"DEFAULT_MTU": "abc"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.DefaultMTU != DefaultENIManagerConfig().DefaultMTU {
					t.Errorf("Expected DefaultMTU to be default %d, got %d", DefaultENIManagerConfig().DefaultMTU, cfg.DefaultMTU)
				}
			},
		},
		{
			name: "INTERFACE_MTUS valid",
			envVars: map[string]string{"INTERFACE_MTUS": "eth1:9001,eth2:1500"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if val, ok := cfg.InterfaceMTUs["eth1"]; !ok || val != 9001 {
					t.Errorf("Expected InterfaceMTUs[eth1] to be 9001, got %v (ok: %v)", val, ok)
				}
				if val, ok := cfg.InterfaceMTUs["eth2"]; !ok || val != 1500 {
					t.Errorf("Expected InterfaceMTUs[eth2] to be 1500, got %v (ok: %v)", val, ok)
				}
			},
		},
		{
			name: "INTERFACE_MTUS invalid format", // Malformed entries should be skipped
			envVars: map[string]string{"INTERFACE_MTUS": "eth1:9001,eth2:abc,eth3:,:123"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if val, ok := cfg.InterfaceMTUs["eth1"]; !ok || val != 9001 {
					t.Errorf("Expected InterfaceMTUs[eth1] to be 9001, got %v (ok: %v)", val, ok)
				}
				if _, ok := cfg.InterfaceMTUs["eth2"]; ok {
					t.Errorf("Expected InterfaceMTUs[eth2] to be skipped, but it was present: %v", cfg.InterfaceMTUs["eth2"])
				}
				if len(cfg.InterfaceMTUs) != 1 {
					t.Errorf("Expected only 1 valid entry in InterfaceMTUs, got %d: %v", len(cfg.InterfaceMTUs), cfg.InterfaceMTUs)
				}
			},
		},
		{
			name: "ENABLE_DPDK true",
			envVars: map[string]string{"ENABLE_DPDK": "true"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if !cfg.EnableDPDK {
					t.Error("Expected EnableDPDK to be true")
				}
			},
		},
		{
			name: "ENABLE_DPDK false",
			envVars: map[string]string{"ENABLE_DPDK": "false"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.EnableDPDK {
					t.Error("Expected EnableDPDK to be false")
				}
			},
		},
		{
			name: "ENABLE_DPDK invalid", // Should be ignored, default used
			envVars: map[string]string{"ENABLE_DPDK": "abc"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.EnableDPDK != DefaultENIManagerConfig().EnableDPDK {
					t.Errorf("Expected EnableDPDK to be default %v, got %v", DefaultENIManagerConfig().EnableDPDK, cfg.EnableDPDK)
				}
			},
		},
		{
			name: "DEFAULT_DPDK_DRIVER",
			envVars: map[string]string{"DEFAULT_DPDK_DRIVER": "uio_pci_generic"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.DefaultDPDKDriver != "uio_pci_generic" {
					t.Errorf("Expected DefaultDPDKDriver to be 'uio_pci_generic', got '%s'", cfg.DefaultDPDKDriver)
				}
			},
		},
		{
			name: "DPDK_BINDING_SCRIPT",
			envVars: map[string]string{"DPDK_BINDING_SCRIPT": "/usr/bin/dpdk-devbind.sh"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.DPDKBindingScript != "/usr/bin/dpdk-devbind.sh" {
					t.Errorf("Expected DPDKBindingScript to be '/usr/bin/dpdk-devbind.sh', got '%s'", cfg.DPDKBindingScript)
				}
			},
		},
		{
			name: "SRIOV_DP_CONFIG_PATH",
			envVars: map[string]string{"SRIOV_DP_CONFIG_PATH": "/test/config.json"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.SRIOVDPConfigPath != "/test/config.json" {
					t.Errorf("Expected SRIOVDPConfigPath to be '/test/config.json', got '%s'", cfg.SRIOVDPConfigPath)
				}
			},
		},
		{
			name: "DPDK_RESOURCE_NAMES valid",
			envVars: map[string]string{"DPDK_RESOURCE_NAMES": "eth1:res1,eth2:res2"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if val, ok := cfg.DPDKResourceNames["eth1"]; !ok || val != "res1" {
					t.Errorf("Expected DPDKResourceNames[eth1] to be 'res1', got %v (ok: %v)", val, ok)
				}
				if val, ok := cfg.DPDKResourceNames["eth2"]; !ok || val != "res2" {
					t.Errorf("Expected DPDKResourceNames[eth2] to be 'res2', got %v (ok: %v)", val, ok)
				}
			},
		},
		{
			name: "DPDK_RESOURCE_NAMES invalid format", // Malformed entries should be skipped
			envVars: map[string]string{"DPDK_RESOURCE_NAMES": "eth1:res1,eth2:,:res3"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if val, ok := cfg.DPDKResourceNames["eth1"]; !ok || val != "res1" {
					t.Errorf("Expected DPDKResourceNames[eth1] to be 'res1', got %v (ok: %v)", val, ok)
				}
				if _, ok := cfg.DPDKResourceNames["eth2"]; ok {
					t.Errorf("Expected DPDKResourceNames[eth2] to be skipped, but it was present: %v", cfg.DPDKResourceNames["eth2"])
				}
                 if len(cfg.DPDKResourceNames) != 1 {
					t.Errorf("Expected only 1 valid entry in DPDKResourceNames, got %d: %v", len(cfg.DPDKResourceNames), cfg.DPDKResourceNames)
				}
			},
		},
		{
			name: "INTERFACE_MAPPING_STORE_PATH",
			envVars: map[string]string{"INTERFACE_MAPPING_STORE_PATH": "/tmp/mappings.json"},
			checkFunc: func(cfg *ENIManagerConfig, t *testing.T) {
				if cfg.InterfaceMappingStorePath != "/tmp/mappings.json" {
					t.Errorf("Expected InterfaceMappingStorePath to be '/tmp/mappings.json', got '%s'", cfg.InterfaceMappingStorePath)
				}
				// Also check if the store was attempted to be initialized with the new path
				// This is a bit of an integration test, but important for this config
				if cfg.InterfaceMappingStore == nil && os.Getenv("INTERFACE_MAPPING_STORE_PATH") != "" {
					// It's okay if it's nil if the file doesn't exist and loading failed,
					// but the path in the config should be updated.
					// We can't easily check for the warning message here without more complex setup.
					// The main check is that InterfaceMappingStorePath is set correctly.
					// If the store is not nil, it implies an attempt was made to initialize it with this path.
				} else if cfg.InterfaceMappingStore != nil && cfg.InterfaceMappingStorePath != "/tmp/mappings.json" {
					// This condition is slightly redundant given the check on cfg.InterfaceMappingStorePath above,
					// but confirms consistency if the store was successfully created.
					t.Errorf("Expected InterfaceMappingStorePath to be consistent if store initialized, expected '/tmp/mappings.json', got '%s'", cfg.InterfaceMappingStorePath)
				}
			},
		},
	}

	for _, tc := range envVarTestCases {
		t.Run(tc.name, func(t *testing.T) {
			// Clean slate for env vars for each test case
			currentEnvVarsToCleanup := make(map[string]string)
			// Set up environment variables for this test case
			for key, value := range tc.envVars {
				originalValue, isSet := os.LookupEnv(key)
				os.Setenv(key, value)
				if isSet {
					currentEnvVarsToCleanup[key] = originalValue
				} else {
					currentEnvVarsToCleanup[key] = "" // Mark for unsetting
				}
			}

			defer func() {
				for key, originalValue := range currentEnvVarsToCleanup {
					if originalValue != "" || os.Getenv(key) != "" { // Only reset/unset if it was set or we set it
						if originalValue == "" && os.Getenv(key) != "" && tc.envVars[key] == os.Getenv(key) { // We set it, and it was not set before
							os.Unsetenv(key)
						} else if originalValue != "" { // It was set before, restore it
							os.Setenv(key, originalValue)
						}
						// If it was not set before, and we didn't set it (value was empty string), do nothing.
					}
				}
			}()

			// Ensure INTERFACE_MAPPING_STORE_PATH related file does not exist to avoid side effects
			// from previous test runs if a file was created.
			if path, ok := tc.envVars["INTERFACE_MAPPING_STORE_PATH"]; ok {
				os.Remove(path) // Attempt to remove, ignore error if it doesn't exist
				defer os.Remove(path) // Cleanup after test
			} else {
				// Remove default path if this test is not specifically setting it
				defaultPath := DefaultENIManagerConfig().InterfaceMappingStorePath
				os.Remove(defaultPath)
				defer os.Remove(defaultPath)
			}


			loadedCfg := LoadENIManagerConfigFromFlags(nil, nil, nil, nil, nil) // Load with nil flags to focus on env vars

			if tc.expectedErr != "" {
				// This block is unlikely to be hit for these specific env vars as LoadENIManagerConfigFromFlags
				// currently doesn't return errors for malformed env var values (it uses defaults or skips).
				// However, it's good practice for future changes.
				// For now, no direct error return from LoadENIManagerConfigFromFlags, it logs warnings.
				// So we can't check for err here directly based on current implementation.
				// Instead, the checkFunc will verify if the parsing was successful or default was used.
				t.Skipf("Skipping error check for now as LoadENIManagerConfigFromFlags doesn't return errors for these env vars: %s", tc.expectedErr)

			} else {
				if tc.checkFunc != nil {
					tc.checkFunc(loadedCfg, t)
				}
			}
		})
	}
}
