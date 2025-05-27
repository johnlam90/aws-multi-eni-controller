// Package main implements the AWS Multi-ENI Manager, which is responsible for
// bringing up secondary network interfaces on AWS EC2 instances.
//
// This refactored version uses a modular architecture with separate packages for:
// - DPDK operations (pkg/eni-manager/dpdk)
// - SR-IOV management (pkg/eni-manager/sriov)
// - Network interface operations (pkg/eni-manager/network)
// - Kubernetes API interactions (pkg/eni-manager/kubernetes)
// - Main coordination logic (pkg/eni-manager/coordinator)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/coordinator"
)

var (
	// Command line flags
	checkInterval = flag.Duration("check-interval", 30*time.Second, "Interval between interface checks")
	primaryIface  = flag.String("primary-interface", "", "Primary interface name to ignore (if empty, will auto-detect)")
	debugMode     = flag.Bool("debug", false, "Enable debug logging")
	eniPattern    = flag.String("eni-pattern", "^(eth|ens|eni|en)[0-9]+", "Regex pattern to identify ENI interfaces")
	ignoreList    = flag.String("ignore-interfaces", "tunl0,gre0,gretap0,erspan0,ip_vti0,ip6_vti0,sit0,ip6tnl0,ip6gre0", "Comma-separated list of interfaces to ignore")
	useNetlink    = flag.Bool("use-netlink", true, "Use netlink subscription instead of polling (recommended)")
	version       = "v1.3.4" // Updated version for refactored architecture
)

func main() {
	log.Printf("AWS Multi-ENI Manager starting (version %s) - Refactored Architecture", version)

	// Parse command line flags
	flag.Parse()

	// Setup configuration
	cfg := setupConfig()

	// Setup context with signal handling
	ctx, cancel := setupContext()
	defer cancel()

	// Get node name
	nodeName := getNodeName()
	if nodeName == "" {
		log.Fatal("Failed to determine node name")
	}
	cfg.NodeName = nodeName

	// Create and start the coordinator manager
	manager, err := coordinator.NewManager(cfg)
	if err != nil {
		log.Fatalf("Failed to create coordinator manager: %v", err)
	}

	log.Printf("Starting ENI Manager Coordinator for node %s", nodeName)
	if err := manager.Start(ctx); err != nil {
		log.Fatalf("ENI Manager Coordinator failed: %v", err)
	}

	log.Printf("ENI Manager Coordinator shutdown complete")
}

// setupConfig initializes and returns the configuration for the ENI Manager
func setupConfig() *config.ENIManagerConfig {
	// Load configuration from command-line flags and environment variables
	cfg := config.LoadENIManagerConfigFromFlags(checkInterval, primaryIface, debugMode, eniPattern, ignoreList)

	log.Printf("Configuration: check interval=%s, debug=%v, interface up timeout=%s, use netlink=%v",
		cfg.CheckInterval, cfg.DebugMode, cfg.InterfaceUpTimeout, *useNetlink)
	log.Printf("ENI pattern: %s, Ignored interfaces: %v",
		cfg.ENIPattern, cfg.IgnoreInterfaces)
	log.Printf("Default MTU: %d", cfg.DefaultMTU)

	// Auto-detect primary interface if not specified
	if cfg.PrimaryInterface == "" {
		detected, err := detectPrimaryInterface()
		if err != nil {
			log.Printf("Warning: Failed to auto-detect primary interface: %v", err)
			log.Printf("Will not ignore any interfaces")
		} else {
			cfg.PrimaryInterface = detected
			log.Printf("Auto-detected primary interface: %s", cfg.PrimaryInterface)
		}
	} else {
		log.Printf("Using specified primary interface: %s", cfg.PrimaryInterface)
	}

	// Set DPDK and SR-IOV configuration paths from environment
	cfg.DPDKBindingScript = getEnvOrDefault("DPDK_BINDING_SCRIPT", "/opt/dpdk/dpdk-devbind.py")
	cfg.SRIOVDPConfigPath = getEnvOrDefault("SRIOV_DP_CONFIG_PATH", "/etc/pcidp/config.json")

	// Enable DPDK if environment variable is set
	if os.Getenv("ENABLE_DPDK") == "true" {
		cfg.EnableDPDK = true
		log.Printf("DPDK enabled via environment variable")
	}

	return cfg
}

// setupContext creates a context that will be canceled on SIGINT or SIGTERM
func setupContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down", sig)
		cancel()
	}()

	return ctx, cancel
}

// getNodeName gets the node name from the environment or hostname
func getNodeName() string {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		var err error
		nodeName, err = os.Hostname()
		if err != nil {
			log.Printf("Warning: Failed to get hostname: %v", err)
			return ""
		}
		log.Printf("Using hostname as node name: %s", nodeName)
	} else {
		log.Printf("Using NODE_NAME from environment: %s", nodeName)
	}
	return nodeName
}

// detectPrimaryInterface attempts to detect the primary network interface
func detectPrimaryInterface() (string, error) {
	// This is a simplified implementation
	// In production, you'd want more sophisticated detection logic

	// Check for common primary interface names
	commonNames := []string{"eth0", "ens5", "eni0"}

	for _, name := range commonNames {
		if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", name)); err == nil {
			return name, nil
		}
	}

	return "", fmt.Errorf("could not detect primary interface")
}

// getEnvOrDefault gets an environment variable or returns a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
