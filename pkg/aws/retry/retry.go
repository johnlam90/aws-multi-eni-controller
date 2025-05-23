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

// ErrorCategorizer categorizes errors as retryable or not
type ErrorCategorizer func(error) bool

// DefaultRetryableErrors checks if an error is a retryable AWS error
func DefaultRetryableErrors(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()
	return strings.Contains(errMsg, "RequestLimitExceeded") ||
		strings.Contains(errMsg, "Throttling") ||
		strings.Contains(errMsg, "rate limit")
}

// WithExponentialBackoff retries a function with exponential backoff
func WithExponentialBackoff(
	ctx context.Context,
	logger logr.Logger,
	operation string,
	fn RetryableFunc,
	isRetryable ErrorCategorizer,
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
