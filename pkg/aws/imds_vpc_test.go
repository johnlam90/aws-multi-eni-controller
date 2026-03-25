package aws

import (
	"context"
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
	// t.Setenv automatically restores the original value after the test
	t.Setenv("IMDS_AGGRESSIVE_CONFIGURATION", "false")

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

// TestResolveCurrentVPCID_NilEC2Client verifies that resolveCurrentVPCID
// returns a clear error when the EC2 client is not initialized.
func TestResolveCurrentVPCID_NilEC2Client(t *testing.T) {
	client := &EC2Client{
		Logger: testr.New(t),
	}

	_, err := client.resolveCurrentVPCID(context.Background())
	if err == nil {
		t.Fatal("expected error when EC2 client is nil, got nil")
	}

	expected := "EC2 client is not initialized"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}
