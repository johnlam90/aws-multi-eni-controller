package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCategorizeError(t *testing.T) {
	tests := []struct {
		name             string
		error            error
		expectedCategory ErrorCategory
	}{
		// Throttling errors
		{
			name:             "RequestLimitExceeded",
			error:            errors.New("RequestLimitExceeded: Request rate exceeded"),
			expectedCategory: ErrorCategoryThrottling,
		},
		{
			name:             "Throttling",
			error:            errors.New("Throttling: Rate exceeded"),
			expectedCategory: ErrorCategoryThrottling,
		},
		{
			name:             "Rate limit",
			error:            errors.New("rate limit exceeded"),
			expectedCategory: ErrorCategoryThrottling,
		},
		{
			name:             "Too many requests",
			error:            errors.New("too many requests"),
			expectedCategory: ErrorCategoryThrottling,
		},

		// Network errors
		{
			name:             "Connection timeout",
			error:            errors.New("connection timeout"),
			expectedCategory: ErrorCategoryNetwork,
		},
		{
			name:             "Network unreachable",
			error:            errors.New("network unreachable"),
			expectedCategory: ErrorCategoryNetwork,
		},
		{
			name:             "Connection reset",
			error:            errors.New("connection reset by peer"),
			expectedCategory: ErrorCategoryNetwork,
		},
		{
			name:             "DNS error",
			error:            errors.New("DNS resolution failed"),
			expectedCategory: ErrorCategoryNetwork,
		},

		// Resource errors
		{
			name:             "InvalidNetworkInterfaceID.NotFound",
			error:            errors.New("InvalidNetworkInterfaceID.NotFound: The network interface 'eni-12345' does not exist"),
			expectedCategory: ErrorCategoryResource,
		},
		{
			name:             "InvalidInstanceID.NotFound",
			error:            errors.New("InvalidInstanceID.NotFound: The instance ID 'i-12345' does not exist"),
			expectedCategory: ErrorCategoryResource,
		},
		{
			name:             "Resource not found",
			error:            errors.New("resource not found"),
			expectedCategory: ErrorCategoryResource,
		},

		// Transient errors
		{
			name:             "Internal error",
			error:            errors.New("internal error occurred"),
			expectedCategory: ErrorCategoryTransient,
		},
		{
			name:             "Service unavailable",
			error:            errors.New("service unavailable"),
			expectedCategory: ErrorCategoryTransient,
		},
		{
			name:             "Temporary failure",
			error:            errors.New("temporary failure, try again"),
			expectedCategory: ErrorCategoryTransient,
		},

		// Non-retryable errors
		{
			name:             "Access denied",
			error:            errors.New("access denied"),
			expectedCategory: ErrorCategoryNonRetryable,
		},
		{
			name:             "Unauthorized",
			error:            errors.New("unauthorized access"),
			expectedCategory: ErrorCategoryNonRetryable,
		},
		{
			name:             "Invalid parameter",
			error:            errors.New("invalid parameter value"),
			expectedCategory: ErrorCategoryNonRetryable,
		},
		{
			name:             "Validation error",
			error:            errors.New("validation failed"),
			expectedCategory: ErrorCategoryNonRetryable,
		},

		// Unknown errors (should default to transient)
		{
			name:             "Unknown error",
			error:            errors.New("some unknown error"),
			expectedCategory: ErrorCategoryTransient,
		},

		// Nil error
		{
			name:             "Nil error",
			error:            nil,
			expectedCategory: ErrorCategoryNonRetryable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			category := CategorizeError(tt.error)
			if category != tt.expectedCategory {
				t.Errorf("Expected category %v for error '%v', got %v",
					tt.expectedCategory, tt.error, category)
			}
		})
	}
}

