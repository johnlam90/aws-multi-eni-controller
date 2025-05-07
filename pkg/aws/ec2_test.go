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
