package aws

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
)

// TestNewEC2Client tests the creation of a new EC2 client
func TestNewEC2Client(t *testing.T) {
	// Skip this test in CI environments where AWS credentials might not be available
	t.Skip("Skipping test that requires AWS credentials")

	// Create a test logger
	logger := testr.New(t)

	// Test creating a new EC2 client
	client, err := NewEC2Client(context.Background(), "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected non-nil EC2 client")
	}

	if client.EC2 == nil {
		t.Fatal("Expected non-nil EC2 service client")
	}

	if client.Logger == (logr.Logger{}) {
		t.Fatal("Expected non-empty logger")
	}
}

// TestCreateEC2Client tests the factory function for creating an EC2 client
func TestCreateEC2Client(t *testing.T) {
	// Skip this test in CI environments where AWS credentials might not be available
	t.Skip("Skipping test that requires AWS credentials")

	// Create a test logger
	logger := testr.New(t)

	// Test creating a new EC2 client using the factory function
	client, err := CreateEC2Client(context.Background(), "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected non-nil EC2 client")
	}
}

// TestEC2Client_ErrorHandling tests error handling scenarios
func TestEC2Client_ErrorHandling(t *testing.T) {
	tests := []struct {
		name        string
		region      string
		expectError bool
	}{
		{
			name:        "valid region",
			region:      "us-east-1",
			expectError: false,
		},
		{
			name:        "empty region",
			region:      "",
			expectError: false, // AWS SDK handles this gracefully
		},
		{
			name:        "invalid region format",
			region:      "invalid-region-format",
			expectError: false, // AWS SDK handles this gracefully
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Skip("Skipping test that requires AWS credentials")

			logger := testr.New(t)
			client, err := NewEC2Client(context.Background(), tt.region, logger)
			if tt.expectError && err == nil {
				t.Errorf("Expected error for region %s, but got none", tt.region)
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error for region %s: %v", tt.region, err)
			}
			if !tt.expectError && client == nil {
				t.Errorf("Expected client for region %s, but got nil", tt.region)
			}
		})
	}
}

// TestEC2Client_ContextHandling tests context handling
func TestEC2Client_ContextHandling(t *testing.T) {
	t.Skip("Skipping test that requires AWS credentials")

	logger := testr.New(t)
	client, err := NewEC2Client(context.Background(), "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// This should handle the cancelled context gracefully
	_, err = client.DescribeInstance(ctx, "i-nonexistent")
	if err == nil {
		t.Error("Expected error with cancelled context")
	}
}

// TestEC2Client_InvalidInputs tests handling of invalid inputs
func TestEC2Client_InvalidInputs(t *testing.T) {
	t.Skip("Skipping test that requires AWS credentials")

	logger := testr.New(t)
	client, err := NewEC2Client(context.Background(), "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	ctx := context.Background()

	// Test with empty instance ID
	_, err = client.DescribeInstance(ctx, "")
	if err == nil {
		t.Error("Expected error with empty instance ID")
	}

	// Test with invalid instance ID format
	_, err = client.DescribeInstance(ctx, "invalid-instance-id")
	if err == nil {
		t.Error("Expected error with invalid instance ID format")
	}

	// Test with empty ENI ID
	_, err = client.DescribeENI(ctx, "")
	if err == nil {
		t.Error("Expected error with empty ENI ID")
	}
}
