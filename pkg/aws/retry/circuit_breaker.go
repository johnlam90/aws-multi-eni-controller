package retry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState int

const (
	// CircuitBreakerClosed - normal operation, requests are allowed
	CircuitBreakerClosed CircuitBreakerState = iota
	// CircuitBreakerOpen - circuit is open, requests are rejected
	CircuitBreakerOpen
	// CircuitBreakerHalfOpen - testing if the service has recovered
	CircuitBreakerHalfOpen
)

// CircuitBreakerConfig holds configuration for the circuit breaker
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures before opening the circuit
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successes needed to close the circuit from half-open
	SuccessThreshold int
	// Timeout is how long to wait before transitioning from open to half-open
	Timeout time.Duration
	// MaxConcurrentRequests is the maximum number of requests allowed in half-open state
	MaxConcurrentRequests int
}

// DefaultCircuitBreakerConfig returns a default circuit breaker configuration
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		FailureThreshold:      5,
		SuccessThreshold:      3,
		Timeout:               30 * time.Second,
		MaxConcurrentRequests: 1,
	}
}

// CircuitBreaker implements the circuit breaker pattern for AWS operations
type CircuitBreaker struct {
	config           *CircuitBreakerConfig
	state            CircuitBreakerState
	failureCount     int
	successCount     int
	lastFailureTime  time.Time
	requestCount     int
	mutex            sync.RWMutex
	logger           logr.Logger
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config *CircuitBreakerConfig, logger logr.Logger) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	return &CircuitBreaker{
		config: config,
		state:  CircuitBreakerClosed,
		logger: logger.WithName("circuit-breaker"),
	}
}

// Execute executes a function with circuit breaker protection
func (cb *CircuitBreaker) Execute(ctx context.Context, operation string, fn func() error) error {
	// Check if request is allowed
	if !cb.allowRequest() {
		cb.logger.Info("Circuit breaker is open, rejecting request", "operation", operation, "state", cb.getStateString())
		return fmt.Errorf("circuit breaker is open for operation: %s", operation)
	}

	// Execute the function
	err := fn()

	// Record the result
	cb.recordResult(err == nil)

	return err
}

// allowRequest checks if a request should be allowed based on circuit breaker state
func (cb *CircuitBreaker) allowRequest() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case CircuitBreakerClosed:
		return true
	case CircuitBreakerOpen:
		// Check if timeout has passed to transition to half-open
		if time.Since(cb.lastFailureTime) > cb.config.Timeout {
			cb.state = CircuitBreakerHalfOpen
			cb.requestCount = 0
			cb.successCount = 0
			cb.logger.Info("Circuit breaker transitioning to half-open", "timeout", cb.config.Timeout)
			return true
		}
		return false
	case CircuitBreakerHalfOpen:
		// Allow limited requests in half-open state
		if cb.requestCount < cb.config.MaxConcurrentRequests {
			cb.requestCount++
			return true
		}
		return false
	default:
		return false
	}
}

// recordResult records the result of an operation and updates circuit breaker state
func (cb *CircuitBreaker) recordResult(success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	if success {
		cb.onSuccess()
	} else {
		cb.onFailure()
	}
}

// onSuccess handles a successful operation
func (cb *CircuitBreaker) onSuccess() {
	cb.failureCount = 0

	switch cb.state {
	case CircuitBreakerHalfOpen:
		cb.successCount++
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.state = CircuitBreakerClosed
			cb.successCount = 0
			cb.requestCount = 0
			cb.logger.Info("Circuit breaker closed after successful recovery")
		}
	}
}

// onFailure handles a failed operation
func (cb *CircuitBreaker) onFailure() {
	cb.failureCount++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case CircuitBreakerClosed:
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.state = CircuitBreakerOpen
			cb.logger.Info("Circuit breaker opened due to consecutive failures", 
				"failureCount", cb.failureCount, 
				"threshold", cb.config.FailureThreshold)
		}
	case CircuitBreakerHalfOpen:
		// Any failure in half-open state immediately opens the circuit
		cb.state = CircuitBreakerOpen
		cb.successCount = 0
		cb.requestCount = 0
		cb.logger.Info("Circuit breaker opened due to failure in half-open state")
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
		"state":                cb.getStateString(),
		"failureCount":         cb.failureCount,
		"successCount":         cb.successCount,
		"requestCount":         cb.requestCount,
		"lastFailureTime":      cb.lastFailureTime,
		"failureThreshold":     cb.config.FailureThreshold,
		"successThreshold":     cb.config.SuccessThreshold,
		"timeout":              cb.config.Timeout,
		"maxConcurrentRequests": cb.config.MaxConcurrentRequests,
	}
}

// getStateString returns a string representation of the current state
func (cb *CircuitBreaker) getStateString() string {
	switch cb.state {
	case CircuitBreakerClosed:
		return "closed"
	case CircuitBreakerOpen:
		return "open"
	case CircuitBreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.state = CircuitBreakerClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.requestCount = 0
	cb.logger.Info("Circuit breaker manually reset to closed state")
}

// IsOperationAllowed checks if an operation would be allowed without executing it
func (cb *CircuitBreaker) IsOperationAllowed() bool {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()

	switch cb.state {
	case CircuitBreakerClosed:
		return true
	case CircuitBreakerOpen:
		return time.Since(cb.lastFailureTime) > cb.config.Timeout
	case CircuitBreakerHalfOpen:
		return cb.requestCount < cb.config.MaxConcurrentRequests
	default:
		return false
	}
}
