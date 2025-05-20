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
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	vnetlink "github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
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

// setupNodeENIUpdater sets up the updater for NodeENI resources
func setupNodeENIUpdater(ctx context.Context, nodeName string, cfg *config.ENIManagerConfig) {
	log.Printf("Starting NodeENI updater with default MTU: %d, DPDK enabled: %v", cfg.DefaultMTU, cfg.EnableDPDK)

	// Skip if no node name
	if nodeName == "" {
		log.Printf("No node name available, skipping NodeENI updater")
		return
	}

	// Try to create a Kubernetes client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Warning: Failed to create in-cluster config: %v", err)
		log.Printf("Will use default MTU only")
		return
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		log.Printf("Warning: Failed to create Kubernetes client: %v", err)
		log.Printf("Will use default MTU only")
		return
	}

	// Start a goroutine to periodically update values from NodeENI resources
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Get all NodeENI resources
		nodeENIs, err := getNodeENIResources(ctx, clientset)
		if err != nil {
			log.Printf("Error getting NodeENI resources: %v", err)
		} else {
			// Do an initial update for MTU
			updateMTUFromNodeENI(ctx, clientset, nodeName, cfg)

			// Do an initial update for DPDK binding
			if cfg.EnableDPDK {
				updateDPDKBindingFromNodeENI(nodeName, cfg, nodeENIs)
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Get all NodeENI resources
				nodeENIs, err := getNodeENIResources(ctx, clientset)
				if err != nil {
					log.Printf("Error getting NodeENI resources: %v", err)
					continue
				}

				// Update MTU values from NodeENI resources
				updateMTUFromNodeENI(ctx, clientset, nodeName, cfg)

				// Update DPDK binding from NodeENI resources
				if cfg.EnableDPDK {
					updateDPDKBindingFromNodeENI(nodeName, cfg, nodeENIs)
				}
			}
		}
	}()

	// Set up a ticker to periodically check and update MTU values
	go func() {
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
	}()
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
	log.Printf("Updating DPDK binding from NodeENI resources for node %s", nodeName)

	// First, check if we have any interfaces that need to be unbound from DPDK
	// This can happen if a NodeENI resource was deleted or DPDK was disabled
	checkForDPDKUnbinding(nodeName, cfg, nodeENIs)

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIs {
		processDPDKBindingForNodeENI(nodeENI, nodeName, cfg)
	}
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

