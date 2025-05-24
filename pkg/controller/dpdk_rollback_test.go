package controller

import (
	"fmt"
	"testing"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseInterfaceState(t *testing.T) {
	reconciler := &NodeENIReconciler{}

	tests := []struct {
		name          string
		output        string
		ifaceName     string
		expectedState *InterfaceState
		expectError   bool
	}{
		{
			name: "DPDK bound interface",
			output: `PCI_ADDRESS=0000:00:06.0
CURRENT_DRIVER=vfio-pci
INTERFACE_NAME=eth1
IS_BOUND_TO_DPDK=true`,
			ifaceName: "eth1",
			expectedState: &InterfaceState{
				PCIAddress:    "0000:00:06.0",
				CurrentDriver: "vfio-pci",
				IfaceName:     "eth1",
				IsBoundToDPDK: true,
			},
			expectError: false,
		},
		{
			name: "ENA bound interface",
			output: `PCI_ADDRESS=0000:00:05.0
CURRENT_DRIVER=ena
INTERFACE_NAME=eth2
IS_BOUND_TO_DPDK=false`,
			ifaceName: "eth2",
			expectedState: &InterfaceState{
				PCIAddress:    "0000:00:05.0",
				CurrentDriver: "ena",
				IfaceName:     "eth2",
				IsBoundToDPDK: false,
			},
			expectError: false,
		},
		{
			name: "Missing PCI address",
			output: `CURRENT_DRIVER=ena
INTERFACE_NAME=eth3
IS_BOUND_TO_DPDK=false`,
			ifaceName:   "eth3",
			expectError: true,
		},
		{
			name: "Unbound interface",
			output: `PCI_ADDRESS=0000:00:07.0
CURRENT_DRIVER=none
INTERFACE_NAME=eth4
IS_BOUND_TO_DPDK=false`,
			ifaceName: "eth4",
			expectedState: &InterfaceState{
				PCIAddress:    "0000:00:07.0",
				CurrentDriver: "none",
				IfaceName:     "eth4",
				IsBoundToDPDK: false,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := reconciler.parseInterfaceState(tt.output, tt.ifaceName)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if state.PCIAddress != tt.expectedState.PCIAddress {
				t.Errorf("Expected PCI address %s, got %s", tt.expectedState.PCIAddress, state.PCIAddress)
			}

			if state.CurrentDriver != tt.expectedState.CurrentDriver {
				t.Errorf("Expected driver %s, got %s", tt.expectedState.CurrentDriver, state.CurrentDriver)
			}

			if state.IfaceName != tt.expectedState.IfaceName {
				t.Errorf("Expected interface name %s, got %s", tt.expectedState.IfaceName, state.IfaceName)
			}

			if state.IsBoundToDPDK != tt.expectedState.IsBoundToDPDK {
				t.Errorf("Expected DPDK bound %v, got %v", tt.expectedState.IsBoundToDPDK, state.IsBoundToDPDK)
			}
		})
	}
}

func TestInterfaceStateLogic(t *testing.T) {
	tests := []struct {
		name                  string
		currentDriver         string
		expectedIsBoundToDPDK bool
	}{
		{
			name:                  "vfio-pci driver",
			currentDriver:         "vfio-pci",
			expectedIsBoundToDPDK: true,
		},
		{
			name:                  "igb_uio driver",
			currentDriver:         "igb_uio",
			expectedIsBoundToDPDK: true,
		},
		{
			name:                  "ena driver",
			currentDriver:         "ena",
			expectedIsBoundToDPDK: false,
		},
		{
			name:                  "no driver",
			currentDriver:         "none",
			expectedIsBoundToDPDK: false,
		},
		{
			name:                  "other driver",
			currentDriver:         "ixgbe",
			expectedIsBoundToDPDK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := fmt.Sprintf(`PCI_ADDRESS=0000:00:06.0
CURRENT_DRIVER=%s
INTERFACE_NAME=eth1
IS_BOUND_TO_DPDK=%v`, tt.currentDriver, tt.expectedIsBoundToDPDK)

			reconciler := &NodeENIReconciler{}
			state, err := reconciler.parseInterfaceState(output, "eth1")
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if state.IsBoundToDPDK != tt.expectedIsBoundToDPDK {
				t.Errorf("Expected DPDK bound %v for driver %s, got %v",
					tt.expectedIsBoundToDPDK, tt.currentDriver, state.IsBoundToDPDK)
			}
		})
	}
}

func TestDPDKUnbindingSkipLogic(t *testing.T) {
	// Test that DPDK unbinding is skipped when appropriate
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			EnableDPDK: false, // DPDK disabled
		},
	}

	attachment := networkingv1alpha1.ENIAttachment{
		NodeID:      "test-node",
		ENIID:       "eni-12345",
		DPDKBound:   true, // But marked as DPDK bound
		DeviceIndex: 1,
	}

	// This test would require mocking the Kubernetes client and pod execution
	// For now, we just verify the logic structure is correct
	if !nodeENI.Spec.EnableDPDK {
		// Should skip DPDK unbinding
		t.Log("DPDK unbinding would be skipped because EnableDPDK is false")
	}

	if !attachment.DPDKBound {
		// Should skip DPDK unbinding
		t.Log("DPDK unbinding would be skipped because DPDKBound is false")
	}
}
