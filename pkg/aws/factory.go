package aws

import (
	"context"
	"os"

	"github.com/go-logr/logr"
)

// CreateEC2Client creates a new EC2 client
// This factory function returns an EC2Interface, allowing for easy mocking in tests
func CreateEC2Client(ctx context.Context, region string, logger logr.Logger) (EC2Interface, error) {
	// Check if we should use the new facade implementation
	if os.Getenv("USE_EC2_FACADE") == "true" {
		return NewEC2ClientFacade(ctx, region, logger)
	}
	// Default to the original implementation for backward compatibility
	return NewEC2Client(ctx, region, logger)
}

// CreateMockEC2Client creates a new mock EC2 client for testing
func CreateMockEC2Client() EC2Interface {
	return NewMockEC2Client()
}
