// Package network provides network interface management functionality
// for the AWS Multi-ENI Controller.
package network

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	vnetlink "github.com/vishvananda/netlink"
)

// Manager handles network interface operations
type Manager struct {
	config *config.ENIManagerConfig
}

// NewManager creates a new network interface manager
func NewManager(cfg *config.ENIManagerConfig) *Manager {
	return &Manager{
		config: cfg,
	}
}

// InterfaceInfo represents information about a network interface
type InterfaceInfo struct {
	Name         string
	Index        int
	State        string
	MTU          int
	PCIAddress   string
	MACAddress   string
	IsAWSENI     bool
	DeviceIndex  int
}

// GetAllInterfaces returns information about all network interfaces
func (m *Manager) GetAllInterfaces() ([]InterfaceInfo, error) {
	links, err := vnetlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %v", err)
	}

	var interfaces []InterfaceInfo
	for _, link := range links {
		info := InterfaceInfo{
			Name:        link.Attrs().Name,
			Index:       link.Attrs().Index,
			MTU:         link.Attrs().MTU,
			MACAddress:  link.Attrs().HardwareAddr.String(),
		}

		// Get interface state
		if link.Attrs().Flags&net.FlagUp != 0 {
			info.State = "UP"
		} else {
			info.State = "DOWN"
		}

		// Check if this is an AWS ENI
		info.IsAWSENI = m.isAWSENI(info.Name)

		// Get PCI address if available
		if pciAddr, err := m.getPCIAddressForInterface(info.Name); err == nil {
			info.PCIAddress = pciAddr
		}

		// Get device index if it's an AWS ENI
		if info.IsAWSENI {
			if devIndex, err := m.getDeviceIndexForInterface(info.Name); err == nil {
				info.DeviceIndex = devIndex
			}
		}

		interfaces = append(interfaces, info)
	}

	return interfaces, nil
}

// BringUpInterface brings up a network interface
func (m *Manager) BringUpInterface(ifaceName string) error {
	log.Printf("Bringing up interface %s", ifaceName)

	// Get the link
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get link for interface %s: %v", ifaceName, err)
	}

	// Check if already up
	if link.Attrs().Flags&net.FlagUp != 0 {
		log.Printf("Interface %s is already up", ifaceName)
		return nil
	}

	// Try to bring up using netlink
	if err := vnetlink.LinkSetUp(link); err != nil {
		log.Printf("Failed to bring up interface %s using netlink: %v, trying ip command", ifaceName, err)
		return m.bringUpInterfaceWithIP(ifaceName)
	}

	log.Printf("Successfully brought up interface %s using netlink", ifaceName)
	return nil
}

// BringDownInterface brings down a network interface
func (m *Manager) BringDownInterface(ifaceName string) error {
	log.Printf("Bringing down interface %s", ifaceName)

	// Get the link
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get link for interface %s: %v", ifaceName, err)
	}

	// Check if already down
	if link.Attrs().Flags&net.FlagUp == 0 {
		log.Printf("Interface %s is already down", ifaceName)
		return nil
	}

	// Bring down using netlink
	if err := vnetlink.LinkSetDown(link); err != nil {
		log.Printf("Failed to bring down interface %s using netlink: %v, trying ip command", ifaceName, err)
		return m.bringDownInterfaceWithIP(ifaceName)
	}

	log.Printf("Successfully brought down interface %s", ifaceName)
	return nil
}

// SetMTU sets the MTU for a network interface
func (m *Manager) SetMTU(ifaceName string, mtu int) error {
	log.Printf("Setting MTU for interface %s to %d", ifaceName, mtu)

	// Get the link
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get link for interface %s: %v", ifaceName, err)
	}

	// Check if MTU is already set
	if link.Attrs().MTU == mtu {
		log.Printf("Interface %s already has MTU %d", ifaceName, mtu)
		return nil
	}

	// Set MTU using netlink
	if err := vnetlink.LinkSetMTU(link, mtu); err != nil {
		log.Printf("Failed to set MTU for interface %s using netlink: %v, trying ip command", ifaceName, err)
		return m.setMTUWithIP(ifaceName, mtu)
	}

	log.Printf("Successfully set MTU for interface %s to %d", ifaceName, mtu)
	return nil
}

// ConfigureInterfaceFromNodeENI configures an interface based on NodeENI specification
func (m *Manager) ConfigureInterfaceFromNodeENI(ifaceName string, nodeENI networkingv1alpha1.NodeENI) error {
	log.Printf("Configuring interface %s from NodeENI %s", ifaceName, nodeENI.Name)

	// Bring up the interface
	if err := m.BringUpInterface(ifaceName); err != nil {
		return fmt.Errorf("failed to bring up interface %s: %v", ifaceName, err)
	}

	// Set MTU if specified
	if nodeENI.Spec.MTU > 0 {
		if err := m.SetMTU(ifaceName, nodeENI.Spec.MTU); err != nil {
			return fmt.Errorf("failed to set MTU for interface %s: %v", ifaceName, err)
		}
	}

	log.Printf("Successfully configured interface %s", ifaceName)
	return nil
}

