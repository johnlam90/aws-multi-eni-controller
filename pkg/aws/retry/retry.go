// Package retry provides utilities for retrying AWS API calls with exponential backoff.
package retry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
)

// RetryableFunc is a function that can be retried
type RetryableFunc func() (bool, error)

// ErrorCategory represents different categories of errors
type ErrorCategory int

const (
	// ErrorCategoryNonRetryable - errors that should not be retried
	ErrorCategoryNonRetryable ErrorCategory = iota
	// ErrorCategoryThrottling - rate limiting errors (long backoff)
	ErrorCategoryThrottling
	// ErrorCategoryTransient - temporary errors (medium backoff)
	ErrorCategoryTransient
	// ErrorCategoryNetwork - network-related errors (short backoff)
	ErrorCategoryNetwork
	// ErrorCategoryResource - resource-related errors (medium backoff)
	ErrorCategoryResource
)

// ErrorCategorizer categorizes errors into different categories
type ErrorCategorizer func(error) ErrorCategory

// LegacyErrorCategorizer categorizes errors as retryable or not (for backward compatibility)
type LegacyErrorCategorizer func(error) bool

// RetryStrategy defines retry behavior for different error categories
type RetryStrategy struct {
	Category    ErrorCategory
	Backoff     wait.Backoff
	MaxAttempts int
}

// DefaultRetryableErrors checks if an error is a retryable AWS error (legacy function)
func DefaultRetryableErrors(err error) bool {
	return CategorizeError(err) != ErrorCategoryNonRetryable
}

// CategorizeError categorizes an AWS error into appropriate retry category
func CategorizeError(err error) ErrorCategory {
	if err == nil {
		return ErrorCategoryNonRetryable
	}

	errMsg := strings.ToLower(err.Error())

	// Throttling errors - need longer backoff
	if strings.Contains(errMsg, "requestlimitexceeded") ||
		strings.Contains(errMsg, "throttling") ||
		strings.Contains(errMsg, "rate limit") ||
		strings.Contains(errMsg, "too many requests") ||
		strings.Contains(errMsg, "slowdown") {
		return ErrorCategoryThrottling
	}

	// Resource errors - check first to avoid conflicts with network errors
	if strings.Contains(errMsg, "invalidnetworkinterfaceid.notfound") ||
		strings.Contains(errMsg, "invalidinstanceid.notfound") ||
		strings.Contains(errMsg, "invalidsubnetid.notfound") ||
		strings.Contains(errMsg, "invalidgroupid.notfound") ||
		strings.Contains(errMsg, "resource not found") ||
		strings.Contains(errMsg, "does not exist") {
		return ErrorCategoryResource
	}

	// Network errors - can retry quickly (more specific checks to avoid conflicts)
	if strings.Contains(errMsg, "connection timeout") ||
		strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "network unreachable") ||
		strings.Contains(errMsg, "dns") ||
		strings.Contains(errMsg, "host unreachable") ||
		strings.Contains(errMsg, "no route to host") {
		return ErrorCategoryNetwork
	}

	// Transient errors - medium backoff
	if strings.Contains(errMsg, "internal error") ||
		strings.Contains(errMsg, "service unavailable") ||
		strings.Contains(errMsg, "temporary failure") ||
		strings.Contains(errMsg, "try again") ||
		strings.Contains(errMsg, "busy") ||
		strings.Contains(errMsg, "conflict") {
		return ErrorCategoryTransient
	}

	// Non-retryable errors
	if strings.Contains(errMsg, "access denied") ||
		strings.Contains(errMsg, "unauthorized") ||
		strings.Contains(errMsg, "forbidden") ||
		strings.Contains(errMsg, "invalid parameter") ||
		strings.Contains(errMsg, "malformed") ||
		strings.Contains(errMsg, "bad request") ||
		strings.Contains(errMsg, "validation") {
		return ErrorCategoryNonRetryable
	}

	// Default to transient for unknown errors (conservative approach)
	return ErrorCategoryTransient
}

