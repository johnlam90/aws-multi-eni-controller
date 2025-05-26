// Package testutil provides testing utilities for the ENI manager components
package testutil

import (
	"context"
	"fmt"
	"sync"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/network"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MockNetworkManager provides a mock implementation of the network manager
type MockNetworkManager struct {
	interfaces     []network.InterfaceInfo
	interfaceState map[string]string // interface name -> state
	interfaceMTU   map[string]int    // interface name -> MTU
	mutex          sync.RWMutex
	failOperations map[string]bool // operation -> should fail
}

// NewMockNetworkManager creates a new mock network manager
func NewMockNetworkManager() *MockNetworkManager {
	return &MockNetworkManager{
		interfaces:     make([]network.InterfaceInfo, 0),
		interfaceState: make(map[string]string),
		interfaceMTU:   make(map[string]int),
		failOperations: make(map[string]bool),
	}
}

// AddInterface adds a mock interface
func (m *MockNetworkManager) AddInterface(name string, state string, mtu int, isAWSENI bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	iface := network.InterfaceInfo{
		Name:     name,
		State:    state,
		MTU:      mtu,
		IsAWSENI: isAWSENI,
	}

	m.interfaces = append(m.interfaces, iface)
	m.interfaceState[name] = state
	m.interfaceMTU[name] = mtu
}

// SetOperationFailure sets whether an operation should fail
func (m *MockNetworkManager) SetOperationFailure(operation string, shouldFail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failOperations[operation] = shouldFail
}

// GetAllInterfaces returns all mock interfaces
func (m *MockNetworkManager) GetAllInterfaces() ([]network.InterfaceInfo, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.failOperations["get_interfaces"] {
		return nil, fmt.Errorf("mock error: get interfaces failed")
	}

	// Return a copy of the interfaces
	result := make([]network.InterfaceInfo, len(m.interfaces))
	copy(result, m.interfaces)
	return result, nil
}

// BringUpInterface mocks bringing up an interface
func (m *MockNetworkManager) BringUpInterface(ifaceName string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.failOperations["bring_up"] {
		return fmt.Errorf("mock error: bring up failed for %s", ifaceName)
	}

	m.interfaceState[ifaceName] = "UP"
	return nil
}

// BringDownInterface mocks bringing down an interface
func (m *MockNetworkManager) BringDownInterface(ifaceName string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.failOperations["bring_down"] {
		return fmt.Errorf("mock error: bring down failed for %s", ifaceName)
	}

	m.interfaceState[ifaceName] = "DOWN"
	return nil
}

// SetMTU mocks setting MTU
func (m *MockNetworkManager) SetMTU(ifaceName string, mtu int) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.failOperations["set_mtu"] {
		return fmt.Errorf("mock error: set MTU failed for %s", ifaceName)
	}

	m.interfaceMTU[ifaceName] = mtu
	return nil
}

// GetInterfaceState returns the current state of an interface
func (m *MockNetworkManager) GetInterfaceState(ifaceName string) string {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.interfaceState[ifaceName]
}

// GetInterfaceMTU returns the current MTU of an interface
func (m *MockNetworkManager) GetInterfaceMTU(ifaceName string) int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.interfaceMTU[ifaceName]
}

// MockDPDKManager provides a mock implementation of the DPDK manager
type MockDPDKManager struct {
	boundInterfaces map[string]MockDPDKBoundInterface
	mutex           sync.RWMutex
	failOperations  map[string]bool
}

// MockDPDKBoundInterface represents a mock DPDK bound interface
type MockDPDKBoundInterface struct {
	PCIAddress    string
	Driver        string
	NodeENIName   string
	ENIID         string
	InterfaceName string
	BoundAt       time.Time
}

// NewMockDPDKManager creates a new mock DPDK manager
func NewMockDPDKManager() *MockDPDKManager {
	return &MockDPDKManager{
		boundInterfaces: make(map[string]MockDPDKBoundInterface),
		failOperations:  make(map[string]bool),
	}
}

// SetOperationFailure sets whether an operation should fail
func (m *MockDPDKManager) SetOperationFailure(operation string, shouldFail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failOperations[operation] = shouldFail
}