// processDPDKBindingForAttachment processes DPDK binding for a single NodeENI attachment
func processDPDKBindingForAttachment(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI,
	nodeName string, cfg *config.ENIManagerConfig) {

	// Skip if this attachment is not for our node
	if attachment.NodeID != nodeName {
		return
	}

	// Skip if this attachment is already bound to DPDK
	if attachment.DPDKBound {
		log.Printf("ENI %s is already bound to DPDK driver %s", attachment.ENIID, attachment.DPDKDriver)
		return
	}

	// This attachment is for our node
	log.Printf("Processing DPDK binding for NodeENI attachment: %s, ENI: %s, DeviceIndex: %d",
		nodeName, attachment.ENIID, attachment.DeviceIndex)

	// Determine the DPDK driver to use
	dpdkDriver := cfg.DefaultDPDKDriver
	if nodeENI.Spec.DPDKDriver != "" {
		dpdkDriver = nodeENI.Spec.DPDKDriver
	}

	// First, try to find the interface using the device index from the NodeENI spec
	// This is the most reliable way to ensure we're binding the correct interface
	expectedIfaceName := fmt.Sprintf("eth%d", attachment.DeviceIndex)
	log.Printf("Expected interface name based on device index: %s", expectedIfaceName)

	// Check if the expected interface exists
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", expectedIfaceName)); err == nil {
		log.Printf("Found interface %s matching device index %d", expectedIfaceName, attachment.DeviceIndex)
		ifaceName := expectedIfaceName

		// Get the link for this interface
		link, err := vnetlink.LinkByName(ifaceName)
		if err != nil {
			log.Printf("Error getting link for interface %s: %v", ifaceName, err)
			return
		}

		// Bind the interface to DPDK
		if err := bindInterfaceToDPDK(link, dpdkDriver, cfg); err != nil {
			log.Printf("Error binding interface %s to DPDK: %v", ifaceName, err)
			return
		}

		// Update the DPDKBoundInterfaces map with NodeENI name and ENI ID
		// First, get the PCI address for this interface
		pciAddress, err := getPCIAddressForInterface(ifaceName)
		if err != nil {
			log.Printf("Error getting PCI address for interface %s: %v", ifaceName, err)
			return
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

		// Update the attachment status to mark it as DPDK-bound
		// This will be used during cleanup to properly unbind the interface
		if err := updateNodeENIDPDKStatus(attachment.ENIID, nodeENI.Name, dpdkDriver, true); err != nil {
			log.Printf("Warning: Failed to update attachment DPDK status: %v", err)
			// Continue anyway, the interface is bound to DPDK
		} else {
			log.Printf("Successfully updated attachment DPDK status for ENI %s", attachment.ENIID)
		}
		return
	}

	// If the expected interface doesn't exist, fall back to the old method
	log.Printf("Interface %s does not exist, falling back to ENI ID lookup", expectedIfaceName)

	// Get the interface name for this ENI
	ifaceName, err := getInterfaceNameForENI(attachment.ENIID)
	if err != nil {
		log.Printf("Error getting interface name for ENI %s: %v", attachment.ENIID, err)
		log.Printf("Could not determine interface name for ENI %s, skipping DPDK binding", attachment.ENIID)
		return
	}

	log.Printf("Found interface %s for ENI %s using ENI ID lookup", ifaceName, attachment.ENIID)

	// Check if the interface exists
	if _, err := os.Stat(fmt.Sprintf("/sys/class/net/%s", ifaceName)); os.IsNotExist(err) {
		log.Printf("Interface %s does not exist yet, skipping DPDK binding for now", ifaceName)
		return
	}

	// Get the link for this interface
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		log.Printf("Error getting link for interface %s: %v", ifaceName, err)
		return
	}

	// Bind the interface to DPDK
	if err := bindInterfaceToDPDK(link, dpdkDriver, cfg); err != nil {
		log.Printf("Error binding interface %s to DPDK: %v", ifaceName, err)
		return
	}

	// Update the DPDKBoundInterfaces map with NodeENI name and ENI ID
	// First, get the PCI address for this interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err != nil {
		log.Printf("Error getting PCI address for interface %s: %v", ifaceName, err)
		return
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

	// Update the attachment status to mark it as DPDK-bound
	// This will be used during cleanup to properly unbind the interface
	if err := updateNodeENIDPDKStatus(attachment.ENIID, nodeENI.Name, dpdkDriver, true); err != nil {
		log.Printf("Warning: Failed to update attachment DPDK status: %v", err)
		// Continue anyway, the interface is bound to DPDK
	} else {
		log.Printf("Successfully updated attachment DPDK status for ENI %s", attachment.ENIID)
	}

	// Store the resource name for this interface if specified in the NodeENI
	if nodeENI.Spec.DPDKResourceName != "" {
		cfg.DPDKResourceNames[ifaceName] = nodeENI.Spec.DPDKResourceName
		// Update the SRIOV device plugin config with the specified resource name
		pciAddress, err := getPCIAddressForInterface(ifaceName)
		if err == nil {
			if err := updateSRIOVDevicePluginConfig(ifaceName, pciAddress, dpdkDriver, cfg); err != nil {
				log.Printf("Warning: Failed to update SRIOV device plugin config with resource name %s: %v",
					nodeENI.Spec.DPDKResourceName, err)
			}
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

// bindInterfaceToDPDK binds a network interface to a DPDK driver
func bindInterfaceToDPDK(link vnetlink.Link, driver string, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	log.Printf("Binding interface %s to DPDK driver %s", ifaceName, driver)

	// Get the PCI address for the interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get PCI address for interface %s: %v", ifaceName, err)
	}

	// Check if the DPDK binding script exists
	if _, err := os.Stat(cfg.DPDKBindingScript); os.IsNotExist(err) {
		return fmt.Errorf("DPDK binding script %s does not exist", cfg.DPDKBindingScript)
	}

	// Execute the DPDK binding script to bind the interface to the DPDK driver
	cmd := exec.Command(cfg.DPDKBindingScript, "-b", driver, pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to bind interface %s to DPDK driver %s: %v, output: %s",
			ifaceName, driver, err, string(output))
	}

	log.Printf("Successfully bound interface %s (PCI: %s) to DPDK driver %s", ifaceName, pciAddress, driver)

	// Store the PCI address and driver mapping for future unbinding
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

	// Update the SRIOV device plugin configuration
	if err := updateSRIOVDevicePluginConfig(ifaceName, pciAddress, driver, cfg); err != nil {
		log.Printf("Warning: Failed to update SRIOV device plugin config: %v", err)
		// Continue anyway, the interface is bound to DPDK
	}

	return nil
}

