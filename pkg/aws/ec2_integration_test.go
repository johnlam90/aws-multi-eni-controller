//go:build integration
// +build integration

package aws

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
)

// Helper functions for integration tests

// SkipIfNoAWSCredentials skips the test if AWS credentials are not available
func SkipIfNoAWSCredentials(t *testing.T) {
	t.Helper()

	// Check for AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		// Also check for AWS_PROFILE as an alternative
		if os.Getenv("AWS_PROFILE") == "" {
			t.Skip("Skipping test that requires AWS credentials - neither AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY nor AWS_PROFILE are set")
		}
	}

	// Check for AWS region
	if os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "" {
		t.Skip("Skipping test that requires AWS region - neither AWS_REGION nor AWS_DEFAULT_REGION are set")
	}
}

// CreateTestLogger creates a logger for testing
func CreateTestLogger(t *testing.T) logr.Logger {
	return testr.New(t)
}

// TestIntegration_CreateAndDeleteENI tests creating and deleting an ENI with real AWS credentials
func TestIntegration_CreateAndDeleteENI(t *testing.T) {
	// Skip if no AWS credentials
	SkipIfNoAWSCredentials(t)

	// Get subnet ID from environment variable or skip
	subnetID := os.Getenv("TEST_SUBNET_ID")
	if subnetID == "" {
		t.Skip("Skipping test that requires TEST_SUBNET_ID environment variable")
	}

	// Get security group ID from environment variable or skip
	securityGroupID := os.Getenv("TEST_SECURITY_GROUP_ID")
	if securityGroupID == "" {
		t.Skip("Skipping test that requires TEST_SECURITY_GROUP_ID environment variable")
	}

	// Create a test logger
	logger := CreateTestLogger(t)

	// Create EC2 client
	ctx := context.Background()
	client, err := CreateEC2Client(ctx, os.Getenv("AWS_REGION"), logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	// Create an ENI
	eniID, err := client.CreateENI(ctx, subnetID, []string{securityGroupID}, "Integration test ENI", map[string]string{
		"Name":        "integration-test-eni",
		"CreatedBy":   "aws-multi-eni-controller-test",
		"TestCase":    "TestIntegration_CreateAndDeleteENI",
		"DeleteAfter": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("Failed to create ENI: %v", err)
	}

	t.Logf("Created ENI: %s", eniID)

	// Clean up the ENI after the test
	defer func() {
		err := client.DeleteENI(ctx, eniID)
		if err != nil {
			t.Logf("Failed to delete ENI %s: %v", eniID, err)
		} else {
			t.Logf("Deleted ENI: %s", eniID)
		}
	}()

	// Describe the ENI
	eni, err := client.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}

	if eni == nil {
		t.Fatal("Expected non-nil ENI")
	}

	if eni.NetworkInterfaceID != eniID {
		t.Errorf("Expected ENI ID %s, got %s", eniID, eni.NetworkInterfaceID)
	}

	// Note: The EC2v2NetworkInterface struct doesn't have SubnetID, SecurityGroupIDs, Description, or Tags fields
	// These fields are only available in the AWS SDK response, not in our simplified struct
	// We can only check the NetworkInterfaceID and Status fields

	if eni.Status != EC2v2NetworkInterfaceStatusAvailable {
		t.Errorf("Expected status 'available', got %s", eni.Status)
	}
}

// TestIntegration_GetSubnetCIDR tests getting a subnet CIDR with real AWS credentials
func TestIntegration_GetSubnetCIDR(t *testing.T) {
	// Skip if no AWS credentials
	SkipIfNoAWSCredentials(t)

	// Get subnet ID from environment variable or skip
	subnetID := os.Getenv("TEST_SUBNET_ID")
	if subnetID == "" {
		t.Skip("Skipping test that requires TEST_SUBNET_ID environment variable")
	}

	// Create a test logger
	logger := CreateTestLogger(t)

	// Create EC2 client
	ctx := context.Background()
	client, err := CreateEC2Client(ctx, os.Getenv("AWS_REGION"), logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	// Get the subnet CIDR
	cidr, err := client.GetSubnetCIDRByID(ctx, subnetID)
	if err != nil {
		t.Fatalf("Failed to get subnet CIDR: %v", err)
	}

	if cidr == "" {
		t.Fatal("Expected non-empty CIDR")
	}

	t.Logf("Subnet %s has CIDR %s", subnetID, cidr)
}

// TestIntegration_GetSecurityGroupByName tests getting a security group by name with real AWS credentials
func TestIntegration_GetSecurityGroupByName(t *testing.T) {
	// Skip if no AWS credentials
	SkipIfNoAWSCredentials(t)

	// Get security group name from environment variable or skip
	securityGroupName := os.Getenv("TEST_SECURITY_GROUP_NAME")
	if securityGroupName == "" {
		t.Skip("Skipping test that requires TEST_SECURITY_GROUP_NAME environment variable")
	}

	// Create a test logger
	logger := CreateTestLogger(t)

	// Create EC2 client
	ctx := context.Background()
	client, err := CreateEC2Client(ctx, os.Getenv("AWS_REGION"), logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	// Get the security group ID
	sgID, err := client.GetSecurityGroupIDByName(ctx, securityGroupName)
	if err != nil {
		t.Fatalf("Failed to get security group ID: %v", err)
	}

	if sgID == "" {
		t.Fatal("Expected non-empty security group ID")
	}

	t.Logf("Security group %s has ID %s", securityGroupName, sgID)
}