// GetInterfaceNameForDeviceIndex returns the interface name for a given device index
func (m *Manager) GetInterfaceNameForDeviceIndex(deviceIndex int) (string, error) {
	interfaces, err := m.GetAllInterfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get interfaces: %v", err)
	}

	for _, iface := range interfaces {
		if iface.IsAWSENI && iface.DeviceIndex == deviceIndex {
			return iface.Name, nil
		}
	}

	return "", fmt.Errorf("no interface found for device index %d", deviceIndex)
}

// WaitForInterface waits for an interface to appear
func (m *Manager) WaitForInterface(ifaceName string, timeout time.Duration) error {
	log.Printf("Waiting for interface %s to appear (timeout: %v)", ifaceName, timeout)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := vnetlink.LinkByName(ifaceName); err == nil {
			log.Printf("Interface %s appeared", ifaceName)
			return nil
		}

		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for interface %s to appear", ifaceName)
}

// Helper methods

func (m *Manager) isAWSENI(ifaceName string) bool {
	// Skip loopback and primary interface
	if ifaceName == "lo" || ifaceName == m.config.PrimaryInterface {
		return false
	}

	// Check against ENI pattern
	if m.config.ENIPattern != "" {
		matched, err := regexp.MatchString(m.config.ENIPattern, ifaceName)
		if err != nil {
			log.Printf("Error matching ENI pattern: %v", err)
			return false
		}
		if !matched {
			return false
		}
	}

	// Check against ignore list
	for _, ignored := range m.config.IgnoreInterfaces {
		if ifaceName == ignored {
			return false
		}
	}

	return true
}

func (m *Manager) getPCIAddressForInterface(ifaceName string) (string, error) {
	// Read PCI address from sysfs
	pciPath := fmt.Sprintf("/sys/class/net/%s/device/uevent", ifaceName)
	data, err := os.ReadFile(pciPath)
	if err != nil {
		return "", fmt.Errorf("failed to read PCI info for interface %s: %v", ifaceName, err)
	}

	// Parse PCI address from uevent
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
			return strings.TrimPrefix(line, "PCI_SLOT_NAME="), nil
		}
	}

	return "", fmt.Errorf("PCI address not found for interface %s", ifaceName)
}

func (m *Manager) getDeviceIndexForInterface(ifaceName string) (int, error) {
	// Try to extract device index from interface name
	// This is a simplified approach - in practice, this would use more sophisticated mapping
	
	// For eth interfaces (eth0, eth1, etc.)
	if strings.HasPrefix(ifaceName, "eth") {
		indexStr := strings.TrimPrefix(ifaceName, "eth")
		if index, err := strconv.Atoi(indexStr); err == nil {
			return index, nil
		}
	}

	// For ens interfaces (ens5, ens6, etc.)
	if strings.HasPrefix(ifaceName, "ens") {
		indexStr := strings.TrimPrefix(ifaceName, "ens")
		if index, err := strconv.Atoi(indexStr); err == nil {
			// EKS typically starts at ens5 for device index 1
			return index - 4, nil
		}
	}

	return 0, fmt.Errorf("could not determine device index for interface %s", ifaceName)
}

func (m *Manager) bringUpInterfaceWithIP(ifaceName string) error {
	cmd := exec.Command("ip", "link", "set", "dev", ifaceName, "up")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to bring up interface %s with ip command: %v, output: %s", 
			ifaceName, err, string(output))
	}

	log.Printf("Successfully brought up interface %s using ip command", ifaceName)
	return nil
}

func (m *Manager) bringDownInterfaceWithIP(ifaceName string) error {
	cmd := exec.Command("ip", "link", "set", "dev", ifaceName, "down")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to bring down interface %s with ip command: %v, output: %s", 
			ifaceName, err, string(output))
	}

	log.Printf("Successfully brought down interface %s using ip command", ifaceName)
	return nil
}

func (m *Manager) setMTUWithIP(ifaceName string, mtu int) error {
	cmd := exec.Command("ip", "link", "set", "dev", ifaceName, "mtu", strconv.Itoa(mtu))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set MTU for interface %s with ip command: %v, output: %s", 
			ifaceName, err, string(output))
	}

	log.Printf("Successfully set MTU for interface %s to %d using ip command", ifaceName, mtu)
	return nil
}
