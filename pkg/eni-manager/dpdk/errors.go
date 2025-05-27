package dpdk

import (
	"fmt"
	"time"
)

// DPDKError represents a DPDK-specific error
type DPDKError struct {
	Operation string
	PCIAddr   string
	Driver    string
	Err       error
	Timestamp time.Time
}

func (e *DPDKError) Error() string {
	return fmt.Sprintf("DPDK %s failed for PCI %s (driver: %s): %v",
		e.Operation, e.PCIAddr, e.Driver, e.Err)
}

func (e *DPDKError) Unwrap() error {
	return e.Err
}

// NewDPDKError creates a new DPDK error
func NewDPDKError(operation, pciAddr, driver string, err error) *DPDKError {
	return &DPDKError{
		Operation: operation,
		PCIAddr:   pciAddr,
		Driver:    driver,
		Err:       err,
		Timestamp: time.Now(),
	}
}

// IsRetryable determines if a DPDK error is retryable
func (e *DPDKError) IsRetryable() bool {
	// Define which operations/errors are retryable
	switch e.Operation {
	case "bind", "unbind":
		// Binding operations are generally retryable
		return true
	case "status_check":
		// Status checks are always retryable
		return true
	default:
		return false
	}
}

// BindingError represents a device binding error
type BindingError struct {
	PCIAddr string
	Driver  string
	Reason  string
	Err     error
}

func (e *BindingError) Error() string {
	return fmt.Sprintf("binding failed for PCI %s to driver %s: %s: %v",
		e.PCIAddr, e.Driver, e.Reason, e.Err)
}

func (e *BindingError) Unwrap() error {
	return e.Err
}

// NewBindingError creates a new binding error
func NewBindingError(pciAddr, driver, reason string, err error) *BindingError {
	return &BindingError{
		PCIAddr: pciAddr,
		Driver:  driver,
		Reason:  reason,
		Err:     err,
	}
}

// ValidationError represents a validation error
type ValidationError struct {
	Field  string
	Value  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for %s='%s': %s", e.Field, e.Value, e.Reason)
}

// NewValidationError creates a new validation error
func NewValidationError(field, value, reason string) *ValidationError {
	return &ValidationError{
		Field:  field,
		Value:  value,
		Reason: reason,
	}
}

// TimeoutError represents a timeout error
type TimeoutError struct {
	Operation string
	Duration  time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("operation %s timed out after %v", e.Operation, e.Duration)
}

// NewTimeoutError creates a new timeout error
func NewTimeoutError(operation string, duration time.Duration) *TimeoutError {
	return &TimeoutError{
		Operation: operation,
		Duration:  duration,
	}
}
