# AWS API Retry Package

This package provides utilities for retrying AWS API calls with exponential backoff. It is designed to be used with the AWS SDK v2 for Go.

## Features

- **Exponential Backoff**: Automatically retries operations with exponential backoff
- **Error Categorization**: Categorizes errors as retryable or non-retryable
- **Context Support**: Respects context cancellation and deadlines
- **Logging**: Provides detailed logging of retry attempts

## Usage

### Basic Usage

```go
import (
    "context"
    "github.com/johnlam90/aws-multi-eni-controller/pkg/aws/retry"
    "github.com/go-logr/logr"
)

func ExampleFunction(ctx context.Context, logger logr.Logger) error {
    operation := "describe resource"
    
    err := retry.WithExponentialBackoff(
        ctx,
        logger,
        operation,
        func() (bool, error) {
            // Your AWS API call here
            result, err := awsClient.SomeOperation(ctx, input)
            if err != nil {
                return false, err
            }
            return true, nil
        },
        retry.DefaultRetryableErrors,
        retry.DefaultBackoff(),
    )
    
    return err
}
```

### Custom Error Categorization

```go
func CustomRetryableErrors(err error) bool {
    if err == nil {
        return false
    }
    
    errMsg := err.Error()
    return strings.Contains(errMsg, "RequestLimitExceeded") ||
        strings.Contains(errMsg, "Throttling") ||
        strings.Contains(errMsg, "ConnectionReset")
}

func ExampleWithCustomErrorCategorization(ctx context.Context, logger logr.Logger) error {
    operation := "custom operation"
    
    err := retry.WithExponentialBackoff(
        ctx,
        logger,
        operation,
        func() (bool, error) {
            // Your AWS API call here
            return true, nil
        },
        CustomRetryableErrors,
        retry.DefaultBackoff(),
    )
    
    return err
}
```

### Custom Backoff

```go
func ExampleWithCustomBackoff(ctx context.Context, logger logr.Logger) error {
    operation := "long-running operation"
    
    customBackoff := wait.Backoff{
        Steps:    10,
        Duration: 2 * time.Second,
        Factor:   1.5,
        Jitter:   0.2,
    }
    
    err := retry.WithExponentialBackoff(
        ctx,
        logger,
        operation,
        func() (bool, error) {
            // Your AWS API call here
            return true, nil
        },
        retry.DefaultRetryableErrors,
        customBackoff,
    )
    
    return err
}
```

## Helper Functions

- `WithContext`: Wraps a RetryableFunc with context cancellation
- `WithLogging`: Wraps a RetryableFunc with logging
- `DefaultBackoff`: Returns the default backoff configuration
- `DefaultRetryableErrors`: Checks if an error is a retryable AWS error
