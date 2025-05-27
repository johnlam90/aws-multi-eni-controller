package lib

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

func TestNewENIManager(t *testing.T) {
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
			expectError: true, // Should error on empty region
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Skip("Skipping test that requires AWS credentials")

			logger := testr.New(t)
			ctx := context.Background()
			manager, err := NewENIManager(ctx, tt.region, logger)
			if tt.expectError && err == nil {
				t.Errorf("Expected error for region %s, but got none", tt.region)
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error for region %s: %v", tt.region, err)
			}
			if !tt.expectError && manager == nil {
				t.Errorf("Expected manager for region %s, but got nil", tt.region)
			}
		})
	}
}

func TestNewENIManagerWithConfig(t *testing.T) {
	logger := testr.New(t)
	ctx := context.Background()

	cfg := config.DefaultControllerConfig()
	cfg.AWSRegion = "us-east-1"

	t.Skip("Skipping test that requires AWS credentials")

	manager, err := NewENIManagerWithConfig(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create ENI manager with config: %v", err)
	}

	if manager == nil {
		t.Fatal("Expected non-nil ENI manager")
	}

	if manager.config != cfg {
		t.Error("Config not properly set in manager")
	}
}

func TestENIOptions_Validation(t *testing.T) {
	logger := testr.New(t)
	ctx := context.Background()

	t.Skip("Skipping test that requires AWS credentials")

	manager, err := NewENIManager(ctx, "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create ENI manager: %v", err)
	}

	// Test with empty subnet ID
	options := ENIOptions{
		SubnetID:         "",
		SecurityGroupIDs: []string{"sg-123"},
	}

	_, err = manager.CreateENI(ctx, options)
	if err == nil {
		t.Error("Expected error with empty subnet ID")
	}

	// Test with empty security groups
	options = ENIOptions{
		SubnetID:         "subnet-123",
		SecurityGroupIDs: []string{},
	}

	_, err = manager.CreateENI(ctx, options)
	if err == nil {
		t.Error("Expected error with empty security groups")
	}
}
