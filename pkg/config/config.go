// Package config provides configuration management for the AWS Multi-ENI Controller
// and ENI Manager components.
//
// This package handles loading configuration from environment variables and command-line
// flags, providing sensible defaults when values are not explicitly provided. It supports
// configuration for both the controller (which runs in Kubernetes) and the ENI manager
// (which runs on each node).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	// Maximum number of concurrent ENI cleanup operations
	MaxConcurrentENICleanup int
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
	// Regex pattern to identify ENI interfaces
	ENIPattern string
	// List of interfaces to ignore
	IgnoreInterfaces []string
	// Default MTU to set on interfaces (0 means use system default)
	DefaultMTU int
	// Map of interface name to MTU value
	InterfaceMTUs map[string]int
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
		MaxConcurrentENICleanup:    3, // Default to 3 concurrent ENI cleanup operations
	}
}

// DefaultENIManagerConfig returns the default configuration for the ENI manager
func DefaultENIManagerConfig() *ENIManagerConfig {
	return &ENIManagerConfig{
		CheckInterval:      30 * time.Second,
		PrimaryInterface:   "",
		DebugMode:          false,
		InterfaceUpTimeout: 2 * time.Second,
		ENIPattern:         "^(eth|ens|eni|en)[0-9]+",
		IgnoreInterfaces:   []string{"tunl0", "gre0", "gretap0", "erspan0", "ip_vti0", "ip6_vti0", "sit0", "ip6tnl0", "ip6gre0"},
		DefaultMTU:         0, // 0 means use system default
		InterfaceMTUs:      make(map[string]int),
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

	// Load max concurrent ENI cleanup from environment variable
	if maxStr := os.Getenv("MAX_CONCURRENT_ENI_CLEANUP"); maxStr != "" {
		max, err := strconv.Atoi(maxStr)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_CONCURRENT_ENI_CLEANUP: %v", err)
		}
		config.MaxConcurrentENICleanup = max
	}

	// AWS SDK version is now always v2

	return config, nil
}

// LoadENIManagerConfigFromFlags loads ENI manager configuration from command-line flags
// This is used by the ENI manager component
func LoadENIManagerConfigFromFlags(checkInterval *time.Duration, primaryIface *string, debugMode *bool, eniPattern *string, ignoreList *string) *ENIManagerConfig {
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

	if eniPattern != nil && *eniPattern != "" {
		config.ENIPattern = *eniPattern
	}

	if ignoreList != nil && *ignoreList != "" {
		// Split the comma-separated list
		config.IgnoreInterfaces = splitCSV(*ignoreList)
	}

	// Check for environment variable overrides
	if timeoutStr := os.Getenv("INTERFACE_UP_TIMEOUT"); timeoutStr != "" {
		if timeout, err := time.ParseDuration(timeoutStr); err == nil {
			config.InterfaceUpTimeout = timeout
		}
	}

	if patternStr := os.Getenv("ENI_PATTERN"); patternStr != "" {
		config.ENIPattern = patternStr
	}

	if ignoreStr := os.Getenv("IGNORE_INTERFACES"); ignoreStr != "" {
		config.IgnoreInterfaces = splitCSV(ignoreStr)
	}

	// Load default MTU from environment variable
	if mtuStr := os.Getenv("DEFAULT_MTU"); mtuStr != "" {
		if mtu, err := strconv.Atoi(mtuStr); err == nil {
			config.DefaultMTU = mtu
		}
	}

	// Load interface-specific MTUs from environment variable
	// Format: "eth1:9000,eth2:1500"
	if mtuMapStr := os.Getenv("INTERFACE_MTUS"); mtuMapStr != "" {
		pairs := splitCSV(mtuMapStr)
		for _, pair := range pairs {
			parts := strings.Split(pair, ":")
			if len(parts) == 2 {
				ifaceName := parts[0]
				mtuStr := parts[1]
				if mtu, err := strconv.Atoi(mtuStr); err == nil {
					config.InterfaceMTUs[ifaceName] = mtu
				}
			}
		}
	}

	return config
}

// splitCSV splits a comma-separated string into a slice of strings
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}
