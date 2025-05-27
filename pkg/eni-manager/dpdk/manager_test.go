package dpdk

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

func TestNewManager(t *testing.T) {
	cfg := &config.ENIManagerConfig{
		EnableDPDK:        true,
		DefaultDPDKDriver: "vfio-pci",
	}

	manager := NewManager(cfg)

	if manager == nil {
		t.Fatal("NewManager returned nil")
	}

	if manager.config != cfg {
		t.Error("Manager config not set correctly")
	}

	if manager.locks == nil {
		t.Error("Manager locks map not initialized")
	}

	if manager.circuitBreaker == nil {
		t.Error("Manager circuit breaker not initialized")
	}
}

func TestCircuitBreaker(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 2
	config.SuccessThreshold = 1
	config.Timeout = 100 * time.Millisecond

	cb := NewCircuitBreaker(config)

	// Test initial state
	if cb.GetState() != StateClosed {
		t.Errorf("Expected initial state to be closed, got %v", cb.GetState())
	}

	// Test failure recording
	cb.recordResult(false)
	if cb.GetState() != StateClosed {
		t.Errorf("Expected state to remain closed after 1 failure, got %v", cb.GetState())
	}

	cb.recordResult(false)
	if cb.GetState() != StateOpen {
		t.Errorf("Expected state to be open after 2 failures, got %v", cb.GetState())
	}

	// Test that execution is blocked when open
	if cb.canExecute() {
		t.Error("Expected canExecute to return false when circuit is open")
	}

	// Wait for timeout and test half-open state
	time.Sleep(150 * time.Millisecond)
	if !cb.canExecute() {
		t.Error("Expected canExecute to return true after timeout")
	}

	if cb.GetState() != StateHalfOpen {
		t.Errorf("Expected state to be half-open after timeout, got %v", cb.GetState())
	}

	// Test success in half-open state closes the circuit
	cb.recordResult(true)
	if cb.GetState() != StateClosed {
		t.Errorf("Expected state to be closed after success in half-open, got %v", cb.GetState())
	}
}

func TestCircuitBreakerExecute(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	cb := NewCircuitBreaker(config)

	// Test successful execution
	err := cb.Execute(context.Background(), "test", func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected successful execution, got error: %v", err)
	}

	// Test failed execution that opens circuit
	err = cb.Execute(context.Background(), "test", func() error {
		return fmt.Errorf("test error")
	})
	if err == nil {
		t.Error("Expected failed execution to return error")
	}

	// Test that circuit is now open
	err = cb.Execute(context.Background(), "test", func() error {
		return nil
	})
	if err == nil {
		t.Error("Expected execution to fail when circuit is open")
	}
}

func TestDPDKErrors(t *testing.T) {
	// Test Error
	originalErr := fmt.Errorf("original error")
	dpdkErr := NewError("bind", "0000:00:06.0", "vfio-pci", originalErr)

	if dpdkErr.Operation != "bind" {
		t.Errorf("Expected operation 'bind', got '%s'", dpdkErr.Operation)
	}

	if dpdkErr.PCIAddr != "0000:00:06.0" {
		t.Errorf("Expected PCI address '0000:00:06.0', got '%s'", dpdkErr.PCIAddr)
	}

	if dpdkErr.Unwrap() != originalErr {
		t.Error("Expected Unwrap to return original error")
	}

	if !dpdkErr.IsRetryable() {
		t.Error("Expected bind operation to be retryable")
	}

	// Test BindingError
	bindingErr := NewBindingError("0000:00:06.0", "vfio-pci", "device busy", originalErr)
	if bindingErr.PCIAddr != "0000:00:06.0" {
		t.Errorf("Expected PCI address '0000:00:06.0', got '%s'", bindingErr.PCIAddr)
	}

	// Test ValidationError
	validationErr := NewValidationError("pci_address", "invalid", "wrong format")
	if validationErr.Field != "pci_address" {
		t.Errorf("Expected field 'pci_address', got '%s'", validationErr.Field)
	}

	// Test TimeoutError
	timeoutErr := NewTimeoutError("bind", 30*time.Second)
	if timeoutErr.Operation != "bind" {
		t.Errorf("Expected operation 'bind', got '%s'", timeoutErr.Operation)
	}
}

func TestManagerStatus(t *testing.T) {
	cfg := &config.ENIManagerConfig{
		EnableDPDK: true,
		DPDKBoundInterfaces: make(map[string]struct {
			PCIAddress  string
			Driver      string
			NodeENIName string
			ENIID       string
			IfaceName   string
		}),
	}

	// Add a test bound interface
	cfg.DPDKBoundInterfaces["0000:00:06.0"] = struct {
		PCIAddress  string
		Driver      string
		NodeENIName string
		ENIID       string
		IfaceName   string
	}{
		PCIAddress:  "0000:00:06.0",
		Driver:      "vfio-pci",
		NodeENIName: "test-nodeeni",
		ENIID:       "eni-12345",
		IfaceName:   "eth1",
	}

	manager := NewManager(cfg)
	status := manager.GetManagerStatus()

	if status["bound_interfaces_count"] != 1 {
		t.Errorf("Expected bound_interfaces_count to be 1, got %v", status["bound_interfaces_count"])
	}

	if status["active_locks"] != 0 {
		t.Errorf("Expected active_locks to be 0, got %v", status["active_locks"])
	}

	// Check circuit breaker stats
	cbStats, ok := status["circuit_breaker"].(map[string]interface{})
	if !ok {
		t.Error("Expected circuit_breaker to be a map")
	} else {
		if cbStats["state"] != "closed" {
			t.Errorf("Expected circuit breaker state to be 'closed', got %v", cbStats["state"])
		}
	}

	// Check bound interfaces details
	boundInterfaces, ok := status["bound_interfaces"].([]map[string]interface{})
	if !ok {
		t.Error("Expected bound_interfaces to be a slice of maps")
	} else if len(boundInterfaces) != 1 {
		t.Errorf("Expected 1 bound interface, got %d", len(boundInterfaces))
	} else {
		iface := boundInterfaces[0]
		if iface["pci_address"] != "0000:00:06.0" {
			t.Errorf("Expected PCI address '0000:00:06.0', got %v", iface["pci_address"])
		}
		if iface["driver"] != "vfio-pci" {
			t.Errorf("Expected driver 'vfio-pci', got %v", iface["driver"])
		}
	}
}

func TestCircuitBreakerReset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	cb := NewCircuitBreaker(config)

	// Force circuit to open
	cb.recordResult(false)
	if cb.GetState() != StateOpen {
		t.Errorf("Expected state to be open, got %v", cb.GetState())
	}

	// Reset circuit breaker
	cb.Reset()
	if cb.GetState() != StateClosed {
		t.Errorf("Expected state to be closed after reset, got %v", cb.GetState())
	}

	// Test that execution works after reset
	if !cb.canExecute() {
		t.Error("Expected canExecute to return true after reset")
	}
}

func TestCircuitBreakerStats(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	cb := NewCircuitBreaker(config)

	stats := cb.GetStats()

	expectedFields := []string{"state", "failures", "successes", "last_fail_time", "failure_threshold", "success_threshold", "timeout"}
	for _, field := range expectedFields {
		if _, exists := stats[field]; !exists {
			t.Errorf("Expected stats to contain field '%s'", field)
		}
	}

	if stats["state"] != "closed" {
		t.Errorf("Expected initial state to be 'closed', got %v", stats["state"])
	}

	if stats["failures"] != 0 {
		t.Errorf("Expected initial failures to be 0, got %v", stats["failures"])
	}
}
