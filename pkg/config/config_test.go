package config

import (
	"os"
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
}
