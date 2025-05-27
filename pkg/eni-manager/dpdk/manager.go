// Package dpdk provides DPDK device binding and management functionality
// for the AWS Multi-ENI Controller.
package dpdk

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	vnetlink "github.com/vishvananda/netlink"
)

// Manager handles DPDK device binding operations
type Manager struct {
	config         *config.ENIManagerConfig
	locksMutex     sync.RWMutex
	locks          map[string]*sync.Mutex
	circuitBreaker *CircuitBreaker
}

// NewManager creates a new DPDK manager instance
func NewManager(cfg *config.ENIManagerConfig) *Manager {
	return &Manager{
		config:         cfg,
		locks:          make(map[string]*sync.Mutex),
		circuitBreaker: NewCircuitBreaker(DefaultCircuitBreakerConfig()),
	}
}

// BindingRequest represents a DPDK binding request
type BindingRequest struct {
	PCIAddress    string
	Driver        string
	NodeENIName   string
	ENIID         string
	InterfaceName string
	ResourceName  string
}

// BindingStatus represents the status of a DPDK binding
type BindingStatus struct {
	IsBound      bool
	Driver       string
	PCIAddress   string
	ResourceName string
	Error        error
}

// IsPCIDeviceBoundToDPDK checks if a PCI device is bound to a DPDK driver
func (m *Manager) IsPCIDeviceBoundToDPDK(pciAddress string, driver string) (bool, error) {
	// Check if the device exists
	pciDevPath := fmt.Sprintf("/sys/bus/pci/devices/%s", pciAddress)
	if _, err := os.Stat(pciDevPath); os.IsNotExist(err) {
		return false, fmt.Errorf("PCI device %s does not exist", pciAddress)
	}

	// Check the current driver
	driverPath := fmt.Sprintf("%s/driver", pciDevPath)
	if _, err := os.Lstat(driverPath); os.IsNotExist(err) {
		// No driver bound
		return false, nil
	}

	// Read the driver symlink
	driverLink, err := os.Readlink(driverPath)
	if err != nil {
		return false, fmt.Errorf("failed to read driver symlink for %s: %v", pciAddress, err)
	}

	currentDriver := filepath.Base(driverLink)
	log.Printf("PCI device %s is bound to driver: %s", pciAddress, currentDriver)

	// Check if it's bound to the expected DPDK driver
	return currentDriver == driver, nil
}

// BindPCIDeviceToDPDK binds a PCI device to a DPDK driver
func (m *Manager) BindPCIDeviceToDPDK(pciAddress string, driver string) error {
	log.Printf("Binding PCI device %s to DPDK driver %s", pciAddress, driver)

	// Use circuit breaker for the binding operation
	return m.circuitBreaker.Execute(context.Background(), "bind", func() error {
		return m.bindPCIDeviceInternal(pciAddress, driver)
	})
}

// bindPCIDeviceInternal performs the actual PCI device binding
func (m *Manager) bindPCIDeviceInternal(pciAddress string, driver string) error {
	// Acquire lock for this PCI address
	unlock, err := m.acquireLock(pciAddress)
	if err != nil {
		return NewError("bind", pciAddress, driver,
			fmt.Errorf("failed to acquire lock: %v", err))
	}
	defer unlock()

	// Check if already bound to the correct driver
	isBound, err := m.IsPCIDeviceBoundToDPDK(pciAddress, driver)
	if err != nil {
		return NewError("bind", pciAddress, driver,
			fmt.Errorf("failed to check current binding: %v", err))
	}

	if isBound {
		log.Printf("PCI device %s is already bound to DPDK driver %s", pciAddress, driver)
		return nil
	}

	// Use dpdk-devbind.py to bind the device
	bindScript := m.config.DPDKBindingScript
	if bindScript == "" {
		bindScript = "/opt/dpdk/dpdk-devbind.py"
	}

	cmd := exec.Command("python3", bindScript, "--bind", driver, pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return NewBindingError(pciAddress, driver, "dpdk-devbind.py failed",
			fmt.Errorf("command failed: %v, output: %s", err, string(output)))
	}

	log.Printf("Successfully bound PCI device %s to DPDK driver %s", pciAddress, driver)
	return nil
}

// BindInterfaceToDPDK binds a network interface to a DPDK driver
func (m *Manager) BindInterfaceToDPDK(link vnetlink.Link, driver string) error {
	ifaceName := link.Attrs().Name
	log.Printf("Binding interface %s to DPDK driver %s", ifaceName, driver)

	// Get PCI address for the interface
	pciAddress, err := m.getPCIAddressForInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get PCI address for interface %s: %v", ifaceName, err)
	}

	// Bring down the interface before binding
	if err := vnetlink.LinkSetDown(link); err != nil {
		log.Printf("Warning: failed to bring down interface %s: %v", ifaceName, err)
	}

	// Bind the PCI device
	if err := m.BindPCIDeviceToDPDK(pciAddress, driver); err != nil {
		return fmt.Errorf("failed to bind PCI device %s to DPDK: %v", pciAddress, err)
	}

	// Store the binding information
	if err := m.storeBoundInterface(pciAddress, driver, ifaceName); err != nil {
		log.Printf("Warning: failed to store DPDK bound interface info: %v", err)
	}

	return nil
}

