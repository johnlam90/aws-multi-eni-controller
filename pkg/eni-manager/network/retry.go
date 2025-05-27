package network

import (
	"context"
	"fmt"
	"log"
	"time"
)

// RetryConfig holds configuration for retry operations
type RetryConfig struct {
	MaxAttempts int           // Maximum number of retry attempts
	BaseDelay   time.Duration // Base delay between retries
	MaxDelay    time.Duration // Maximum delay between retries
	Multiplier  float64       // Backoff multiplier
}

// DefaultRetryConfig returns default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    30 * time.Second,
		Multiplier:  2.0,
	}
}

// RetryableError represents an error that can be retried
type RetryableError struct {
	Err       error
	Retryable bool
}

func (e *RetryableError) Error() string {
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// NewRetryableError creates a new retryable error
func NewRetryableError(err error, retryable bool) *RetryableError {
	return &RetryableError{
		Err:       err,
		Retryable: retryable,
	}
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	if retryableErr, ok := err.(*RetryableError); ok {
		return retryableErr.Retryable
	}

	// Default: most network operations are retryable
	return true
}

// RetryWithBackoff executes a function with exponential backoff retry
func RetryWithBackoff(ctx context.Context, config RetryConfig, operation string, fn func() error) error {
	var lastErr error
	delay := config.BaseDelay

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// Execute the function
		err := fn()
		if err == nil {
			if attempt > 1 {
				log.Printf("Operation %s succeeded on attempt %d", operation, attempt)
			}
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !IsRetryable(err) {
			log.Printf("Operation %s failed with non-retryable error: %v", operation, err)
			return err
		}

		// Don't retry on last attempt
		if attempt == config.MaxAttempts {
			break
		}

		log.Printf("Operation %s failed on attempt %d/%d: %v, retrying in %v",
			operation, attempt, config.MaxAttempts, err, delay)

		// Wait with context cancellation support
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation %s cancelled: %v", operation, ctx.Err())
		case <-time.After(delay):
			// Continue to next attempt
		}

		// Calculate next delay with exponential backoff
		delay = time.Duration(float64(delay) * config.Multiplier)
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}
	}

	return fmt.Errorf("operation %s failed after %d attempts, last error: %v",
		operation, config.MaxAttempts, lastErr)
}

// RetryInterfaceOperation retries a network interface operation
func (m *Manager) RetryInterfaceOperation(ctx context.Context, operation string, ifaceName string, fn func() error) error {
	config := DefaultRetryConfig()

	// Customize retry config for specific operations
	switch operation {
	case "bring_up", "bring_down":
		config.MaxAttempts = 5
		config.BaseDelay = 500 * time.Millisecond
	case "set_mtu":
		config.MaxAttempts = 3
		config.BaseDelay = 1 * time.Second
	case "wait_for_interface":
		config.MaxAttempts = 10
		config.BaseDelay = 2 * time.Second
		config.MaxDelay = 10 * time.Second
	}

	operationWithInterface := fmt.Sprintf("%s(%s)", operation, ifaceName)
	return RetryWithBackoff(ctx, config, operationWithInterface, fn)
}

// BringUpInterfaceWithRetry brings up an interface with retry logic
func (m *Manager) BringUpInterfaceWithRetry(ctx context.Context, ifaceName string) error {
	return m.RetryInterfaceOperation(ctx, "bring_up", ifaceName, func() error {
		return m.BringUpInterface(ifaceName)
	})
}

// BringDownInterfaceWithRetry brings down an interface with retry logic
func (m *Manager) BringDownInterfaceWithRetry(ctx context.Context, ifaceName string) error {
	return m.RetryInterfaceOperation(ctx, "bring_down", ifaceName, func() error {
		return m.BringDownInterface(ifaceName)
	})
}

// SetMTUWithRetry sets MTU with retry logic
func (m *Manager) SetMTUWithRetry(ctx context.Context, ifaceName string, mtu int) error {
	return m.RetryInterfaceOperation(ctx, "set_mtu", ifaceName, func() error {
		return m.SetMTU(ifaceName, mtu)
	})
}

// WaitForInterfaceWithRetry waits for an interface to appear with retry logic
func (m *Manager) WaitForInterfaceWithRetry(ctx context.Context, ifaceName string, timeout time.Duration) error {
	return m.RetryInterfaceOperation(ctx, "wait_for_interface", ifaceName, func() error {
		return m.WaitForInterface(ifaceName, timeout)
	})
}

// ConfigureInterfaceWithRetry configures an interface with retry logic
func (m *Manager) ConfigureInterfaceWithRetry(ctx context.Context, ifaceName string, nodeENI interface{}) error {
	return m.RetryInterfaceOperation(ctx, "configure", ifaceName, func() error {
		// This would need to be updated to use the actual NodeENI type
		// For now, we'll just call the basic configuration
		return fmt.Errorf("configure interface with retry not implemented")
	})
}

// ValidateInterfaceWithRetry validates an interface configuration with retry logic
func (m *Manager) ValidateInterfaceWithRetry(ctx context.Context, ifaceName string) error {
	return m.RetryInterfaceOperation(ctx, "validate", ifaceName, func() error {
		// Get interface information
		interfaces, err := m.GetAllInterfaces()
		if err != nil {
			return NewRetryableError(fmt.Errorf("failed to get interfaces: %v", err), true)
		}

		// Find the interface
		for _, iface := range interfaces {
			if iface.Name == ifaceName {
				// Validate interface state
				if iface.State != "UP" {
					return NewRetryableError(fmt.Errorf("interface %s is not up (state: %s)", ifaceName, iface.State), true)
				}
				return nil
			}
		}

		return NewRetryableError(fmt.Errorf("interface %s not found", ifaceName), true)
	})
}
