package main

import (
	"flag"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	// Command line flags
	checkInterval = flag.Duration("check-interval", 30*time.Second, "Interval between interface checks")
	primaryIface  = flag.String("primary-interface", "", "Primary interface name to ignore (if empty, will auto-detect)")
	debugMode     = flag.Bool("debug", false, "Enable debug logging")
)

func main() {
	flag.Parse()

	log.Printf("ENI Manager starting (version 1.0.0)")
	log.Printf("Check interval: %s", *checkInterval)

	// Auto-detect primary interface if not specified
	if *primaryIface == "" {
		detected, err := detectPrimaryInterface()
		if err != nil {
			log.Printf("Warning: Failed to auto-detect primary interface: %v", err)
			log.Printf("Will not ignore any interfaces")
		} else {
			*primaryIface = detected
			log.Printf("Auto-detected primary interface: %s", *primaryIface)
		}
	} else {
		log.Printf("Using specified primary interface: %s", *primaryIface)
	}

	// Run the main loop
	wait.Forever(func() {
		if err := checkAndBringUpInterfaces(); err != nil {
			log.Printf("Error checking interfaces: %v", err)
		}
	}, *checkInterval)
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
func checkAndBringUpInterfaces() error {
	// Get all network interfaces
	links, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %v", err)
	}

	for _, link := range links {
		// Skip loopback and primary interface
		if link.Attrs().Name == "lo" || link.Attrs().Name == *primaryIface {
			continue
		}

		// Check if interface is DOWN
		if link.Attrs().OperState == netlink.OperDown {
			log.Printf("Found DOWN interface: %s", link.Attrs().Name)

			if err := bringUpInterface(link); err != nil {
				log.Printf("Error bringing up interface %s: %v", link.Attrs().Name, err)
				continue
			}
		} else if *debugMode {
			log.Printf("Interface %s is already UP", link.Attrs().Name)
		}
	}

	return nil
}

// bringUpInterface brings up a network interface
func bringUpInterface(link netlink.Link) error {
	ifaceName := link.Attrs().Name
	log.Printf("Bringing up interface: %s", ifaceName)

	// Try using netlink first
	err := netlinkBringUpInterface(link)
	if err != nil {
		log.Printf("Netlink method failed, trying fallback method: %v", err)
		// Fall back to using ip command
		return fallbackBringUpInterface(ifaceName)
	}

	log.Printf("Successfully brought up interface %s", ifaceName)
	return nil
}

// netlinkBringUpInterface brings up an interface using netlink
func netlinkBringUpInterface(link netlink.Link) error {
	ifaceName := link.Attrs().Name

	// Set the interface UP
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set interface %s up: %v", ifaceName, err)
	}

	// Wait for the interface to come up
	time.Sleep(2 * time.Second)

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
func fallbackBringUpInterface(ifaceName string) error {
	// Use the ip command to bring up the interface
	cmd := exec.Command("ip", "link", "set", "dev", ifaceName, "up")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to bring up interface %s using ip command: %v, output: %s",
			ifaceName, err, string(output))
	}

	// Wait for the interface to come up
	time.Sleep(2 * time.Second)

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
