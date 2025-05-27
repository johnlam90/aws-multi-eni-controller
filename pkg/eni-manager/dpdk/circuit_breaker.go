package dpdk

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	// StateClosed means the circuit breaker is closed (normal operation)
	StateClosed CircuitBreakerState = iota
	// StateOpen means the circuit breaker is open (failing fast)
	StateOpen
	// StateHalfOpen means the circuit breaker is half-open (testing)
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig holds configuration for the circuit breaker
type CircuitBreakerConfig struct {
	FailureThreshold int           // Number of failures before opening
	SuccessThreshold int           // Number of successes before closing from half-open
	Timeout          time.Duration // Time to wait before trying half-open
}

// DefaultCircuitBreakerConfig returns default circuit breaker configuration
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 3,
		Timeout:          30 * time.Second,
	}
}

// CircuitBreaker implements the circuit breaker pattern for DPDK operations
type CircuitBreaker struct {
	config       CircuitBreakerConfig
	state        CircuitBreakerState
	failures     int
	successes    int
	lastFailTime time.Time
	mutex        sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		state:  StateClosed,
	}
}

// Execute executes a function with circuit breaker protection
func (cb *CircuitBreaker) Execute(ctx context.Context, operation string, fn func() error) error {
	// Check if we can execute
	if !cb.canExecute() {
		return fmt.Errorf("circuit breaker is open for operation %s", operation)
	}

	// Execute the function
	err := fn()

	// Record the result
	cb.recordResult(err == nil)

	return err
}

// canExecute checks if the circuit breaker allows execution
func (cb *CircuitBreaker) canExecute() bool {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Check if timeout has passed
		if time.Since(cb.lastFailTime) > cb.config.Timeout {
			// Transition to half-open
			cb.mutex.RUnlock()
			cb.mutex.Lock()
			if cb.state == StateOpen && time.Since(cb.lastFailTime) > cb.config.Timeout {
				cb.state = StateHalfOpen
				cb.successes = 0
			}
			cb.mutex.Unlock()
			cb.mutex.RLock()
			return cb.state == StateHalfOpen
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return false
	}
}

// recordResult records the result of an operation
func (cb *CircuitBreaker) recordResult(success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	if success {
		cb.failures = 0
		cb.successes++

		// If we're half-open and have enough successes, close the circuit
		if cb.state == StateHalfOpen && cb.successes >= cb.config.SuccessThreshold {
			cb.state = StateClosed
			cb.successes = 0
		}
	} else {
		cb.successes = 0
		cb.failures++
		cb.lastFailTime = time.Now()

		// If we have too many failures, open the circuit
		if cb.failures >= cb.config.FailureThreshold {
			cb.state = StateOpen
		}
	}
}

// GetState returns the current state of the circuit breaker
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state
}

// GetStats returns statistics about the circuit breaker
func (cb *CircuitBreaker) GetStats() map[string]interface{} {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	return map[string]interface{}{
		"state":             cb.state.String(),
		"failures":          cb.failures,
		"successes":         cb.successes,
		"last_fail_time":    cb.lastFailTime,
		"failure_threshold": cb.config.FailureThreshold,
		"success_threshold": cb.config.SuccessThreshold,
		"timeout":           cb.config.Timeout.String(),
	}
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.state = StateClosed
	cb.failures = 0
	cb.successes = 0
	cb.lastFailTime = time.Time{}
}
