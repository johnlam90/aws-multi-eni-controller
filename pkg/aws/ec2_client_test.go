package aws

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
)

// TestNewEC2ClientFacade tests the creation of a new EC2 client facade
func TestNewEC2ClientFacade(t *testing.T) {
	// Skip this test in CI environments where AWS credentials might not be available
	t.Skip("Skipping test that requires AWS credentials")

	// Create a test logger
	logger := testr.New(t)

	// Test creating a new EC2 client facade
	client, err := NewEC2ClientFacade(context.Background(), "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client facade: %v", err)
	}

	if client == nil {
		t.Fatal("Expected non-nil EC2 client facade")
	}

	// Verify that all component implementations are initialized
	if client.eniManager == nil {
		t.Fatal("Expected non-nil ENI manager")
	}

	if client.eniDescriber == nil {
		t.Fatal("Expected non-nil ENI describer")
	}

	if client.subnetResolver == nil {
		t.Fatal("Expected non-nil subnet resolver")
	}

	if client.securityGroupResolver == nil {
		t.Fatal("Expected non-nil security group resolver")
	}
}

// TestCreateEC2ClientWithFacade tests the factory function for creating an EC2 client with facade
func TestCreateEC2ClientWithFacade(t *testing.T) {
	// Skip this test in CI environments where AWS credentials might not be available
	t.Skip("Skipping test that requires AWS credentials")

	// Create a test logger
	logger := testr.New(t)

	// Set environment variable to use facade
	oldEnv := os.Getenv("USE_EC2_FACADE")
	os.Setenv("USE_EC2_FACADE", "true")
	defer os.Setenv("USE_EC2_FACADE", oldEnv)

	// Test creating a new EC2 client using the factory function
	client, err := CreateEC2Client(context.Background(), "us-east-1", logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected non-nil EC2 client")
	}

	// Verify that we got a facade implementation
	_, ok := client.(*EC2ClientFacade)
	if !ok {
		t.Fatal("Expected client to be an EC2ClientFacade")
	}
}

// setupMockEC2ClientTest initializes a mock EC2 client for testing
func setupMockEC2ClientTest(t *testing.T) (EC2Interface, *MockEC2Client, context.Context) {
	// Test creating a new mock EC2 client using the factory function
	client := CreateMockEC2Client()
	if client == nil {
		t.Fatal("Expected non-nil mock EC2 client")
	}

	// Verify that the client implements the EC2Interface
	mockClient, ok := client.(*MockEC2Client)
	if !ok {
		t.Fatal("Expected client to be a MockEC2Client")
	}

	// Add test data
	mockClient.AddSubnet("subnet-123", "10.0.0.0/24")
	mockClient.AddSubnetName("test-subnet", "subnet-123")
	mockClient.AddSecurityGroup("test-sg", "sg-123")

	return client, mockClient, context.Background()
}

// TestCreateMockEC2Client_Factory tests the factory function for creating a mock EC2 client
func TestCreateMockEC2Client_Factory(t *testing.T) {
	client, _, _ := setupMockEC2ClientTest(t)
	if client == nil {
		t.Fatal("Expected non-nil mock EC2 client")
	}
}

// TestCreateMockEC2Client_ENIOperations tests ENI operations with the mock EC2 client
func TestCreateMockEC2Client_ENIOperations(t *testing.T) {
	client, _, ctx := setupMockEC2ClientTest(t)

	// Test CreateENI
	eniID, err := client.CreateENI(ctx, "subnet-123", []string{"sg-123"}, "Test ENI", map[string]string{"Name": "test-eni"})
	if err != nil {
		t.Fatalf("Failed to create ENI: %v", err)
	}
	if eniID == "" {
		t.Fatal("Expected non-empty ENI ID")
	}

	// Test DescribeENI
	eni, err := client.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni == nil {
		t.Fatal("Expected non-nil ENI")
	}
	if eni.NetworkInterfaceID != eniID {
		t.Fatalf("Expected ENI ID %s, got %s", eniID, eni.NetworkInterfaceID)
	}

	// Test AttachENI
	attachmentID, err := client.AttachENI(ctx, eniID, "i-123", 1, true)
	if err != nil {
		t.Fatalf("Failed to attach ENI: %v", err)
	}
	if attachmentID == "" {
		t.Fatal("Expected non-empty attachment ID")
	}

	// Test DetachENI
	err = client.DetachENI(ctx, attachmentID, false)
	if err != nil {
		t.Fatalf("Failed to detach ENI: %v", err)
	}

	// Test WaitForENIDetachment
	err = client.WaitForENIDetachment(ctx, eniID, 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to wait for ENI detachment: %v", err)
	}

	// Test DeleteENI
	err = client.DeleteENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to delete ENI: %v", err)
	}
}

// TestCreateMockEC2Client_SubnetAndSG tests subnet and security group operations
func TestCreateMockEC2Client_SubnetAndSG(t *testing.T) {
	client, _, ctx := setupMockEC2ClientTest(t)

	// Test GetSubnetCIDRByID
	cidr, err := client.GetSubnetCIDRByID(ctx, "subnet-123")
	if err != nil {
		t.Fatalf("Failed to get subnet CIDR: %v", err)
	}
	if cidr != "10.0.0.0/24" {
		t.Fatalf("Expected CIDR 10.0.0.0/24, got %s", cidr)
	}

	// Test GetSubnetIDByName
	subnetID, err := client.GetSubnetIDByName(ctx, "test-subnet")
	if err != nil {
		t.Fatalf("Failed to get subnet ID: %v", err)
	}
	if subnetID != "subnet-123" {
		t.Fatalf("Expected subnet ID subnet-123, got %s", subnetID)
	}

	// Test GetSecurityGroupIDByName
	sgID, err := client.GetSecurityGroupIDByName(ctx, "test-sg")
	if err != nil {
		t.Fatalf("Failed to get security group ID: %v", err)
	}
	if sgID != "sg-123" {
		t.Fatalf("Expected security group ID sg-123, got %s", sgID)
	}
}
