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
)

func main() {
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

	// Create a context that will be canceled on SIGINT or SIGTERM
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down", sig)
		cancel()
	}()

	// Start the MTU updater
	log.Printf("Starting MTU updater with default MTU: %d", cfg.DefaultMTU)

	// Get the node name from the environment
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		var err error
		nodeName, err = os.Hostname()
		if err != nil {
			log.Printf("Warning: Failed to get hostname: %v", err)
			log.Printf("Will use default MTU only")
		} else {
			log.Printf("Using hostname as node name: %s", nodeName)
		}
	} else {
		log.Printf("Using NODE_NAME from environment: %s", nodeName)
	}

	// Create a Kubernetes client if we have a node name
	if nodeName != "" {
		// Try to create a Kubernetes client
		k8sConfig, err := rest.InClusterConfig()
		if err != nil {
			log.Printf("Warning: Failed to create in-cluster config: %v", err)
			log.Printf("Will use default MTU only")
		} else {
			// Create the clientset
			clientset, err := kubernetes.NewForConfig(k8sConfig)
			if err != nil {
				log.Printf("Warning: Failed to create Kubernetes client: %v", err)
				log.Printf("Will use default MTU only")
			} else {
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
			}
		}
	}

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
		return fallbackSetMTU(ifaceName, mtu)
	}

	log.Printf("Successfully set MTU for interface %s to %d", ifaceName, mtu)
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
	// Get the MAC address for this ENI from AWS metadata
	// We'll use the EC2 instance metadata service to get the MAC addresses of all interfaces
	// and then match them to the ENI ID

	// First, try to get the MAC address from the ENI ID using the AWS metadata service
	macAddress, err := getMacAddressForENI(eniID)
	if err != nil {
		log.Printf("Warning: Failed to get MAC address for ENI %s: %v", eniID, err)
		// Fall back to the old method if we can't get the MAC address
	} else if macAddress != "" {
		// If we have a MAC address, find the interface with this MAC
		ifaceName, err := getInterfaceNameByMAC(macAddress)
		if err != nil {
			log.Printf("Warning: Failed to find interface with MAC %s: %v", macAddress, err)
		} else if ifaceName != "" {
			log.Printf("Successfully mapped ENI %s to interface %s using MAC %s", eniID, ifaceName, macAddress)
			return ifaceName, nil
		}
	}

	// Fall back to checking all interfaces
	log.Printf("Falling back to interface pattern matching for ENI %s", eniID)
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

	// Try to find a secondary interface
	// We'll look for interfaces that match common AWS ENI patterns
	// and aren't the primary interface or loopback
	for _, link := range links {
		ifaceName := link.Attrs().Name
		if ifaceName != "lo" && ifaceName != primaryIface {
			// Check if it matches common AWS ENI patterns
			if strings.HasPrefix(ifaceName, "eth") ||
				strings.HasPrefix(ifaceName, "ens") ||
				strings.HasPrefix(ifaceName, "eni") ||
				strings.HasPrefix(ifaceName, "en") {
				log.Printf("Found potential interface %s for ENI %s (using pattern matching)", ifaceName, eniID)
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

	// Try to get the MAC addresses from the metadata service
	cmd := exec.Command("curl", "-s", "http://169.254.169.254/latest/meta-data/network/interfaces/macs/")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get MAC addresses from metadata service: %v", err)
	}

	// Parse the output - it should be a list of MAC addresses with trailing slashes
	macAddresses := strings.Split(strings.TrimSpace(string(output)), "\n")

	// For each MAC address, check if it corresponds to our ENI
	for _, macWithSlash := range macAddresses {
		// Remove the trailing slash
		mac := strings.TrimSuffix(macWithSlash, "/")

		// Get the interface ID for this MAC
		cmd = exec.Command("curl", "-s", fmt.Sprintf("http://169.254.169.254/latest/meta-data/network/interfaces/macs/%s/interface-id", macWithSlash))
		output, err = cmd.CombinedOutput()
		if err != nil {
			log.Printf("Warning: Failed to get interface ID for MAC %s: %v", mac, err)
			continue
		}

		interfaceID := strings.TrimSpace(string(output))
		if interfaceID == eniID {
			log.Printf("Found MAC address %s for ENI %s", mac, eniID)
			return mac, nil
		}
	}

	return "", fmt.Errorf("no MAC address found for ENI %s", eniID)
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

		// Set MTU if configured
		if err := setInterfaceMTU(link, cfg); err != nil {
			log.Printf("Warning: Failed to set MTU for interface %s: %v", ifaceName, err)
			// Continue with other interfaces
		}
	}

	return nil
}
