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

// Strategy defines retry behavior for different error categories
type Strategy struct {
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

	if isThrottlingError(errMsg) {
		return ErrorCategoryThrottling
	}

	if isResourceError(errMsg) {
		return ErrorCategoryResource
	}

	if isNetworkError(errMsg) {
		return ErrorCategoryNetwork
	}

	if isTransientError(errMsg) {
		return ErrorCategoryTransient
	}

	if isNonRetryableError(errMsg) {
		return ErrorCategoryNonRetryable
	}

	// Default to transient for unknown errors (conservative approach)
	return ErrorCategoryTransient
}

// isThrottlingError checks if the error is related to throttling
func isThrottlingError(errMsg string) bool {
	throttlingKeywords := []string{
		"requestlimitexceeded",
		"throttling",
		"rate limit",
		"too many requests",
		"slowdown",
	}
	return containsAny(errMsg, throttlingKeywords)
}

// isResourceError checks if the error is related to resource not found
func isResourceError(errMsg string) bool {
	resourceKeywords := []string{
		"invalidnetworkinterfaceid.notfound",
		"invalidinstanceid.notfound",
		"invalidsubnetid.notfound",
		"invalidgroupid.notfound",
		"resource not found",
		"does not exist",
	}
	return containsAny(errMsg, resourceKeywords)
}

// isNetworkError checks if the error is related to network connectivity
func isNetworkError(errMsg string) bool {
	networkKeywords := []string{
		"connection timeout",
		"connection refused",
		"connection reset",
		"network unreachable",
		"dns",
		"host unreachable",
		"no route to host",
	}
	return containsAny(errMsg, networkKeywords)
}

// isTransientError checks if the error is transient and should be retried
func isTransientError(errMsg string) bool {
	transientKeywords := []string{
		"internal error",
		"service unavailable",
		"temporary failure",
		"try again",
		"busy",
		"conflict",
	}
	return containsAny(errMsg, transientKeywords)
}

// isNonRetryableError checks if the error should not be retried
func isNonRetryableError(errMsg string) bool {
	nonRetryableKeywords := []string{
		"access denied",
		"unauthorized",
		"forbidden",
		"invalid parameter",
		"malformed",
		"bad request",
		"validation",
	}
	return containsAny(errMsg, nonRetryableKeywords)
}

// containsAny checks if the error message contains any of the given keywords
func containsAny(errMsg string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(errMsg, keyword) {
			return true
		}
	}
	return false
}

// GetRetryStrategy returns the appropriate retry strategy for an error category
func GetRetryStrategy(category ErrorCategory) Strategy {
	switch category {
	case ErrorCategoryThrottling:
		return Strategy{
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
		return Strategy{
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
		return Strategy{
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
		return Strategy{
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
		return Strategy{
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
