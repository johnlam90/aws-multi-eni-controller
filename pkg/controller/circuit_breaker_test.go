package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/johnlam90/aws-multi-eni-controller/pkg/aws/retry"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCircuitBreakerIntegration(t *testing.T) {
	// Create a circuit breaker with low thresholds for testing
	cbConfig := &retry.CircuitBreakerConfig{
		FailureThreshold:      2, // Open after 2 failures
		SuccessThreshold:      2, // Close after 2 successes
		Timeout:               100 * time.Millisecond,
		MaxConcurrentRequests: 1,
	}

	circuitBreaker := retry.NewCircuitBreaker(cbConfig, zap.New(zap.UseDevMode(true)))

	// Create reconciler with circuit breaker
	reconciler := &NodeENIReconciler{
		Log:            zap.New(zap.UseDevMode(true)),
		CircuitBreaker: circuitBreaker,
		Config: &config.ControllerConfig{
			CircuitBreakerEnabled: true,
		},
	}

	ctx := context.Background()

	// Test 1: Successful operations should work normally
	err := reconciler.executeWithCircuitBreaker(ctx, "test-success", func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected successful operation to work, got error: %v", err)
	}

	// Test 2: First failure should not open circuit
	err = reconciler.executeWithCircuitBreaker(ctx, "test-failure-1", func() error {
		return errors.New("simulated failure")
	})
	if err == nil {
		t.Error("Expected operation to fail")
	}

	// Circuit should still be closed
	if circuitBreaker.GetState() != retry.CircuitBreakerClosed {
		t.Errorf("Expected circuit to be closed after 1 failure, got state: %v", circuitBreaker.GetState())
	}

	// Test 3: Second failure should open circuit
	err = reconciler.executeWithCircuitBreaker(ctx, "test-failure-2", func() error {
		return errors.New("simulated failure")
	})
	if err == nil {
		t.Error("Expected operation to fail")
	}

	// Circuit should now be open
	if circuitBreaker.GetState() != retry.CircuitBreakerOpen {
		t.Errorf("Expected circuit to be open after 2 failures, got state: %v", circuitBreaker.GetState())
	}

	// Test 4: Operations should be rejected when circuit is open
	err = reconciler.executeWithCircuitBreaker(ctx, "test-rejected", func() error {
		return nil
	})
	if err == nil {
		t.Error("Expected operation to be rejected when circuit is open")
	}
	if !circuitBreaker.IsOperationAllowed() {
		// This is expected when circuit is open
	}

	// Test 5: Wait for timeout and test half-open state
	time.Sleep(150 * time.Millisecond) // Wait longer than timeout

	// Circuit should allow one request in half-open state
	if !circuitBreaker.IsOperationAllowed() {
		t.Error("Expected circuit to allow operation after timeout")
	}

	// Test 6: Successful operation in half-open should start recovery
	err = reconciler.executeWithCircuitBreaker(ctx, "test-recovery-1", func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected successful operation in half-open state, got error: %v", err)
	}

	// Test 7: Another successful operation should close the circuit
	err = reconciler.executeWithCircuitBreaker(ctx, "test-recovery-2", func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected successful operation to close circuit, got error: %v", err)
	}

	// Circuit should be closed again
	if circuitBreaker.GetState() != retry.CircuitBreakerClosed {
		t.Errorf("Expected circuit to be closed after recovery, got state: %v", circuitBreaker.GetState())
	}
}

func TestCircuitBreakerDisabled(t *testing.T) {
	// Create reconciler without circuit breaker
	reconciler := &NodeENIReconciler{
		Log:            zap.New(zap.UseDevMode(true)),
		CircuitBreaker: nil, // Disabled
		Config: &config.ControllerConfig{
			CircuitBreakerEnabled: false,
		},
	}

	ctx := context.Background()

	// Test that operations work normally when circuit breaker is disabled
	callCount := 0
	err := reconciler.executeWithCircuitBreaker(ctx, "test-disabled", func() error {
		callCount++
		if callCount <= 5 {
			return errors.New("simulated failure")
		}
		return nil
	})

	// Should eventually succeed after multiple failures (no circuit breaker protection)
	for i := 0; i < 10 && err != nil; i++ {
		err = reconciler.executeWithCircuitBreaker(ctx, "test-disabled", func() error {
			callCount++
			if callCount <= 5 {
				return errors.New("simulated failure")
			}
			return nil
		})
	}

	if err != nil {
		t.Errorf("Expected operation to eventually succeed when circuit breaker is disabled, got error: %v", err)
	}

	if callCount <= 5 {
		t.Errorf("Expected multiple attempts when circuit breaker is disabled, got %d calls", callCount)
	}
}

func TestCircuitBreakerStats(t *testing.T) {
	cbConfig := &retry.CircuitBreakerConfig{
		FailureThreshold:      3,
		SuccessThreshold:      2,
		Timeout:               1 * time.Second,
		MaxConcurrentRequests: 1,
	}

	circuitBreaker := retry.NewCircuitBreaker(cbConfig, zap.New(zap.UseDevMode(true)))

	// Test initial stats
	stats := circuitBreaker.GetStats()
	if stats["state"] != "closed" {
		t.Errorf("Expected initial state to be closed, got %v", stats["state"])
	}

	if stats["failureCount"] != 0 {
		t.Errorf("Expected initial failure count to be 0, got %v", stats["failureCount"])
	}

	// Cause some failures
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = circuitBreaker.Execute(ctx, "test", func() error {
			return errors.New("failure")
		})
	}

	// Check stats after failures
	stats = circuitBreaker.GetStats()
	if stats["state"] != "open" {
		t.Errorf("Expected state to be open after failures, got %v", stats["state"])
	}

	if stats["failureCount"] != 3 {
		t.Errorf("Expected failure count to be 3, got %v", stats["failureCount"])
	}

	// Test reset functionality
	circuitBreaker.Reset()
	stats = circuitBreaker.GetStats()
	if stats["state"] != "closed" {
		t.Errorf("Expected state to be closed after reset, got %v", stats["state"])
	}

	if stats["failureCount"] != 0 {
		t.Errorf("Expected failure count to be 0 after reset, got %v", stats["failureCount"])
	}
}

func TestCircuitBreakerConfiguration(t *testing.T) {
	// Test with different configurations
	configs := []*retry.CircuitBreakerConfig{
		{
			FailureThreshold:      1,
			SuccessThreshold:      1,
			Timeout:               10 * time.Millisecond,
			MaxConcurrentRequests: 1,
		},
		{
			FailureThreshold:      5,
			SuccessThreshold:      3,
			Timeout:               100 * time.Millisecond,
			MaxConcurrentRequests: 2,
		},
	}

	for i, config := range configs {
		t.Run(fmt.Sprintf("config-%d", i), func(t *testing.T) {
			cb := retry.NewCircuitBreaker(config, zap.New(zap.UseDevMode(true)))

			// Verify configuration is applied
			stats := cb.GetStats()
			if stats["failureThreshold"] != config.FailureThreshold {
				t.Errorf("Expected failure threshold %d, got %v", config.FailureThreshold, stats["failureThreshold"])
			}

			if stats["successThreshold"] != config.SuccessThreshold {
				t.Errorf("Expected success threshold %d, got %v", config.SuccessThreshold, stats["successThreshold"])
			}

			if stats["timeout"] != config.Timeout {
				t.Errorf("Expected timeout %v, got %v", config.Timeout, stats["timeout"])
			}
		})
	}
}
