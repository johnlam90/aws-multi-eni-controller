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

	"github.com/johnlam90/aws-multi-eni-controller/pkg/mapping"
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
	// Maximum duration for cleanup operations before forcing finalizer removal
	MaxCleanupDuration time.Duration
	// Circuit breaker configuration for AWS operations
	CircuitBreakerEnabled          bool
	CircuitBreakerFailureThreshold int
	CircuitBreakerSuccessThreshold int
	CircuitBreakerTimeout          time.Duration
}

// ENIManagerConfig holds configuration for the ENI manager
type ENIManagerConfig struct {
	// Node name for this ENI manager instance
	NodeName string
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
	// Enable DPDK device binding
	EnableDPDK bool
	// Default DPDK driver to use for binding (default: vfio-pci)
	DefaultDPDKDriver string
	// Map of interface name to DPDK resource name
	DPDKResourceNames map[string]string
	// Map of PCI address to DPDK bound interface information
	DPDKBoundInterfaces map[string]struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}
	// Path to DPDK device binding script
	DPDKBindingScript string
	// Path to SRIOV device plugin config file
	SRIOVDPConfigPath string
	// Interface mapping store for persistent mapping between ENI IDs, interface names, and PCI addresses
	InterfaceMappingStore *mapping.InterfaceMappingStore
	// Path to interface mapping store file
	InterfaceMappingStorePath string
}

// DefaultControllerConfig returns the default configuration for the controller
func DefaultControllerConfig() *ControllerConfig {
	return &ControllerConfig{
		AWSRegion:                      "us-east-1",
		ReconcilePeriod:                5 * time.Minute,
		DetachmentTimeout:              15 * time.Second,
		MaxConcurrentReconciles:        5,
		DefaultDeviceIndex:             1,
		DefaultDeleteOnTermination:     true,
		MaxConcurrentENICleanup:        3,                // Default to 3 concurrent ENI cleanup operations
		MaxCleanupDuration:             30 * time.Minute, // Default to 30 minutes maximum cleanup duration
		CircuitBreakerEnabled:          true,             // Enable circuit breaker by default
		CircuitBreakerFailureThreshold: 5,                // Open circuit after 5 consecutive failures
		CircuitBreakerSuccessThreshold: 3,                // Close circuit after 3 consecutive successes
		CircuitBreakerTimeout:          30 * time.Second, // Wait 30 seconds before trying half-open
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
		DefaultMTU:         0,                        // 0 means use system default
		InterfaceMTUs:      make(map[string]int, 16), // Typical node has 2-8 ENIs
		EnableDPDK:         false,
		DefaultDPDKDriver:  "vfio-pci",
		DPDKResourceNames:  make(map[string]string, 8), // Typical node has few DPDK interfaces
		DPDKBoundInterfaces: make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}, 8), // Typical node has few DPDK-bound interfaces
		DPDKBindingScript:         "/opt/dpdk/dpdk-devbind.py",
		SRIOVDPConfigPath:         "/etc/pcidp/config.json",
		InterfaceMappingStore:     nil, // Will be initialized later
		InterfaceMappingStorePath: "/var/lib/aws-multi-eni-controller/interface-mappings.json",
	}
}

// Validate validates the controller configuration
func (c *ControllerConfig) Validate() error {
	if c.MaxConcurrentReconciles <= 0 {
		return fmt.Errorf("MaxConcurrentReconciles must be positive, got %d", c.MaxConcurrentReconciles)
	}
	if c.DetachmentTimeout <= 0 {
		return fmt.Errorf("DetachmentTimeout must be positive, got %v", c.DetachmentTimeout)
	}
	if c.MaxConcurrentENICleanup <= 0 {
		return fmt.Errorf("MaxConcurrentENICleanup must be positive, got %d", c.MaxConcurrentENICleanup)
	}
	if c.ReconcilePeriod <= 0 {
		return fmt.Errorf("ReconcilePeriod must be positive, got %v", c.ReconcilePeriod)
	}
	if c.MaxCleanupDuration <= 0 {
		return fmt.Errorf("MaxCleanupDuration must be positive, got %v", c.MaxCleanupDuration)
	}
	if c.CircuitBreakerEnabled {
		if c.CircuitBreakerFailureThreshold <= 0 {
			return fmt.Errorf("CircuitBreakerFailureThreshold must be positive when circuit breaker is enabled, got %d", c.CircuitBreakerFailureThreshold)
		}
		if c.CircuitBreakerSuccessThreshold <= 0 {
			return fmt.Errorf("CircuitBreakerSuccessThreshold must be positive when circuit breaker is enabled, got %d", c.CircuitBreakerSuccessThreshold)
		}
		if c.CircuitBreakerTimeout <= 0 {
			return fmt.Errorf("CircuitBreakerTimeout must be positive when circuit breaker is enabled, got %v", c.CircuitBreakerTimeout)
		}
	}
	return nil
}