// unbindInterfaceFromDPDK unbinds a network interface from a DPDK driver and rebinds it to the original driver
func unbindInterfaceFromDPDK(ifaceName string, cfg *config.ENIManagerConfig) error {
	// If ifaceName is a PCI address, we're being called from checkForDPDKUnbinding
	isPCIAddress := false
	pciRegex := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	if pciRegex.MatchString(ifaceName) {
		isPCIAddress = true
		log.Printf("Unbinding interface with PCI address %s from DPDK driver", ifaceName)
	} else {
		log.Printf("Unbinding interface %s from DPDK driver", ifaceName)
	}

	// First, try to find the PCI address for this interface
	pciAddress := ""

	// Check if ifaceName is already a PCI address
	if isPCIAddress {
		pciAddress = ifaceName
	} else {
		// Try to get the PCI address for the interface
		var err error
		pciAddress, err = getPCIAddressForInterface(ifaceName)
		if err != nil {
			log.Printf("Could not get PCI address for interface %s: %v", ifaceName, err)

			// Try to find the interface by ENI ID in the map
			eniID := ""
			// Check if ifaceName contains an ENI ID (eni-xxxxxxxxx)
			eniIDRegex := regexp.MustCompile(`eni-[0-9a-f]+`)
			matches := eniIDRegex.FindStringSubmatch(ifaceName)
			if len(matches) > 0 {
				eniID = matches[0]
				log.Printf("Extracted ENI ID %s from interface name %s", eniID, ifaceName)

				// Look for this ENI ID in our bound interfaces map
				for addr, info := range cfg.DPDKBoundInterfaces {
					if info.ENIID == eniID {
						pciAddress = addr
						log.Printf("Found PCI address %s for ENI ID %s", pciAddress, eniID)
						break
					}
				}
			}

			// If we still don't have the PCI address, try to find it by interface name pattern
			if pciAddress == "" {
				// This is a fallback for cases where the interface was bound before we started tracking it
				pciAddress, err = findPCIAddressByInterfacePattern(ifaceName)
				if err != nil {
					return fmt.Errorf("failed to find PCI address for interface %s: %v", ifaceName, err)
				}

				if pciAddress == "" {
					return fmt.Errorf("no PCI address found for interface %s", ifaceName)
				}
			}
		}
	}

	// Now check if we have information about this PCI address
	boundInterface, exists := cfg.DPDKBoundInterfaces[pciAddress]
	if !exists {
		// If we don't have information about this PCI address, log a warning and return
		// This is likely a case where the interface was never bound to DPDK
		if isPCIAddress {
			log.Printf("Warning: No information found for PCI address %s in DPDKBoundInterfaces map", pciAddress)
			return nil
		}

		// If we're trying to unbind by interface name, create a default entry
		boundInterface = struct {
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

	// Log detailed information about what we're unbinding
	log.Printf("Unbinding details - PCI: %s, Interface: %s, ENI ID: %s, NodeENI: %s",
		pciAddress, boundInterface.IfaceName, boundInterface.ENIID, boundInterface.NodeENIName)

	// Check if the DPDK binding script exists
	if _, err := os.Stat(cfg.DPDKBindingScript); os.IsNotExist(err) {
		return fmt.Errorf("DPDK binding script %s does not exist", cfg.DPDKBindingScript)
	}

	// First unbind from the current driver
	cmd := exec.Command(cfg.DPDKBindingScript, "-u", pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Warning: Failed to unbind PCI address %s from DPDK driver: %v, output: %s",
			pciAddress, err, string(output))
		// Continue anyway, we'll try to bind to the original driver
	}

	// Now bind to the original driver (ena for AWS instances)
	cmd = exec.Command(cfg.DPDKBindingScript, "-b", "ena", pciAddress)
	output, err = cmd.CombinedOutput()
	if err != nil {
		log.Printf("Warning: Failed to bind PCI address %s to ena driver: %v, output: %s",
			pciAddress, err, string(output))
		// Don't return an error here, as we've already unbound from DPDK
		// The interface might reappear on its own after a while
	} else {
		log.Printf("Successfully unbound PCI address %s from DPDK driver and bound to ena driver", pciAddress)
	}

	// Update the NodeENI status if we have the NodeENI name and ENI ID
	if boundInterface.NodeENIName != "" && boundInterface.ENIID != "" {
		if err := updateNodeENIDPDKStatus(boundInterface.ENIID, boundInterface.NodeENIName, "", false); err != nil {
			log.Printf("Warning: Failed to update NodeENI status for DPDK unbinding: %v", err)
			// Continue anyway, the interface is unbound from DPDK
		} else {
			log.Printf("Successfully updated NodeENI status for DPDK unbinding of ENI %s", boundInterface.ENIID)
		}
	}

	// Remove from our tracking map
	delete(cfg.DPDKBoundInterfaces, pciAddress)

	// Update the SRIOV device plugin configuration to remove this device
	if err := removeSRIOVDevicePluginConfig(boundInterface.IfaceName, pciAddress, cfg); err != nil {
		log.Printf("Warning: Failed to update SRIOV device plugin config: %v", err)
		// Continue anyway, the interface is unbound from DPDK
	}

	// Wait a moment for the interface to reappear
	log.Printf("Waiting for interface to reappear after unbinding from DPDK")
	time.Sleep(2 * time.Second)

	return nil
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

// findPCIAddressByInterfacePattern tries to find the PCI address for an interface
// that might be bound to a DPDK driver and no longer visible as a network interface
func findPCIAddressByInterfacePattern(ifaceName string) (string, error) {
	// First try to get the device index from the interface name
	// Interface names are typically eth0, eth1, eth2, etc.
	var deviceIndex int
	if strings.HasPrefix(ifaceName, "eth") {
		_, err := fmt.Sscanf(ifaceName, "eth%d", &deviceIndex)
		if err != nil {
			return "", fmt.Errorf("failed to parse device index from interface name %s: %v", ifaceName, err)
		}
	} else if strings.HasPrefix(ifaceName, "ens") {
		_, err := fmt.Sscanf(ifaceName, "ens%d", &deviceIndex)
		if err != nil {
			return "", fmt.Errorf("failed to parse device index from interface name %s: %v", ifaceName, err)
		}
	} else {
		return "", fmt.Errorf("unsupported interface name format: %s", ifaceName)
	}

	log.Printf("Searching for PCI address for interface %s with device index %d", ifaceName, deviceIndex)

	// For AWS instances, the PCI addresses typically follow a pattern
	// The primary interface (eth0) is usually at 0000:00:05.0, and secondary interfaces
	// follow a pattern based on the device index

	// First, try to find the PCI address by checking all PCI devices
	// This is more reliable than using patterns
	pciDevices, err := listPCIDevices()
	if err != nil {
		log.Printf("Error listing PCI devices: %v", err)
		// Continue with pattern-based approach as fallback
	} else {
		// Try to find a device that matches the expected device index
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
							return pciAddr, nil
						}
					}
				}
			}
		}

		// If we couldn't find a direct match, try to infer based on device index
		// In AWS, eth0 is typically at 0000:00:05.0
		// For secondary interfaces, we need to check the pattern
		if deviceIndex == 0 {
			// For eth0, check if 0000:00:05.0 exists
			if contains(pciDevices, "0000:00:05.0") {
				log.Printf("Using PCI address 0000:00:05.0 for eth0 (standard AWS pattern)")
				return "0000:00:05.0", nil
			}
		} else {
			// For secondary interfaces, try common patterns
			// In AWS, secondary interfaces often follow a pattern like 0000:00:06.0, 0000:00:07.0, etc.
			candidateAddr := fmt.Sprintf("0000:00:%02d.0", deviceIndex+5)
			if contains(pciDevices, candidateAddr) {
				log.Printf("Using PCI address %s for %s (standard AWS pattern)", candidateAddr, ifaceName)
				return candidateAddr, nil
			}
		}
	}

	// Fallback to common patterns if direct lookup failed
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
			driverPath := fmt.Sprintf("%s/driver", pciDevPath)
			if driverLink, err := os.Readlink(driverPath); err == nil {
				// Extract the driver name from the path
				driverParts := strings.Split(driverLink, "/")
				if len(driverParts) > 0 {
					driverName := driverParts[len(driverParts)-1]
					if driverName == "vfio-pci" || driverName == "igb_uio" {
						// This is likely our device
						log.Printf("Found PCI device %s bound to DPDK driver %s, likely for interface %s",
							pattern, driverName, ifaceName)
						return pattern, nil
					}
				}
			}
		}
	}

	// If we couldn't find a matching device, try a more exhaustive search
	// List all PCI devices and check if any are bound to DPDK drivers
	allPciDevices, err := filepath.Glob("/sys/bus/pci/devices/*")
	if err != nil {
		return "", fmt.Errorf("failed to list PCI devices: %v", err)
	}

	for _, devPath := range allPciDevices {
		// Extract the PCI address from the path
		pciAddress := filepath.Base(devPath)

		// Check if this device is bound to a DPDK driver
		driverPath := fmt.Sprintf("%s/driver", devPath)
		if driverLink, err := os.Readlink(driverPath); err == nil {
			// Extract the driver name from the path
			driverParts := strings.Split(driverLink, "/")
			if len(driverParts) > 0 {
				driverName := driverParts[len(driverParts)-1]
				if driverName == "vfio-pci" || driverName == "igb_uio" {
					// This is a DPDK-bound device, check if it might be our interface
					// For now, we'll just return the first DPDK-bound device we find
					// This is a best-effort approach
					log.Printf("Found PCI device %s bound to DPDK driver %s, might be for interface %s",
						pciAddress, driverName, ifaceName)
					return pciAddress, nil
				}
			}
		}
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
	for i, resource := range config.ResourceList {
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
		config.ResourceList[i].Devices = newDevices
	}

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

// updateSRIOVDevicePluginConfig updates the SRIOV device plugin configuration
func updateSRIOVDevicePluginConfig(ifaceName, pciAddress, driver string, cfg *config.ENIManagerConfig) error {
	// Get the resource name for this interface
	resourceName, ok := cfg.DPDKResourceNames[ifaceName]
	if !ok {
		// Generate a default resource name based on the interface index
		// Extract the index from the interface name (e.g., eth2 -> 2)
		indexRegex := regexp.MustCompile(`[0-9]+$`)
		indexMatch := indexRegex.FindString(ifaceName)
		if indexMatch == "" {
			// If we can't extract an index, use a default
			resourceName = "intel.com/intel_sriov_netdevice_default"
		} else {
			resourceName = fmt.Sprintf("intel.com/intel_sriov_netdevice_%s", indexMatch)
		}
		// Store the generated resource name for future use
		cfg.DPDKResourceNames[ifaceName] = resourceName
	}

	// Check if the SRIOV device plugin config file exists
	var config SRIOVDPConfig
	if _, err := os.Stat(cfg.SRIOVDPConfigPath); os.IsNotExist(err) {
		// Create a new config file with this device
		config = SRIOVDPConfig{
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
	} else {
		// Read the existing config file
		configData, err := os.ReadFile(cfg.SRIOVDPConfigPath)
		if err != nil {
			return fmt.Errorf("failed to read SRIOV device plugin config: %v", err)
		}

		// Parse the config
		if err := json.Unmarshal(configData, &config); err != nil {
			return fmt.Errorf("failed to parse SRIOV device plugin config: %v", err)
		}

		// Check if the resource already exists
		resourceExists := false
		deviceExists := false
		for i, resource := range config.ResourceList {
			if resource.ResourceName == resourceName {
				resourceExists = true
				// Check if the device already exists
				for _, device := range resource.Devices {
					if device.PCIAddress == pciAddress {
						deviceExists = true
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
	}

	// Write the updated config back to the file
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SRIOV device plugin config: %v", err)
	}

	if err := os.WriteFile(cfg.SRIOVDPConfigPath, configData, 0644); err != nil {
		return fmt.Errorf("failed to write SRIOV device plugin config: %v", err)
	}

	log.Printf("Successfully updated SRIOV device plugin config for interface %s with resource name %s",
		ifaceName, resourceName)
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
	if !strings.Contains(string(output), "state UP") {
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

// getInterfaceNameForENI gets the interface name for an ENI ID
func getInterfaceNameForENI(eniID string) (string, error) {
	// Extract the last part of the ENI ID to use for matching
	// This is just for logging purposes
	_ = extractENIIDSuffix(eniID)

	// First, check if we've already mapped this ENI to an interface
	if ifaceName := checkExistingMapping(eniID); ifaceName != "" {
		return ifaceName, nil
	}

	// Try to get the interface name using MAC address
	ifaceName, err := getInterfaceNameByMACForENI(eniID)
	if err == nil && ifaceName != "" {
		return ifaceName, nil
	}

	// Fall back to pattern matching
	return findInterfaceByPattern(eniID)
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

	// First try to find eth interfaces
	ifaceName := findEthInterfaces(links, primaryIface, eniID)
	if ifaceName != "" {
		return ifaceName, nil
	}

	// If we couldn't find a suitable eth interface, try any interface
	return findAnyInterface(links, primaryIface, eniID)
}

// findEthInterfaces finds interfaces that match the eth pattern
func findEthInterfaces(links []vnetlink.Link, primaryIface, eniID string) string {
	// For AWS, secondary ENIs are typically named eth1, eth2, etc.
	deviceIndexPattern := regexp.MustCompile(`^eth[1-9][0-9]*$`)
	var ethInterfaces []string

	for _, link := range links {
		ifaceName := link.Attrs().Name
		if ifaceName != "lo" && ifaceName != primaryIface && deviceIndexPattern.MatchString(ifaceName) {
			// Check if this interface is already mapped to an ENI
			if _, ok := usedInterfaces[ifaceName]; !ok {
				ethInterfaces = append(ethInterfaces, ifaceName)
				log.Printf("Found potential eth interface: %s", ifaceName)
			}
		}
	}

	// If we found eth interfaces, use them in order
	if len(ethInterfaces) > 0 {
		// Sort the interfaces by name to ensure consistent ordering
		sort.Strings(ethInterfaces)

		// Log all found eth interfaces
		for i, name := range ethInterfaces {
			log.Printf("Eth interface %d: %s", i, name)
		}

		// Try to find the device index from the ENI attachment
		deviceIndex := findDeviceIndexForENI(eniID)
		if deviceIndex > 0 {
			// Look for an interface with the matching device index (e.g., eth2 for device index 2)
			targetIfaceName := fmt.Sprintf("eth%d", deviceIndex)
			for _, ifaceName := range ethInterfaces {
				if ifaceName == targetIfaceName {
					log.Printf("Selected eth interface %s for ENI %s based on device index %d",
						ifaceName, eniID, deviceIndex)
					usedInterfaces[ifaceName] = eniID
					return ifaceName
				}
			}
			log.Printf("Warning: Could not find interface with name %s for ENI %s with device index %d",
				targetIfaceName, eniID, deviceIndex)
		}

		// If we couldn't find a match by device index, use the first available interface
		ifaceName := ethInterfaces[0]
		log.Printf("Selected eth interface %s for ENI %s (fallback)", ifaceName, eniID)
		usedInterfaces[ifaceName] = eniID
		return ifaceName
	}

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
