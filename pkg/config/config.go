package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// ControllerConfig holds configuration for the ENI controller
type ControllerConfig struct {
	// AWS Region to use for API calls
	AWSRegion string
	// Reconciliation period for checking ENI status
	ReconcilePeriod time.Duration
	// Timeout for ENI detachment operations
	DetachmentTimeout time.Duration
	// Maximum concurrent reconciles
	MaxConcurrentReconciles int
	// Default device index if not specified in NodeENI
	DefaultDeviceIndex int
	// Default delete on termination setting
	DefaultDeleteOnTermination bool
}

// ENIManagerConfig holds configuration for the ENI manager
type ENIManagerConfig struct {
	// Interval between interface checks
	CheckInterval time.Duration
	// Primary interface name to ignore (if empty, will auto-detect)
	PrimaryInterface string
	// Enable debug logging
	DebugMode bool
	// Timeout for interface to come up after configuration
	InterfaceUpTimeout time.Duration
}

// DefaultControllerConfig returns the default configuration for the controller
func DefaultControllerConfig() *ControllerConfig {
	return &ControllerConfig{
		AWSRegion:                  "us-east-1",
		ReconcilePeriod:            5 * time.Minute,
		DetachmentTimeout:          15 * time.Second,
		MaxConcurrentReconciles:    5,
		DefaultDeviceIndex:         1,
		DefaultDeleteOnTermination: true,
	}
}

// DefaultENIManagerConfig returns the default configuration for the ENI manager
func DefaultENIManagerConfig() *ENIManagerConfig {
	return &ENIManagerConfig{
		CheckInterval:      30 * time.Second,
		PrimaryInterface:   "",
		DebugMode:          false,
		InterfaceUpTimeout: 2 * time.Second,
	}
}

// LoadControllerConfig loads controller configuration from environment variables
func LoadControllerConfig() (*ControllerConfig, error) {
	config := DefaultControllerConfig()

	// Load AWS region from environment variable
	if region := os.Getenv("AWS_REGION"); region != "" {
		config.AWSRegion = region
	}

	// Load reconcile period from environment variable
	if periodStr := os.Getenv("RECONCILE_PERIOD"); periodStr != "" {
		period, err := time.ParseDuration(periodStr)
		if err != nil {
			return nil, fmt.Errorf("invalid RECONCILE_PERIOD: %v", err)
		}
		config.ReconcilePeriod = period
	}

	// Load detachment timeout from environment variable
	if timeoutStr := os.Getenv("DETACHMENT_TIMEOUT"); timeoutStr != "" {
		timeout, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return nil, fmt.Errorf("invalid DETACHMENT_TIMEOUT: %v", err)
		}
		config.DetachmentTimeout = timeout
	}

	// Load max concurrent reconciles from environment variable
	if maxStr := os.Getenv("MAX_CONCURRENT_RECONCILES"); maxStr != "" {
		max, err := strconv.Atoi(maxStr)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_CONCURRENT_RECONCILES: %v", err)
		}
		config.MaxConcurrentReconciles = max
	}

	// Load default device index from environment variable
	if indexStr := os.Getenv("DEFAULT_DEVICE_INDEX"); indexStr != "" {
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			return nil, fmt.Errorf("invalid DEFAULT_DEVICE_INDEX: %v", err)
		}
		config.DefaultDeviceIndex = index
	}

	// Load default delete on termination setting from environment variable
	if dotStr := os.Getenv("DEFAULT_DELETE_ON_TERMINATION"); dotStr != "" {
		dot, err := strconv.ParseBool(dotStr)
		if err != nil {
			return nil, fmt.Errorf("invalid DEFAULT_DELETE_ON_TERMINATION: %v", err)
		}
		config.DefaultDeleteOnTermination = dot
	}

	// AWS SDK version is now always v2

	return config, nil
}

// LoadENIManagerConfigFromFlags loads ENI manager configuration from command-line flags
// This is used by the ENI manager component
func LoadENIManagerConfigFromFlags(checkInterval *time.Duration, primaryIface *string, debugMode *bool) *ENIManagerConfig {
	config := DefaultENIManagerConfig()

	if checkInterval != nil {
		config.CheckInterval = *checkInterval
	}

	if primaryIface != nil && *primaryIface != "" {
		config.PrimaryInterface = *primaryIface
	}

	if debugMode != nil {
		config.DebugMode = *debugMode
	}

	// Check for environment variable overrides
	if timeoutStr := os.Getenv("INTERFACE_UP_TIMEOUT"); timeoutStr != "" {
		if timeout, err := time.ParseDuration(timeoutStr); err == nil {
			config.InterfaceUpTimeout = timeout
		}
	}

	return config
}
