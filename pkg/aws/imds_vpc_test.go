package aws

import (
	"context"
	"os"
	"testing"

	"github.com/go-logr/logr/testr"
)

// TestTryVPCWideConfiguration_EmptyVPCID verifies that tryVPCWideConfiguration
// rejects an empty VPC ID immediately without making any API calls.
func TestTryVPCWideConfiguration_EmptyVPCID(t *testing.T) {
	client := &EC2Client{
		Logger: testr.New(t),
	}

	err := client.tryVPCWideConfiguration(context.Background(), 2, "")
	if err == nil {
		t.Fatal("expected error when VPC ID is empty, got nil")
	}

	expected := "cannot perform VPC-wide configuration without a VPC ID"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

// TestTryVPCWideConfiguration_AggressiveDisabled verifies that tryVPCWideConfiguration
// returns an error when aggressive configuration is disabled.
func TestTryVPCWideConfiguration_AggressiveDisabled(t *testing.T) {
	// Ensure aggressive configuration is disabled
	os.Unsetenv("IMDS_AGGRESSIVE_CONFIGURATION")

	client := &EC2Client{
		Logger: testr.New(t),
	}

	err := client.tryVPCWideConfiguration(context.Background(), 2, "vpc-12345")
	if err == nil {
		t.Fatal("expected error when aggressive configuration is disabled, got nil")
	}

	expected := "aggressive configuration disabled"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

// TestResolveCurrentVPCID_NoPrivateIP verifies that resolveCurrentVPCID
// returns an error when no private IP can be determined.
func TestResolveCurrentVPCID_NoPrivateIP(t *testing.T) {
	// Ensure PRIVATE_IP env var is not set so the lookup relies on network interfaces
	os.Unsetenv("PRIVATE_IP")

	client := &EC2Client{
		Logger: testr.New(t),
	}

	// This will fail because there's no EC2 client and the network interface
	// lookup will either return a local IP or fail - either way, without a
	// real EC2 client the DescribeInstances call would fail.
	_, err := client.resolveCurrentVPCID(context.Background())
	if err == nil {
		t.Fatal("expected error when resolving VPC ID without AWS credentials, got nil")
	}
}
