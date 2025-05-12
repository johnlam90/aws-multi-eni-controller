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
	version       = "v1.2.5" // Version of the ENI Manager

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

// setupMTUUpdater sets up the MTU updater for NodeENI resources
func setupMTUUpdater(ctx context.Context, nodeName string, cfg *config.ENIManagerConfig) {
	log.Printf("Starting MTU updater with default MTU: %d", cfg.DefaultMTU)

	// Skip if no node name
	if nodeName == "" {
		log.Printf("No node name available, skipping MTU updater")
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

	// Start a goroutine to periodically update MTU values from NodeENI resources
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Do an initial update
		updateMTUFromNodeENI(ctx, clientset, nodeName, cfg)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Update MTU values from NodeENI resources
				updateMTUFromNodeENI(ctx, clientset, nodeName, cfg)
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

	// Setup MTU updater
	setupMTUUpdater(ctx, nodeName, cfg)

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
	updateInterfaceStates(interfaceStates, cfg)

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
			changed := checkInterfaceChanges(interfaceStates, cfg)
			if changed && cfg.DebugMode {
				log.Printf("Detected interface changes")
			}
		}
	}
}

// updateInterfaceStates updates the map of interface states
func updateInterfaceStates(states map[string]bool, cfg *config.ENIManagerConfig) {
	links, err := vnetlink.LinkList()
	if err != nil {
		log.Printf("Error listing interfaces: %v", err)
		return
	}

	// Create a new map to detect removed interfaces
	newStates := make(map[string]bool)

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

		// Check if interface is UP
		isUp := link.Attrs().OperState == vnetlink.OperUp
		newStates[ifaceName] = isUp
	}

	// Update the states map
	for k := range states {
		if _, exists := newStates[k]; !exists {
			delete(states, k) // Interface was removed
		}
	}

	for k, v := range newStates {
		states[k] = v // Add or update interface state
	}
}