// Validate validates the ENI manager configuration
func (c *ENIManagerConfig) Validate() error {
	if c.CheckInterval <= 0 {
		return fmt.Errorf("CheckInterval must be positive, got %v", c.CheckInterval)
	}
	if c.InterfaceUpTimeout <= 0 {
		return fmt.Errorf("InterfaceUpTimeout must be positive, got %v", c.InterfaceUpTimeout)
	}
	if c.ENIPattern == "" {
		return fmt.Errorf("ENIPattern cannot be empty")
	}
	return nil
}

// LoadControllerConfig loads controller configuration from environment variables
func LoadControllerConfig() (*ControllerConfig, error) {
	config := DefaultControllerConfig()

	if err := loadBasicConfig(config); err != nil {
		return nil, err
	}

	if err := loadConcurrencyConfig(config); err != nil {
		return nil, err
	}

	if err := loadTimeoutConfig(config); err != nil {
		return nil, err
	}

	if err := loadCircuitBreakerConfig(config); err != nil {
		return nil, err
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return config, nil
}

// loadBasicConfig loads basic configuration from environment variables
func loadBasicConfig(config *ControllerConfig) error {
	// Load AWS region from environment variable
	if region := os.Getenv("AWS_REGION"); region != "" {
		config.AWSRegion = region
	}

	// Load reconcile period from environment variable
	if periodStr := os.Getenv("RECONCILE_PERIOD"); periodStr != "" {
		period, err := time.ParseDuration(periodStr)
		if err != nil {
			return fmt.Errorf("invalid RECONCILE_PERIOD: %w", err)
		}
		config.ReconcilePeriod = period
	}

	// Load default device index from environment variable
	if indexStr := os.Getenv("DEFAULT_DEVICE_INDEX"); indexStr != "" {
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			return fmt.Errorf("invalid DEFAULT_DEVICE_INDEX: %w", err)
		}
		config.DefaultDeviceIndex = index
	}

	// Load default delete on termination setting from environment variable
	if dotStr := os.Getenv("DEFAULT_DELETE_ON_TERMINATION"); dotStr != "" {
		dot, err := strconv.ParseBool(dotStr)
		if err != nil {
			return fmt.Errorf("invalid DEFAULT_DELETE_ON_TERMINATION: %w", err)
		}
		config.DefaultDeleteOnTermination = dot
	}

	return nil
}

// loadConcurrencyConfig loads concurrency-related configuration from environment variables
func loadConcurrencyConfig(config *ControllerConfig) error {
	// Load max concurrent reconciles from environment variable
	if maxStr := os.Getenv("MAX_CONCURRENT_RECONCILES"); maxStr != "" {
		max, err := strconv.Atoi(maxStr)
		if err != nil {
			return fmt.Errorf("invalid MAX_CONCURRENT_RECONCILES: %w", err)
		}
		config.MaxConcurrentReconciles = max
	}

	// Load max concurrent ENI cleanup from environment variable
	if maxStr := os.Getenv("MAX_CONCURRENT_ENI_CLEANUP"); maxStr != "" {
		max, err := strconv.Atoi(maxStr)
		if err != nil {
			return fmt.Errorf("invalid MAX_CONCURRENT_ENI_CLEANUP: %w", err)
		}
		config.MaxConcurrentENICleanup = max
	}

	return nil
}

// loadTimeoutConfig loads timeout-related configuration from environment variables
func loadTimeoutConfig(config *ControllerConfig) error {
	// Load detachment timeout from environment variable
	if timeoutStr := os.Getenv("DETACHMENT_TIMEOUT"); timeoutStr != "" {
		timeout, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("invalid DETACHMENT_TIMEOUT: %w", err)
		}
		config.DetachmentTimeout = timeout
	}

	// Load max cleanup duration from environment variable
	if durationStr := os.Getenv("MAX_CLEANUP_DURATION"); durationStr != "" {
		duration, err := time.ParseDuration(durationStr)
		if err != nil {
			return fmt.Errorf("invalid MAX_CLEANUP_DURATION: %w", err)
		}
		config.MaxCleanupDuration = duration
	}

	return nil
}

