// Package main implements the AWS Multi-ENI Manager, which is responsible for
// bringing up secondary network interfaces on AWS EC2 instances.
//
// The ENI Manager can run in two modes:
// 1. Polling mode: periodically checks for network interfaces that are in the DOWN state
// 2. Netlink subscription mode: subscribes to netlink events and reacts immediately to interface changes
//
// When it finds interfaces that need to be brought up, it attempts to do so using either
// the netlink library or the 'ip' command as a fallback. This ensures that secondary ENIs
// attached by the controller are properly configured and ready for use.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/mapping"
	vnetlink "github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	// Command line flags
	checkInterval = flag.Duration("check-interval", 30*time.Second, "Interval between interface checks when using polling mode")
	primaryIface  = flag.String("primary-interface", "", "Primary interface name to ignore (if empty, will auto-detect)")
	debugMode     = flag.Bool("debug", false, "Enable debug logging")
	eniPattern    = flag.String("eni-pattern", "^(eth|ens|eni|en)[0-9]+", "Regex pattern to identify ENI interfaces")
	ignoreList    = flag.String("ignore-interfaces", "tunl0,gre0,gretap0,erspan0,ip_vti0,ip6_vti0,sit0,ip6tnl0,ip6gre0", "Comma-separated list of interfaces to ignore")
	useNetlink    = flag.Bool("use-netlink", true, "Use netlink subscription instead of polling (recommended)")
	version       = "v1.2.7" // Version of the ENI Manager

	// Map to track which interfaces are already mapped to ENIs
	usedInterfaces = make(map[string]string)

	// Global configuration
	globalConfig *config.ENIManagerConfig

	// Mutex for DPDK operations to prevent race conditions
	dpdkMutex sync.Mutex

	// Map to track ongoing DPDK operations by PCI address
	// This helps prevent concurrent operations on the same device
	dpdkOperations = make(map[string]bool)
	dpdkOpsMutex   sync.Mutex
)

// setupConfig initializes and returns the configuration for the ENI Manager
func setupConfig() *config.ENIManagerConfig {
	flag.Parse()

	// Load configuration from command-line flags and environment variables
	cfg := config.LoadENIManagerConfigFromFlags(checkInterval, primaryIface, debugMode, eniPattern, ignoreList)

	log.Printf("ENI Manager starting (version %s)", version)
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
			log.Printf("Will use default MTU only")
			return ""
		}
		log.Printf("Using hostname as node name: %s", nodeName)
	} else {
		log.Printf("Using NODE_NAME from environment: %s", nodeName)
	}
	return nodeName
}

// createK8sClientset creates a Kubernetes clientset
func createK8sClientset() (*kubernetes.Clientset, error) {
	// Try to create a Kubernetes client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	return clientset, nil
}

// performInitialNodeENIUpdate performs the initial update of NodeENI resources
func performInitialNodeENIUpdate(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, cfg *config.ENIManagerConfig) bool {
	// Get all NodeENI resources
	nodeENIs, err := getNodeENIResources(ctx, clientset)
	if err != nil {
		log.Printf("Error getting NodeENI resources: %v", err)
		return false
	}

	// Do an initial update for MTU
	updateMTUFromNodeENI(ctx, clientset, nodeName, cfg)

	// Add a startup delay to allow system to stabilize
	startupDelay := 15 * time.Second
	log.Printf("Waiting %v for system to stabilize before DPDK/SR-IOV operations", startupDelay)
	time.Sleep(startupDelay)

	// Do an initial update for DPDK binding with startup-specific logic
	if cfg.EnableDPDK {
		log.Printf("Performing initial DPDK binding check after startup (important for node reboots and Helm upgrades)")
		updateDPDKBindingFromNodeENIWithStartup(nodeName, cfg, nodeENIs, true)
	} else {
		// Also check for non-DPDK SR-IOV configuration updates during startup
		log.Printf("Performing initial non-DPDK SR-IOV configuration check")
		updateSRIOVConfigForAllInterfacesWithStartup(nodeName, cfg, nodeENIs, true)
	}

	return true
}

// startNodeENIUpdaterLoop starts the main loop for updating NodeENI resources
func startNodeENIUpdaterLoop(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, cfg *config.ENIManagerConfig) {
	// Initial ticker with a short interval for faster startup
	initialTicker := time.NewTicker(5 * time.Second)
	defer initialTicker.Stop()

	// Regular ticker for ongoing updates
	regularTicker := time.NewTicker(1 * time.Minute)
	defer regularTicker.Stop()

	// Flag to track if we've done the initial update
	initialUpdateDone := performInitialNodeENIUpdate(ctx, clientset, nodeName, cfg)

	// Track previously seen NodeENI resources to detect deletions
	// Initialize with current NodeENI resources to avoid missing deletions during startup
	var previousNodeENIs map[string]bool
	if initialUpdateDone {
		// Get initial NodeENI resources for deletion tracking
		if nodeENIs, err := getNodeENIResources(ctx, clientset); err == nil {
			previousNodeENIs = make(map[string]bool)
			for _, nodeENI := range nodeENIs {
				previousNodeENIs[nodeENI.Name] = true
			}
			log.Printf("Initialized deletion tracking with %d NodeENI resources", len(previousNodeENIs))
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-initialTicker.C:
			// Only process this if we haven't done the initial update yet
			if !initialUpdateDone {
				initialUpdateDone = performInitialNodeENIUpdate(ctx, clientset, nodeName, cfg)

				// Stop the initial ticker once we've done the initial update
				if initialUpdateDone {
					initialTicker.Stop()

					// Initialize deletion tracking after successful initial update
					if nodeENIs, err := getNodeENIResources(ctx, clientset); err == nil {
						previousNodeENIs = make(map[string]bool)
						for _, nodeENI := range nodeENIs {
							previousNodeENIs[nodeENI.Name] = true
						}
						log.Printf("Initialized deletion tracking with %d NodeENI resources", len(previousNodeENIs))
					}
				}
			}
		case <-regularTicker.C:
			// Get all NodeENI resources
			nodeENIs, err := getNodeENIResources(ctx, clientset)
			if err != nil {
				log.Printf("Error getting NodeENI resources: %v", err)
				continue
			}

			// Detect deleted NodeENI resources and clean up SR-IOV configuration
			currentNodeENIs := make(map[string]bool)
			for _, nodeENI := range nodeENIs {
				currentNodeENIs[nodeENI.Name] = true
			}

			log.Printf("NodeENI deletion check: found %d current NodeENIs, tracking %d previous NodeENIs",
				len(currentNodeENIs), len(previousNodeENIs))

			// Check for deleted NodeENI resources
			if previousNodeENIs != nil {
				deletedCount := 0
				for nodeENIName := range previousNodeENIs {
					if !currentNodeENIs[nodeENIName] {
						deletedCount++
						log.Printf("Detected deleted NodeENI: %s, cleaning up SR-IOV configuration", nodeENIName)
						if err := cleanupSRIOVConfigForNodeENI(nodeENIName, cfg); err != nil {
							log.Printf("Warning: Failed to cleanup SR-IOV config for deleted NodeENI %s: %v", nodeENIName, err)
						} else {
							log.Printf("Successfully cleaned up SR-IOV config for deleted NodeENI: %s", nodeENIName)
						}
					}
				}
				if deletedCount == 0 {
					log.Printf("No deleted NodeENI resources detected")
				}
			} else {
				log.Printf("Previous NodeENI tracking not initialized yet, skipping deletion check")
			}

			// Update the tracking map
			previousNodeENIs = currentNodeENIs

			// Update MTU values from NodeENI resources
			updateMTUFromNodeENI(ctx, clientset, nodeName, cfg)

			// Update DPDK binding from NodeENI resources
			if cfg.EnableDPDK {
				updateDPDKBindingFromNodeENI(nodeName, cfg, nodeENIs)
			}

			// Update SR-IOV configuration for all interfaces
			updateSRIOVConfigForAllInterfaces(nodeName, cfg, nodeENIs)
		}
	}
}

// startMTUUpdaterLoop starts a loop to periodically check and update MTU values
func startMTUUpdaterLoop(ctx context.Context, cfg *config.ENIManagerConfig) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check all interfaces and update MTU if needed
			if err := updateAllInterfacesMTU(cfg); err != nil {
				log.Printf("Failed to update interface MTUs: %v", err)
			}
		}
	}
}

// setupNodeENIUpdater sets up the updater for NodeENI resources
func setupNodeENIUpdater(ctx context.Context, nodeName string, cfg *config.ENIManagerConfig) {
	log.Printf("Starting NodeENI updater with default MTU: %d, DPDK enabled: %v", cfg.DefaultMTU, cfg.EnableDPDK)

	// Skip if no node name
	if nodeName == "" {
		log.Printf("No node name available, skipping NodeENI updater")
		return
	}

	// Create a Kubernetes clientset
	clientset, err := createK8sClientset()
	if err != nil {
		log.Printf("Warning: %v", err)
		log.Printf("Will use default MTU only")
		return
	}

	// Start a goroutine to periodically update values from NodeENI resources
	go startNodeENIUpdaterLoop(ctx, clientset, nodeName, cfg)

	// Set up a ticker to periodically check and update MTU values
	go startMTUUpdaterLoop(ctx, cfg)
}

// runENIManager runs the ENI Manager in either netlink or polling mode
func runENIManager(ctx context.Context, cfg *config.ENIManagerConfig) {
	// Use netlink subscription if enabled, otherwise use polling
	if *useNetlink {
		log.Printf("Using netlink subscription mode")
		if err := runNetlinkMode(ctx, cfg); err != nil {
			log.Printf("Error in netlink mode: %v, falling back to polling mode", err)
			runPollingMode(ctx, cfg)
		}
	} else {
		log.Printf("Using polling mode")
		runPollingMode(ctx, cfg)
	}
}

func main() {
	// Setup configuration
	cfg := setupConfig()

	// Set the global configuration
	globalConfig = cfg

	// Ensure SR-IOV device plugin config file exists
	ensureSRIOVConfigExists(cfg)

	// Setup context with signal handling
	ctx, cancel := setupContext()
	defer cancel()

	// Get node name
	nodeName := getNodeName()

	// Setup NodeENI updater (handles both MTU and DPDK)
	setupNodeENIUpdater(ctx, nodeName, cfg)

	// Run the ENI Manager
	runENIManager(ctx, cfg)
}

// runNetlinkMode runs the ENI Manager in netlink subscription mode
// This is a simplified implementation that uses a goroutine to monitor for interface changes
func runNetlinkMode(ctx context.Context, cfg *config.ENIManagerConfig) error {
	// Initial check to bring up any interfaces that are already down
	if err := checkAndBringUpInterfaces(cfg); err != nil {
		log.Printf("Initial interface check failed: %v", err)
		// Continue anyway, we'll catch changes via our monitoring
	}

	log.Printf("Starting netlink subscription mode (simplified implementation)")

	// Create a WaitGroup to wait for the goroutine to finish
	var wg sync.WaitGroup
	wg.Add(1)

	// Start a goroutine to monitor for interface changes
	go func() {
		defer wg.Done()
		monitorInterfaces(ctx, cfg)
	}()

	// Wait for the context to be canceled
	<-ctx.Done()
	log.Printf("Context canceled, waiting for interface monitor to finish")
	wg.Wait()
	return nil
}

// monitorInterfaces monitors for interface changes using a more frequent polling approach
// This is a simplified implementation that doesn't use netlink subscription directly
func monitorInterfaces(ctx context.Context, cfg *config.ENIManagerConfig) {
	// Use a shorter interval for more responsive monitoring
	monitorInterval := 2 * time.Second

	log.Printf("Interface monitor started with interval: %v", monitorInterval)

	// Keep track of interface states to detect changes
	interfaceStates := make(map[string]bool) // map[ifaceName]isUp

	// Initial scan to populate the interface states
	// Create a cache for interface filtering
	interfaceCache := make(map[string]bool) // map[ifaceName]isENI

	// Do an initial check for interface changes to populate the states
	checkInterfaceChanges(interfaceStates, cfg, interfaceCache)

	// Monitor for changes
	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Interface monitor shutting down")
			return
		case <-ticker.C:
			// Check for interface changes
			changed := checkInterfaceChanges(interfaceStates, cfg, interfaceCache)
			if changed && cfg.DebugMode {
				log.Printf("Detected interface changes")
			}
		}
	}
}

// isENICached checks if an interface is an AWS ENI using a cache to avoid repeated regex compilation
func isENICached(ifaceName string, cfg *config.ENIManagerConfig, cache map[string]bool) bool {
	// Check cache first
	if isENI, ok := cache[ifaceName]; ok {
		return isENI
	}

	// Not in cache, compute and store result
	isENI := isAWSENI(ifaceName, cfg)
	cache[ifaceName] = isENI
	return isENI
}

// checkInterfaceChanges checks for interface changes and brings up interfaces as needed
// Uses a combined approach to reduce redundant operations
func checkInterfaceChanges(states map[string]bool, cfg *config.ENIManagerConfig, cache map[string]bool) bool {
	links, err := vnetlink.LinkList()
	if err != nil {
		log.Printf("Error listing interfaces: %v", err)
		return false
	}

	changed := false

	// Create a new map to detect removed interfaces
	newStates := make(map[string]bool)

	for _, link := range links {
		ifaceName := link.Attrs().Name

		// Skip loopback and primary interface
		if ifaceName == "lo" || ifaceName == cfg.PrimaryInterface {
			continue
		}

		// Skip interfaces that don't match our ENI pattern or are in the ignore list
		if !isENICached(ifaceName, cfg, cache) {
			continue
		}

		// Check if interface is UP
		isUp := link.Attrs().OperState == vnetlink.OperUp
		newStates[ifaceName] = isUp

		prevState, exists := states[ifaceName]

		// If this is a new interface or its state has changed
		if !exists || prevState != isUp {
			changed = true

			// If the interface is DOWN, bring it up
			if !isUp {
				log.Printf("Monitor detected DOWN ENI interface: %s", ifaceName)
				if err := bringUpInterface(link, cfg); err != nil {
					log.Printf("Error bringing up interface %s: %v", ifaceName, err)
				}
			} else if cfg.DebugMode {
				log.Printf("Monitor detected new UP ENI interface: %s", ifaceName)
			}
		}
	}

	// Update the states map - detect removed interfaces
	for k := range states {
		if _, exists := newStates[k]; !exists {
			delete(states, k) // Interface was removed
			changed = true
			if cfg.DebugMode {
				log.Printf("Interface removed: %s", k)
			}
		}
	}

	// Update the states map with current values
	for k, v := range newStates {
		states[k] = v
	}

	return changed
}

// runPollingMode runs the ENI Manager in polling mode
func runPollingMode(ctx context.Context, cfg *config.ENIManagerConfig) {
	// Run the main polling loop
	wait.Until(func() {
		if err := checkAndBringUpInterfaces(cfg); err != nil {
			log.Printf("Error checking interfaces: %v", err)
		}
	}, cfg.CheckInterval, ctx.Done())
}

// detectPrimaryInterface attempts to detect the primary network interface
// It uses the default route to determine which interface is the primary one
func detectPrimaryInterface() (string, error) {
	// Get the default route
	routes, err := vnetlink.RouteList(nil, unix.AF_INET)
	if err != nil {
		return "", fmt.Errorf("failed to get routes: %v", err)
	}

	// Find the default route (0.0.0.0/0)
	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() == "0.0.0.0/0" {
			if route.LinkIndex > 0 {
				// Get the interface by index
				link, err := vnetlink.LinkByIndex(route.LinkIndex)
				if err != nil {
					return "", fmt.Errorf("failed to get link by index %d: %v", route.LinkIndex, err)
				}
				return link.Attrs().Name, nil
			}
		}
	}

	return "", fmt.Errorf("no default route found")
}

// isAWSENI checks if an interface is an AWS ENI based on the configured pattern
// and ignore list
func isAWSENI(ifaceName string, cfg *config.ENIManagerConfig) bool {
	// Check if the interface is in the ignore list
	for _, ignoredIface := range cfg.IgnoreInterfaces {
		if ifaceName == ignoredIface {
			return false
		}
	}

	// Check if the interface matches the ENI pattern
	pattern := regexp.MustCompile(cfg.ENIPattern)
	return pattern.MatchString(ifaceName)
}

// checkAndBringUpInterfaces checks for DOWN interfaces and brings them up
func checkAndBringUpInterfaces(cfg *config.ENIManagerConfig) error {
	// Get all network interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %v", err)
	}

	for _, link := range links {
		ifaceName := link.Attrs().Name

		// Skip loopback and primary interface
		if ifaceName == "lo" || ifaceName == cfg.PrimaryInterface {
			continue
		}

		// Skip interfaces that don't match our ENI pattern or are in the ignore list
		if !isAWSENI(ifaceName, cfg) {
			if cfg.DebugMode {
				log.Printf("Skipping non-ENI interface: %s", ifaceName)
			}
			continue
		}

		// Check if interface is DOWN
		if link.Attrs().OperState == vnetlink.OperDown {
			log.Printf("Found DOWN ENI interface: %s", ifaceName)

			if err := bringUpInterface(link, cfg); err != nil {
				log.Printf("Error bringing up interface %s: %v", ifaceName, err)
				continue
			}
		} else if cfg.DebugMode {
			log.Printf("ENI interface %s is already UP", ifaceName)
		}
	}

	return nil
}

// getNodeENIResources gets all NodeENI resources
func getNodeENIResources(ctx context.Context, clientset *kubernetes.Clientset) ([]networkingv1alpha1.NodeENI, error) {
	// List all NodeENI resources
	nodeENIList, err := clientset.CoreV1().RESTClient().
		Get().
		AbsPath("/apis/networking.k8s.aws/v1alpha1/nodeenis").
		Do(ctx).
		Raw()

	if err != nil {
		return nil, fmt.Errorf("error listing NodeENI resources: %v", err)
	}

	// Parse the response
	var nodeENIs struct {
		Items []networkingv1alpha1.NodeENI `json:"items"`
	}
	if err := json.Unmarshal(nodeENIList, &nodeENIs); err != nil {
		return nil, fmt.Errorf("error parsing NodeENI list: %v", err)
	}

	return nodeENIs.Items, nil
}

// updateDPDKBindingFromNodeENI updates DPDK binding from NodeENI resources
func updateDPDKBindingFromNodeENI(nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI) {
	updateDPDKBindingFromNodeENIWithStartup(nodeName, cfg, nodeENIs, false)
}

// updateDPDKBindingFromNodeENIWithStartup updates DPDK binding from NodeENI resources with startup-specific logic
func updateDPDKBindingFromNodeENIWithStartup(nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI, isStartup bool) {
	if isStartup {
		log.Printf("Updating DPDK binding from NodeENI resources for node %s (STARTUP MODE - will force SR-IOV restart if needed)", nodeName)
	} else {
		log.Printf("Updating DPDK binding from NodeENI resources for node %s", nodeName)
	}

	// First, check if we have any interfaces that need to be unbound from DPDK
	// This can happen if a NodeENI resource was deleted or DPDK was disabled
	checkForDPDKUnbinding(nodeName, cfg, nodeENIs)

	// Track if we found any existing DPDK-bound devices during startup
	foundExistingDPDKDevices := false

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIs {
		if isStartup && nodeENI.Spec.EnableDPDK && nodeENI.Spec.DPDKResourceName != "" {
			// Check if this NodeENI has existing DPDK-bound devices
			if hasExistingDPDKBinding(nodeENI, nodeName, cfg) {
				foundExistingDPDKDevices = true
			}
		}
		processDPDKBindingForNodeENI(nodeENI, nodeName, cfg)
	}

	// During startup, if we found existing DPDK devices, force an SR-IOV device plugin restart
	if isStartup && foundExistingDPDKDevices {
		log.Printf("STARTUP: Found existing DPDK-bound devices, forcing SR-IOV device plugin restart to refresh inventory")
		forceRestartSRIOVDevicePlugin(cfg)
	}
}

// hasExistingDPDKBinding checks if a NodeENI has existing DPDK-bound devices
func hasExistingDPDKBinding(nodeENI networkingv1alpha1.NodeENI, nodeName string, cfg *config.ENIManagerConfig) bool {
	// Check if any attachments for this node have DPDK binding information
	for _, attachment := range nodeENI.Status.Attachments {
		if attachment.NodeID == nodeName {
			// Check if this attachment has DPDK-related status
			if attachment.DPDKBound && attachment.DPDKResourceName != "" {
				// Try to find the PCI address for this ENI
				pciAddr := ""

				// First check if we have it in our bound interfaces map
				for pci, boundInterface := range cfg.DPDKBoundInterfaces {
					if boundInterface.ENIID == attachment.ENIID {
						pciAddr = pci
						break
					}
				}

				// If we found a PCI address, verify the driver
				if pciAddr != "" {
					if driver, err := getCurrentDriverForPCI(pciAddr); err == nil {
						if driver == "vfio-pci" || driver == "igb_uio" || driver == "uio_pci_generic" {
							log.Printf("STARTUP: Found existing DPDK binding for ENI %s (PCI: %s, Resource: %s)",
								attachment.ENIID, pciAddr, attachment.DPDKResourceName)
							return true
						}
					}
				} else {
					// If no PCI address found but DPDKBound is true, assume it exists
					log.Printf("STARTUP: Found existing DPDK binding for ENI %s (Resource: %s, PCI unknown)",
						attachment.ENIID, attachment.DPDKResourceName)
					return true
				}
			}
		}
	}
	return false
}

// forceRestartSRIOVDevicePlugin forces a restart of the SR-IOV device plugin during startup with verification
func forceRestartSRIOVDevicePlugin(cfg *config.ENIManagerConfig) {
	if cfg.SRIOVDPConfigPath == "" {
		log.Printf("STARTUP: SR-IOV device plugin config path not configured, skipping forced restart")
		return
	}

	manager := NewSRIOVConfigManager(cfg.SRIOVDPConfigPath)

	log.Printf("STARTUP: Forcing SR-IOV device plugin restart with verification to refresh device inventory after pod restart")

	// Use enhanced restart with verification and retry logic
	if err := manager.restartDevicePluginWithVerification(); err != nil {
		log.Printf("STARTUP: Warning - Failed to force restart SR-IOV device plugin with verification: %v", err)
		// Fall back to basic restart as last resort
		if basicErr := manager.restartDevicePlugin(); basicErr != nil {
			log.Printf("STARTUP: Warning - Basic restart also failed: %v", basicErr)
		} else {
			log.Printf("STARTUP: Basic restart succeeded as fallback")
		}
	} else {
		log.Printf("STARTUP: Successfully forced restart of SR-IOV device plugin with verification")
	}
}