// checkInterfaceChanges checks for interface changes and brings up interfaces as needed
func checkInterfaceChanges(states map[string]bool, cfg *config.ENIManagerConfig) bool {
	links, err := vnetlink.LinkList()
	if err != nil {
		log.Printf("Error listing interfaces: %v", err)
		return false
	}

	changed := false

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

		// Check if interface is UP
		isUp := link.Attrs().OperState == vnetlink.OperUp
		prevState, exists := states[ifaceName]

		// If this is a new interface or its state has changed
		if !exists || prevState != isUp {
			changed = true
			states[ifaceName] = isUp

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

// bringUpInterface brings up a network interface
func bringUpInterface(link vnetlink.Link, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	log.Printf("Bringing up interface: %s", ifaceName)

	// Try using netlink first
	err := netlinkBringUpInterface(link, cfg.InterfaceUpTimeout)
	if err != nil {
		log.Printf("Netlink method failed, trying fallback method: %v", err)
		// Fall back to using ip command
		return fallbackBringUpInterface(ifaceName, cfg.InterfaceUpTimeout)
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
		log.Printf("Using default MTU %d for interface %s (current MTU: %d)", mtu, ifaceName, currentMTU)
	} else {
		log.Printf("Using interface-specific MTU %d for interface %s (current MTU: %d)", mtu, ifaceName, currentMTU)
	}

	// If MTU is 0 or negative, don't change it (use system default)
	if mtu <= 0 {
		log.Printf("No MTU specified for interface %s, using system default (current MTU: %d)", ifaceName, currentMTU)
		return nil
	}

	// If the MTU is already set correctly, don't change it
	if currentMTU == mtu {
		log.Printf("Interface %s already has the correct MTU: %d", ifaceName, mtu)
		return nil
	}

	log.Printf("Setting MTU for interface %s from %d to %d", ifaceName, currentMTU, mtu)

	// Try using netlink first
	err := vnetlink.LinkSetMTU(link, mtu)
	if err != nil {
		log.Printf("Netlink method failed to set MTU, trying fallback method: %v", err)
		// Fall back to using ip command
		err = fallbackSetMTU(ifaceName, mtu)
		if err != nil {
			log.Printf("Fallback method failed to set MTU, trying direct sysfs method: %v", err)
			// Try direct sysfs method as a last resort
			return sysfsSetMTU(ifaceName, mtu)
		}
		return err
	}

	// Verify the MTU was set correctly
	updatedLink, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		log.Printf("Warning: Failed to verify MTU for interface %s: %v", ifaceName, err)
		return nil // Continue anyway
	}

	if updatedLink.Attrs().MTU != mtu {
		log.Printf("Warning: MTU verification failed for interface %s. Expected: %d, Actual: %d",
			ifaceName, mtu, updatedLink.Attrs().MTU)

		// Try the fallback method
		log.Printf("Trying fallback method to set MTU")
		err = fallbackSetMTU(ifaceName, mtu)
		if err != nil {
			log.Printf("Fallback method failed to set MTU, trying direct sysfs method: %v", err)
			// Try direct sysfs method as a last resort
			return sysfsSetMTU(ifaceName, mtu)
		}
	} else {
		log.Printf("Successfully set and verified MTU for interface %s to %d", ifaceName, mtu)
	}

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

	log.Printf("Successfully set MTU for interface %s to %d using sysfs method", ifaceName, mtu)

	// Verify the MTU was set correctly
	mtuBytes, err := os.ReadFile(mtuPath)
	if err != nil {
		log.Printf("Warning: Failed to verify MTU for interface %s: %v", ifaceName, err)
		return nil // Continue anyway
	}

	actualMTU := strings.TrimSpace(string(mtuBytes))
	if actualMTU != mtuStr {
		return fmt.Errorf("MTU verification failed for interface %s. Expected: %s, Actual: %s",
			ifaceName, mtuStr, actualMTU)
	}

	log.Printf("Successfully verified MTU for interface %s is now %s", ifaceName, actualMTU)
	return nil
}

// updateMTUFromNodeENI updates the MTU values from NodeENI resources
func updateMTUFromNodeENI(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, cfg *config.ENIManagerConfig) {
	log.Printf("Updating MTU values from NodeENI resources for node %s", nodeName)

	// List all NodeENI resources
	nodeENIList, err := clientset.CoreV1().RESTClient().
		Get().
		AbsPath("/apis/networking.k8s.aws/v1alpha1/nodeenis").
		Do(ctx).
		Raw()

	if err != nil {
		log.Printf("Error listing NodeENI resources: %v", err)
		return
	}

	// Parse the response
	var nodeENIs struct {
		Items []networkingv1alpha1.NodeENI `json:"items"`
	}
	if err := json.Unmarshal(nodeENIList, &nodeENIs); err != nil {
		log.Printf("Error parsing NodeENI list: %v", err)
		return
	}

	log.Printf("Found %d NodeENI resources", len(nodeENIs.Items))

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIs.Items {
		log.Printf("Processing NodeENI: %s", nodeENI.Name)

		// Check if this NodeENI applies to our node
		if nodeENI.Status.Attachments == nil {
			log.Printf("NodeENI %s has no attachments, skipping", nodeENI.Name)
			continue
		}

		// Check each attachment
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.NodeID == nodeName {
				// This attachment is for our node
				log.Printf("Found NodeENI attachment for our node: %s, ENI: %s", nodeName, attachment.ENIID)

				// Get the MTU value - first check the attachment MTU, then fall back to the spec MTU
				mtuValue := attachment.MTU
				if mtuValue <= 0 {
					mtuValue = nodeENI.Spec.MTU
				}

				log.Printf("MTU value from NodeENI: %d", mtuValue)

				// Skip if MTU is not set
				if mtuValue <= 0 {
					log.Printf("No MTU specified in NodeENI %s, skipping", nodeENI.Name)
					continue
				}

				// Get the interface name for this ENI
				ifaceName, err := getInterfaceNameForENI(attachment.ENIID)
				if err != nil {
					log.Printf("Error getting interface name for ENI %s: %v", attachment.ENIID, err)
					continue
				}

				// Set the MTU for this interface
				if ifaceName != "" {
					log.Printf("Setting MTU for interface %s to %d from NodeENI %s", ifaceName, mtuValue, nodeENI.Name)
					cfg.InterfaceMTUs[ifaceName] = mtuValue

					// Apply the MTU immediately
					link, err := vnetlink.LinkByName(ifaceName)
					if err != nil {
						log.Printf("Error getting link for interface %s: %v", ifaceName, err)
						continue
					}

					if err := setInterfaceMTU(link, cfg); err != nil {
						log.Printf("Error setting MTU for interface %s: %v", ifaceName, err)
					} else {
						log.Printf("Successfully applied MTU %d to interface %s", mtuValue, ifaceName)
					}
				}
			}
		}
	}

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

		// Use the first eth interface that's not already in use
		ifaceName := ethInterfaces[0]
		log.Printf("Selected eth interface %s for ENI %s", ifaceName, eniID)
		usedInterfaces[ifaceName] = eniID
		return ifaceName
	}

	return ""
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
			// Check if the interface matches the eth pattern
			if strings.HasPrefix(ifaceName, "eth") {
				log.Printf("Interface %s is not mapped to any ENI, using default MTU %d from NodeENI resources",
					ifaceName, defaultMTUFromNodeENI)
				cfg.InterfaceMTUs[ifaceName] = defaultMTUFromNodeENI
			}
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