// loadCircuitBreakerConfig loads circuit breaker configuration from environment variables
func loadCircuitBreakerConfig(config *ControllerConfig) error {
	if enabledStr := os.Getenv("CIRCUIT_BREAKER_ENABLED"); enabledStr != "" {
		enabled, err := strconv.ParseBool(enabledStr)
		if err != nil {
			return fmt.Errorf("invalid CIRCUIT_BREAKER_ENABLED: %w", err)
		}
		config.CircuitBreakerEnabled = enabled
	}

	if thresholdStr := os.Getenv("CIRCUIT_BREAKER_FAILURE_THRESHOLD"); thresholdStr != "" {
		threshold, err := strconv.Atoi(thresholdStr)
		if err != nil {
			return fmt.Errorf("invalid CIRCUIT_BREAKER_FAILURE_THRESHOLD: %w", err)
		}
		config.CircuitBreakerFailureThreshold = threshold
	}

	if thresholdStr := os.Getenv("CIRCUIT_BREAKER_SUCCESS_THRESHOLD"); thresholdStr != "" {
		threshold, err := strconv.Atoi(thresholdStr)
		if err != nil {
			return fmt.Errorf("invalid CIRCUIT_BREAKER_SUCCESS_THRESHOLD: %w", err)
		}
		config.CircuitBreakerSuccessThreshold = threshold
	}

	if timeoutStr := os.Getenv("CIRCUIT_BREAKER_TIMEOUT"); timeoutStr != "" {
		timeout, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("invalid CIRCUIT_BREAKER_TIMEOUT: %w", err)
		}
		config.CircuitBreakerTimeout = timeout
	}

	return nil
}

// applyFlagOverrides applies command-line flag overrides to the config
func applyFlagOverrides(config *ENIManagerConfig, checkInterval *time.Duration, primaryIface *string, debugMode *bool, eniPattern *string, ignoreList *string) {
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
}

// applyEnvOverrides applies environment variable overrides to the config
func applyEnvOverrides(config *ENIManagerConfig) {
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
}

// loadMTUConfig loads MTU configuration from environment variables
func loadMTUConfig(config *ENIManagerConfig) {
	// Load default MTU from environment variable
	if mtuStr := os.Getenv("DEFAULT_MTU"); mtuStr != "" {
		if mtu, err := strconv.Atoi(mtuStr); err == nil {
			config.DefaultMTU = mtu
		}
	}

	// Load interface-specific MTUs from environment variable
	loadInterfaceMTUs(config)
}

// loadDPDKConfig loads DPDK configuration from environment variables
func loadDPDKConfig(config *ENIManagerConfig) {
	// Load DPDK enable flag from environment variable
	if enableStr := os.Getenv("ENABLE_DPDK"); enableStr != "" {
		if enable, err := strconv.ParseBool(enableStr); err == nil {
			config.EnableDPDK = enable
		}
	}

	// Load default DPDK driver from environment variable
	if driver := os.Getenv("DEFAULT_DPDK_DRIVER"); driver != "" {
		config.DefaultDPDKDriver = driver
	}

	// Load DPDK binding script path from environment variable
	if scriptPath := os.Getenv("DPDK_BINDING_SCRIPT"); scriptPath != "" {
		config.DPDKBindingScript = scriptPath
	}

	// Load SRIOV device plugin config path from environment variable
	if configPath := os.Getenv("SRIOV_DP_CONFIG_PATH"); configPath != "" {
		config.SRIOVDPConfigPath = configPath
	}

	// Load interface-specific DPDK resource names from environment variable
	// Format: "eth1:intel_sriov_netdevice_1,eth2:intel_sriov_netdevice_2"
	if resourceMapStr := os.Getenv("DPDK_RESOURCE_NAMES"); resourceMapStr != "" {
		pairs := splitCSV(resourceMapStr)
		for _, pair := range pairs {
			parts := strings.Split(pair, ":")
			if len(parts) == 2 {
				ifaceName := parts[0]
				resourceName := parts[1]
				config.DPDKResourceNames[ifaceName] = resourceName
			}
		}
	}
}

// loadInterfaceMappingConfig loads interface mapping configuration from environment variables
func loadInterfaceMappingConfig(config *ENIManagerConfig) {
	// Load interface mapping store path from environment variable
	if storePath := os.Getenv("INTERFACE_MAPPING_STORE_PATH"); storePath != "" {
		config.InterfaceMappingStorePath = storePath
	}

	// Initialize the interface mapping store
	store, err := mapping.NewInterfaceMappingStore(config.InterfaceMappingStorePath)
	if err != nil {
		fmt.Printf("Warning: Failed to initialize interface mapping store: %v\n", err)
		// Continue without the store, it will be initialized later
	} else {
		config.InterfaceMappingStore = store
	}
}

// loadInterfaceMTUs loads interface-specific MTUs from environment variables
func loadInterfaceMTUs(config *ENIManagerConfig) {
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
}

// LoadENIManagerConfigFromFlags loads ENI manager configuration from command-line flags
func LoadENIManagerConfigFromFlags(checkInterval *time.Duration, primaryIface *string, debugMode *bool, eniPattern *string, ignoreList *string) *ENIManagerConfig {
	config := DefaultENIManagerConfig()

	// Apply command-line flag overrides
	applyFlagOverrides(config, checkInterval, primaryIface, debugMode, eniPattern, ignoreList)

	// Apply environment variable overrides
	applyEnvOverrides(config)

	// Load MTU configuration
	loadMTUConfig(config)

	// Load DPDK configuration
	loadDPDKConfig(config)

	// Load interface mapping configuration
	loadInterfaceMappingConfig(config)

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