func TestGetRetryStrategy(t *testing.T) {
	tests := []struct {
		category         ErrorCategory
		expectedAttempts int
		minDuration      time.Duration
		maxDuration      time.Duration
	}{
		{
			category:         ErrorCategoryThrottling,
			expectedAttempts: 8,
			minDuration:      2 * time.Second,
			maxDuration:      5 * time.Second,
		},
		{
			category:         ErrorCategoryNetwork,
			expectedAttempts: 6,
			minDuration:      400 * time.Millisecond,
			maxDuration:      1 * time.Second,
		},
		{
			category:         ErrorCategoryResource,
			expectedAttempts: 4,
			minDuration:      800 * time.Millisecond,
			maxDuration:      2 * time.Second,
		},
		{
			category:         ErrorCategoryTransient,
			expectedAttempts: 5,
			minDuration:      800 * time.Millisecond,
			maxDuration:      2 * time.Second,
		},
		{
			category:         ErrorCategoryNonRetryable,
			expectedAttempts: 1,
			minDuration:      0,
			maxDuration:      1 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(getCategoryString(tt.category), func(t *testing.T) {
			strategy := GetRetryStrategy(tt.category)

			if strategy.MaxAttempts != tt.expectedAttempts {
				t.Errorf("Expected %d attempts for category %v, got %d",
					tt.expectedAttempts, tt.category, strategy.MaxAttempts)
			}

			if strategy.Backoff.Duration < tt.minDuration || strategy.Backoff.Duration > tt.maxDuration {
				t.Errorf("Expected duration between %v and %v for category %v, got %v",
					tt.minDuration, tt.maxDuration, tt.category, strategy.Backoff.Duration)
			}

			if strategy.Category != tt.category {
				t.Errorf("Expected category %v, got %v", tt.category, strategy.Category)
			}
		})
	}
}

func TestWithCategorizedRetry(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	ctx := context.Background()

	// Test successful operation
	t.Run("successful operation", func(t *testing.T) {
		callCount := 0
		err := WithCategorizedRetry(ctx, logger, "test-success", func() (bool, error) {
			callCount++
			return true, nil
		})

		if err != nil {
			t.Errorf("Expected successful operation, got error: %v", err)
		}

		if callCount != 1 {
			t.Errorf("Expected 1 call for successful operation, got %d", callCount)
		}
	})

	// Test non-retryable error
	t.Run("non-retryable error", func(t *testing.T) {
		callCount := 0
		err := WithCategorizedRetry(ctx, logger, "test-non-retryable", func() (bool, error) {
			callCount++
			return false, errors.New("access denied")
		})

		if err == nil {
			t.Error("Expected error for non-retryable operation")
		}

		if callCount != 1 {
			t.Errorf("Expected 1 call for non-retryable error, got %d", callCount)
		}
	})

	// Test retryable error that eventually succeeds
	t.Run("retryable error with eventual success", func(t *testing.T) {
		callCount := 0
		err := WithCategorizedRetry(ctx, logger, "test-retryable-success", func() (bool, error) {
			callCount++
			if callCount < 3 {
				return false, errors.New("network timeout") // Network error
			}
			return true, nil
		})

		if err != nil {
			t.Errorf("Expected eventual success, got error: %v", err)
		}

		if callCount != 3 {
			t.Errorf("Expected 3 calls for retryable error with success, got %d", callCount)
		}
	})

	// Test retryable error that keeps failing
	t.Run("retryable error that keeps failing", func(t *testing.T) {
		callCount := 0
		err := WithCategorizedRetry(ctx, logger, "test-retryable-fail", func() (bool, error) {
			callCount++
			return false, errors.New("temporary failure") // Transient error
		})

		if err == nil {
			t.Error("Expected error for consistently failing operation")
		}

		// Should retry multiple times based on default backoff
		if callCount < 2 {
			t.Errorf("Expected multiple calls for retryable error, got %d", callCount)
		}
	})
}

func TestGetCategoryString(t *testing.T) {
	tests := []struct {
		category ErrorCategory
		expected string
	}{
		{ErrorCategoryNonRetryable, "non-retryable"},
		{ErrorCategoryThrottling, "throttling"},
		{ErrorCategoryTransient, "transient"},
		{ErrorCategoryNetwork, "network"},
		{ErrorCategoryResource, "resource"},
		{ErrorCategory(999), "unknown"}, // Invalid category
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := getCategoryString(tt.category)
			if result != tt.expected {
				t.Errorf("Expected '%s' for category %v, got '%s'", tt.expected, tt.category, result)
			}
		})
	}
}

func TestDefaultRetryableErrors(t *testing.T) {
	// Test backward compatibility
	tests := []struct {
		name     string
		error    error
		expected bool
	}{
		{
			name:     "Throttling error should be retryable",
			error:    errors.New("RequestLimitExceeded"),
			expected: true,
		},
		{
			name:     "Access denied should not be retryable",
			error:    errors.New("access denied"),
			expected: false,
		},
		{
			name:     "Network error should be retryable",
			error:    errors.New("connection timeout"),
			expected: true,
		},
		{
			name:     "Nil error should not be retryable",
			error:    nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DefaultRetryableErrors(tt.error)
			if result != tt.expected {
				t.Errorf("Expected %v for error '%v', got %v", tt.expected, tt.error, result)
			}
		})
	}
}