// updateSRIOVConfigForAllInterfacesWithStartup updates SR-IOV configuration with startup-specific logic
func updateSRIOVConfigForAllInterfacesWithStartup(nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI, isStartup bool) {
	logStartupMode(nodeName, isStartup)

	// Track if we found any existing SR-IOV resources during startup
	foundExistingSRIOVResources := false

	// Get all network interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		log.Printf("Error listing interfaces for SR-IOV config update: %v", err)
		return
	}

	for _, link := range links {
		ifaceName := link.Attrs().Name

		// Skip interfaces that should not be processed
		if shouldSkipInterface(ifaceName, cfg) {
			continue
		}

		// Process this interface for SR-IOV configuration
		if processInterfaceForSRIOV(ifaceName, nodeName, cfg, nodeENIs, isStartup) {
			foundExistingSRIOVResources = true
		}
	}

	// Handle startup-specific restart logic
	handleStartupSRIOVRestart(isStartup, foundExistingSRIOVResources, cfg)
}

// logStartupMode logs the appropriate message based on startup mode
func logStartupMode(nodeName string, isStartup bool) {
	if isStartup {
		log.Printf("Checking SR-IOV configuration for all interfaces on node %s (STARTUP MODE - will force restart if SR-IOV resources found)", nodeName)
	} else {
		log.Printf("Checking SR-IOV configuration for all interfaces on node %s", nodeName)
	}
}

// shouldSkipInterface determines if an interface should be skipped for SR-IOV processing
func shouldSkipInterface(ifaceName string, cfg *config.ENIManagerConfig) bool {
	// Skip loopback and primary interface
	if ifaceName == "lo" || ifaceName == cfg.PrimaryInterface {
		return true
	}

	// Skip interfaces that don't match our ENI pattern
	if !isAWSENI(ifaceName, cfg) {
		return true
	}

	// Check if this is a DPDK-bound interface (skip for non-DPDK reconciliation)
	if isInterfaceDPDKBound(ifaceName, cfg) {
		return true
	}

	return false
}

// processInterfaceForSRIOV processes a single interface for SR-IOV configuration
// Returns true if SR-IOV resources were found during startup
func processInterfaceForSRIOV(ifaceName, nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI, isStartup bool) bool {
	log.Printf("Checking SR-IOV configuration for non-DPDK interface %s", ifaceName)

	// Get PCI address for this interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err != nil {
		log.Printf("Warning: Failed to get PCI address for interface %s: %v", ifaceName, err)
		return false
	}

	// Check if there's a NodeENI resource for this interface
	nodeENI := findNodeENIResourceForInterface(ifaceName, pciAddress, nodeName, nodeENIs)
	if nodeENI == nil {
		return false
	}

	// Skip if this NodeENI doesn't have SR-IOV resource name
	if nodeENI.Spec.DPDKResourceName == "" {
		return false
	}

	// Find the corresponding attachment for this interface
	targetAttachment := findTargetAttachment(ifaceName, pciAddress, nodeName, *nodeENI)
	if targetAttachment != nil {
		// Process SR-IOV configuration for this attachment
		processSRIOVConfigForAttachment(ifaceName, pciAddress, cfg, *nodeENI, *targetAttachment)
	}

	// Return true if we found SR-IOV resources during startup
	return isStartup
}

// findTargetAttachment finds the target attachment for a given interface
func findTargetAttachment(ifaceName, pciAddress, nodeName string, nodeENI networkingv1alpha1.NodeENI) *networkingv1alpha1.ENIAttachment {
	for _, attachment := range nodeENI.Status.Attachments {
		if attachment.NodeID == nodeName {
			// Try to match by interface name or PCI address
			targetInterface, attachmentPCI := resolveInterfaceAndPCI(nodeENI, attachment)
			if targetInterface == ifaceName || attachmentPCI == pciAddress {
				return &attachment
			}
		}
	}
	return nil
}

// handleStartupSRIOVRestart handles the startup-specific restart logic
func handleStartupSRIOVRestart(isStartup, foundExistingSRIOVResources bool, cfg *config.ENIManagerConfig) {
	if isStartup && foundExistingSRIOVResources {
		log.Printf("STARTUP: Found existing SR-IOV resources, forcing device plugin restart to refresh inventory")
		forceRestartSRIOVDevicePlugin(cfg)
	}
}

// isInterfaceDPDKBound checks if an interface is currently bound to DPDK
func isInterfaceDPDKBound(ifaceName string, cfg *config.ENIManagerConfig) bool {
	// Check if this interface is in our DPDK bound interfaces map
	for _, boundInterface := range cfg.DPDKBoundInterfaces {
		if boundInterface.IfaceName == ifaceName {
			return true
		}
	}
	return false
}

// findNodeENIResourceForInterface finds a NodeENI resource that matches the given interface
func findNodeENIResourceForInterface(ifaceName, pciAddress, nodeName string, nodeENIs []networkingv1alpha1.NodeENI) *networkingv1alpha1.NodeENI {
	for _, nodeENI := range nodeENIs {
		// Check if this NodeENI has attachments for our node
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.NodeID == nodeName {
				// Try to match by interface name or PCI address
				targetInterface, attachmentPCI := resolveInterfaceAndPCI(nodeENI, attachment)
				if targetInterface == ifaceName || attachmentPCI == pciAddress {
					return &nodeENI
				}
			}
		}
	}
	return nil
}

// checkForDPDKUnbinding checks if any interfaces need to be unbound from DPDK
func checkForDPDKUnbinding(nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI) {
	// Create a map of all ENI IDs that should be bound to DPDK
	shouldBeBound := make(map[string]bool)

	// Also track which PCI addresses should be bound to DPDK
	shouldBeBoundPCI := make(map[string]bool)

	// Populate the map with ENI IDs from NodeENI resources
	for _, nodeENI := range nodeENIs {
		// Skip if DPDK is not enabled for this NodeENI
		if !nodeENI.Spec.EnableDPDK {
			continue
		}

		// Skip if this NodeENI doesn't apply to our node
		// For now, just check if the hostname matches
		if nodeENI.Spec.NodeSelector != nil {
			if hostname, ok := nodeENI.Spec.NodeSelector["kubernetes.io/hostname"]; ok && hostname != nodeName {
				continue
			}
		}

		// Add all ENI IDs from this NodeENI to the map
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.NodeID == nodeName {
				shouldBeBound[attachment.ENIID] = true

				// Try to find the PCI address for this ENI ID
				for pciAddr, boundInterface := range cfg.DPDKBoundInterfaces {
					if boundInterface.ENIID == attachment.ENIID {
						shouldBeBoundPCI[pciAddr] = true
						break
					}
				}
			}
		}
	}

	log.Printf("DPDK unbinding check - Found %d ENIs that should be bound to DPDK", len(shouldBeBound))

	// Check all bound interfaces
	for pciAddr, boundInterface := range cfg.DPDKBoundInterfaces {
		// Skip if this interface doesn't have an ENI ID
		if boundInterface.ENIID == "" {
			continue
		}

		// If this ENI ID is not in the shouldBeBound map, unbind it
		if !shouldBeBound[boundInterface.ENIID] {
			log.Printf("ENI %s (PCI: %s, Interface: %s) is bound to DPDK but should not be, unbinding",
				boundInterface.ENIID, pciAddr, boundInterface.IfaceName)

			// Use the PCI address directly to unbind
			if err := unbindInterfaceFromDPDK(pciAddr, cfg); err != nil {
				log.Printf("Error unbinding interface with PCI address %s from DPDK: %v", pciAddr, err)
			} else {
				log.Printf("Successfully unbound interface with PCI address %s from DPDK", pciAddr)
			}
		} else {
			log.Printf("ENI %s (PCI: %s, Interface: %s) should remain bound to DPDK",
				boundInterface.ENIID, pciAddr, boundInterface.IfaceName)
		}
	}
}

// processDPDKBindingForNodeENI processes DPDK binding for a NodeENI resource
func processDPDKBindingForNodeENI(nodeENI networkingv1alpha1.NodeENI, nodeName string, cfg *config.ENIManagerConfig) {
	// Skip if DPDK is not enabled globally
	if !cfg.EnableDPDK && !nodeENI.Spec.EnableDPDK {
		return
	}

	log.Printf("Processing DPDK binding for NodeENI %s", nodeENI.Name)

	// Check if this NodeENI applies to our node
	if nodeENI.Status.Attachments == nil {
		log.Printf("NodeENI %s has no attachments, skipping DPDK binding", nodeENI.Name)
		return
	}

	// Process each attachment
	for _, attachment := range nodeENI.Status.Attachments {
		processDPDKBindingForAttachment(attachment, nodeENI, nodeName, cfg)
	}
}

// updateNodeENIDPDKStatus updates the DPDK status of an ENI attachment in the NodeENI resource
func updateNodeENIDPDKStatus(eniID string, nodeENIName string, dpdkDriver string, dpdkBound bool) error {
	return updateNodeENIDPDKStatusWithPCI(eniID, nodeENIName, dpdkDriver, dpdkBound, "")
}

// updateNodeENIDPDKStatusWithPCI updates the DPDK status of an ENI attachment in the NodeENI resource
// including the PCI address of the device bound to DPDK
func updateNodeENIDPDKStatusWithPCI(eniID string, nodeENIName string, dpdkDriver string, dpdkBound bool, pciAddress string) error {
	return updateNodeENIDPDKStatusComplete(eniID, nodeENIName, dpdkDriver, dpdkBound, pciAddress, "")
}

// updateNodeENIDPDKStatusComplete updates the DPDK status of an ENI attachment in the NodeENI resource
// including the PCI address and resource name of the device bound to DPDK
func updateNodeENIDPDKStatusComplete(eniID string, nodeENIName string, dpdkDriver string, dpdkBound bool, pciAddress string, resourceName string) error {
	// Create a Kubernetes client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	// Get the NodeENI resource
	nodeENIRaw, err := clientset.CoreV1().RESTClient().
		Get().
		AbsPath(fmt.Sprintf("/apis/networking.k8s.aws/v1alpha1/nodeenis/%s", nodeENIName)).
		Do(context.Background()).
		Raw()
	if err != nil {
		return fmt.Errorf("failed to get NodeENI %s: %v", nodeENIName, err)
	}

	// Parse the NodeENI resource
	var nodeENI networkingv1alpha1.NodeENI
	if err := json.Unmarshal(nodeENIRaw, &nodeENI); err != nil {
		return fmt.Errorf("failed to parse NodeENI %s: %v", nodeENIName, err)
	}

	// Find the attachment in the NodeENI status
	attachmentFound := false
	for i, att := range nodeENI.Status.Attachments {
		if att.ENIID == eniID {
			// Update the attachment DPDK status
			nodeENI.Status.Attachments[i].DPDKBound = dpdkBound
			nodeENI.Status.Attachments[i].DPDKDriver = dpdkDriver

			// Update the PCI address if provided
			if pciAddress != "" {
				nodeENI.Status.Attachments[i].DPDKPCIAddress = pciAddress
				log.Printf("Setting PCI address %s in NodeENI status for ENI %s", pciAddress, eniID)
			}

			// Update the resource name if provided
			if resourceName != "" {
				nodeENI.Status.Attachments[i].DPDKResourceName = resourceName
				log.Printf("Setting DPDK resource name %s in NodeENI status for ENI %s", resourceName, eniID)
			}

			nodeENI.Status.Attachments[i].LastUpdated = metav1.Now()
			attachmentFound = true
			break
		}
	}

	if !attachmentFound {
		return fmt.Errorf("attachment for ENI %s not found in NodeENI %s", eniID, nodeENIName)
	}

	// Update the NodeENI status
	statusPatch := struct {
		Status networkingv1alpha1.NodeENIStatus `json:"status"`
	}{
		Status: nodeENI.Status,
	}

	statusPatchBytes, err := json.Marshal(statusPatch)
	if err != nil {
		return fmt.Errorf("failed to marshal status patch: %v", err)
	}

	_, err = clientset.CoreV1().RESTClient().
		Patch(types.MergePatchType).
		AbsPath(fmt.Sprintf("/apis/networking.k8s.aws/v1alpha1/nodeenis/%s/status", nodeENIName)).
		Body(statusPatchBytes).
		Do(context.Background()).
		Raw()
	if err != nil {
		return fmt.Errorf("failed to update NodeENI status: %v", err)
	}

	return nil
}

// shouldSkipDPDKBinding checks if DPDK binding should be skipped for this attachment
func shouldSkipDPDKBinding(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI, nodeName string) bool {
	// Skip if DPDK is not enabled for this NodeENI
	if !nodeENI.Spec.EnableDPDK {
		log.Printf("DPDK is not enabled for NodeENI %s, skipping DPDK binding for ENI %s", nodeENI.Name, attachment.ENIID)
		return true
	}

	// Skip if this attachment is not for our node
	if attachment.NodeID != nodeName {
		return true
	}

	return false
}

// verifyExistingDPDKBinding verifies if a device is already correctly bound to DPDK
// Returns true if the device is correctly bound and no further action is needed
func verifyExistingDPDKBinding(attachment networkingv1alpha1.ENIAttachment, cfg *config.ENIManagerConfig) bool {
	if !attachment.DPDKBound {
		return false
	}

	// Even if it's marked as bound, we need to verify the actual binding
	// This handles the case where the node was rebooted and the binding was lost
	log.Printf("ENI %s is marked as bound to DPDK driver %s, verifying actual binding",
		attachment.ENIID, attachment.DPDKDriver)

	// If we have a PCI address, verify the binding directly
	if attachment.DPDKPCIAddress != "" {
		isActuallyBound, err := isPCIDeviceBoundToDPDK(attachment.DPDKPCIAddress, attachment.DPDKDriver, cfg)
		if err != nil {
			log.Printf("Error checking DPDK binding for PCI address %s: %v", attachment.DPDKPCIAddress, err)
			return false // Continue with binding attempt
		}

		if !isActuallyBound {
			log.Printf("PCI device %s is marked as DPDK-bound but is actually using a different driver, rebinding",
				attachment.DPDKPCIAddress)
			return false // Continue with binding attempt
		}

		log.Printf("PCI device %s is correctly bound to DPDK driver %s",
			attachment.DPDKPCIAddress, attachment.DPDKDriver)
		return true // It's actually bound, so we can skip
	}

	// No PCI address, we'll need to find the interface and check
	log.Printf("No PCI address in NodeENI status for ENI %s, will attempt to find and verify", attachment.ENIID)
	return false
}

// getDPDKDriverForNodeENI determines which DPDK driver to use
func getDPDKDriverForNodeENI(nodeENI networkingv1alpha1.NodeENI, cfg *config.ENIManagerConfig) string {
	dpdkDriver := cfg.DefaultDPDKDriver
	if nodeENI.Spec.DPDKDriver != "" {
		dpdkDriver = nodeENI.Spec.DPDKDriver
	}
	return dpdkDriver
}

// bindWithExplicitPCIAddress binds a device to DPDK using an explicitly provided PCI address
func bindWithExplicitPCIAddress(pciAddress, dpdkDriver string, nodeENI networkingv1alpha1.NodeENI,
	attachment networkingv1alpha1.ENIAttachment, cfg *config.ENIManagerConfig) bool {

	log.Printf("Using explicitly provided PCI address: %s", pciAddress)

	// Validate the PCI address format
	if !isPCIAddressFormat(pciAddress) {
		log.Printf("Invalid PCI address format: %s, skipping DPDK binding", pciAddress)
		return false
	}

	// Check if the PCI device exists
	pciDevPath := fmt.Sprintf("/sys/bus/pci/devices/%s", pciAddress)
	if _, err := os.Stat(pciDevPath); os.IsNotExist(err) {
		log.Printf("PCI device %s does not exist, skipping DPDK binding", pciAddress)
		return false
	}

	// Bind the PCI device directly to DPDK
	if err := bindPCIDeviceToDPDK(pciAddress, dpdkDriver, cfg); err != nil {
		log.Printf("Error binding PCI device %s to DPDK: %v", pciAddress, err)
		return false
	}

	// Try to get the interface name for this PCI device (if it's not already bound to DPDK)
	ifaceName := getInterfaceNameForPCIDevice(pciAddress)

	// Update the DPDKBoundInterfaces map
	updateDPDKBoundInterfacesMap(pciAddress, dpdkDriver, nodeENI.Name, attachment.ENIID, ifaceName, cfg)

	log.Printf("Successfully bound PCI device %s to DPDK driver %s", pciAddress, dpdkDriver)

	// Get the resource name for this device
	resourceName := ""
	if nodeENI.Spec.DPDKResourceName != "" {
		resourceName = nodeENI.Spec.DPDKResourceName
	}

	// Update the attachment status to mark it as DPDK-bound and include the PCI address and resource name
	if err := updateNodeENIDPDKStatusComplete(attachment.ENIID, nodeENI.Name, dpdkDriver, true, pciAddress, resourceName); err != nil {
		log.Printf("Warning: Failed to update attachment DPDK status: %v", err)
		// Continue anyway, the interface is bound to DPDK
	} else {
		log.Printf("Successfully updated attachment DPDK status for ENI %s", attachment.ENIID)
	}

	// Update SR-IOV config for this DPDK-bound device
	updateSRIOVConfigForDPDK(pciAddress, nodeENI, dpdkDriver, cfg)

	return true
}

// getInterfaceNameForPCIDevice gets the interface name for a PCI device
func getInterfaceNameForPCIDevice(pciAddress string) string {
	netDir := fmt.Sprintf("/sys/bus/pci/devices/%s/net", pciAddress)
	if _, err := os.Stat(netDir); err == nil {
		// This device has a network interface, get its name
		files, err := os.ReadDir(netDir)
		if err == nil && len(files) > 0 {
			ifaceName := files[0].Name()
			log.Printf("Found interface %s for PCI address %s", ifaceName, pciAddress)
			return ifaceName
		}
	}
	return ""
}

// updateDPDKBoundInterfacesMap updates the DPDKBoundInterfaces map with the given information
func updateDPDKBoundInterfacesMap(pciAddress, driver, nodeENIName, eniID, ifaceName string, cfg *config.ENIManagerConfig) {
	cfg.DPDKBoundInterfaces[pciAddress] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  pciAddress,
		Driver:      driver,
		NodeENIName: nodeENIName,
		ENIID:       eniID,
		IfaceName:   ifaceName,
	}
}

// bindInterfaceByName binds an interface to DPDK by its name
func bindInterfaceByName(ifaceName, dpdkDriver string, nodeENI networkingv1alpha1.NodeENI,
	attachment networkingv1alpha1.ENIAttachment, cfg *config.ENIManagerConfig) bool {

	// Check if the interface exists
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); os.IsNotExist(err) {
		log.Printf("Interface %s does not exist yet, skipping DPDK binding for now", ifaceName)
		return false
	}

	// Get the link for this interface
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		log.Printf("Error getting link for interface %s: %v", ifaceName, err)
		return false
	}

	// Bind the interface to DPDK
	if err := bindInterfaceToDPDK(link, dpdkDriver, cfg); err != nil {
		log.Printf("Error binding interface %s to DPDK: %v", ifaceName, err)
		return false
	}

	// Update the DPDKBoundInterfaces map with NodeENI name and ENI ID
	// First, get the PCI address for this interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err != nil {
		log.Printf("Error getting PCI address for interface %s: %v", ifaceName, err)
		return false
	}

	// Update the entry using the PCI address as the key
	if boundInterface, exists := cfg.DPDKBoundInterfaces[pciAddress]; exists {
		boundInterface.NodeENIName = nodeENI.Name
		boundInterface.ENIID = attachment.ENIID
		cfg.DPDKBoundInterfaces[pciAddress] = boundInterface
		log.Printf("Updated DPDKBoundInterfaces map for interface %s (PCI: %s) with NodeENI %s and ENI ID %s",
			ifaceName, pciAddress, nodeENI.Name, attachment.ENIID)
	}

	log.Printf("Successfully bound interface %s to DPDK driver %s", ifaceName, dpdkDriver)

	// Get the resource name for this device
	resourceName := ""
	if nodeENI.Spec.DPDKResourceName != "" {
		resourceName = nodeENI.Spec.DPDKResourceName
	}

	// Update the attachment status to mark it as DPDK-bound and include the PCI address and resource name
	if err := updateNodeENIDPDKStatusComplete(attachment.ENIID, nodeENI.Name, dpdkDriver, true, pciAddress, resourceName); err != nil {
		log.Printf("Warning: Failed to update attachment DPDK status: %v", err)
		// Continue anyway, the interface is bound to DPDK
	} else {
		log.Printf("Successfully updated attachment DPDK status for ENI %s with PCI address %s", attachment.ENIID, pciAddress)
	}

	// Update SR-IOV config for this DPDK-bound device
	updateSRIOVConfigForDPDK(pciAddress, nodeENI, dpdkDriver, cfg)

	return true
}

// updateSRIOVConfigIfNeeded updates the SRIOV device plugin config if a resource name is specified
func updateSRIOVConfigIfNeeded(ifaceName string, nodeENI networkingv1alpha1.NodeENI, dpdkDriver string, cfg *config.ENIManagerConfig) {
	// Store the resource name for this interface if specified in the NodeENI
	if nodeENI.Spec.DPDKResourceName != "" {
		// Validate the resource name first
		if err := validateSRIOVResourceName(nodeENI.Spec.DPDKResourceName); err != nil {
			log.Printf("Error: Invalid DPDK resource name '%s' in NodeENI %s: %v",
				nodeENI.Spec.DPDKResourceName, nodeENI.Name, err)
			return
		}

		cfg.DPDKResourceNames[ifaceName] = nodeENI.Spec.DPDKResourceName
		// Update the SRIOV device plugin config with the specified resource name using change detection
		pciAddress, err := getPCIAddressForInterface(ifaceName)
		if err == nil {
			if err := updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, nodeENI.Spec.DPDKResourceName, cfg); err != nil {
				log.Printf("Warning: Failed to update SRIOV device plugin config with resource name %s: %v",
					nodeENI.Spec.DPDKResourceName, err)
			} else {
				log.Printf("Successfully configured SR-IOV device plugin for resource %s (interface: %s, PCI: %s)",
					nodeENI.Spec.DPDKResourceName, ifaceName, pciAddress)
			}
		} else {
			log.Printf("Warning: Failed to get PCI address for interface %s: %v", ifaceName, err)
		}
	}
}