// GetRetryStrategy returns the appropriate retry strategy for an error category
func GetRetryStrategy(category ErrorCategory) RetryStrategy {
	switch category {
	case ErrorCategoryThrottling:
		return RetryStrategy{
			Category: category,
			Backoff: wait.Backoff{
				Steps:    8,               // More attempts for throttling
				Duration: 2 * time.Second, // Longer initial delay
				Factor:   2.5,             // Aggressive backoff
				Jitter:   0.2,             // More jitter to spread load
			},
			MaxAttempts: 8,
		}
	case ErrorCategoryNetwork:
		return RetryStrategy{
			Category: category,
			Backoff: wait.Backoff{
				Steps:    6,
				Duration: 500 * time.Millisecond, // Quick retry for network issues
				Factor:   1.5,                    // Gentle backoff
				Jitter:   0.1,
			},
			MaxAttempts: 6,
		}
	case ErrorCategoryResource:
		return RetryStrategy{
			Category: category,
			Backoff: wait.Backoff{
				Steps:    4,
				Duration: 1 * time.Second, // Medium delay
				Factor:   2.0,
				Jitter:   0.1,
			},
			MaxAttempts: 4,
		}
	case ErrorCategoryTransient:
		return RetryStrategy{
			Category: category,
			Backoff: wait.Backoff{
				Steps:    5,
				Duration: 1 * time.Second, // Standard delay
				Factor:   2.0,
				Jitter:   0.1,
			},
			MaxAttempts: 5,
		}
	default: // ErrorCategoryNonRetryable
		return RetryStrategy{
			Category:    category,
			Backoff:     wait.Backoff{Steps: 1}, // No retry
			MaxAttempts: 1,
		}
	}
}

// WithCategorizedRetry retries a function with category-specific backoff strategies
func WithCategorizedRetry(
	ctx context.Context,
	logger logr.Logger,
	operation string,
	fn RetryableFunc,
) error {
	var lastErr error
	var lastCategory ErrorCategory

	err := wait.ExponentialBackoff(DefaultBackoff(), func() (bool, error) {
		done, err := fn()
		if err != nil {
			category := CategorizeError(err)
			lastCategory = category
			strategy := GetRetryStrategy(category)

			// Don't retry non-retryable errors
			if category == ErrorCategoryNonRetryable {
				logger.Info("Non-retryable error encountered, failing immediately",
					"operation", operation,
					"error", err.Error(),
					"category", getCategoryString(category))
				lastErr = err
				return false, err
			}

			logger.Info("Categorized error encountered, will retry with appropriate strategy",
				"operation", operation,
				"error", err.Error(),
				"category", getCategoryString(category),
				"maxAttempts", strategy.MaxAttempts,
				"backoffDuration", strategy.Backoff.Duration)

			lastErr = err
			return false, nil // Continue retrying
		}
		return done, nil
	})

	if err != nil {
		return fmt.Errorf("failed to %s after categorized retries (category: %s): %w",
			operation, getCategoryString(lastCategory), lastErr)
	}

	return nil
}

// getCategoryString returns a string representation of an error category
func getCategoryString(category ErrorCategory) string {
	switch category {
	case ErrorCategoryNonRetryable:
		return "non-retryable"
	case ErrorCategoryThrottling:
		return "throttling"
	case ErrorCategoryTransient:
		return "transient"
	case ErrorCategoryNetwork:
		return "network"
	case ErrorCategoryResource:
		return "resource"
	default:
		return "unknown"
	}
}

// WithExponentialBackoff retries a function with exponential backoff (legacy function)
func WithExponentialBackoff(
	ctx context.Context,
	logger logr.Logger,
	operation string,
	fn RetryableFunc,
	isRetryable LegacyErrorCategorizer,
	backoff wait.Backoff,
) error {
	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		done, err := fn()
		if err != nil {
			// Check if this is a retryable error
			if isRetryable(err) {
				logger.Info("Retryable error encountered, will retry",
					"operation", operation,
					"error", err.Error())
				lastErr = err
				return false, nil // Return nil error to continue retrying
			}

			// For other errors, fail immediately
			lastErr = err
			return false, err
		}
		return done, nil
	})

	if err != nil {
		return fmt.Errorf("failed to %s after retries: %w", operation, lastErr)
	}

	return nil
}

// DefaultBackoff returns the default backoff configuration
func DefaultBackoff() wait.Backoff {
	return wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}
}

// WithContext wraps a RetryableFunc with context cancellation
func WithContext(ctx context.Context, fn RetryableFunc) RetryableFunc {
	return func() (bool, error) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
			return fn()
		}
	}
}

// WithLogging wraps a RetryableFunc with logging
func WithLogging(logger logr.Logger, operation string, fn RetryableFunc) RetryableFunc {
	return func() (bool, error) {
		done, err := fn()
		if err != nil {
			logger.Error(err, "Operation failed", "operation", operation)
		} else if done {
			logger.V(1).Info("Operation completed successfully", "operation", operation)
		} else {
			logger.V(1).Info("Operation in progress", "operation", operation)
		}
		return done, err
	}
}
