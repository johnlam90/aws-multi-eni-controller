// Package main implements the AWS Multi-ENI Manager, which is responsible for
// bringing up secondary network interfaces on AWS EC2 instances.
//
// The ENI Manager runs as a daemon on each node and periodically checks for
// network interfaces that are in the DOWN state. When it finds such interfaces,
// it attempts to bring them up using either the netlink library or the 'ip' command
// as a fallback. This ensures that secondary ENIs attached by the controller are
// properly configured and ready for use.
package main

import (
	"flag"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	// Command line flags
	checkInterval = flag.Duration("check-interval", 30*time.Second, "Interval between interface checks")
	primaryIface  = flag.String("primary-interface", "", "Primary interface name to ignore (if empty, will auto-detect)")
	debugMode     = flag.Bool("debug", false, "Enable debug logging")
	version       = "1.0.0" // Version of the ENI Manager
)

func main() {
	flag.Parse()

	// Load configuration from command-line flags and environment variables
	cfg := config.LoadENIManagerConfigFromFlags(checkInterval, primaryIface, debugMode)

	log.Printf("ENI Manager starting (version %s)", version)
	log.Printf("Configuration: check interval=%s, debug=%v, interface up timeout=%s",
		cfg.CheckInterval, cfg.DebugMode, cfg.InterfaceUpTimeout)

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

	// Run the main loop
	wait.Forever(func() {
		if err := checkAndBringUpInterfaces(cfg); err != nil {
			log.Printf("Error checking interfaces: %v", err)
		}
	}, cfg.CheckInterval)
}

// detectPrimaryInterface attempts to detect the primary network interface
// It uses the default route to determine which interface is the primary one
func detectPrimaryInterface() (string, error) {
	// Get the default route
	routes, err := netlink.RouteList(nil, unix.AF_INET) // Use unix.AF_INET instead of netlink.FAMILY_V4
	if err != nil {
		return "", fmt.Errorf("failed to get routes: %v", err)
	}

	// Find the default route (0.0.0.0/0)
	for _, route := range routes {
		if route.Dst == nil || route.Dst.String() == "0.0.0.0/0" {
			if route.LinkIndex > 0 {
				// Get the interface by index
				link, err := netlink.LinkByIndex(route.LinkIndex)
				if err != nil {
					return "", fmt.Errorf("failed to get link by index %d: %v", route.LinkIndex, err)
				}
				return link.Attrs().Name, nil
			}
		}
	}

	return "", fmt.Errorf("no default route found")
}

// checkAndBringUpInterfaces checks for DOWN interfaces and brings them up
func checkAndBringUpInterfaces(cfg *config.ENIManagerConfig) error {
	// Get all network interfaces
	links, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %v", err)
	}

	for _, link := range links {
		// Skip loopback and primary interface
		if link.Attrs().Name == "lo" || link.Attrs().Name == cfg.PrimaryInterface {
			continue
		}

		// Check if interface is DOWN
		if link.Attrs().OperState == netlink.OperDown {
			log.Printf("Found DOWN interface: %s", link.Attrs().Name)

			if err := bringUpInterface(link, cfg); err != nil {
				log.Printf("Error bringing up interface %s: %v", link.Attrs().Name, err)
				continue
			}
		} else if cfg.DebugMode {
			log.Printf("Interface %s is already UP", link.Attrs().Name)
		}
	}

	return nil
}

// bringUpInterface brings up a network interface
func bringUpInterface(link netlink.Link, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	log.Printf("Bringing up interface: %s", ifaceName)

	// Try using netlink first
	err := netlinkBringUpInterface(link, cfg.InterfaceUpTimeout)
	if err != nil {
		log.Printf("Netlink method failed, trying fallback method: %v", err)
		// Fall back to using ip command
		return fallbackBringUpInterface(ifaceName, cfg.InterfaceUpTimeout)
	}

	log.Printf("Successfully brought up interface %s", ifaceName)
	return nil
}

// netlinkBringUpInterface brings up an interface using netlink
func netlinkBringUpInterface(link netlink.Link, timeout time.Duration) error {
	ifaceName := link.Attrs().Name

	// Set the interface UP
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set interface %s up: %v", ifaceName, err)
	}

	// Wait for the interface to come up
	time.Sleep(timeout)

	// Verify the interface is now UP
	updatedLink, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get updated link info for %s: %v", ifaceName, err)
	}

	if updatedLink.Attrs().OperState != netlink.OperUp {
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