// updateSRIOVConfigForDPDK updates the SRIOV device plugin config for DPDK-bound devices using PCI address
func updateSRIOVConfigForDPDK(pciAddress string, nodeENI networkingv1alpha1.NodeENI, dpdkDriver string, cfg *config.ENIManagerConfig) {
	// Only proceed if a resource name is specified in the NodeENI
	if nodeENI.Spec.DPDKResourceName == "" {
		log.Printf("No DPDK resource name specified for NodeENI %s, skipping SR-IOV config update", nodeENI.Name)
		return
	}

	// Validate the resource name first
	if err := validateSRIOVResourceName(nodeENI.Spec.DPDKResourceName); err != nil {
		log.Printf("Error: Invalid DPDK resource name '%s' in NodeENI %s: %v",
			nodeENI.Spec.DPDKResourceName, nodeENI.Name, err)
		return
	}

	log.Printf("Updating SR-IOV device plugin config for PCI device %s with resource name %s",
		pciAddress, nodeENI.Spec.DPDKResourceName)

	// Update the SRIOV device plugin config using the modern format
	if err := updateModernSRIOVDevicePluginConfig(pciAddress, dpdkDriver, nodeENI.Spec.DPDKResourceName, cfg); err != nil {
		log.Printf("Warning: Failed to update SR-IOV device plugin config for PCI %s with resource name %s: %v",
			pciAddress, nodeENI.Spec.DPDKResourceName, err)
	} else {
		log.Printf("Successfully configured SR-IOV device plugin for resource %s (PCI: %s, driver: %s)",
			nodeENI.Spec.DPDKResourceName, pciAddress, dpdkDriver)
	}
}

// processDPDKBindingForAttachment processes DPDK binding for a single NodeENI attachment
func processDPDKBindingForAttachment(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI,
	nodeName string, cfg *config.ENIManagerConfig) {

	// Skip if DPDK is not enabled or attachment is not for our node
	if shouldSkipDPDKBinding(attachment, nodeENI, nodeName) {
		return
	}

	// Check if this attachment is already correctly bound to DPDK
	if verifyExistingDPDKBinding(attachment, cfg) {
		return
	}

	// This attachment is for our node
	log.Printf("Processing DPDK binding for NodeENI attachment: %s, ENI: %s, DeviceIndex: %d",
		nodeName, attachment.ENIID, attachment.DeviceIndex)

	// Determine the DPDK driver to use
	dpdkDriver := getDPDKDriverForNodeENI(nodeENI, cfg)

	// Check if a specific PCI address is provided in the NodeENI spec
	if nodeENI.Spec.DPDKPCIAddress != "" {
		if bindWithExplicitPCIAddress(nodeENI.Spec.DPDKPCIAddress, dpdkDriver, nodeENI, attachment, cfg) {
			return
		}
	}

	// If no PCI address is provided, fall back to the device index method
	// First, try to find the interface using the device index from the NodeENI spec
	expectedIfaceName := fmt.Sprintf("eth%d", attachment.DeviceIndex)
	log.Printf("No PCI address provided, using device index. Expected interface name: %s", expectedIfaceName)

	// Try binding with the expected interface name
	if bindInterfaceByName(expectedIfaceName, dpdkDriver, nodeENI, attachment, cfg) {
		// Update SRIOV config if needed
		updateSRIOVConfigIfNeeded(expectedIfaceName, nodeENI, dpdkDriver, cfg)
		return
	}

	// If the expected interface doesn't exist, fall back to the ENI ID lookup
	log.Printf("Interface %s does not exist, falling back to ENI ID lookup", expectedIfaceName)

	// Get the interface name for this ENI
	ifaceName, err := getInterfaceNameForENI(attachment.ENIID)
	if err != nil {
		log.Printf("Error getting interface name for ENI %s: %v", attachment.ENIID, err)
		log.Printf("Could not determine interface name for ENI %s, skipping DPDK binding", attachment.ENIID)
		return
	}

	log.Printf("Found interface %s for ENI %s using ENI ID lookup", ifaceName, attachment.ENIID)

	// Try binding with the interface name from ENI ID lookup
	if bindInterfaceByName(ifaceName, dpdkDriver, nodeENI, attachment, cfg) {
		// Update SRIOV config if needed - use PCI address since interface may not be available after DPDK binding
		if pciAddr, err := getPCIAddressForInterface(ifaceName); err == nil {
			updateSRIOVConfigForDPDK(pciAddr, nodeENI, dpdkDriver, cfg)
		} else {
			log.Printf("Warning: Failed to get PCI address for interface %s: %v", ifaceName, err)
		}
	}
}