// BindPCIDeviceToDPDK mocks binding a PCI device to DPDK
func (m *MockDPDKManager) BindPCIDeviceToDPDK(pciAddress string, driver string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.failOperations["bind"] {
		return fmt.Errorf("mock error: bind failed for PCI %s", pciAddress)
	}

	m.boundInterfaces[pciAddress] = MockDPDKBoundInterface{
		PCIAddress: pciAddress,
		Driver:     driver,
		BoundAt:    time.Now(),
	}

	return nil
}

// IsPCIDeviceBoundToDPDK mocks checking if a PCI device is bound to DPDK
func (m *MockDPDKManager) IsPCIDeviceBoundToDPDK(pciAddress string, driver string) (bool, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.failOperations["check_bound"] {
		return false, fmt.Errorf("mock error: check bound failed for PCI %s", pciAddress)
	}

	boundInterface, exists := m.boundInterfaces[pciAddress]
	if !exists {
		return false, nil
	}

	return boundInterface.Driver == driver, nil
}

// GetBoundInterfaces returns mock bound interfaces
func (m *MockDPDKManager) GetBoundInterfaces() map[string]MockDPDKBoundInterface {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make(map[string]MockDPDKBoundInterface)
	for k, v := range m.boundInterfaces {
		result[k] = v
	}
	return result
}

// MockKubernetesClient provides a mock implementation of the Kubernetes client
type MockKubernetesClient struct {
	nodeENIs       []networkingv1alpha1.NodeENI
	mutex          sync.RWMutex
	failOperations map[string]bool
}

// NewMockKubernetesClient creates a new mock Kubernetes client
func NewMockKubernetesClient() *MockKubernetesClient {
	return &MockKubernetesClient{
		nodeENIs:       make([]networkingv1alpha1.NodeENI, 0),
		failOperations: make(map[string]bool),
	}
}

// AddNodeENI adds a mock NodeENI resource
func (m *MockKubernetesClient) AddNodeENI(nodeENI networkingv1alpha1.NodeENI) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.nodeENIs = append(m.nodeENIs, nodeENI)
}

// SetOperationFailure sets whether an operation should fail
func (m *MockKubernetesClient) SetOperationFailure(operation string, shouldFail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.failOperations[operation] = shouldFail
}

// GetNodeENIResources returns mock NodeENI resources
func (m *MockKubernetesClient) GetNodeENIResources(ctx context.Context, nodeName string) ([]networkingv1alpha1.NodeENI, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if m.failOperations["get_nodeenis"] {
		return nil, fmt.Errorf("mock error: get NodeENI resources failed")
	}

	// Filter by node name (simplified logic)
	var result []networkingv1alpha1.NodeENI
	for _, nodeENI := range m.nodeENIs {
		// Simple check - in real implementation this would use node selector logic
		if len(nodeENI.Spec.NodeSelector) > 0 {
			for _, value := range nodeENI.Spec.NodeSelector {
				if value == nodeName {
					result = append(result, nodeENI)
					break
				}
			}
		}
	}

	return result, nil
}

// TestConfig provides a test configuration
func TestConfig() *config.ENIManagerConfig {
	cfg := config.DefaultENIManagerConfig()
	cfg.NodeName = "test-node"
	cfg.CheckInterval = 1 * time.Second
	cfg.DebugMode = true
	cfg.EnableDPDK = true
	cfg.DefaultDPDKDriver = "vfio-pci"
	cfg.DPDKBindingScript = "/opt/dpdk/dpdk-devbind.py"
	cfg.SRIOVDPConfigPath = "/tmp/test-sriov-config.json"
	return cfg
}

// CreateTestNodeENI creates a test NodeENI resource
func CreateTestNodeENI(name, nodeName string, enableDPDK bool) networkingv1alpha1.NodeENI {
	nodeENI := networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			SubnetID:   "subnet-12345",
			EnableDPDK: enableDPDK,
			DPDKDriver: "vfio-pci",
			MTU:        9000,
		},
	}

	return nodeENI
}