// UnbindInterfaceFromDPDK unbinds an interface from DPDK
func (m *Manager) UnbindInterfaceFromDPDK(ifaceName string) error {
	log.Printf("Unbinding interface %s from DPDK", ifaceName)

	// Find the PCI address for this interface
	pciAddress, err := m.findPCIAddressForInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to find PCI address for interface %s: %v", ifaceName, err)
	}

	// Use dpdk-devbind.py to unbind
	bindScript := m.config.DPDKBindingScript
	if bindScript == "" {
		bindScript = "/opt/dpdk/dpdk-devbind.py"
	}

	cmd := exec.Command("python3", bindScript, "--unbind", pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to unbind interface %s (PCI: %s): %v, output: %s",
			ifaceName, pciAddress, err, string(output))
	}

	// Remove from bound interfaces map
	delete(m.config.DPDKBoundInterfaces, pciAddress)

	log.Printf("Successfully unbound interface %s from DPDK", ifaceName)
	return nil
}

// BoundInterface represents a DPDK bound interface
type BoundInterface struct {
	PCIAddress    string
	Driver        string
	NodeENIName   string
	ENIID         string
	InterfaceName string
	BoundAt       time.Time
}

// GetBoundInterfaces returns a map of currently bound DPDK interfaces
func (m *Manager) GetBoundInterfaces() map[string]BoundInterface {
	result := make(map[string]BoundInterface)
	for pciAddr, boundInterface := range m.config.DPDKBoundInterfaces {
		result[pciAddr] = BoundInterface{
			PCIAddress:    boundInterface.PCIAddress,
			Driver:        boundInterface.Driver,
			NodeENIName:   boundInterface.NodeENIName,
			ENIID:         boundInterface.ENIID,
			InterfaceName: boundInterface.IfaceName,
		}
	}
	return result
}

// IsInterfaceBound checks if an interface is bound to DPDK
func (m *Manager) IsInterfaceBound(ifaceName string) bool {
	for _, boundInterface := range m.config.DPDKBoundInterfaces {
		if boundInterface.IfaceName == ifaceName {
			return true
		}
	}
	return false
}

// acquireLock acquires a lock for a specific PCI address
func (m *Manager) acquireLock(pciAddress string) (func(), error) {
	m.locksMutex.Lock()
	defer m.locksMutex.Unlock()

	if _, exists := m.locks[pciAddress]; !exists {
		m.locks[pciAddress] = &sync.Mutex{}
	}

	lock := m.locks[pciAddress]

	// Try to acquire the lock with timeout
	done := make(chan struct{})
	go func() {
		lock.Lock()
		close(done)
	}()

	select {
	case <-done:
		return func() { lock.Unlock() }, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timeout acquiring lock for PCI address %s", pciAddress)
	}
}

// getPCIAddressForInterface gets the PCI address for a network interface
func (m *Manager) getPCIAddressForInterface(ifaceName string) (string, error) {
	// Read the PCI address from sysfs
	pciPath := fmt.Sprintf("/sys/class/net/%s/device/uevent", ifaceName)
	data, err := os.ReadFile(pciPath)
	if err != nil {
		return "", fmt.Errorf("failed to read PCI info for interface %s: %v", ifaceName, err)
	}

	// Parse the PCI address from uevent
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
			return strings.TrimPrefix(line, "PCI_SLOT_NAME="), nil
		}
	}

	return "", fmt.Errorf("PCI address not found for interface %s", ifaceName)
}

// findPCIAddressForInterface finds PCI address for interface from bound interfaces map
func (m *Manager) findPCIAddressForInterface(ifaceName string) (string, error) {
	for pciAddr, boundInterface := range m.config.DPDKBoundInterfaces {
		if boundInterface.IfaceName == ifaceName {
			return pciAddr, nil
		}
	}
	return "", fmt.Errorf("PCI address not found for interface %s in bound interfaces", ifaceName)
}

// storeBoundInterface stores information about a bound DPDK interface
func (m *Manager) storeBoundInterface(pciAddress, driver, ifaceName string) error {
	if m.config.DPDKBoundInterfaces == nil {
		m.config.DPDKBoundInterfaces = make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		})
	}

	m.config.DPDKBoundInterfaces[pciAddress] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress: pciAddress,
		Driver:     driver,
		IfaceName:  ifaceName,
	}

	log.Printf("Stored DPDK bound interface: %s (PCI: %s, driver: %s)",
		ifaceName, pciAddress, driver)
	return nil
}

// GetManagerStatus returns the current status of the DPDK manager
func (m *Manager) GetManagerStatus() map[string]interface{} {
	status := map[string]interface{}{
		"bound_interfaces_count": len(m.config.DPDKBoundInterfaces),
		"circuit_breaker":        m.circuitBreaker.GetStats(),
		"active_locks":           len(m.locks),
	}

	// Add bound interfaces details
	boundInterfaces := make([]map[string]interface{}, 0, len(m.config.DPDKBoundInterfaces))
	for pciAddr, boundInterface := range m.config.DPDKBoundInterfaces {
		boundInterfaces = append(boundInterfaces, map[string]interface{}{
			"pci_address":    pciAddr,
			"driver":         boundInterface.Driver,
			"interface_name": boundInterface.IfaceName,
			"node_eni_name":  boundInterface.NodeENIName,
			"eni_id":         boundInterface.ENIID,
		})
	}
	status["bound_interfaces"] = boundInterfaces

	return status
}

// ResetCircuitBreaker resets the circuit breaker to closed state
func (m *Manager) ResetCircuitBreaker() {
	m.circuitBreaker.Reset()
	log.Printf("DPDK manager circuit breaker has been reset")
}