// PCIDeviceInfo represents a PCI device information
type PCIDeviceInfo struct {
	PCIAddress string `json:"pciAddress"`
	Driver     string `json:"driver,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	DeviceID   string `json:"deviceID,omitempty"`
}

// SRIOVDeviceConfig represents the configuration for a SRIOV device
type SRIOVDeviceConfig struct {
	ResourceName string          `json:"resourceName"`
	RootDevices  []string        `json:"rootDevices,omitempty"`
	DeviceType   string          `json:"deviceType,omitempty"`
	Devices      []PCIDeviceInfo `json:"devices,omitempty"`
}

// SRIOVDPConfig represents the configuration for the SRIOV device plugin
type SRIOVDPConfig struct {
	ResourceList []SRIOVDeviceConfig `json:"resourceList"`
}

// isPCIDeviceBoundToDPDK checks if a PCI device is bound to the specified DPDK driver
func isPCIDeviceBoundToDPDK(pciAddress string, driver string, cfg *config.ENIManagerConfig) (bool, error) {
	log.Printf("Checking if PCI device %s is bound to DPDK driver %s", pciAddress, driver)

	// Check if the DPDK binding script exists
	if _, err := os.Stat(cfg.DPDKBindingScript); os.IsNotExist(err) {
		return false, fmt.Errorf("DPDK binding script %s does not exist", cfg.DPDKBindingScript)
	}

	// Execute the DPDK binding script with --status flag to check current binding
	cmd := exec.Command(cfg.DPDKBindingScript, "--status")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to check DPDK binding status: %v, output: %s", err, string(output))
	}

	// Parse the output to check if the device is bound to the specified driver
	outputStr := string(output)

	// Look for the PCI address in the output
	// The output format is like:
	// Network devices using DPDK-compatible driver
	// ============================================
	// 0000:00:08.0 'Elastic Network Adapter (ENA) ec20' drv=vfio-pci unused=ena
	//
	// Network devices using kernel driver
	// ===================================
	// 0000:00:05.0 'Elastic Network Adapter (ENA) ec20' if=eth0 drv=ena unused=vfio-pci *Active*

	// Extract the DPDK-compatible driver section
	// Look for the section header first
	dpdkHeaderRegex := regexp.MustCompile(`Network devices using DPDK-compatible driver\s*\n=+`)
	headerMatch := dpdkHeaderRegex.FindStringIndex(outputStr)

	if headerMatch == nil {
		log.Printf("No DPDK section header found in output, device %s is not bound to DPDK", pciAddress)
		return false, nil
	}

	// Find the start of the content after the header
	contentStart := headerMatch[1]

	// Find the end of the DPDK section (next section header or end of string)
	nextSectionRegex := regexp.MustCompile(`\n\n[A-Z][^=]*\n=+`)
	nextSectionMatch := nextSectionRegex.FindStringIndex(outputStr[contentStart:])

	var dpdkSection string
	if nextSectionMatch != nil {
		// Extract content up to the next section
		dpdkSection = outputStr[contentStart : contentStart+nextSectionMatch[0]]
	} else {
		// Extract content to the end of the string
		dpdkSection = outputStr[contentStart:]
	}

	// Clean up the section (remove leading/trailing whitespace and empty lines)
	dpdkSection = strings.TrimSpace(dpdkSection)
	log.Printf("DPDK section content: %q", dpdkSection)

	// Check if the PCI address is in the DPDK section and using the specified driver
	pciRegex := regexp.MustCompile(fmt.Sprintf(`%s.*?drv=%s`, regexp.QuoteMeta(pciAddress), regexp.QuoteMeta(driver)))

	isMatched := pciRegex.MatchString(dpdkSection)
	log.Printf("PCI device %s bound to driver %s: %v", pciAddress, driver, isMatched)

	return isMatched, nil
}

// bindPCIDeviceToDPDK binds a PCI device directly to a DPDK driver
func bindPCIDeviceToDPDK(pciAddress string, driver string, cfg *config.ENIManagerConfig) error {
	startTime := time.Now()
	log.Printf("[DPDK-BIND] Starting DPDK binding for PCI device %s to driver %s", pciAddress, driver)

	// Check if the DPDK binding script exists
	if _, err := os.Stat(cfg.DPDKBindingScript); os.IsNotExist(err) {
		log.Printf("[DPDK-BIND] ERROR: DPDK binding script not found at %s", cfg.DPDKBindingScript)
		return fmt.Errorf("DPDK binding script %s does not exist", cfg.DPDKBindingScript)
	}

	log.Printf("[DPDK-BIND] DPDK binding script found at %s", cfg.DPDKBindingScript)

	// Acquire a lock for this PCI address to prevent concurrent operations
	dpdkOpsMutex.Lock()
	if inProgress, exists := dpdkOperations[pciAddress]; exists && inProgress {
		dpdkOpsMutex.Unlock()
		return fmt.Errorf("another DPDK operation is already in progress for PCI device %s", pciAddress)
	}
	dpdkOperations[pciAddress] = true
	dpdkOpsMutex.Unlock()

	// Ensure we release the lock when we're done
	defer func() {
		dpdkOpsMutex.Lock()
		delete(dpdkOperations, pciAddress)
		// Periodic cleanup of stale operations (safety measure)
		if len(dpdkOperations) > 100 {
			log.Printf("Warning: DPDK operations map has grown large (%d entries), potential memory leak", len(dpdkOperations))
		}
		dpdkOpsMutex.Unlock()
	}()

	// Acquire the global DPDK mutex to ensure only one DPDK operation happens at a time
	// This prevents race conditions when multiple goroutines try to bind/unbind interfaces
	dpdkMutex.Lock()
	defer dpdkMutex.Unlock()

	// Execute the DPDK binding script to bind the device to the DPDK driver
	cmd := exec.Command(cfg.DPDKBindingScript, "-b", driver, pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)

		// Check for specific error conditions and provide more helpful error messages
		if strings.Contains(outputStr, "Cannot open /sys/bus/pci/drivers") {
			return fmt.Errorf("failed to bind PCI device %s to DPDK driver %s: driver directory not found, kernel module may not be loaded: %v",
				pciAddress, driver, err)
		} else if strings.Contains(outputStr, "Cannot bind to driver") {
			return fmt.Errorf("failed to bind PCI device %s to DPDK driver %s: device cannot be bound to this driver, check compatibility: %v",
				pciAddress, driver, err)
		} else if strings.Contains(outputStr, "Unknown device") {
			return fmt.Errorf("failed to bind PCI device %s to DPDK driver %s: device not found, check PCI address: %v",
				pciAddress, driver, err)
		} else if strings.Contains(outputStr, "routing table indicates") {
			return fmt.Errorf("failed to bind PCI device %s to DPDK driver %s: interface is in use by the system, use --force flag if needed: %v",
				pciAddress, driver, err)
		} else if strings.Contains(outputStr, "Permission denied") {
			return fmt.Errorf("failed to bind PCI device %s to DPDK driver %s: permission denied, check if running as root: %v",
				pciAddress, driver, err)
		}

		// Generic error message for other cases
		return fmt.Errorf("failed to bind PCI device %s to DPDK driver %s: %v, output: %s",
			pciAddress, driver, err, outputStr)
	}

	// Verify the binding was successful by checking the current driver
	// This helps catch cases where the command appeared to succeed but the binding didn't actually happen
	time.Sleep(500 * time.Millisecond) // Give the system a moment to apply the binding

	// Check if the device is now bound to the expected driver
	driverPath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddress)
	if _, err := os.Stat(driverPath); err == nil {
		// Read the driver symlink to verify it points to the expected driver
		driverLink, err := os.Readlink(driverPath)
		if err == nil {
			driverName := filepath.Base(driverLink)
			if driverName != driver {
				return fmt.Errorf("binding verification failed: device %s is bound to %s instead of %s",
					pciAddress, driverName, driver)
			}
		}
	}

	duration := time.Since(startTime)
	log.Printf("[DPDK-BIND] Successfully bound PCI device %s to DPDK driver %s (duration: %v)", pciAddress, driver, duration)
	return nil
}

// bindInterfaceToDPDK binds a network interface to a DPDK driver
func bindInterfaceToDPDK(link vnetlink.Link, driver string, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	log.Printf("Binding interface %s to DPDK driver %s", ifaceName, driver)

	// Get the PCI address for the interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get PCI address for interface %s: %v", ifaceName, err)
	}

	// Use the common PCI binding function
	if err := bindPCIDeviceToDPDK(pciAddress, driver, cfg); err != nil {
		return err
	}

	log.Printf("Successfully bound interface %s (PCI: %s) to DPDK driver %s", ifaceName, pciAddress, driver)

	// Store interface information for tracking
	if err := storeDPDKBoundInterface(pciAddress, driver, ifaceName, cfg); err != nil {
		log.Printf("Warning: Failed to store DPDK bound interface information: %v", err)
	}

	// Store the mapping in the persistent store
	if err := updatePersistentMappingStore(pciAddress, driver, ifaceName, cfg); err != nil {
		log.Printf("Warning: Failed to update persistent mapping store: %v", err)
	}

	// Update the SRIOV device plugin configuration with change detection
	// This ensures SR-IOV device plugin restarts only when configuration actually changes
	resourceName := getResourceNameForDevice(ifaceName, pciAddress, cfg)
	if err := updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName, cfg); err != nil {
		log.Printf("Error: Failed to update SRIOV device plugin config: %v", err)
		// This is a critical failure - the interface is bound to DPDK but not available to pods
		// Try to unbind the interface to maintain consistency
		if unbindErr := unbindInterfaceFromDPDK(pciAddress, cfg); unbindErr != nil {
			log.Printf("Critical: Failed to unbind interface after SR-IOV config failure: %v", unbindErr)
		}
		return fmt.Errorf("failed to update SR-IOV device plugin configuration: %v", err)
	}

	return nil
}

// storeDPDKBoundInterface stores interface information for tracking DPDK bound interfaces
func storeDPDKBoundInterface(pciAddress, driver, ifaceName string, cfg *config.ENIManagerConfig) error {
	// Initialize the map if it doesn't exist
	if cfg.DPDKBoundInterfaces == nil {
		cfg.DPDKBoundInterfaces = make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		})
	}

	// Store the interface information using PCI address as the key
	cfg.DPDKBoundInterfaces[pciAddress] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  pciAddress,
		Driver:      driver,
		NodeENIName: "", // Will be set by the caller
		ENIID:       "", // Will be set by the caller
		IfaceName:   ifaceName,
	}

	return nil
}

// updatePersistentMappingStore updates the persistent mapping store with DPDK binding information
func updatePersistentMappingStore(pciAddress, driver, ifaceName string, cfg *config.ENIManagerConfig) error {
	if cfg.InterfaceMappingStore == nil {
		return nil
	}

	// Try to find the ENI ID for this interface
	eniID := findENIIDForInterface(ifaceName)

	if eniID != "" {
		return updateMappingWithENIID(eniID, pciAddress, driver, ifaceName, cfg)
	}

	return updateMappingWithoutENIID(pciAddress, driver, ifaceName, cfg)
}

// findENIIDForInterface finds the ENI ID for a given interface name
func findENIIDForInterface(ifaceName string) string {
	for k, v := range usedInterfaces {
		if v == ifaceName {
			return k
		}
	}
	return ""
}

// updateMappingWithENIID updates the mapping store when ENI ID is known
func updateMappingWithENIID(eniID, pciAddress, driver, ifaceName string, cfg *config.ENIManagerConfig) error {
	existingMapping, exists := cfg.InterfaceMappingStore.GetMappingByENIID(eniID)
	if exists {
		// Update the existing mapping
		existingMapping.PCIAddress = pciAddress
		existingMapping.DPDKBound = true
		existingMapping.DPDKDriver = driver

		if err := cfg.InterfaceMappingStore.UpdateMapping(existingMapping); err != nil {
			return err
		}
		log.Printf("Updated interface mapping in persistent store: ENI %s -> Interface %s -> PCI %s (DPDK bound)",
			eniID, ifaceName, pciAddress)
	} else {
		// Create a new mapping
		newMapping := mapping.InterfaceMapping{
			ENIID:      eniID,
			IfaceName:  ifaceName,
			PCIAddress: pciAddress,
			DPDKBound:  true,
			DPDKDriver: driver,
		}

		if err := cfg.InterfaceMappingStore.AddMapping(newMapping); err != nil {
			return err
		}
		log.Printf("Added interface mapping to persistent store: ENI %s -> Interface %s -> PCI %s (DPDK bound)",
			eniID, ifaceName, pciAddress)
	}

	return nil
}

// updateMappingWithoutENIID updates the mapping store when ENI ID is not known
func updateMappingWithoutENIID(pciAddress, driver, ifaceName string, cfg *config.ENIManagerConfig) error {
	existingMapping, exists := cfg.InterfaceMappingStore.GetMappingByPCIAddress(pciAddress)
	if exists {
		// Update the existing mapping
		existingMapping.IfaceName = ifaceName
		existingMapping.DPDKBound = true
		existingMapping.DPDKDriver = driver

		if err := cfg.InterfaceMappingStore.UpdateMapping(existingMapping); err != nil {
			return err
		}
		log.Printf("Updated interface mapping in persistent store: Interface %s -> PCI %s (DPDK bound)",
			ifaceName, pciAddress)
	} else {
		// Create a new mapping with just the PCI address and interface name
		newMapping := mapping.InterfaceMapping{
			IfaceName:  ifaceName,
			PCIAddress: pciAddress,
			DPDKBound:  true,
			DPDKDriver: driver,
		}

		if err := cfg.InterfaceMappingStore.AddMapping(newMapping); err != nil {
			return err
		}
		log.Printf("Added interface mapping to persistent store: Interface %s -> PCI %s (DPDK bound)",
			ifaceName, pciAddress)
	}

	return nil
}

// isPCIAddressFormat checks if a string is in the format of a PCI address
func isPCIAddressFormat(addr string) bool {
	pciRegex := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	return pciRegex.MatchString(addr)
}

// extractENIIDFromName extracts an ENI ID from a string if present
func extractENIIDFromName(name string) string {
	eniIDRegex := regexp.MustCompile(`eni-[0-9a-f]+`)
	matches := eniIDRegex.FindStringSubmatch(name)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// findPCIAddressByENIID looks for a PCI address in the bound interfaces map by ENI ID
func findPCIAddressByENIID(eniID string, boundInterfaces map[string]struct {
	PCIAddress  string
	Driver      string
	NodeENIName string
	ENIID       string
	IfaceName   string
}) string {
	if eniID == "" {
		return ""
	}

	for addr, info := range boundInterfaces {
		if info.ENIID == eniID {
			log.Printf("Found PCI address %s for ENI ID %s", addr, eniID)
			return addr
		}
	}
	return ""
}

// resolvePCIAddress tries to find the PCI address for an interface or PCI address string
func resolvePCIAddress(ifaceOrPCI string, cfg *config.ENIManagerConfig) (string, error) {
	// Check if it's already a PCI address
	if isPCIAddressFormat(ifaceOrPCI) {
		log.Printf("Unbinding interface with PCI address %s from DPDK driver", ifaceOrPCI)
		return ifaceOrPCI, nil
	}

	log.Printf("Unbinding interface %s from DPDK driver", ifaceOrPCI)

	// Try to get the PCI address for the interface
	pciAddress, err := getPCIAddressForInterface(ifaceOrPCI)
	if err == nil {
		return pciAddress, nil
	}

	log.Printf("Could not get PCI address for interface %s: %v", ifaceOrPCI, err)

	// Try to find the interface by ENI ID in the map
	eniID := extractENIIDFromName(ifaceOrPCI)
	if eniID != "" {
		log.Printf("Extracted ENI ID %s from interface name %s", eniID, ifaceOrPCI)
		pciAddress = findPCIAddressByENIID(eniID, cfg.DPDKBoundInterfaces)
		if pciAddress != "" {
			return pciAddress, nil
		}
	}

	// Last resort: try to find it by interface name pattern
	pciAddress, err = findPCIAddressByInterfacePattern(ifaceOrPCI)
	if err != nil {
		return "", fmt.Errorf("failed to find PCI address for interface %s: %v", ifaceOrPCI, err)
	}

	if pciAddress == "" {
		return "", fmt.Errorf("no PCI address found for interface %s", ifaceOrPCI)
	}

	return pciAddress, nil
}

// createDefaultBoundInterface creates a default bound interface entry when none exists
func createDefaultBoundInterface(pciAddress, ifaceName string) struct {
	PCIAddress  string
	Driver      string
	NodeENIName string
	ENIID       string
	IfaceName   string
} {
	return struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  pciAddress,
		Driver:      "vfio-pci", // Assume vfio-pci as the default DPDK driver
		NodeENIName: "",
		ENIID:       "",
		IfaceName:   ifaceName,
	}
}

// handleNonExistentPCIDevice handles the case when a PCI device no longer exists
func handleNonExistentPCIDevice(pciAddress string, boundInterface struct {
	PCIAddress  string
	Driver      string
	NodeENIName string
	ENIID       string
	IfaceName   string
}, cfg *config.ENIManagerConfig) error {
	log.Printf("PCI device %s no longer exists, skipping DPDK unbinding", pciAddress)

	// Update the interface mapping store to mark the device as unbound
	if cfg.InterfaceMappingStore != nil {
		updateMappingForNonExistentDevice(pciAddress, boundInterface, cfg)
	}

	// Remove from our tracking map
	delete(cfg.DPDKBoundInterfaces, pciAddress)

	// Update the NodeENI status if we have the NodeENI name and ENI ID
	if boundInterface.NodeENIName != "" && boundInterface.ENIID != "" {
		// When unbinding, we clear the PCI address from the status
		if err := updateNodeENIDPDKStatusWithPCI(boundInterface.ENIID, boundInterface.NodeENIName, "", false, ""); err != nil {
			log.Printf("Warning: Failed to update NodeENI status for DPDK unbinding: %v", err)
		} else {
			log.Printf("Successfully updated NodeENI status for DPDK unbinding of ENI %s (device no longer exists)", boundInterface.ENIID)
		}
	}

	return nil
}

// updateMappingForNonExistentDevice updates the interface mapping store for a non-existent device
func updateMappingForNonExistentDevice(pciAddress string, boundInterface struct {
	PCIAddress  string
	Driver      string
	NodeENIName string
	ENIID       string
	IfaceName   string
}, cfg *config.ENIManagerConfig) {
	// Try to find the mapping by PCI address
	mapping, exists := cfg.InterfaceMappingStore.GetMappingByPCIAddress(pciAddress)
	if exists {
		mapping.DPDKBound = false
		mapping.DPDKDriver = ""
		if err := cfg.InterfaceMappingStore.UpdateMapping(mapping); err != nil {
			log.Printf("Warning: Failed to update interface mapping in persistent store: %v", err)
		} else {
			log.Printf("Updated interface mapping in persistent store: PCI %s is no longer bound to DPDK (device no longer exists)",
				pciAddress)
		}
	} else if boundInterface.ENIID != "" {
		// Try to find the mapping by ENI ID
		mapping, exists := cfg.InterfaceMappingStore.GetMappingByENIID(boundInterface.ENIID)
		if exists {
			mapping.DPDKBound = false
			mapping.DPDKDriver = ""
			if err := cfg.InterfaceMappingStore.UpdateMapping(mapping); err != nil {
				log.Printf("Warning: Failed to update interface mapping in persistent store: %v", err)
			} else {
				log.Printf("Updated interface mapping in persistent store: ENI %s is no longer bound to DPDK (device no longer exists)",
					boundInterface.ENIID)
			}
		}
	}
}

// acquireDPDKLock acquires a lock for a PCI device to prevent concurrent operations
// Returns a function that releases the lock when called
// This function implements a proper queuing mechanism for concurrent operations
func acquireDPDKLock(pciAddress string) (func(), error) {
	const maxWaitTime = 30 * time.Second
	const retryInterval = 50 * time.Millisecond

	startTime := time.Now()

	for {
		dpdkOpsMutex.Lock()

		// Check if another operation is already in progress for this PCI address
		if inProgress, exists := dpdkOperations[pciAddress]; !exists || !inProgress {
			// Mark this PCI address as having an operation in progress
			dpdkOperations[pciAddress] = true
			dpdkOpsMutex.Unlock()

			// Return a function to release the lock
			return func() {
				dpdkOpsMutex.Lock()
				delete(dpdkOperations, pciAddress)
				// Periodic cleanup of stale operations (safety measure)
				if len(dpdkOperations) > 100 {
					log.Printf("Warning: DPDK operations map has grown large (%d entries), potential memory leak", len(dpdkOperations))
				}
				dpdkOpsMutex.Unlock()
			}, nil
		}

		dpdkOpsMutex.Unlock()

		// Check if we've exceeded the maximum wait time
		if time.Since(startTime) > maxWaitTime {
			return nil, fmt.Errorf("timeout waiting for DPDK operation lock on PCI device %s (waited %v)", pciAddress, time.Since(startTime))
		}

		// Wait a bit before retrying
		time.Sleep(retryInterval)
	}
}

// executeUnbindCommand executes the command to unbind a PCI device from its current driver
func executeUnbindCommand(pciAddress string, bindingScript string) error {
	cmd := exec.Command(bindingScript, "-u", pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)

		// Check for specific error conditions and provide more helpful error messages
		if strings.Contains(outputStr, "Unknown device") {
			log.Printf("Warning: Failed to unbind PCI address %s from DPDK driver: device not found, check PCI address: %v, output: %s",
				pciAddress, err, outputStr)
			return fmt.Errorf("failed to unbind PCI address %s from DPDK driver: device not found: %v", pciAddress, err)
		} else if strings.Contains(outputStr, "not currently managed by any driver") {
			log.Printf("Notice: PCI address %s is not currently managed by any driver, continuing with binding to ena", pciAddress)
			// This is not a fatal error, the device is already unbound
			return nil
		} else if strings.Contains(outputStr, "Cannot open") {
			log.Printf("Warning: Failed to unbind PCI address %s from DPDK driver: cannot open unbind file, driver may not be loaded: %v, output: %s",
				pciAddress, err, outputStr)
			return fmt.Errorf("failed to unbind PCI address %s from DPDK driver: driver not loaded: %v", pciAddress, err)
		}

		log.Printf("Warning: Failed to unbind PCI address %s from DPDK driver: %v, output: %s",
			pciAddress, err, outputStr)
		return fmt.Errorf("failed to unbind PCI address %s from DPDK driver: %v", pciAddress, err)
	}

	return nil
}

// executeBindToEnaCommand executes the command to bind a PCI device to the ena driver
func executeBindToEnaCommand(pciAddress string, bindingScript string) error {
	cmd := exec.Command(bindingScript, "-b", "ena", pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)

		// Check for specific error conditions and provide more helpful error messages
		if strings.Contains(outputStr, "Cannot open /sys/bus/pci/drivers") {
			log.Printf("Warning: Failed to bind PCI address %s to ena driver: driver directory not found, kernel module may not be loaded: %v, output: %s",
				pciAddress, err, outputStr)
			return fmt.Errorf("failed to bind PCI address %s to ena driver: ena module not loaded: %v", pciAddress, err)
		} else if strings.Contains(outputStr, "Cannot bind to driver") {
			log.Printf("Warning: Failed to bind PCI address %s to ena driver: device cannot be bound to this driver, check compatibility: %v, output: %s",
				pciAddress, err, outputStr)
			return fmt.Errorf("failed to bind PCI address %s to ena driver: incompatible device: %v", pciAddress, err)
		} else if strings.Contains(outputStr, "Unknown device") {
			log.Printf("Warning: Failed to bind PCI address %s to ena driver: device not found, check PCI address: %v, output: %s",
				pciAddress, err, outputStr)
			return fmt.Errorf("failed to bind PCI address %s to ena driver: device not found: %v", pciAddress, err)
		}

		log.Printf("Warning: Failed to bind PCI address %s to ena driver: %v, output: %s",
			pciAddress, err, outputStr)
		return fmt.Errorf("failed to bind PCI address %s to ena driver: %v", pciAddress, err)
	}

	return nil
}

// updatePersistentStore updates the persistent store to mark a device as unbound from DPDK
func updatePersistentStore(pciAddress string, boundInterface struct {
	PCIAddress  string
	Driver      string
	NodeENIName string
	ENIID       string
	IfaceName   string
}, cfg *config.ENIManagerConfig) {
	if cfg.InterfaceMappingStore == nil {
		return
	}

	// Try to find the mapping by PCI address
	mapping, exists := cfg.InterfaceMappingStore.GetMappingByPCIAddress(pciAddress)
	if exists {
		// Update the mapping to indicate it's no longer bound to DPDK
		mapping.DPDKBound = false
		mapping.DPDKDriver = ""

		if err := cfg.InterfaceMappingStore.UpdateMapping(mapping); err != nil {
			log.Printf("Warning: Failed to update interface mapping in persistent store: %v", err)
		} else {
			log.Printf("Updated interface mapping in persistent store: PCI %s is no longer bound to DPDK",
				pciAddress)
		}
	} else if boundInterface.ENIID != "" {
		// Try to find the mapping by ENI ID
		mapping, exists := cfg.InterfaceMappingStore.GetMappingByENIID(boundInterface.ENIID)
		if exists {
			// Update the mapping to indicate it's no longer bound to DPDK
			mapping.DPDKBound = false
			mapping.DPDKDriver = ""

			if err := cfg.InterfaceMappingStore.UpdateMapping(mapping); err != nil {
				log.Printf("Warning: Failed to update interface mapping in persistent store: %v", err)
			} else {
				log.Printf("Updated interface mapping in persistent store: ENI %s is no longer bound to DPDK",
					boundInterface.ENIID)
			}
		}
	}
}

// updateNodeENIForUnbinding updates the NodeENI status to mark a device as unbound from DPDK
func updateNodeENIForUnbinding(boundInterface struct {
	PCIAddress  string
	Driver      string
	NodeENIName string
	ENIID       string
	IfaceName   string
}) error {
	if boundInterface.NodeENIName == "" || boundInterface.ENIID == "" {
		return nil
	}

	// When unbinding, we clear the PCI address from the status
	if err := updateNodeENIDPDKStatusWithPCI(boundInterface.ENIID, boundInterface.NodeENIName, "", false, ""); err != nil {
		log.Printf("Warning: Failed to update NodeENI status for DPDK unbinding: %v", err)
		return err
	}

	log.Printf("Successfully updated NodeENI status for DPDK unbinding of ENI %s", boundInterface.ENIID)
	return nil
}

// executeUnbindWithRetry executes the unbind and rebind commands with retry logic
func executeUnbindWithRetry(pciAddress string, bindingScript string) error {
	maxRetries := 3
	var lastErr error

	for retry := 0; retry < maxRetries; retry++ {
		if retry > 0 {
			backoffTime := time.Duration(1<<uint(retry)) * time.Second
			log.Printf("Retrying unbind operation after %v (attempt %d/%d)", backoffTime, retry+1, maxRetries)
			time.Sleep(backoffTime)
		}

		// First unbind from the current driver
		if err := executeUnbindCommand(pciAddress, bindingScript); err != nil {
			lastErr = err
			continue // Retry
		}

		// Now bind to the original driver (ena for AWS instances)
		if err := executeBindToEnaCommand(pciAddress, bindingScript); err != nil {
			lastErr = err
			continue // Retry
		}

		log.Printf("Successfully unbound PCI address %s from DPDK driver and bound to ena driver", pciAddress)
		return nil // Success, exit the retry loop
	}

	// If we still have an error after all retries, log it and return
	if lastErr != nil {
		log.Printf("Warning: Failed to unbind PCI address %s after %d retries: %v", pciAddress, maxRetries, lastErr)
	}

	return lastErr
}

// unbindInterfaceFromDPDK unbinds a network interface from a DPDK driver and rebinds it to the original driver
func unbindInterfaceFromDPDK(ifaceName string, cfg *config.ENIManagerConfig) error {
	// Resolve the PCI address for the interface or PCI address string
	pciAddress, err := resolvePCIAddress(ifaceName, cfg)
	if err != nil {
		return err
	}

	// Get or create bound interface information
	boundInterface, exists := cfg.DPDKBoundInterfaces[pciAddress]
	if !exists {
		// If we don't have information about this PCI address, log a warning and return
		// This is likely a case where the interface was never bound to DPDK
		if isPCIAddressFormat(ifaceName) {
			log.Printf("Warning: No information found for PCI address %s in DPDKBoundInterfaces map", pciAddress)
			return nil
		}

		// If we're trying to unbind by interface name, create a default entry
		boundInterface = createDefaultBoundInterface(pciAddress, ifaceName)
	}

	// Log detailed information about what we're unbinding
	log.Printf("Unbinding details - PCI: %s, Interface: %s, ENI ID: %s, NodeENI: %s",
		pciAddress, boundInterface.IfaceName, boundInterface.ENIID, boundInterface.NodeENIName)

	// Check if the DPDK binding script exists
	if _, err := os.Stat(cfg.DPDKBindingScript); os.IsNotExist(err) {
		return fmt.Errorf("DPDK binding script %s does not exist", cfg.DPDKBindingScript)
	}

	// Check if the PCI device exists before attempting to unbind it
	pciDevicePath := fmt.Sprintf("/sys/bus/pci/devices/%s", pciAddress)
	if _, err := os.Stat(pciDevicePath); os.IsNotExist(err) {
		return handleNonExistentPCIDevice(pciAddress, boundInterface, cfg)
	}

	// Acquire a lock for this PCI address to prevent concurrent operations
	releaseLock, err := acquireDPDKLock(pciAddress)
	if err != nil {
		return err
	}
	defer releaseLock()

	// Acquire the global DPDK mutex to ensure only one DPDK operation happens at a time
	dpdkMutex.Lock()
	defer dpdkMutex.Unlock()

	// Execute the unbind and rebind commands with retry logic
	err = executeUnbindWithRetry(pciAddress, cfg.DPDKBindingScript)

	// Even if we have an error, continue with cleanup
	// Update the NodeENI status
	if updateErr := updateNodeENIForUnbinding(boundInterface); updateErr != nil {
		log.Printf("Warning: Failed to update NodeENI status: %v", updateErr)
	}

	// Remove from our tracking map
	delete(cfg.DPDKBoundInterfaces, pciAddress)

	// Update the persistent store
	updatePersistentStore(pciAddress, boundInterface, cfg)

	// Update the SRIOV device plugin configuration to remove this device
	if sriovErr := removeSRIOVDevicePluginConfig(boundInterface.IfaceName, pciAddress, cfg); sriovErr != nil {
		log.Printf("Warning: Failed to update SRIOV device plugin config: %v", sriovErr)
	}

	// Wait a moment for the interface to reappear
	log.Printf("Waiting for interface to reappear after unbinding from DPDK")
	time.Sleep(2 * time.Second)

	return err
}

// getPCIAddressForInterface gets the PCI address for a network interface
func getPCIAddressForInterface(ifaceName string) (string, error) {
	// Check if the interface exists
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); os.IsNotExist(err) {
		return "", fmt.Errorf("interface %s does not exist", ifaceName)
	}

	// Path to the sysfs PCI device link for this interface
	sysfsPCIPath := fmt.Sprintf("/sys/class/net/%s/device", ifaceName)

	// Read the symlink to get the PCI path
	pciPath, err := os.Readlink(sysfsPCIPath)
	if err != nil {
		return "", fmt.Errorf("failed to read PCI path for interface %s: %v", ifaceName, err)
	}

	// Extract the PCI address from the path
	// The path is typically something like "../../../0000:00:06.0"
	pciAddress := filepath.Base(pciPath)

	// Validate the PCI address format
	pciRegex := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	if !pciRegex.MatchString(pciAddress) {
		return "", fmt.Errorf("invalid PCI address format: %s", pciAddress)
	}

	return pciAddress, nil
}

// extractDeviceIndexFromInterfaceName extracts the device index from an interface name
func extractDeviceIndexFromInterfaceName(ifaceName string) (int, error) {
	var deviceIndex int
	var err error

	if strings.HasPrefix(ifaceName, "eth") {
		_, err = fmt.Sscanf(ifaceName, "eth%d", &deviceIndex)
	} else if strings.HasPrefix(ifaceName, "ens") {
		_, err = fmt.Sscanf(ifaceName, "ens%d", &deviceIndex)
	} else {
		return 0, fmt.Errorf("unsupported interface name format: %s", ifaceName)
	}

	if err != nil {
		return 0, fmt.Errorf("failed to parse device index from interface name %s: %v", ifaceName, err)
	}

	return deviceIndex, nil
}

// findPCIAddressByDirectLookup tries to find a PCI address by directly looking for the interface
func findPCIAddressByDirectLookup(ifaceName string, pciDevices []string) string {
	for _, pciAddr := range pciDevices {
		// Check if this device has a network interface
		netDir := fmt.Sprintf("/sys/bus/pci/devices/%s/net", pciAddr)
		if _, err := os.Stat(netDir); err == nil {
			// This device has a network interface, check if it matches our interface name
			files, err := os.ReadDir(netDir)
			if err == nil && len(files) > 0 {
				for _, file := range files {
					if file.Name() == ifaceName {
						log.Printf("Found PCI address %s for interface %s by direct lookup", pciAddr, ifaceName)
						return pciAddr
					}
				}
			}
		}
	}
	return ""
}

// findPCIAddressByDeviceIndex tries to find a PCI address based on the device index
func findPCIAddressByDeviceIndex(deviceIndex int, pciDevices []string) string {
	// In AWS, eth0 is typically at 0000:00:05.0
	if deviceIndex == 0 {
		if contains(pciDevices, "0000:00:05.0") {
			log.Printf("Using PCI address 0000:00:05.0 for eth0 (standard AWS pattern)")
			return "0000:00:05.0"
		}
	} else {
		// For secondary interfaces, try common patterns
		// In AWS, secondary interfaces often follow a pattern like 0000:00:06.0, 0000:00:07.0, etc.
		candidateAddr := fmt.Sprintf("0000:00:%02d.0", deviceIndex+5)
		if contains(pciDevices, candidateAddr) {
			log.Printf("Using PCI address %s for interface with index %d (standard AWS pattern)",
				candidateAddr, deviceIndex)
			return candidateAddr
		}
	}
	return ""
}

// checkPCIDeviceForDPDKDriver checks if a PCI device is bound to a DPDK driver
func checkPCIDeviceForDPDKDriver(pciDevPath string) (string, bool) {
	driverPath := fmt.Sprintf("%s/driver", pciDevPath)
	driverLink, err := os.Readlink(driverPath)
	if err != nil {
		return "", false
	}

	// Extract the driver name from the path
	driverParts := strings.Split(driverLink, "/")
	if len(driverParts) == 0 {
		return "", false
	}

	driverName := driverParts[len(driverParts)-1]
	return driverName, (driverName == "vfio-pci" || driverName == "igb_uio")
}

// findPCIAddressByPatternMatching tries to find a PCI address using common patterns
func findPCIAddressByPatternMatching(deviceIndex int, ifaceName string) string {
	// Common PCI address patterns for AWS instances
	pciPatterns := []string{
		fmt.Sprintf("0000:00:%02d.0", deviceIndex+5), // Most common pattern for AWS (eth0 -> 00:05.0, eth1 -> 00:06.0)
		fmt.Sprintf("0000:00:%02d.0", deviceIndex+3), // Alternative pattern
		fmt.Sprintf("0000:00:%02d.0", deviceIndex+4), // Another alternative
	}

	// Try each pattern
	for _, pattern := range pciPatterns {
		// Check if this PCI device exists
		pciDevPath := fmt.Sprintf("/sys/bus/pci/devices/%s", pattern)
		if _, err := os.Stat(pciDevPath); err == nil {
			// Device exists, check if it's bound to a DPDK driver
			driverName, isDPDKDriver := checkPCIDeviceForDPDKDriver(pciDevPath)
			if isDPDKDriver {
				// This is likely our device
				log.Printf("Found PCI device %s bound to DPDK driver %s, likely for interface %s",
					pattern, driverName, ifaceName)
				return pattern
			}
		}
	}
	return ""
}

// findAnyDPDKBoundDevice tries to find any PCI device bound to a DPDK driver
func findAnyDPDKBoundDevice(ifaceName string) (string, error) {
	// List all PCI devices and check if any are bound to DPDK drivers
	allPciDevices, err := filepath.Glob("/sys/bus/pci/devices/*")
	if err != nil {
		return "", fmt.Errorf("failed to list PCI devices: %v", err)
	}

	for _, devPath := range allPciDevices {
		// Extract the PCI address from the path
		pciAddress := filepath.Base(devPath)

		// Check if this device is bound to a DPDK driver
		driverName, isDPDKDriver := checkPCIDeviceForDPDKDriver(devPath)
		if isDPDKDriver {
			// This is a DPDK-bound device, return it as a best-effort approach
			log.Printf("Found PCI device %s bound to DPDK driver %s, might be for interface %s",
				pciAddress, driverName, ifaceName)
			return pciAddress, nil
		}
	}

	return "", nil
}

// findPCIAddressByInterfacePattern tries to find the PCI address for an interface
// that might be bound to a DPDK driver and no longer visible as a network interface
func findPCIAddressByInterfacePattern(ifaceName string) (string, error) {
	// First try to get the device index from the interface name
	deviceIndex, err := extractDeviceIndexFromInterfaceName(ifaceName)
	if err != nil {
		return "", err
	}

	log.Printf("Searching for PCI address for interface %s with device index %d", ifaceName, deviceIndex)

	// First, try to find the PCI address by checking all PCI devices
	// This is more reliable than using patterns
	pciDevices, err := listPCIDevices()
	if err != nil {
		log.Printf("Error listing PCI devices: %v", err)
		// Continue with pattern-based approach as fallback
	} else {
		// Try to find a device that matches the interface name directly
		if pciAddr := findPCIAddressByDirectLookup(ifaceName, pciDevices); pciAddr != "" {
			return pciAddr, nil
		}

		// If we couldn't find a direct match, try to infer based on device index
		if pciAddr := findPCIAddressByDeviceIndex(deviceIndex, pciDevices); pciAddr != "" {
			return pciAddr, nil
		}
	}

	// Fallback to common patterns if direct lookup failed
	if pciAddr := findPCIAddressByPatternMatching(deviceIndex, ifaceName); pciAddr != "" {
		return pciAddr, nil
	}

	// If we couldn't find a matching device, try a more exhaustive search
	pciAddr, err := findAnyDPDKBoundDevice(ifaceName)
	if err != nil {
		return "", err
	}

	if pciAddr != "" {
		return pciAddr, nil
	}

	return "", fmt.Errorf("could not find PCI address for interface %s", ifaceName)
}

// removeSRIOVDevicePluginConfig removes a device from the SRIOV device plugin configuration
func removeSRIOVDevicePluginConfig(ifaceName, pciAddress string, cfg *config.ENIManagerConfig) error {
	// Check if the SRIOV device plugin config path is set
	if cfg.SRIOVDPConfigPath == "" {
		return fmt.Errorf("SRIOV device plugin config path is not set")
	}

	// Check if the config file exists
	if _, err := os.Stat(cfg.SRIOVDPConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("SRIOV device plugin config file %s does not exist", cfg.SRIOVDPConfigPath)
	}

	// Read the current config
	configData, err := os.ReadFile(cfg.SRIOVDPConfigPath)
	if err != nil {
		return fmt.Errorf("failed to read SRIOV device plugin config: %v", err)
	}

	// Parse the config
	var config SRIOVDPConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to parse SRIOV device plugin config: %v", err)
	}

	// Find and remove the resource that contains this PCI address
	modified := false
	for i := range config.ResourceList {
		resource := &config.ResourceList[i]

		// Find the resource that contains this PCI address
		newDevices := []PCIDeviceInfo{}
		for _, device := range resource.Devices {
			if device.PCIAddress != pciAddress {
				newDevices = append(newDevices, device)
			} else {
				modified = true
				log.Printf("Removing PCI device %s from SRIOV device plugin config for resource %s",
					pciAddress, resource.ResourceName)
			}
		}

		// Update the devices list
		resource.Devices = newDevices
	}

	// Remove empty resource lists
	var newResourceList []SRIOVDeviceConfig
	for _, resource := range config.ResourceList {
		if len(resource.Devices) > 0 {
			newResourceList = append(newResourceList, resource)
		} else {
			log.Printf("Removing empty resource %s", resource.ResourceName)
			modified = true
		}
	}
	config.ResourceList = newResourceList

	// If we didn't modify anything, return
	if !modified {
		log.Printf("PCI device %s not found in SRIOV device plugin config", pciAddress)
		return nil
	}

	// Write the updated config back to the file
	updatedConfig, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal updated SRIOV device plugin config: %v", err)
	}

	if err := os.WriteFile(cfg.SRIOVDPConfigPath, updatedConfig, 0644); err != nil {
		return fmt.Errorf("failed to write updated SRIOV device plugin config: %v", err)
	}

	log.Printf("Successfully updated SRIOV device plugin config to remove PCI device %s", pciAddress)
	return nil
}

// getNodeENI retrieves a NodeENI resource by name
func getNodeENI(name string) (*networkingv1alpha1.NodeENI, error) {
	// Create a Kubernetes client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	// Get the NodeENI resource
	result := &networkingv1alpha1.NodeENI{}
	err = clientset.CoreV1().RESTClient().
		Get().
		AbsPath(fmt.Sprintf("/apis/networking.k8s.aws/v1alpha1/nodeenis/%s", name)).
		Do(context.Background()).
		Into(result)
	if err != nil {
		return nil, fmt.Errorf("failed to get NodeENI %s: %v", name, err)
	}

	return result, nil
}

// getResourceNameForDevice determines the resource name to use for a DPDK device
func getResourceNameForDevice(ifaceName, pciAddress string, cfg *config.ENIManagerConfig) string {
	// First check if we have a resource name for this interface
	resourceName, ok := cfg.DPDKResourceNames[ifaceName]
	if ok {
		return resourceName
	}

	// Check if we have a resource name for this PCI address
	for _, boundInterface := range cfg.DPDKBoundInterfaces {
		if boundInterface.PCIAddress == pciAddress {
			if boundInterface.NodeENIName != "" {
				// Try to get the resource name from the NodeENI
				nodeENI, err := getNodeENI(boundInterface.NodeENIName)
				if err == nil && nodeENI.Spec.DPDKResourceName != "" {
					resourceName = nodeENI.Spec.DPDKResourceName
					log.Printf("Using resource name %s from NodeENI %s for PCI address %s",
						resourceName, boundInterface.NodeENIName, pciAddress)
					return resourceName
				}
			}
		}
	}

	// If we still don't have a resource name, generate one based on the PCI address
	// Use a hash of the PCI address to generate a unique, stable resource name
	// This ensures the same PCI address always gets the same resource name
	// regardless of the interface name
	h := fnv.New32a()
	h.Write([]byte(pciAddress))
	hash := h.Sum32()
	resourceName = fmt.Sprintf("intel.com/intel_sriov_netdevice_pci_%x", hash)
	log.Printf("Generated resource name %s for PCI address %s", resourceName, pciAddress)

	return resourceName
}

// createNewSRIOVConfig creates a new SRIOV device plugin configuration
func createNewSRIOVConfig(resourceName, pciAddress, driver string) SRIOVDPConfig {
	return SRIOVDPConfig{
		ResourceList: []SRIOVDeviceConfig{
			{
				ResourceName: resourceName,
				DeviceType:   "netdevice",
				Devices: []PCIDeviceInfo{
					{
						PCIAddress: pciAddress,
						Driver:     driver,
					},
				},
			},
		},
	}
}

// readExistingSRIOVConfig reads and parses the existing SRIOV device plugin configuration
func readExistingSRIOVConfig(configPath string) (SRIOVDPConfig, error) {
	var config SRIOVDPConfig

	// Read the existing config file
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return config, fmt.Errorf("failed to read SRIOV device plugin config: %v", err)
	}

	// Parse the config
	if err := json.Unmarshal(configData, &config); err != nil {
		return config, fmt.Errorf("failed to parse SRIOV device plugin config: %v", err)
	}

	return config, nil
}

// updateExistingSRIOVConfig updates an existing SRIOV device plugin configuration
func updateExistingSRIOVConfig(config SRIOVDPConfig, resourceName, pciAddress, driver string) SRIOVDPConfig {
	// Check if the resource already exists
	resourceExists := false
	deviceExists := false

	for i, resource := range config.ResourceList {
		if resource.ResourceName == resourceName {
			resourceExists = true
			// Check if the device already exists
			for j, device := range resource.Devices {
				if device.PCIAddress == pciAddress {
					deviceExists = true
					// Update the driver if it's different
					if device.Driver != driver {
						config.ResourceList[i].Devices[j].Driver = driver
					}
					break
				}
			}
			// If the device doesn't exist, add it
			if !deviceExists {
				config.ResourceList[i].Devices = append(config.ResourceList[i].Devices, PCIDeviceInfo{
					PCIAddress: pciAddress,
					Driver:     driver,
				})
			}
			break
		}
	}

	// If the resource doesn't exist, add it
	if !resourceExists {
		config.ResourceList = append(config.ResourceList, SRIOVDeviceConfig{
			ResourceName: resourceName,
			DeviceType:   "netdevice",
			Devices: []PCIDeviceInfo{
				{
					PCIAddress: pciAddress,
					Driver:     driver,
				},
			},
		})
	}

	return config
}

// writeSRIOVConfig writes the SRIOV device plugin configuration to a file
func writeSRIOVConfig(config SRIOVDPConfig, configPath string) error {
	// Marshal the config to JSON
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SRIOV device plugin config: %v", err)
	}

	// Write the config to the file
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return fmt.Errorf("failed to write SRIOV device plugin config: %v", err)
	}

	return nil
}

// updateSRIOVDevicePluginConfig updates the SRIOV device plugin configuration
func updateSRIOVDevicePluginConfig(ifaceName, pciAddress, driver string, cfg *config.ENIManagerConfig) error {
	// Step 1: Validate configuration
	if cfg.SRIOVDPConfigPath == "" {
		return fmt.Errorf("SRIOV device plugin config path is not set")
	}

	// Step 2: Determine the resource name to use
	resourceName := getResourceNameForDevice(ifaceName, pciAddress, cfg)

	// Store the resource name for future use
	cfg.DPDKResourceNames[ifaceName] = resourceName

	// Step 3: Check if the config file exists and handle accordingly
	var config SRIOVDPConfig
	if _, err := os.Stat(cfg.SRIOVDPConfigPath); os.IsNotExist(err) {
		// Create a new config file with this device
		config = createNewSRIOVConfig(resourceName, pciAddress, driver)
	} else {
		// Read and update the existing config file
		var err error
		config, err = readExistingSRIOVConfig(cfg.SRIOVDPConfigPath)
		if err != nil {
			return err
		}

		config = updateExistingSRIOVConfig(config, resourceName, pciAddress, driver)
	}

	// Step 4: Write the updated config back to the file
	if err := writeSRIOVConfig(config, cfg.SRIOVDPConfigPath); err != nil {
		return err
	}

	log.Printf("Successfully updated SRIOV device plugin config for interface %s with resource name %s",
		ifaceName, resourceName)
	return nil
}

// SRIOVConfigManager manages SR-IOV device plugin configuration with enhanced error handling
type SRIOVConfigManager struct {
	configPath string
	backupPath string
	maxRetries int
	retryDelay time.Duration
	fileMutex  sync.Mutex // Add file-level mutex for atomic operations
	k8sClient  *kubernetes.Clientset
}

// NewSRIOVConfigManager creates a new SR-IOV configuration manager
func NewSRIOVConfigManager(configPath string) *SRIOVConfigManager {
	// Create Kubernetes client for device plugin restart
	k8sClient, err := createK8sClientset()
	if err != nil {
		log.Printf("Warning: Failed to create Kubernetes client for SR-IOV restart: %v", err)
		// Continue without client - restart will be skipped but config updates will work
	}

	return &SRIOVConfigManager{
		configPath: configPath,
		backupPath: configPath + ".backup",
		maxRetries: 3,
		retryDelay: 2 * time.Second,
		k8sClient:  k8sClient,
	}
}

// updateSRIOVDevicePluginConfigWithRetry updates the SR-IOV device plugin config with retry logic and rollback
func updateSRIOVDevicePluginConfigWithRetry(ifaceName, pciAddress, driver, resourceName string, cfg *config.ENIManagerConfig) error {
	manager := NewSRIOVConfigManager(cfg.SRIOVDPConfigPath)

	// Create backup before making changes
	if err := manager.createBackup(); err != nil {
		log.Printf("Warning: Failed to create backup of SR-IOV config: %v", err)
	}

	// Load current configuration to check for changes
	currentConfig, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load current SR-IOV config: %v", err)
	}

	// Create a copy for comparison
	originalConfig := deepCopySRIOVConfig(currentConfig)

	// Attempt to update the configuration with retries
	var lastErr error
	var configChanged bool
	for attempt := 1; attempt <= manager.maxRetries; attempt++ {
		log.Printf("Attempting to update SR-IOV config (attempt %d/%d) for resource %s",
			attempt, manager.maxRetries, resourceName)

		if err := manager.updateConfig(ifaceName, pciAddress, driver, resourceName); err != nil {
			lastErr = err
			log.Printf("Attempt %d failed: %v", attempt, err)

			if attempt < manager.maxRetries {
				time.Sleep(manager.retryDelay)
				continue
			}

			// All attempts failed, try to restore backup
			log.Printf("All attempts failed, attempting to restore backup")
			if restoreErr := manager.restoreBackup(); restoreErr != nil {
				log.Printf("Critical: Failed to restore backup: %v", restoreErr)
			}
			return fmt.Errorf("failed to update SR-IOV config after %d attempts: %v", manager.maxRetries, lastErr)
		}

		// Success - validate the configuration
		if err := manager.validateConfig(); err != nil {
			log.Printf("Configuration validation failed: %v", err)
			if restoreErr := manager.restoreBackup(); restoreErr != nil {
				log.Printf("Critical: Failed to restore backup after validation failure: %v", restoreErr)
			}
			return fmt.Errorf("configuration validation failed: %v", err)
		}

		// Check if configuration actually changed
		updatedConfig, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
		if err != nil {
			log.Printf("Warning: Failed to load updated config for comparison: %v", err)
			configChanged = true // Assume changed if we can't verify
		} else {
			configChanged = !sriovConfigsEqual(originalConfig, updatedConfig)
		}

		// Only restart device plugin if configuration changed
		if configChanged {
			if err := manager.restartDevicePlugin(); err != nil {
				log.Printf("Warning: Failed to restart SR-IOV device plugin: %v", err)
				// Don't fail the operation for restart failures
			} else {
				log.Printf("Successfully restarted SR-IOV device plugin due to configuration change")
			}
		} else {
			log.Printf("SR-IOV configuration unchanged for resource %s - skipping device plugin restart", resourceName)
		}

		log.Printf("Successfully updated SR-IOV config for resource %s", resourceName)
		return nil
	}

	return lastErr
}

// createBackup creates a backup of the current configuration
func (m *SRIOVConfigManager) createBackup() error {
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		// No config file exists, nothing to backup
		return nil
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %v", err)
	}

	if err := os.WriteFile(m.backupPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write backup file: %v", err)
	}

	log.Printf("Created backup of SR-IOV config at %s", m.backupPath)
	return nil
}

// restoreBackup restores the configuration from backup
func (m *SRIOVConfigManager) restoreBackup() error {
	if _, err := os.Stat(m.backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup file does not exist")
	}

	data, err := os.ReadFile(m.backupPath)
	if err != nil {
		return fmt.Errorf("failed to read backup file: %v", err)
	}

	if err := os.WriteFile(m.configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to restore config file: %v", err)
	}

	log.Printf("Restored SR-IOV config from backup")
	return nil
}

// updateConfig updates the SR-IOV configuration with the specified device
func (m *SRIOVConfigManager) updateConfig(ifaceName, pciAddress, driver, resourceName string) error {
	// Acquire file-level mutex for atomic operations
	m.fileMutex.Lock()
	defer m.fileMutex.Unlock()

	// Step 1: Check if the config file exists and handle accordingly
	var config SRIOVDPConfig
	if _, err := os.Stat(m.configPath); os.IsNotExist(err) {
		// Create a new config file with this device
		config = createNewSRIOVConfig(resourceName, pciAddress, driver)
	} else {
		// Read and update the existing config file
		var err error
		config, err = readExistingSRIOVConfig(m.configPath)
		if err != nil {
			return err
		}

		config = updateExistingSRIOVConfig(config, resourceName, pciAddress, driver)
	}

	// Step 2: Write the updated config back to the file atomically
	if err := m.writeConfigAtomic(config); err != nil {
		return err
	}

	return nil
}

// writeConfigAtomic writes the SR-IOV configuration atomically using a temporary file
func (m *SRIOVConfigManager) writeConfigAtomic(config SRIOVDPConfig) error {
	// Create a unique temporary file to avoid conflicts
	tempFile, err := os.CreateTemp(filepath.Dir(m.configPath), "sriov-config-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary config file: %v", err)
	}
	tempPath := tempFile.Name()

	// Ensure cleanup on any failure
	defer func() {
		tempFile.Close()
		if _, err := os.Stat(tempPath); err == nil {
			os.Remove(tempPath)
		}
	}()

	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SR-IOV config: %v", err)
	}

	if _, err := tempFile.Write(configData); err != nil {
		return fmt.Errorf("failed to write temporary config file: %v", err)
	}

	// Sync to ensure data is written to disk
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary config file: %v", err)
	}

	// Close the file before rename
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary config file: %v", err)
	}

	// Atomically move the temporary file to the final location
	if err := os.Rename(tempPath, m.configPath); err != nil {
		return fmt.Errorf("failed to move temporary config file: %v", err)
	}

	// Clear the defer cleanup since rename was successful
	tempPath = ""
	return nil
}

// validateConfig validates the SR-IOV configuration file
func (m *SRIOVConfigManager) validateConfig() error {
	// Check if file exists and is readable
	if _, err := os.Stat(m.configPath); err != nil {
		return fmt.Errorf("config file is not accessible: %v", err)
	}

	// Try to parse the configuration
	config, err := readExistingSRIOVConfig(m.configPath)
	if err != nil {
		return fmt.Errorf("config file is not valid JSON: %v", err)
	}

	// Validate the structure
	if len(config.ResourceList) == 0 {
		return fmt.Errorf("config file has no resources defined")
	}

	// Track PCI addresses and resource names to detect duplicates
	pciAddressMap := make(map[string][]string) // PCI address -> list of resource names using it
	resourceNameMap := make(map[string]bool)   // Track duplicate resource names

	// Validate each resource
	for i, resource := range config.ResourceList {
		if resource.ResourceName == "" {
			return fmt.Errorf("resource %d has empty resource name", i)
		}

		// Check for duplicate resource names
		if resourceNameMap[resource.ResourceName] {
			return fmt.Errorf("duplicate resource name found: %s", resource.ResourceName)
		}
		resourceNameMap[resource.ResourceName] = true

		// Validate resource name format
		if err := validateSRIOVResourceName(resource.ResourceName); err != nil {
			return fmt.Errorf("resource %d has invalid name: %v", i, err)
		}

		if len(resource.Devices) == 0 {
			log.Printf("Warning: Resource %s has no devices defined", resource.ResourceName)
		}

		// Validate each device
		for j, device := range resource.Devices {
			if device.PCIAddress == "" {
				return fmt.Errorf("device %d in resource %s has empty PCI address", j, resource.ResourceName)
			}

			// Validate PCI address format (strict check)
			if !isValidPCIAddress(device.PCIAddress) {
				return fmt.Errorf("device %d in resource %s has invalid PCI address format: %s",
					j, resource.ResourceName, device.PCIAddress)
			}

			// Track PCI addresses for duplicate detection
			pciAddressMap[device.PCIAddress] = append(pciAddressMap[device.PCIAddress], resource.ResourceName)
		}
	}

	// Check for duplicate PCI addresses across different resources
	for pciAddr, resourceNames := range pciAddressMap {
		if len(resourceNames) > 1 {
			return fmt.Errorf("PCI address %s is used by multiple resources: %v", pciAddr, resourceNames)
		}
	}

	log.Printf("SR-IOV configuration validation passed")
	return nil
}

// isValidPCIAddress performs strict validation of PCI address format
func isValidPCIAddress(addr string) bool {
	// Strict format check: XXXX:XX:XX.X where X is lowercase hex digit
	if len(addr) != 12 {
		return false
	}

	// Use regex to validate exact format with lowercase hex digits only
	pciRegex := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	if !pciRegex.MatchString(addr) {
		return false
	}

	// Additional validation: split and check each part
	parts := strings.Split(addr, ":")
	if len(parts) != 3 {
		return false
	}

	// Check domain (4 hex digits)
	if len(parts[0]) != 4 || !isValidHexString(parts[0]) {
		return false
	}

	// Check bus (2 hex digits)
	if len(parts[1]) != 2 || !isValidHexString(parts[1]) {
		return false
	}

	// Check device.function (XX.X format)
	devFunc := parts[2]
	if len(devFunc) != 4 || devFunc[2] != '.' {
		return false
	}

	// Validate device and function parts
	devicePart := devFunc[:2]
	functionPart := devFunc[3:]

	if !isValidHexString(devicePart) || !isValidHexString(functionPart) {
		return false
	}

	return true
}

// isValidHexString checks if a string contains only valid lowercase hex characters
func isValidHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// restartDevicePlugin restarts the SR-IOV device plugin to pick up configuration changes
func (m *SRIOVConfigManager) restartDevicePlugin() error {
	log.Printf("Attempting to restart SR-IOV device plugin using Kubernetes API")

	if m.k8sClient == nil {
		return fmt.Errorf("Kubernetes client not available for device plugin restart")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Try multiple common SR-IOV device plugin label selectors
	labelSelectors := []string{
		"app=sriov-device-plugin",  // Most common
		"app=sriovdp",              // Alternative naming
		"name=sriov-device-plugin", // Another common pattern
	}

	var lastErr error
	var deletedPods int

	for _, labelSelector := range labelSelectors {
		log.Printf("Searching for SR-IOV device plugin pods with label selector: %s", labelSelector)

		// List pods in kube-system namespace with the label selector
		pods, err := m.k8sClient.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			lastErr = fmt.Errorf("failed to list pods with selector %s: %v", labelSelector, err)
			log.Printf("Warning: %v", lastErr)
			continue
		}

		if len(pods.Items) == 0 {
			log.Printf("No pods found with label selector: %s", labelSelector)
			continue
		}

		log.Printf("Found %d SR-IOV device plugin pods with selector %s", len(pods.Items), labelSelector)

		// Delete each pod to trigger restart
		for _, pod := range pods.Items {
			log.Printf("Deleting SR-IOV device plugin pod: %s", pod.Name)

			err := m.k8sClient.CoreV1().Pods("kube-system").Delete(ctx, pod.Name, metav1.DeleteOptions{
				GracePeriodSeconds: &[]int64{0}[0], // Force immediate deletion
			})
			if err != nil {
				log.Printf("Warning: Failed to delete pod %s: %v", pod.Name, err)
				lastErr = err
			} else {
				log.Printf("Successfully deleted SR-IOV device plugin pod: %s", pod.Name)
				deletedPods++
			}
		}

		// If we found and processed pods with this selector, we're done
		if len(pods.Items) > 0 {
			break
		}
	}

	if deletedPods == 0 {
		if lastErr != nil {
			return fmt.Errorf("failed to restart any SR-IOV device plugin pods: %v", lastErr)
		}
		return fmt.Errorf("no SR-IOV device plugin pods found in kube-system namespace")
	}

	log.Printf("Successfully initiated restart of %d SR-IOV device plugin pods", deletedPods)
	return nil
}

// SRIOVRestartConfig holds configuration for SR-IOV device plugin restart behavior
type SRIOVRestartConfig struct {
	MaxRetries             int           `json:"maxRetries"`             // Maximum number of restart attempts
	BaseWaitTime           time.Duration `json:"baseWaitTime"`           // Base wait time after restart
	MaxWaitTime            time.Duration `json:"maxWaitTime"`            // Maximum wait time for verification
	RetryBackoffMultiplier float64       `json:"retryBackoffMultiplier"` // Exponential backoff multiplier
	PodReadinessTimeout    time.Duration `json:"podReadinessTimeout"`    // Timeout for pod readiness check
	ResourceVerifyTimeout  time.Duration `json:"resourceVerifyTimeout"`  // Timeout for resource verification
	StaleResourceCleanup   bool          `json:"staleResourceCleanup"`   // Whether to verify stale resource cleanup
}

// getDefaultSRIOVRestartConfig returns default configuration for SR-IOV restart behavior
func getDefaultSRIOVRestartConfig() SRIOVRestartConfig {
	return SRIOVRestartConfig{
		MaxRetries:             3,
		BaseWaitTime:           60 * time.Second,  // Default 60 seconds as requested
		MaxWaitTime:            180 * time.Second, // Maximum 3 minutes total wait
		RetryBackoffMultiplier: 1.5,               // Exponential backoff
		PodReadinessTimeout:    120 * time.Second, // 2 minutes for pod readiness
		ResourceVerifyTimeout:  90 * time.Second,  // 90 seconds for resource verification
		StaleResourceCleanup:   true,              // Verify stale resource cleanup
	}
}

// restartDevicePluginWithVerification restarts the SR-IOV device plugin with enhanced verification and retry logic
func (m *SRIOVConfigManager) restartDevicePluginWithVerification() error {
	return m.restartDevicePluginWithConfig(getDefaultSRIOVRestartConfig())
}

// restartDevicePluginWithConfig restarts the SR-IOV device plugin with custom configuration
func (m *SRIOVConfigManager) restartDevicePluginWithConfig(config SRIOVRestartConfig) error {
	log.Printf("Starting enhanced SR-IOV device plugin restart with verification (maxRetries=%d, baseWait=%v)",
		config.MaxRetries, config.BaseWaitTime)

	if m.k8sClient == nil {
		return fmt.Errorf("Kubernetes client not available for device plugin restart with verification")
	}

	// Get current node resources before restart for comparison
	preRestartResources, err := m.getCurrentNodeResources()
	if err != nil {
		log.Printf("Warning: Failed to get current node resources: %v", err)
	} else {
		log.Printf("Pre-restart node resources: %v", preRestartResources)
	}

	// Get expected resources from configuration
	expectedResources, err := m.getExpectedDPDKResources()
	if err != nil {
		log.Printf("Warning: Failed to get expected DPDK resources: %v", err)
		// Continue with restart anyway
	} else {
		log.Printf("Expected resources after restart: %v", expectedResources)
	}

	// Identify stale resources that should be removed
	staleResources := m.identifyStaleResources(preRestartResources, expectedResources)
	if len(staleResources) > 0 {
		log.Printf("Stale resources that should be removed: %v", staleResources)
	}

	for attempt := 1; attempt <= config.MaxRetries; attempt++ {
		log.Printf("SR-IOV device plugin restart attempt %d/%d", attempt, config.MaxRetries)

		// Perform the restart
		if err := m.restartDevicePlugin(); err != nil {
			log.Printf("Restart attempt %d failed: %v", attempt, err)
			if attempt == config.MaxRetries {
				return fmt.Errorf("all restart attempts failed, last error: %v", err)
			}

			// Exponential backoff before retry
			backoffDelay := time.Duration(float64(config.BaseWaitTime) *
				(config.RetryBackoffMultiplier * float64(attempt-1)))
			if backoffDelay > config.MaxWaitTime {
				backoffDelay = config.MaxWaitTime
			}
			log.Printf("Waiting %v before retry attempt %d (exponential backoff)", backoffDelay, attempt+1)
			time.Sleep(backoffDelay)
			continue
		}

		// Wait for device plugin pods to be ready
		log.Printf("Waiting for SR-IOV device plugin pods to be ready (timeout: %v)", config.PodReadinessTimeout)
		if !m.waitForDevicePluginPodsReady(config.PodReadinessTimeout) {
			log.Printf("Device plugin pods not ready after attempt %d, will retry", attempt)
			if attempt < config.MaxRetries {
				continue
			}
			return fmt.Errorf("device plugin pods not ready after %d attempts", config.MaxRetries)
		}

		// Wait for the base time to allow plugin to scan and update resources
		log.Printf("Waiting %v for SR-IOV device plugin to scan devices and update node resources", config.BaseWaitTime)
		time.Sleep(config.BaseWaitTime)

		// Verify expected resources are properly advertised
		if len(expectedResources) > 0 {
			log.Printf("Verifying expected resources are advertised (timeout: %v)", config.ResourceVerifyTimeout)
			if !m.verifyResourcesWithTimeout(expectedResources, config.ResourceVerifyTimeout) {
				log.Printf("Expected resources not properly advertised after attempt %d", attempt)
				if attempt < config.MaxRetries {
					continue
				}
				return fmt.Errorf("expected resources not advertised after %d attempts", config.MaxRetries)
			}
			log.Printf("✓ All expected resources are properly advertised")
		}

		// Verify stale resources are removed if cleanup is enabled
		if config.StaleResourceCleanup && len(staleResources) > 0 {
			log.Printf("Verifying stale resources are removed: %v", staleResources)
			if !m.verifyStaleResourcesRemoved(staleResources, config.ResourceVerifyTimeout) {
				log.Printf("Stale resources still present after attempt %d", attempt)
				if attempt < config.MaxRetries {
					continue
				}
				log.Printf("Warning: Some stale resources may still be present after %d attempts", config.MaxRetries)
				// Don't fail for stale resources, just warn
			} else {
				log.Printf("✓ All stale resources have been removed")
			}
		}

		// Final verification that device plugin is running properly
		if !m.verifyDevicePluginRunning() {
			log.Printf("Device plugin not running properly after attempt %d", attempt)
			if attempt < config.MaxRetries {
				continue
			}
			return fmt.Errorf("device plugin not running properly after %d attempts", config.MaxRetries)
		}

		log.Printf("✓ SR-IOV device plugin restart successful after attempt %d", attempt)
		return nil
	}

	return fmt.Errorf("failed to verify SR-IOV device plugin restart after %d attempts", config.MaxRetries)
}

// getCurrentNodeResources gets the current node's SR-IOV resources
func (m *SRIOVConfigManager) getCurrentNodeResources() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("NODE_NAME environment variable not set")
	}

	node, err := m.k8sClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s: %v", nodeName, err)
	}

	resources := make(map[string]string)
	for resourceName, quantity := range node.Status.Capacity {
		resourceStr := string(resourceName)
		// Only include SR-IOV related resources
		if strings.Contains(resourceStr, "intel.com/") || strings.Contains(resourceStr, "sriov") {
			resources[resourceStr] = quantity.String()
		}
	}

	return resources, nil
}

// identifyStaleResources identifies resources that should be removed
func (m *SRIOVConfigManager) identifyStaleResources(currentResources map[string]string, expectedResources []string) []string {
	expectedSet := make(map[string]bool)
	for _, resource := range expectedResources {
		expectedSet[resource] = true
	}

	var staleResources []string
	for resource := range currentResources {
		if !expectedSet[resource] {
			// This resource exists but is not expected, so it's stale
			staleResources = append(staleResources, resource)
		}
	}

	return staleResources
}

// waitForDevicePluginPodsReady waits for SR-IOV device plugin pods to be ready
func (m *SRIOVConfigManager) waitForDevicePluginPodsReady(timeout time.Duration) bool {
	if m.k8sClient == nil {
		log.Printf("Warning: Kubernetes client not available for pod readiness check")
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	labelSelectors := []string{
		"app=sriov-device-plugin",
		"app=sriovdp",
		"name=sriov-device-plugin",
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Timeout waiting for SR-IOV device plugin pods to be ready")
			return false
		case <-ticker.C:
			for _, selector := range labelSelectors {
				pods, err := m.k8sClient.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
					LabelSelector: selector,
				})
				if err != nil {
					log.Printf("Warning: Failed to list pods with selector %s: %v", selector, err)
					continue
				}

				if len(pods.Items) == 0 {
					continue
				}

				allReady := true
				for _, pod := range pods.Items {
					if pod.Status.Phase != corev1.PodRunning {
						allReady = false
						break
					}

					// Check if all containers are ready
					for _, condition := range pod.Status.Conditions {
						if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
							allReady = false
							break
						}
					}
					if !allReady {
						break
					}
				}

				if allReady {
					log.Printf("✓ All SR-IOV device plugin pods are ready (%d pods with selector %s)", len(pods.Items), selector)
					return true
				}

				log.Printf("Waiting for SR-IOV device plugin pods to be ready (%d pods found with selector %s)", len(pods.Items), selector)
			}
		}
	}
}

// verifyResourcesWithTimeout verifies that expected resources are advertised within timeout
func (m *SRIOVConfigManager) verifyResourcesWithTimeout(expectedResources []string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Timeout waiting for expected resources to be advertised")
			return false
		case <-ticker.C:
			if m.verifyDPDKResourcesAdvertised(expectedResources) {
				return true
			}
			log.Printf("Still waiting for expected resources to be advertised...")
		}
	}
}

// verifyStaleResourcesRemoved verifies that stale resources are removed within timeout
func (m *SRIOVConfigManager) verifyStaleResourcesRemoved(staleResources []string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Timeout waiting for stale resources to be removed")
			return false
		case <-ticker.C:
			currentResources, err := m.getCurrentNodeResources()
			if err != nil {
				log.Printf("Warning: Failed to get current node resources: %v", err)
				continue
			}

			allRemoved := true
			for _, staleResource := range staleResources {
				if _, exists := currentResources[staleResource]; exists {
					log.Printf("Stale resource %s still present", staleResource)
					allRemoved = false
				}
			}

			if allRemoved {
				log.Printf("✓ All stale resources have been removed")
				return true
			}

			log.Printf("Still waiting for stale resources to be removed...")
		}
	}
}

// getExpectedDPDKResources gets the list of DPDK resources that should be advertised
func (m *SRIOVConfigManager) getExpectedDPDKResources() ([]string, error) {
	// Load the SR-IOV configuration to see what resources should be advertised
	config, err := loadOrCreateSRIOVConfig(m.configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	var expectedResources []string
	for _, resource := range config.ResourceList {
		// Check if resource has any selectors with PCI addresses
		hasDevices := false
		for _, selector := range resource.Selectors {
			if len(selector.PCIAddresses) > 0 {
				hasDevices = true
				break
			}
		}
		if hasDevices {
			// Construct full resource name with prefix
			fullResourceName := resource.ResourceName
			if resource.ResourcePrefix != "" {
				fullResourceName = resource.ResourcePrefix + "/" + resource.ResourceName
			}
			expectedResources = append(expectedResources, fullResourceName)
		}
	}

	log.Printf("Expected DPDK resources from config: %v", expectedResources)
	return expectedResources, nil
}

// verifyDPDKResourcesAdvertised verifies that the expected DPDK resources are advertised by the node
func (m *SRIOVConfigManager) verifyDPDKResourcesAdvertised(expectedResources []string) bool {
	if len(expectedResources) == 0 {
		log.Printf("No expected resources to verify")
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get the current node name
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Printf("Warning: NODE_NAME not set, cannot verify node resources")
		return false
	}

	// Get the node object
	node, err := m.k8sClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Warning: Failed to get node %s: %v", nodeName, err)
		return false
	}

	log.Printf("Verifying DPDK resources on node %s", nodeName)
	log.Printf("Node capacity: %v", node.Status.Capacity)
	log.Printf("Node allocatable: %v", node.Status.Allocatable)

	// Check each expected resource
	allResourcesFound := true
	for _, resourceName := range expectedResources {
		// Check if resource exists in capacity
		capacityQuantity, hasCapacity := node.Status.Capacity[corev1.ResourceName(resourceName)]
		allocatableQuantity, hasAllocatable := node.Status.Allocatable[corev1.ResourceName(resourceName)]

		if !hasCapacity {
			log.Printf("Resource %s not found in node capacity", resourceName)
			allResourcesFound = false
			continue
		}

		if !hasAllocatable {
			log.Printf("Resource %s not found in node allocatable", resourceName)
			allResourcesFound = false
			continue
		}

		capacityValue := capacityQuantity.Value()
		allocatableValue := allocatableQuantity.Value()

		log.Printf("Resource %s - Capacity: %d, Allocatable: %d", resourceName, capacityValue, allocatableValue)

		// Check if allocatable matches capacity (indicating proper resource discovery)
		if capacityValue > 0 && allocatableValue != capacityValue {
			log.Printf("Resource %s has mismatched capacity (%d) and allocatable (%d)",
				resourceName, capacityValue, allocatableValue)
			allResourcesFound = false
		} else if capacityValue == 0 {
			log.Printf("Resource %s has zero capacity", resourceName)
			allResourcesFound = false
		}
	}

	if allResourcesFound {
		log.Printf("All expected DPDK resources are properly advertised")
	} else {
		log.Printf("Some expected DPDK resources are not properly advertised")
	}

	return allResourcesFound
}

// verifyDevicePluginRunning verifies that the SR-IOV device plugin is running
func (m *SRIOVConfigManager) verifyDevicePluginRunning() bool {
	if m.k8sClient == nil {
		log.Printf("Warning: Kubernetes client not available for device plugin verification")
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Try multiple common SR-IOV device plugin label selectors
	labelSelectors := []string{
		"app=sriov-device-plugin",  // Most common
		"app=sriovdp",              // Alternative naming
		"name=sriov-device-plugin", // Another common pattern
	}

	for _, labelSelector := range labelSelectors {
		// List pods in kube-system namespace with the label selector
		pods, err := m.k8sClient.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			log.Printf("Warning: Failed to list pods with selector %s: %v", labelSelector, err)
			continue
		}

		if len(pods.Items) == 0 {
			continue
		}

		// Check if any pods are running
		runningPods := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				// Check if all containers are ready
				allReady := true
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
						allReady = false
						break
					}
				}
				if allReady {
					runningPods++
				}
			}
		}

		log.Printf("Found %d running SR-IOV device plugin pods with selector %s", runningPods, labelSelector)
		if runningPods > 0 {
			return true
		}
	}

	log.Printf("No running SR-IOV device plugin pods found")
	return false
}

// findSRIOVResourcesForNodeENI finds SR-IOV resources associated with a specific NodeENI
func findSRIOVResourcesForNodeENI(nodeENIName string, cfg *config.ENIManagerConfig) ([]string, error) {
	if cfg.SRIOVDPConfigPath == "" {
		return nil, fmt.Errorf("SR-IOV config path not configured")
	}

	// Check if config file exists
	if _, err := os.Stat(cfg.SRIOVDPConfigPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("SR-IOV config file does not exist")
	}

	// Load the current SR-IOV configuration
	config, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load SR-IOV config: %v", err)
	}

	var foundResources []string

	// Search through all resources to find ones that might be associated with this NodeENI
	// Since we don't have a direct mapping for non-DPDK resources, we'll look for resource names
	// that contain the NodeENI name or check if any resources exist (indicating potential cleanup needed)
	for _, resource := range config.ResourceList {
		// For now, we'll be conservative and assume any existing resources might need cleanup
		// when a NodeENI is deleted. This ensures we don't miss any resources.
		if len(resource.Selectors) > 0 && len(resource.Selectors[0].Devices) > 0 {
			foundResources = append(foundResources, resource.ResourceName)
		}
	}

	return foundResources, nil
}

// cleanupSRIOVConfigForNodeENI removes SR-IOV configuration for a deleted NodeENI
func cleanupSRIOVConfigForNodeENI(nodeENIName string, cfg *config.ENIManagerConfig) error {
	log.Printf("Cleaning up SR-IOV configuration for deleted NodeENI: %s", nodeENIName)

	// Find all DPDK bound interfaces for this NodeENI
	var dpdkInterfacesToCleanup []string
	for pciAddr, boundInterface := range cfg.DPDKBoundInterfaces {
		if boundInterface.NodeENIName == nodeENIName {
			dpdkInterfacesToCleanup = append(dpdkInterfacesToCleanup, pciAddr)
		}
	}

	// Also check for non-DPDK SR-IOV resources in the configuration file
	var nonDpdkResourcesToCleanup []string
	if cfg.SRIOVDPConfigPath != "" {
		if resources, err := findSRIOVResourcesForNodeENI(nodeENIName, cfg); err == nil {
			nonDpdkResourcesToCleanup = resources
		}
	}

	totalResourcesToCleanup := len(dpdkInterfacesToCleanup) + len(nonDpdkResourcesToCleanup)

	if totalResourcesToCleanup == 0 && cfg.SRIOVDPConfigPath == "" {
		log.Printf("No DPDK interfaces, no SR-IOV resources, and no SR-IOV config path configured for NodeENI %s", nodeENIName)
		return nil
	}

	if len(dpdkInterfacesToCleanup) > 0 {
		log.Printf("Found %d DPDK interfaces to cleanup for NodeENI %s", len(dpdkInterfacesToCleanup), nodeENIName)
	}
	if len(nonDpdkResourcesToCleanup) > 0 {
		log.Printf("Found %d non-DPDK SR-IOV resources to cleanup for NodeENI %s", len(nonDpdkResourcesToCleanup), nodeENIName)
	}
	if totalResourcesToCleanup == 0 {
		log.Printf("No specific resources found, but will force SR-IOV device plugin restart for deleted NodeENI %s to ensure cleanup", nodeENIName)
	}

	manager := NewSRIOVConfigManager(cfg.SRIOVDPConfigPath)

	// Create backup before making changes
	if err := manager.createBackup(); err != nil {
		log.Printf("Warning: Failed to create backup before cleanup: %v", err)
	}

	// Track if any configuration was actually modified
	configModified := false

	// Remove each DPDK interface from SR-IOV config
	for _, pciAddr := range dpdkInterfacesToCleanup {
		boundInterface := cfg.DPDKBoundInterfaces[pciAddr]
		if err := removeSRIOVDevicePluginConfig(boundInterface.IfaceName, pciAddr, cfg); err != nil {
			log.Printf("Warning: Failed to remove SR-IOV config for PCI %s: %v", pciAddr, err)
		} else {
			log.Printf("Removed SR-IOV config for PCI %s (NodeENI: %s)", pciAddr, nodeENIName)
			configModified = true
		}

		// Remove from our tracking map
		delete(cfg.DPDKBoundInterfaces, pciAddr)
	}

	// Always restart the SR-IOV device plugin when a NodeENI is deleted if SR-IOV is configured
	// This ensures any SR-IOV resources (DPDK or non-DPDK) are properly cleaned up from node capacity
	shouldRestart := configModified || len(dpdkInterfacesToCleanup) > 0 || len(nonDpdkResourcesToCleanup) > 0 || (totalResourcesToCleanup == 0 && cfg.SRIOVDPConfigPath != "")

	if shouldRestart {
		if configModified {
			log.Printf("SR-IOV configuration was modified for NodeENI %s, restarting device plugin", nodeENIName)
		} else if len(dpdkInterfacesToCleanup) > 0 || len(nonDpdkResourcesToCleanup) > 0 {
			log.Printf("Found SR-IOV resources for deleted NodeENI %s, restarting device plugin to update node capacity", nodeENIName)
		} else {
			log.Printf("Forcing SR-IOV device plugin restart for deleted NodeENI %s to ensure any stale resources are cleaned up", nodeENIName)
		}

		// Set up Kubernetes client for the manager if not already set
		if manager.k8sClient == nil {
			k8sClient, err := createK8sClientset()
			if err != nil {
				log.Printf("Warning: Failed to setup Kubernetes client for device plugin restart: %v", err)
				// Try basic restart without verification
				if basicErr := manager.restartDevicePlugin(); basicErr != nil {
					log.Printf("Warning: Basic device plugin restart also failed: %v", basicErr)
				}
				return err
			}
			manager.k8sClient = k8sClient
		}

		// Use enhanced restart with verification
		if err := manager.restartDevicePluginWithVerification(); err != nil {
			log.Printf("Warning: Failed to restart SR-IOV device plugin with verification: %v", err)
			// Fall back to basic restart
			if basicErr := manager.restartDevicePlugin(); basicErr != nil {
				log.Printf("Warning: Basic device plugin restart also failed: %v", basicErr)
				return basicErr
			}
		} else {
			log.Printf("Successfully restarted SR-IOV device plugin after NodeENI %s cleanup", nodeENIName)
		}
	} else {
		log.Printf("No SR-IOV device plugin restart needed for NodeENI %s", nodeENIName)
	}

	return nil
}

// validateSRIOVResourceName validates that a resource name follows best practices
func validateSRIOVResourceName(resourceName string) error {
	if resourceName == "" {
		return fmt.Errorf("resource name cannot be empty")
	}

	// Check for valid format (domain/resource)
	parts := strings.Split(resourceName, "/")
	if len(parts) != 2 {
		return fmt.Errorf("resource name must be in format 'domain/resource', got: %s", resourceName)
	}

	domain := parts[0]
	resource := parts[1]

	// Validate domain
	if domain == "" {
		return fmt.Errorf("domain part cannot be empty")
	}

	// Validate resource
	if resource == "" {
		return fmt.Errorf("resource part cannot be empty")
	}

	// Check for reserved prefixes
	reservedPrefixes := []string{"kubernetes.io", "k8s.io"}
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(domain, prefix) {
			return fmt.Errorf("resource name cannot use reserved domain prefix: %s", prefix)
		}
	}

	log.Printf("Resource name validation passed: %s", resourceName)
	return nil
}

// bringUpInterface brings up a network interface
func bringUpInterface(link vnetlink.Link, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	log.Printf("Bringing up interface: %s", ifaceName)

	// Try using netlink first
	err := netlinkBringUpInterface(link, cfg.InterfaceUpTimeout)
	if err != nil {
		log.Printf("Netlink method failed, trying fallback method: %v", err)
		// Fall back to using ip command
		err = fallbackBringUpInterface(ifaceName, cfg.InterfaceUpTimeout)
		if err != nil {
			return err
		}
	}

	// Check if we have any NodeENI resources with MTU or DPDK settings
	// This is a more aggressive approach to ensure the settings are applied correctly
	// even if the interface is brought up before the NodeENI resources are processed
	nodeName := os.Getenv("NODE_NAME")
	if nodeName != "" {
		// Try to create a Kubernetes client
		k8sConfig, err := rest.InClusterConfig()
		if err == nil {
			// Create the clientset
			clientset, err := kubernetes.NewForConfig(k8sConfig)
			if err == nil {
				// Get all NodeENI resources
				nodeENIs, err := getNodeENIResources(context.Background(), clientset)
				if err == nil {
					// Force an immediate update of MTU values from NodeENI resources
					log.Printf("Forcing immediate MTU update for interface %s", ifaceName)
					updateMTUFromNodeENI(context.Background(), clientset, nodeName, cfg)

					// Check if DPDK is enabled and apply binding if needed
					if cfg.EnableDPDK {
						log.Printf("Checking DPDK binding for interface %s", ifaceName)
						updateDPDKBindingFromNodeENI(nodeName, cfg, nodeENIs)
					} else {
						// Even if DPDK is disabled, check if SR-IOV configuration should be updated
						updateSRIOVConfigForNonDPDK(ifaceName, nodeName, cfg, nodeENIs)
					}
				}
			}
		}
	}

	// Set MTU if configured
	if err := setInterfaceMTU(link, cfg); err != nil {
		log.Printf("Warning: Failed to set MTU for interface %s: %v", ifaceName, err)
		// Continue anyway, the interface is up
	}

	log.Printf("Successfully brought up interface %s", ifaceName)
	return nil
}

// netlinkBringUpInterface brings up an interface using netlink
func netlinkBringUpInterface(link vnetlink.Link, timeout time.Duration) error {
	ifaceName := link.Attrs().Name

	// Set the interface UP
	if err := vnetlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set interface %s up: %v", ifaceName, err)
	}

	// Wait for the interface to come up
	time.Sleep(timeout)

	// Verify the interface is now UP
	updatedLink, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get updated link info for %s: %v", ifaceName, err)
	}

	if updatedLink.Attrs().OperState != vnetlink.OperUp {
		return fmt.Errorf("interface %s is still not UP after configuration", ifaceName)
	}

	return nil
}

// fallbackBringUpInterface brings up an interface using the ip command
func fallbackBringUpInterface(ifaceName string, timeout time.Duration) error {
	// Use the ip command to bring up the interface
	cmd := exec.Command("ip", "link", "set", "dev", ifaceName, "up")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to bring up interface %s using ip command: %v, output: %s",
			ifaceName, err, string(output))
	}

	// Wait for the interface to come up
	time.Sleep(timeout)

	// Verify the interface is now UP using the ip command
	cmd = exec.Command("ip", "-o", "link", "show", "dev", ifaceName)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check interface %s status: %v", ifaceName, err)
	}

	// Check if the interface is UP
	if !bytes.Contains(output, []byte("state UP")) {
		return fmt.Errorf("interface %s is still not UP after configuration", ifaceName)
	}

	log.Printf("Successfully brought up interface %s using fallback method", ifaceName)
	return nil
}

// setInterfaceMTU sets the MTU for an interface
func setInterfaceMTU(link vnetlink.Link, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	currentMTU := link.Attrs().MTU

	// Check if we have a specific MTU for this interface
	mtu, ok := cfg.InterfaceMTUs[ifaceName]
	if !ok {
		// Use default MTU if specified
		mtu = cfg.DefaultMTU
		if cfg.DebugMode {
			log.Printf("Using default MTU %d for interface %s (current MTU: %d)", mtu, ifaceName, currentMTU)
		}
	} else if cfg.DebugMode {
		log.Printf("Using interface-specific MTU %d for interface %s (current MTU: %d)", mtu, ifaceName, currentMTU)
	}

	// If MTU is 0 or negative, don't change it (use system default)
	if mtu <= 0 {
		if cfg.DebugMode {
			log.Printf("No MTU specified for interface %s, using system default (current MTU: %d)", ifaceName, currentMTU)
		}
		return nil
	}

	// If the MTU is already set correctly, don't change it
	if currentMTU == mtu {
		if cfg.DebugMode {
			log.Printf("Interface %s already has the correct MTU: %d", ifaceName, mtu)
		}
		return nil
	}

	log.Printf("Setting MTU for interface %s from %d to %d", ifaceName, currentMTU, mtu)

	// Try the most direct method first - sysfs
	if err := sysfsSetMTU(ifaceName, mtu); err == nil {
		// Verify the MTU was set correctly
		updatedLink, err := vnetlink.LinkByName(ifaceName)
		if err == nil && updatedLink.Attrs().MTU == mtu {
			log.Printf("Successfully set MTU for interface %s to %d using sysfs method", ifaceName, mtu)
			return nil
		}
	}

	// Try using netlink next
	if err := vnetlink.LinkSetMTU(link, mtu); err == nil {
		// Verify the MTU was set correctly
		updatedLink, err := vnetlink.LinkByName(ifaceName)
		if err == nil && updatedLink.Attrs().MTU == mtu {
			log.Printf("Successfully set MTU for interface %s to %d using netlink method", ifaceName, mtu)
			return nil
		}
	}

	// Fall back to using ip command as last resort
	if err := fallbackSetMTU(ifaceName, mtu); err != nil {
		log.Printf("All methods failed to set MTU for interface %s: %v", ifaceName, err)
		return err
	}

	log.Printf("Successfully set MTU for interface %s to %d using fallback method", ifaceName, mtu)
	return nil
}

// fallbackSetMTU sets the MTU for an interface using the ip command
func fallbackSetMTU(ifaceName string, mtu int) error {
	cmd := exec.Command("ip", "link", "set", "dev", ifaceName, "mtu", fmt.Sprintf("%d", mtu))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set MTU for interface %s using ip command: %v, output: %s",
			ifaceName, err, string(output))
	}

	log.Printf("Successfully set MTU for interface %s to %d using fallback method", ifaceName, mtu)
	return nil
}

// sysfsSetMTU sets the MTU for an interface using the sysfs interface
func sysfsSetMTU(ifaceName string, mtu int) error {
	// Path to the sysfs MTU file for this interface
	mtuPath := fmt.Sprintf("/sys/class/net/%s/mtu", ifaceName)

	// Check if the file exists
	if _, err := os.Stat(mtuPath); os.IsNotExist(err) {
		return fmt.Errorf("sysfs MTU file does not exist for interface %s: %v", ifaceName, err)
	}

	// Write the MTU value to the sysfs file
	mtuStr := fmt.Sprintf("%d", mtu)
	if err := os.WriteFile(mtuPath, []byte(mtuStr), 0644); err != nil {
		return fmt.Errorf("failed to write MTU to sysfs for interface %s: %v", ifaceName, err)
	}

	// No need to log success here as the caller will log it if verification succeeds

	// Verify the MTU was set correctly - just return the error if it fails
	// The caller will try other methods if this fails
	mtuBytes, err := os.ReadFile(mtuPath)
	if err != nil {
		return fmt.Errorf("failed to verify MTU for interface %s: %v", ifaceName, err)
	}

	actualMTU := strings.TrimSpace(string(mtuBytes))
	if actualMTU != mtuStr {
		return fmt.Errorf("MTU verification failed for interface %s. Expected: %s, Actual: %s",
			ifaceName, mtuStr, actualMTU)
	}

	return nil
}

// Note: getNodeENIResources function is defined earlier in the file

// getMTUFromAttachment gets the MTU value from an attachment or NodeENI spec
func getMTUFromAttachment(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI) int {
	// Get the MTU value - first check the attachment MTU, then fall back to the spec MTU
	mtuValue := attachment.MTU
	if mtuValue <= 0 {
		mtuValue = nodeENI.Spec.MTU
	}
	return mtuValue
}

// applyMTUToInterface applies the MTU value to an interface
func applyMTUToInterface(ifaceName string, mtuValue int, cfg *config.ENIManagerConfig, source string) {
	log.Printf("Setting MTU for interface %s to %d from %s", ifaceName, mtuValue, source)
	cfg.InterfaceMTUs[ifaceName] = mtuValue

	// Apply the MTU immediately
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		log.Printf("Error getting link for interface %s: %v", ifaceName, err)
		return
	}

	if err := setInterfaceMTU(link, cfg); err != nil {
		log.Printf("Error setting MTU for interface %s: %v", ifaceName, err)
	} else {
		log.Printf("Successfully applied MTU %d to interface %s", mtuValue, ifaceName)
	}
}

// findMostCommonMTU finds the most common MTU value from a map of MTU values
func findMostCommonMTU(mtuValues map[int]int) int {
	var mostCommonMTU int
	var maxCount int
	for mtu, count := range mtuValues {
		if count > maxCount {
			maxCount = count
			mostCommonMTU = mtu
		}
	}
	return mostCommonMTU
}

// applyCommonMTUToUnmappedInterfaces applies the common MTU value to all interfaces that don't have an MTU set
func applyCommonMTUToUnmappedInterfaces(mostCommonMTU int, cfg *config.ENIManagerConfig) {
	if mostCommonMTU <= 0 {
		return
	}

	log.Printf("Found common MTU value %d from NodeENI resources", mostCommonMTU)

	// Get all network interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		log.Printf("Error listing network interfaces: %v", err)
		return
	}

	// Apply the common MTU value to all interfaces that match our pattern
	for _, link := range links {
		ifaceName := link.Attrs().Name

		// Skip loopback and primary interface
		if ifaceName == "lo" || ifaceName == cfg.PrimaryInterface {
			continue
		}

		// Skip interfaces that don't match our ENI pattern or are in the ignore list
		if !isAWSENI(ifaceName, cfg) {
			continue
		}

		// Check if this interface already has an MTU set
		if _, ok := cfg.InterfaceMTUs[ifaceName]; !ok {
			// This interface doesn't have an MTU set, apply the common value
			applyMTUToInterface(ifaceName, mostCommonMTU, cfg, "common MTU")
		}
	}
}

// processNodeENIAttachment processes a single NodeENI attachment
func processNodeENIAttachment(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI,
	nodeName string, mtuValues map[int]int, cfg *config.ENIManagerConfig) {

	if attachment.NodeID != nodeName {
		return
	}

	// This attachment is for our node
	log.Printf("Found NodeENI attachment for our node: %s, ENI: %s", nodeName, attachment.ENIID)

	// Check if this attachment is for a DPDK-enabled interface
	if nodeENI.Spec.EnableDPDK {
		log.Printf("NodeENI %s has DPDK enabled", nodeENI.Name)

		// TODO: Update the attachment status to mark it as DPDK-bound
		// This will be used during cleanup to properly unbind the interface
	}

	// Get the MTU value
	mtuValue := getMTUFromAttachment(attachment, nodeENI)
	log.Printf("MTU value from NodeENI: %d", mtuValue)

	// Skip if MTU is not set
	if mtuValue <= 0 {
		log.Printf("No MTU specified in NodeENI %s, skipping", nodeENI.Name)
		return
	}

	// Track this MTU value
	mtuValues[mtuValue]++

	// Get the interface name for this ENI
	ifaceName, err := getInterfaceNameForENI(attachment.ENIID)
	if err != nil {
		log.Printf("Error getting interface name for ENI %s: %v", attachment.ENIID, err)
		return
	}

	// Set the MTU for this interface
	if ifaceName != "" {
		applyMTUToInterface(ifaceName, mtuValue, cfg, fmt.Sprintf("NodeENI %s", nodeENI.Name))
	}
}

// updateMTUFromNodeENI updates the MTU values from NodeENI resources
func updateMTUFromNodeENI(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, cfg *config.ENIManagerConfig) {
	log.Printf("Updating MTU values from NodeENI resources for node %s", nodeName)

	// Get all NodeENI resources
	nodeENIs, err := getNodeENIResources(ctx, clientset)
	if err != nil {
		log.Printf("Error getting NodeENI resources: %v", err)
		return
	}

	log.Printf("Found %d NodeENI resources", len(nodeENIs))

	// Track the MTU values from NodeENI resources
	mtuValues := make(map[int]int) // map[mtuValue]count

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIs {
		log.Printf("Processing NodeENI: %s", nodeENI.Name)

		// Check if this NodeENI applies to our node
		if nodeENI.Status.Attachments == nil {
			log.Printf("NodeENI %s has no attachments, skipping", nodeENI.Name)
			continue
		}

		// Check each attachment
		for _, attachment := range nodeENI.Status.Attachments {
			processNodeENIAttachment(attachment, nodeENI, nodeName, mtuValues, cfg)
		}
	}

	// Find the most common MTU value and apply it to unmapped interfaces
	mostCommonMTU := findMostCommonMTU(mtuValues)
	applyCommonMTUToUnmappedInterfaces(mostCommonMTU, cfg)

	// Log the current MTU configuration
	log.Printf("Current MTU configuration:")
	log.Printf("Default MTU: %d", cfg.DefaultMTU)
	for iface, mtu := range cfg.InterfaceMTUs {
		log.Printf("Interface %s: MTU %d", iface, mtu)
	}
}

// createInterfaceMapping creates a new interface mapping for an ENI ID and interface name
func createInterfaceMapping(eniID, ifaceName string) mapping.InterfaceMapping {
	newMapping := mapping.InterfaceMapping{
		ENIID:     eniID,
		IfaceName: ifaceName,
	}

	// Try to get the PCI address for this interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err == nil {
		newMapping.PCIAddress = pciAddress
	}

	return newMapping
}

// saveInterfaceMapping saves an interface mapping to the persistent store
func saveInterfaceMapping(mapping mapping.InterfaceMapping) {
	if globalConfig == nil || globalConfig.InterfaceMappingStore == nil {
		return
	}

	if err := globalConfig.InterfaceMappingStore.AddMapping(mapping); err != nil {
		log.Printf("Warning: Failed to add interface mapping to persistent store: %v", err)
	} else {
		log.Printf("Added interface mapping to persistent store: ENI %s -> Interface %s -> PCI %s",
			mapping.ENIID, mapping.IfaceName, mapping.PCIAddress)
	}
}

// checkPersistentStore checks if an ENI ID is mapped in the persistent store
func checkPersistentStore(eniID string) (string, bool) {
	if globalConfig == nil || globalConfig.InterfaceMappingStore == nil {
		return "", false
	}

	mapping, exists := globalConfig.InterfaceMappingStore.GetMappingByENIID(eniID)
	if exists && mapping.IfaceName != "" {
		log.Printf("Found interface %s for ENI %s in persistent mapping store", mapping.IfaceName, eniID)
		return mapping.IfaceName, true
	}

	return "", false
}

// getInterfaceNameForENI gets the interface name for an ENI ID
func getInterfaceNameForENI(eniID string) (string, error) {
	// Extract the last part of the ENI ID to use for matching
	// This is just for logging purposes
	_ = extractENIIDSuffix(eniID)

	// Step 1: Check if we have a mapping in the persistent store
	if ifaceName, found := checkPersistentStore(eniID); found {
		return ifaceName, nil
	}

	// Step 2: Check if we've already mapped this ENI to an interface in memory
	if ifaceName := checkExistingMapping(eniID); ifaceName != "" {
		// If we found a mapping in memory, add it to the persistent store
		mapping := createInterfaceMapping(eniID, ifaceName)
		saveInterfaceMapping(mapping)
		return ifaceName, nil
	}

	// Step 3: Try to get the interface name using MAC address
	ifaceName, err := getInterfaceNameByMACForENI(eniID)
	if err == nil && ifaceName != "" {
		// If we found a mapping using MAC address, add it to the persistent store
		mapping := createInterfaceMapping(eniID, ifaceName)
		saveInterfaceMapping(mapping)
		return ifaceName, nil
	}

	// Step 4: Fall back to pattern matching
	ifaceName, err = findInterfaceByPattern(eniID)
	if err == nil && ifaceName != "" {
		// If we found a mapping using pattern matching, add it to the persistent store
		mapping := createInterfaceMapping(eniID, ifaceName)
		saveInterfaceMapping(mapping)
	}

	return ifaceName, err
}

// extractENIIDSuffix extracts the suffix from an ENI ID
func extractENIIDSuffix(eniID string) string {
	if parts := strings.Split(eniID, "-"); len(parts) > 1 {
		suffix := parts[len(parts)-1]
		log.Printf("Using ENI ID suffix for matching: %s", suffix)
		return suffix
	}
	return eniID
}

// checkExistingMapping checks if we've already mapped this ENI to an interface
func checkExistingMapping(eniID string) string {
	for ifaceName, mappedENIID := range usedInterfaces {
		if mappedENIID == eniID {
			// Found a mapping, but let's verify if the interface still exists
			if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); os.IsNotExist(err) {
				log.Printf("Previously mapped interface %s for ENI %s no longer exists, removing mapping",
					ifaceName, eniID)
				delete(usedInterfaces, ifaceName)
				return ""
			}

			log.Printf("Using previously mapped interface %s for ENI %s", ifaceName, eniID)
			return ifaceName
		}
	}
	return ""
}

// getInterfaceNameByMACForENI tries to get the interface name using MAC address
func getInterfaceNameByMACForENI(eniID string) (string, error) {
	// Try to get the MAC address for this ENI from AWS metadata
	macAddress, err := getMacAddressForENI(eniID)
	if err != nil {
		log.Printf("Warning: Failed to get MAC address for ENI %s: %v", eniID, err)
		return "", err
	}

	if macAddress == "" {
		return "", fmt.Errorf("empty MAC address returned for ENI %s", eniID)
	}

	// If we have a MAC address, find the interface with this MAC
	ifaceName, err := getInterfaceNameByMAC(macAddress)
	if err != nil {
		log.Printf("Warning: Failed to find interface with MAC %s: %v", macAddress, err)
		return "", err
	}

	if ifaceName == "" {
		return "", fmt.Errorf("no interface found with MAC %s", macAddress)
	}

	log.Printf("Successfully mapped ENI %s to interface %s using MAC %s", eniID, ifaceName, macAddress)
	usedInterfaces[ifaceName] = eniID
	return ifaceName, nil
}

// findInterfaceByPattern finds an interface for an ENI using pattern matching
func findInterfaceByPattern(eniID string) (string, error) {
	log.Printf("Falling back to interface pattern matching for ENI %s", eniID)

	// Get all network interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		return "", fmt.Errorf("failed to list network interfaces: %v", err)
	}

	// Get the primary interface name to exclude it
	primaryIface, err := detectPrimaryInterface()
	if err != nil {
		log.Printf("Warning: Failed to detect primary interface: %v", err)
		// Default to eth0 if we can't detect the primary interface
		primaryIface = "eth0"
	}

	// Try to find interfaces that match the eth pattern
	ifaceName := findEthInterfaces(links, primaryIface, eniID)
	if ifaceName != "" {
		return ifaceName, nil
	}

	// If we couldn't find a suitable eth interface, try any interface
	log.Printf("No eth interfaces found for ENI %s, trying any interface", eniID)
	return findAnyInterface(links, primaryIface, eniID)
}

// findEthInterfaces finds interfaces that match the eth pattern
func findEthInterfaces(links []vnetlink.Link, primaryIface, eniID string) string {
	// For AWS, secondary ENIs are typically named eth1, eth2, etc.
	deviceIndexPattern := regexp.MustCompile(`^eth[1-9][0-9]*$`)
	var ethInterfaces []string

	// First, try to find the device index from the ENI attachment
	deviceIndex := findDeviceIndexForENI(eniID)
	if deviceIndex <= 0 {
		log.Printf("Error: Could not determine device index for ENI %s, cannot proceed with interface mapping", eniID)
		return ""
	}

	log.Printf("Found device index %d for ENI %s", deviceIndex, eniID)

	// Map to store interfaces by their device index
	interfacesByIndex := make(map[int]string)

	for _, link := range links {
		ifaceName := link.Attrs().Name
		if ifaceName != "lo" && ifaceName != primaryIface && deviceIndexPattern.MatchString(ifaceName) {
			// Check if this interface is already mapped to an ENI
			if _, ok := usedInterfaces[ifaceName]; !ok {
				ethInterfaces = append(ethInterfaces, ifaceName)
				log.Printf("Found potential eth interface: %s", ifaceName)

				// Extract the device index from the interface name
				var index int
				if _, err := fmt.Sscanf(ifaceName, "eth%d", &index); err == nil {
					interfacesByIndex[index] = ifaceName
					log.Printf("Mapped interface %s to device index %d", ifaceName, index)
				}
			}
		}
	}

	// If we found eth interfaces, look for the one matching our device index
	if len(ethInterfaces) > 0 {
		// Log all found eth interfaces
		for i, name := range ethInterfaces {
			log.Printf("Eth interface %d: %s", i, name)
		}

		// First, check if we have an interface with the exact device index
		if ifaceName, ok := interfacesByIndex[deviceIndex]; ok {
			log.Printf("Selected eth interface %s for ENI %s based on exact device index %d",
				ifaceName, eniID, deviceIndex)
			usedInterfaces[ifaceName] = eniID
			return ifaceName
		}

		// If not, look for an interface with the matching name pattern
		targetIfaceName := fmt.Sprintf("eth%d", deviceIndex)
		for _, ifaceName := range ethInterfaces {
			if ifaceName == targetIfaceName {
				log.Printf("Selected eth interface %s for ENI %s based on device index %d",
					ifaceName, eniID, deviceIndex)
				usedInterfaces[ifaceName] = eniID
				return ifaceName
			}
		}

		// If we couldn't find a match by device index, use any available interface
		// This is necessary because AWS doesn't always create interfaces with names that match the device index
		log.Printf("WARNING: Could not find interface with name %s for ENI %s with device index %d. Available interfaces: %v",
			targetIfaceName, eniID, deviceIndex, ethInterfaces)
		log.Printf("Using first available interface to ensure DPDK functionality")

		// Sort the interfaces by name to ensure consistent ordering
		sort.Strings(ethInterfaces)
		ifaceName := ethInterfaces[0]
		log.Printf("Selected eth interface %s for ENI %s (fallback for DPDK)", ifaceName, eniID)
		usedInterfaces[ifaceName] = eniID
		return ifaceName
	}

	log.Printf("ERROR: No suitable eth interfaces found for ENI %s with device index %d", eniID, deviceIndex)
	return ""
}

// findDeviceIndexForENI tries to find the device index for an ENI ID by querying the NodeENI resources
func findDeviceIndexForENI(eniID string) int {
	// Create a Kubernetes client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Failed to create in-cluster config: %v", err)
		return 0
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		log.Printf("Failed to create Kubernetes client: %v", err)
		return 0
	}

	// Get all NodeENI resources
	nodeENIList, err := clientset.CoreV1().RESTClient().
		Get().
		AbsPath("/apis/networking.k8s.aws/v1alpha1/nodeenis").
		Do(context.Background()).
		Raw()
	if err != nil {
		log.Printf("Failed to get NodeENI resources: %v", err)
		return 0
	}

	// Parse the NodeENI list
	var nodeENIs struct {
		Items []networkingv1alpha1.NodeENI `json:"items"`
	}
	if err := json.Unmarshal(nodeENIList, &nodeENIs); err != nil {
		log.Printf("Failed to parse NodeENI list: %v", err)
		return 0
	}

	// Look for the ENI ID in the NodeENI attachments
	for _, nodeENI := range nodeENIs.Items {
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.ENIID == eniID {
				log.Printf("Found device index %d for ENI %s in NodeENI %s",
					attachment.DeviceIndex, eniID, nodeENI.Name)
				return attachment.DeviceIndex
			}
		}
	}

	log.Printf("Could not find device index for ENI %s in any NodeENI resource", eniID)
	return 0
}

// findAnyInterface finds any interface that matches common AWS ENI patterns
func findAnyInterface(links []vnetlink.Link, primaryIface, eniID string) (string, error) {
	log.Printf("Falling back to any interface for ENI %s for DPDK functionality", eniID)

	for _, link := range links {
		ifaceName := link.Attrs().Name
		if ifaceName != "lo" && ifaceName != primaryIface {
			// Check if this interface is already mapped to an ENI
			if _, ok := usedInterfaces[ifaceName]; ok {
				continue
			}

			// Check if it matches common AWS ENI patterns
			if strings.HasPrefix(ifaceName, "eth") ||
				strings.HasPrefix(ifaceName, "ens") ||
				strings.HasPrefix(ifaceName, "eni") ||
				strings.HasPrefix(ifaceName, "en") {
				log.Printf("Found potential interface %s for ENI %s (using pattern matching)", ifaceName, eniID)
				usedInterfaces[ifaceName] = eniID
				return ifaceName, nil
			}
		}
	}

	return "", fmt.Errorf("no matching interface found for ENI %s", eniID)
}

// getMacAddressForENI attempts to get the MAC address for an ENI using the AWS metadata service
func getMacAddressForENI(eniID string) (string, error) {
	// This is a simplified implementation that uses the EC2 instance metadata service
	// to get information about network interfaces

	// Try to read the metadata directly from the filesystem
	// This is more reliable than using curl and works in containers without curl
	macAddressesBytes, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return "", fmt.Errorf("failed to read ARP table: %v", err)
	}

	// Log the ARP table for debugging
	log.Printf("ARP table: %s", string(macAddressesBytes))

	// For now, we'll just return an error to fall back to the pattern matching
	return "", fmt.Errorf("metadata service approach not implemented, falling back to pattern matching")
}

// getInterfaceNameByMAC finds the interface name for a given MAC address
func getInterfaceNameByMAC(macAddress string) (string, error) {
	// Normalize the MAC address format (lowercase, no colons)
	normalizedMAC := strings.ToLower(strings.ReplaceAll(macAddress, ":", ""))

	// List all interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		return "", fmt.Errorf("failed to list network interfaces: %v", err)
	}

	// Check each interface's MAC address
	for _, link := range links {
		ifaceMAC := link.Attrs().HardwareAddr.String()
		// Normalize the interface MAC address format
		normalizedIfaceMAC := strings.ToLower(strings.ReplaceAll(ifaceMAC, ":", ""))

		if normalizedIfaceMAC == normalizedMAC {
			return link.Attrs().Name, nil
		}
	}

	return "", fmt.Errorf("no interface found with MAC address %s", macAddress)
}

// updateAllInterfacesMTU updates the MTU for all ENI interfaces
func updateAllInterfacesMTU(cfg *config.ENIManagerConfig) error {
	// Get all network interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %v", err)
	}

	// Get the default MTU from the NodeENI resources
	defaultMTUFromNodeENI := getDefaultMTUFromNodeENI(cfg)
	if defaultMTUFromNodeENI > 0 {
		log.Printf("Using default MTU %d from NodeENI resources for unmapped interfaces", defaultMTUFromNodeENI)
	}

	for _, link := range links {
		ifaceName := link.Attrs().Name

		// Skip loopback and primary interface
		if ifaceName == "lo" || ifaceName == cfg.PrimaryInterface {
			continue
		}

		// Skip interfaces that don't match our ENI pattern or are in the ignore list
		if !isAWSENI(ifaceName, cfg) {
			if cfg.DebugMode {
				log.Printf("Skipping non-ENI interface: %s", ifaceName)
			}
			continue
		}

		// Check if this interface is mapped to an ENI
		_, isMapped := usedInterfaces[ifaceName]

		// If it's not mapped and we have a default MTU from NodeENI resources, use that
		if !isMapped && defaultMTUFromNodeENI > 0 {
			// Apply to all interfaces that match our ENI pattern
			// This will include eth*, ens*, eni*, en* interfaces based on the default pattern
			log.Printf("Interface %s is not mapped to any ENI, using default MTU %d from NodeENI resources",
				ifaceName, defaultMTUFromNodeENI)
			cfg.InterfaceMTUs[ifaceName] = defaultMTUFromNodeENI
		}

		// Set MTU if configured
		if err := setInterfaceMTU(link, cfg); err != nil {
			log.Printf("Warning: Failed to set MTU for interface %s: %v", ifaceName, err)
			// Continue with other interfaces
		}
	}

	return nil
}

// getDefaultMTUFromNodeENI gets the default MTU from NodeENI resources
func getDefaultMTUFromNodeENI(cfg *config.ENIManagerConfig) int {
	// If we have any interface MTUs configured, use the most common value
	if len(cfg.InterfaceMTUs) > 0 {
		// Count the occurrences of each MTU value
		mtuCounts := make(map[int]int)
		for _, mtu := range cfg.InterfaceMTUs {
			if mtu > 0 {
				mtuCounts[mtu]++
			}
		}

		// Find the most common MTU value
		var mostCommonMTU int
		var maxCount int
		for mtu, count := range mtuCounts {
			if count > maxCount {
				maxCount = count
				mostCommonMTU = mtu
			}
		}

		return mostCommonMTU
	}

	// If no interface MTUs are configured, return 0 (use system default)
	return 0
}

// listPCIDevices returns a list of all PCI device addresses
func listPCIDevices() ([]string, error) {
	// List all PCI devices
	pciPaths, err := filepath.Glob("/sys/bus/pci/devices/*")
	if err != nil {
		return nil, fmt.Errorf("failed to list PCI devices: %v", err)
	}

	// Extract the PCI addresses from the paths
	var pciAddresses []string
	for _, path := range pciPaths {
		pciAddresses = append(pciAddresses, filepath.Base(path))
	}

	return pciAddresses, nil
}

// contains checks if a string slice contains a specific string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// ModernSRIOVSelector represents a selector in the modern SR-IOV device plugin format
type ModernSRIOVSelector struct {
	Drivers      []string `json:"drivers,omitempty"`
	PCIAddresses []string `json:"pciAddresses,omitempty"`
	Vendors      []string `json:"vendors,omitempty"`
	Devices      []string `json:"devices,omitempty"`
}

// ModernSRIOVResource represents a resource in the modern SR-IOV device plugin format
type ModernSRIOVResource struct {
	ResourceName   string                `json:"resourceName"`
	ResourcePrefix string                `json:"resourcePrefix,omitempty"`
	Selectors      []ModernSRIOVSelector `json:"selectors"`
}

// ModernSRIOVDPConfig represents the modern SR-IOV device plugin configuration
type ModernSRIOVDPConfig struct {
	ResourceList []ModernSRIOVResource `json:"resourceList"`
}

// Track processed SR-IOV configurations to avoid unnecessary restarts
var processedSRIOVConfigs = make(map[string]string)
var sriovConfigMutex sync.Mutex

// updateModernSRIOVDevicePluginConfig updates the SR-IOV device plugin config using the modern format
func updateModernSRIOVDevicePluginConfig(pciAddress, driver, resourceName string, cfg *config.ENIManagerConfig) error {
	if cfg.SRIOVDPConfigPath == "" {
		return fmt.Errorf("SR-IOV device plugin config path not configured")
	}

	// Create a unique key for this configuration
	configKey := fmt.Sprintf("%s:%s:%s", pciAddress, driver, resourceName)

	// Check if we've already processed this exact configuration
	sriovConfigMutex.Lock()
	lastProcessed, exists := processedSRIOVConfigs[configKey]
	sriovConfigMutex.Unlock()

	if exists && lastProcessed == configKey {
		log.Printf("SR-IOV configuration already processed for PCI %s with resource %s - skipping", pciAddress, resourceName)
		return nil
	}

	log.Printf("Updating modern SR-IOV device plugin config for PCI %s with resource %s", pciAddress, resourceName)

	resourcePrefix, resourceShortName, err := parseResourceName(resourceName)
	if err != nil {
		return err
	}

	manager := NewSRIOVConfigManager(cfg.SRIOVDPConfigPath)

	// Create backup before making changes
	if err := manager.createBackup(); err != nil {
		log.Printf("Warning: Failed to create backup of SR-IOV config: %v", err)
	}

	// Load current configuration
	currentConfig, err := loadOrCreateSRIOVConfig(cfg.SRIOVDPConfigPath)
	if err != nil {
		return err
	}

	// Create a copy for comparison
	originalConfig := deepCopySRIOVConfig(currentConfig)

	// Update the configuration
	updateSRIOVResourceConfig(&currentConfig, pciAddress, driver, resourcePrefix, resourceShortName)

	// Check if configuration actually changed
	configChanged := !sriovConfigsEqual(originalConfig, currentConfig)

	// Save the updated configuration first
	if err := saveSRIOVConfig(cfg.SRIOVDPConfigPath, currentConfig); err != nil {
		return err
	}

	if !configChanged {
		log.Printf("SR-IOV configuration unchanged for resource %s/%s (PCI: %s) - skipping device plugin restart",
			resourcePrefix, resourceShortName, pciAddress)

		// Still update the tracking to avoid repeated processing
		sriovConfigMutex.Lock()
		processedSRIOVConfigs[configKey] = configKey
		sriovConfigMutex.Unlock()

		return nil
	}

	log.Printf("SR-IOV configuration changed for resource %s/%s (PCI: %s) - restarting device plugin",
		resourcePrefix, resourceShortName, pciAddress)

	log.Printf("Successfully updated modern SR-IOV config for resource %s/%s (PCI: %s) - configuration changed",
		resourcePrefix, resourceShortName, pciAddress)

	// Restart the SR-IOV device plugin only when configuration actually changed
	if err := manager.restartDevicePlugin(); err != nil {
		log.Printf("Warning: Failed to restart SR-IOV device plugin: %v", err)
		// Don't fail the operation for restart failures
	} else {
		log.Printf("Successfully restarted SR-IOV device plugin due to configuration change")
	}

	// Update the tracking after successful processing
	sriovConfigMutex.Lock()
	processedSRIOVConfigs[configKey] = configKey
	sriovConfigMutex.Unlock()

	return nil
}

// parseResourceName parses a resource name into prefix and short name
func parseResourceName(resourceName string) (string, string, error) {
	parts := strings.Split(resourceName, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid resource name format: %s (expected domain/resource)", resourceName)
	}
	return parts[0], parts[1], nil
}

// loadOrCreateSRIOVConfig loads existing SR-IOV config or creates a new one
func loadOrCreateSRIOVConfig(configPath string) (ModernSRIOVDPConfig, error) {
	var config ModernSRIOVDPConfig

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Create new configuration
		config = ModernSRIOVDPConfig{
			ResourceList: []ModernSRIOVResource{},
		}
		return config, nil
	}

	// Read existing configuration
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return config, fmt.Errorf("failed to read SR-IOV config: %v", err)
	}

	if err := json.Unmarshal(configData, &config); err != nil {
		return config, fmt.Errorf("failed to parse SR-IOV config: %v", err)
	}

	return config, nil
}

// updateSRIOVResourceConfig updates the SR-IOV resource configuration
func updateSRIOVResourceConfig(config *ModernSRIOVDPConfig, pciAddress, driver, resourcePrefix, resourceShortName string) {
	// Find existing resource
	for i := range config.ResourceList {
		resource := &config.ResourceList[i]
		if resource.ResourceName == resourceShortName && resource.ResourcePrefix == resourcePrefix {
			updateExistingResource(resource, pciAddress, driver)
			return
		}
	}

	// Create new resource if not found
	createNewResource(config, pciAddress, driver, resourcePrefix, resourceShortName)
}

// updateExistingResource updates an existing SR-IOV resource
func updateExistingResource(resource *ModernSRIOVResource, pciAddress, driver string) {
	// Check if PCI address already exists in selectors
	for j := range resource.Selectors {
		selector := &resource.Selectors[j]
		for _, addr := range selector.PCIAddresses {
			if addr == pciAddress {
				// Update driver if different
				if len(selector.Drivers) == 0 || selector.Drivers[0] != driver {
					selector.Drivers = []string{driver}
				}
				return
			}
		}
	}

	// If PCI address not found, add it
	resource.Selectors = append(resource.Selectors, ModernSRIOVSelector{
		Drivers:      []string{driver},
		PCIAddresses: []string{pciAddress},
	})
}

// createNewResource creates a new SR-IOV resource
func createNewResource(config *ModernSRIOVDPConfig, pciAddress, driver, resourcePrefix, resourceShortName string) {
	newResource := ModernSRIOVResource{
		ResourceName:   resourceShortName,
		ResourcePrefix: resourcePrefix,
		Selectors: []ModernSRIOVSelector{
			{
				Drivers:      []string{driver},
				PCIAddresses: []string{pciAddress},
			},
		},
	}
	config.ResourceList = append(config.ResourceList, newResource)
}

// saveSRIOVConfig saves the SR-IOV configuration to file
func saveSRIOVConfig(configPath string, config ModernSRIOVDPConfig) error {
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SR-IOV config: %v", err)
	}

	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return fmt.Errorf("failed to write SR-IOV config: %v", err)
	}

	return nil
}

// deepCopySRIOVConfig creates a deep copy of the SR-IOV configuration
func deepCopySRIOVConfig(config ModernSRIOVDPConfig) ModernSRIOVDPConfig {
	configCopy := ModernSRIOVDPConfig{
		ResourceList: make([]ModernSRIOVResource, len(config.ResourceList)),
	}

	for i, resource := range config.ResourceList {
		configCopy.ResourceList[i] = ModernSRIOVResource{
			ResourceName:   resource.ResourceName,
			ResourcePrefix: resource.ResourcePrefix,
			Selectors:      make([]ModernSRIOVSelector, len(resource.Selectors)),
		}

		for j, selector := range resource.Selectors {
			configCopy.ResourceList[i].Selectors[j] = ModernSRIOVSelector{
				Drivers:      make([]string, len(selector.Drivers)),
				PCIAddresses: make([]string, len(selector.PCIAddresses)),
			}
			copy(configCopy.ResourceList[i].Selectors[j].Drivers, selector.Drivers)
			copy(configCopy.ResourceList[i].Selectors[j].PCIAddresses, selector.PCIAddresses)
		}
	}

	return configCopy
}

// sriovConfigsEqual compares two SR-IOV configurations for equality
func sriovConfigsEqual(config1, config2 ModernSRIOVDPConfig) bool {
	if len(config1.ResourceList) != len(config2.ResourceList) {
		return false
	}

	// Create maps for easier comparison
	resources1 := make(map[string]ModernSRIOVResource)
	resources2 := make(map[string]ModernSRIOVResource)

	for _, resource := range config1.ResourceList {
		key := resource.ResourcePrefix + "/" + resource.ResourceName
		resources1[key] = resource
	}

	for _, resource := range config2.ResourceList {
		key := resource.ResourcePrefix + "/" + resource.ResourceName
		resources2[key] = resource
	}

	// Check if all resources from config1 exist in config2 and are equal
	for key, resource1 := range resources1 {
		resource2, exists := resources2[key]
		if !exists || !sriovResourcesEqual(resource1, resource2) {
			return false
		}
	}

	// Check if all resources from config2 exist in config1 (should be covered above, but for completeness)
	for key := range resources2 {
		if _, exists := resources1[key]; !exists {
			return false
		}
	}

	return true
}

// sriovResourcesEqual compares two SR-IOV resources for equality
func sriovResourcesEqual(resource1, resource2 ModernSRIOVResource) bool {
	if resource1.ResourceName != resource2.ResourceName ||
		resource1.ResourcePrefix != resource2.ResourcePrefix ||
		len(resource1.Selectors) != len(resource2.Selectors) {
		return false
	}

	// Create maps for easier comparison of selectors
	selectors1 := make(map[string]ModernSRIOVSelector)
	selectors2 := make(map[string]ModernSRIOVSelector)

	for _, selector := range resource1.Selectors {
		// Create a key based on sorted PCI addresses for consistent comparison
		key := strings.Join(sortedStringSlice(selector.PCIAddresses), ",")
		selectors1[key] = selector
	}

	for _, selector := range resource2.Selectors {
		key := strings.Join(sortedStringSlice(selector.PCIAddresses), ",")
		selectors2[key] = selector
	}

	// Compare selectors
	for key, selector1 := range selectors1 {
		selector2, exists := selectors2[key]
		if !exists || !sriovSelectorsEqual(selector1, selector2) {
			return false
		}
	}

	for key := range selectors2 {
		if _, exists := selectors1[key]; !exists {
			return false
		}
	}

	return true
}

// sriovSelectorsEqual compares two SR-IOV selectors for equality
func sriovSelectorsEqual(selector1, selector2 ModernSRIOVSelector) bool {
	// Compare drivers (order doesn't matter)
	if !stringSlicesEqual(selector1.Drivers, selector2.Drivers) {
		return false
	}

	// Compare PCI addresses (order doesn't matter)
	if !stringSlicesEqual(selector1.PCIAddresses, selector2.PCIAddresses) {
		return false
	}

	return true
}

// stringSlicesEqual compares two string slices for equality (order doesn't matter)
func stringSlicesEqual(slice1, slice2 []string) bool {
	if len(slice1) != len(slice2) {
		return false
	}

	sorted1 := sortedStringSlice(slice1)
	sorted2 := sortedStringSlice(slice2)

	for i := range sorted1 {
		if sorted1[i] != sorted2[i] {
			return false
		}
	}

	return true
}

// sortedStringSlice returns a sorted copy of a string slice
func sortedStringSlice(slice []string) []string {
	sorted := make([]string, len(slice))
	copy(sorted, slice)
	sort.Strings(sorted)
	return sorted
}

// updateSRIOVConfigForNonDPDK updates SR-IOV configuration for non-DPDK scenarios
func updateSRIOVConfigForNonDPDK(ifaceName, nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI) {
	log.Printf("Checking SR-IOV configuration for non-DPDK interface %s", ifaceName)

	// Find NodeENI resources that apply to this node and have dpdkResourceName specified
	for _, nodeENI := range nodeENIs {
		if nodeENI.Spec.DPDKResourceName == "" {
			continue // Skip if no resource name specified
		}

		processNodeENIForInterface(ifaceName, nodeName, cfg, nodeENI)
	}
}

// processNodeENIForInterface processes a single NodeENI for a specific interface
func processNodeENIForInterface(ifaceName, nodeName string, cfg *config.ENIManagerConfig, nodeENI networkingv1alpha1.NodeENI) {
	// Check if this NodeENI applies to our node
	if nodeENI.Status.Attachments == nil {
		return
	}

	for _, attachment := range nodeENI.Status.Attachments {
		if attachment.NodeID != nodeName {
			continue
		}

		targetInterface, pciAddress := resolveInterfaceAndPCI(nodeENI, attachment)

		// Check if this is the interface we're processing
		if targetInterface == ifaceName && pciAddress != "" {
			processSRIOVConfigForAttachment(ifaceName, pciAddress, cfg, nodeENI, attachment)
		}
	}
}

// resolveInterfaceAndPCI resolves the interface name and PCI address for a NodeENI attachment
func resolveInterfaceAndPCI(nodeENI networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) (string, string) {
	var targetInterface string
	var pciAddress string

	// If PCI address is explicitly provided, use it
	if nodeENI.Spec.DPDKPCIAddress != "" {
		pciAddress = nodeENI.Spec.DPDKPCIAddress
		// Try to find the interface name for this PCI address
		if interfaceName, err := getInterfaceNameForPCIAddress(pciAddress); err == nil {
			targetInterface = interfaceName
		}
	} else {
		// Try to get the interface name for this ENI
		if interfaceName, err := getInterfaceNameForENI(attachment.ENIID); err == nil {
			targetInterface = interfaceName
			// Get PCI address for this interface
			if addr, err := getPCIAddressForInterface(interfaceName); err == nil {
				pciAddress = addr
			}
		}
	}

	return targetInterface, pciAddress
}

// processSRIOVConfigForAttachment processes SR-IOV configuration for a specific attachment
func processSRIOVConfigForAttachment(ifaceName, pciAddress string, cfg *config.ENIManagerConfig, nodeENI networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) {
	log.Printf("Found NodeENI %s with SR-IOV resource name %s for interface %s (PCI: %s)",
		nodeENI.Name, nodeENI.Spec.DPDKResourceName, ifaceName, pciAddress)

	// Validate the resource name
	if err := validateSRIOVResourceName(nodeENI.Spec.DPDKResourceName); err != nil {
		log.Printf("Error: Invalid DPDK resource name '%s' in NodeENI %s: %v",
			nodeENI.Spec.DPDKResourceName, nodeENI.Name, err)
		return
	}

	// Determine the driver to use (detect actual driver for non-DPDK)
	// For non-DPDK reconciliation, always use the actual current driver, not the specified DPDK driver
	driver := determineDriverForInterface(ifaceName, pciAddress)

	// Note: We do NOT use nodeENI.Spec.DPDKDriver here because this is the non-DPDK reconciliation path
	// The explicitly specified DPDK driver should only be used during actual DPDK binding operations
	log.Printf("Using detected driver %s for non-DPDK interface %s (ignoring specified DPDK driver)", driver, ifaceName)

	// Update SR-IOV configuration
	if err := updateModernSRIOVDevicePluginConfig(pciAddress, driver, nodeENI.Spec.DPDKResourceName, cfg); err != nil {
		log.Printf("Warning: Failed to update SR-IOV device plugin config for non-DPDK interface %s with resource name %s: %v",
			ifaceName, nodeENI.Spec.DPDKResourceName, err)
	} else {
		log.Printf("Successfully configured SR-IOV device plugin for non-DPDK resource %s (interface: %s, PCI: %s, driver: %s)",
			nodeENI.Spec.DPDKResourceName, ifaceName, pciAddress, driver)

		// Update the NodeENI status to reflect the resource name (even for non-DPDK)
		if err := updateNodeENISRIOVStatus(attachment.ENIID, nodeENI.Name, pciAddress, nodeENI.Spec.DPDKResourceName); err != nil {
			log.Printf("Warning: Failed to update NodeENI status for non-DPDK SR-IOV resource: %v", err)
		}
	}
}

// getInterfaceNameForPCIAddress gets the interface name for a PCI address
func getInterfaceNameForPCIAddress(pciAddress string) (string, error) {
	// Look for the network interface in /sys/bus/pci/devices/<pci_address>/net/
	netPath := fmt.Sprintf("/sys/bus/pci/devices/%s/net", pciAddress)

	entries, err := os.ReadDir(netPath)
	if err != nil {
		return "", fmt.Errorf("failed to read network interfaces for PCI %s: %v", pciAddress, err)
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("no network interfaces found for PCI address %s", pciAddress)
	}

	// Return the first interface found (there should typically be only one)
	interfaceName := entries[0].Name()
	log.Printf("Found interface %s for PCI address %s", interfaceName, pciAddress)
	return interfaceName, nil
}

// updateNodeENISRIOVStatus updates the NodeENI status with SR-IOV resource information
func updateNodeENISRIOVStatus(eniID, nodeENIName, pciAddress, resourceName string) error {
	// This is a simplified version - in a full implementation, you would update the Kubernetes resource
	log.Printf("Updating NodeENI %s status: ENI %s, PCI %s, Resource %s",
		nodeENIName, eniID, pciAddress, resourceName)

	// For now, just log the update. In a full implementation, you would:
	// 1. Get the NodeENI resource from the Kubernetes API
	// 2. Update the status with the SR-IOV resource information
	// 3. Update the resource in the cluster

	return nil
}

// ensureSRIOVConfigExists ensures that the SR-IOV device plugin config file exists
func ensureSRIOVConfigExists(cfg *config.ENIManagerConfig) {
	if cfg.SRIOVDPConfigPath == "" {
		return // SR-IOV not configured
	}

	// Check if the config file already exists
	if _, err := os.Stat(cfg.SRIOVDPConfigPath); err == nil {
		log.Printf("SR-IOV device plugin config file already exists: %s", cfg.SRIOVDPConfigPath)
		return
	}

	// Create the directory if it doesn't exist
	configDir := filepath.Dir(cfg.SRIOVDPConfigPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		log.Printf("Warning: Failed to create SR-IOV config directory %s: %v", configDir, err)
		return
	}

	// Create a default empty configuration
	defaultConfig := ModernSRIOVDPConfig{
		ResourceList: []ModernSRIOVResource{},
	}

	configData, err := json.MarshalIndent(defaultConfig, "", "  ")
	if err != nil {
		log.Printf("Warning: Failed to marshal default SR-IOV config: %v", err)
		return
	}

	if err := os.WriteFile(cfg.SRIOVDPConfigPath, configData, 0644); err != nil {
		log.Printf("Warning: Failed to create default SR-IOV config file %s: %v", cfg.SRIOVDPConfigPath, err)
		return
	}

	log.Printf("Created default SR-IOV device plugin config file: %s", cfg.SRIOVDPConfigPath)
}

// updateSRIOVConfigForAllInterfaces updates SR-IOV configuration for all interfaces based on NodeENI resources
func updateSRIOVConfigForAllInterfaces(nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI) {
	log.Printf("Checking SR-IOV configuration for all interfaces on node %s", nodeName)

	// Get all network interfaces
	links, err := vnetlink.LinkList()
	if err != nil {
		log.Printf("Error listing network interfaces for SR-IOV config: %v", err)
		return
	}

	// Process each interface
	for _, link := range links {
		ifaceName := link.Attrs().Name

		// Skip loopback and primary interface
		if ifaceName == "lo" || ifaceName == cfg.PrimaryInterface {
			continue
		}

		// Skip interfaces that don't match our ENI pattern or are in the ignore list
		if !isAWSENI(ifaceName, cfg) {
			continue
		}

		// Check if this interface should have SR-IOV configuration
		updateSRIOVConfigForNonDPDK(ifaceName, nodeName, cfg, nodeENIs)
	}
}

// getCurrentDriverForPCI gets the current driver for a PCI device
func getCurrentDriverForPCI(pciAddress string) (string, error) {
	// Read the driver symlink from /sys/bus/pci/devices/<pci_address>/driver
	driverPath := fmt.Sprintf("/sys/bus/pci/devices/%s/driver", pciAddress)

	// Read the symlink to get the driver name
	driverLink, err := os.Readlink(driverPath)
	if err != nil {
		return "", fmt.Errorf("failed to read driver symlink for PCI %s: %v", pciAddress, err)
	}

	// Extract the driver name from the path (e.g., "../../../bus/pci/drivers/ena" -> "ena")
	driverName := filepath.Base(driverLink)

	log.Printf("Found driver %s for PCI address %s", driverName, pciAddress)
	return driverName, nil
}

// determineDriverForInterface determines the appropriate driver for an interface
// This function implements the project requirement to use 'ena' for AWS ENA devices
func determineDriverForInterface(ifaceName, pciAddress string) string {
	// Try to detect the actual driver in use
	if actualDriver, err := getCurrentDriverForPCI(pciAddress); err == nil && actualDriver != "" {
		// For AWS ENA devices, ensure we use 'ena' driver as specified in requirements
		if actualDriver == "ena" {
			log.Printf("Detected AWS ENA device for interface %s, using 'ena' driver", ifaceName)
			return "ena"
		}
		log.Printf("Detected driver %s for interface %s (PCI: %s)", actualDriver, ifaceName, pciAddress)
		return actualDriver
	}

	// Default fallback - assume ENA for AWS environments
	log.Printf("Could not detect driver for interface %s, defaulting to 'ena' for AWS ENA devices", ifaceName)
	return "ena"
}
