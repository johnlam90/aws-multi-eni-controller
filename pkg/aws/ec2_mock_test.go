package aws

import (
	"context"
	"testing"
)

// setupMockEC2Client creates and initializes a mock EC2 client for testing
func setupMockEC2Client(t *testing.T) (*MockEC2Client, context.Context) {
	// Create a mock EC2 client
	mockClient := NewMockEC2Client()

	// Add test data
	mockClient.AddSubnet("subnet-123", "10.0.0.0/24")
	mockClient.AddSubnetName("test-subnet", "subnet-123")
	mockClient.AddSecurityGroup("test-sg", "sg-123")

	return mockClient, context.Background()
}

// TestMockEC2Client_ENILifecycle tests the ENI lifecycle operations (create, attach, detach, delete)
func TestMockEC2Client_ENILifecycle(t *testing.T) {
	mockClient, ctx := setupMockEC2Client(t)

	// Test CreateENI
	eniID, err := mockClient.CreateENI(ctx, "subnet-123", []string{"sg-123"}, "Test ENI", map[string]string{"Name": "test-eni"})
	if err != nil {
		t.Fatalf("Failed to create ENI: %v", err)
	}
	if eniID == "" {
		t.Fatal("Expected non-empty ENI ID")
	}

	// Test DescribeENI after creation
	eni, err := mockClient.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni == nil {
		t.Fatal("Expected non-nil ENI")
	}
	if eni.NetworkInterfaceID != eniID {
		t.Errorf("Expected ENI ID %s, got %s", eniID, eni.NetworkInterfaceID)
	}
	if eni.Status != EC2v2NetworkInterfaceStatusAvailable {
		t.Errorf("Expected status 'available', got %s", eni.Status)
	}

	// Test AttachENI
	attachmentID, err := mockClient.AttachENI(ctx, eniID, "i-123", 1, true)
	if err != nil {
		t.Fatalf("Failed to attach ENI: %v", err)
	}
	if attachmentID == "" {
		t.Fatal("Expected non-empty attachment ID")
	}

	// Verify ENI attachment
	verifyENIAttachment(ctx, t, mockClient, eniID, attachmentID)

	// Test DetachENI
	err = mockClient.DetachENI(ctx, attachmentID, false)
	if err != nil {
		t.Fatalf("Failed to detach ENI: %v", err)
	}

	// Verify ENI is detached
	verifyENIDetached(ctx, t, mockClient, eniID)

	// Test DeleteENI
	err = mockClient.DeleteENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to delete ENI: %v", err)
	}

	// Verify ENI is deleted
	verifyENIDeleted(ctx, t, mockClient, eniID)
}

// verifyENIAttachment verifies that an ENI is properly attached
func verifyENIAttachment(ctx context.Context, t *testing.T, mockClient *MockEC2Client, eniID, attachmentID string) {
	eni, err := mockClient.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni.Status != EC2v2NetworkInterfaceStatusInUse {
		t.Errorf("Expected status 'in-use', got %s", eni.Status)
	}
	if eni.Attachment == nil {
		t.Fatal("Expected non-nil attachment")
	}
	if eni.Attachment.AttachmentID != attachmentID {
		t.Errorf("Expected attachment ID %s, got %s", attachmentID, eni.Attachment.AttachmentID)
	}
	if eni.Attachment.InstanceID != "i-123" {
		t.Errorf("Expected instance ID i-123, got %s", eni.Attachment.InstanceID)
	}
	if eni.Attachment.DeviceIndex != int32(1) {
		t.Errorf("Expected device index 1, got %d", eni.Attachment.DeviceIndex)
	}
	if !eni.Attachment.DeleteOnTermination {
		t.Error("Expected DeleteOnTermination to be true")
	}
}

// verifyENIDetached verifies that an ENI is properly detached
func verifyENIDetached(ctx context.Context, t *testing.T, mockClient *MockEC2Client, eniID string) {
	eni, err := mockClient.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni.Status != EC2v2NetworkInterfaceStatusAvailable {
		t.Errorf("Expected status 'available', got %s", eni.Status)
	}
	if eni.Attachment != nil {
		t.Error("Expected nil attachment")
	}
}

// verifyENIDeleted verifies that an ENI is properly deleted
func verifyENIDeleted(ctx context.Context, t *testing.T, mockClient *MockEC2Client, eniID string) {
	eni, err := mockClient.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni != nil {
		t.Error("Expected nil ENI (deleted)")
	}
}

// TestMockEC2Client_SubnetAndSG tests subnet and security group operations
func TestMockEC2Client_SubnetAndSG(t *testing.T) {
	mockClient, ctx := setupMockEC2Client(t)

	// Test GetSubnetIDByName
	subnetID, err := mockClient.GetSubnetIDByName(ctx, "test-subnet")
	if err != nil {
		t.Fatalf("Failed to get subnet ID by name: %v", err)
	}
	if subnetID != "subnet-123" {
		t.Errorf("Expected subnet ID subnet-123, got %s", subnetID)
	}

	// Test GetSubnetCIDRByID
	cidr, err := mockClient.GetSubnetCIDRByID(ctx, "subnet-123")
	if err != nil {
		t.Fatalf("Failed to get subnet CIDR by ID: %v", err)
	}
	if cidr != "10.0.0.0/24" {
		t.Errorf("Expected CIDR 10.0.0.0/24, got %s", cidr)
	}

	// Test GetSecurityGroupIDByName
	sgID, err := mockClient.GetSecurityGroupIDByName(ctx, "test-sg")
	if err != nil {
		t.Fatalf("Failed to get security group ID by name: %v", err)
	}
	if sgID != "sg-123" {
		t.Errorf("Expected security group ID sg-123, got %s", sgID)
	}
}

// TestMockEC2Client_ErrorScenarios tests error scenarios
func TestMockEC2Client_ErrorScenarios(t *testing.T) {
	mockClient, ctx := setupMockEC2Client(t)

	// Test failure scenarios
	mockClient.SetFailureScenario("CreateENI", true)
	_, err := mockClient.CreateENI(ctx, "subnet-123", []string{"sg-123"}, "Test ENI", nil)
	if err == nil {
		t.Error("Expected error for CreateENI failure scenario")
	}

	mockClient.SetFailureScenario("GetSubnetIDByName", true)
	_, err = mockClient.GetSubnetIDByName(ctx, "test-subnet")
	if err == nil {
		t.Error("Expected error for GetSubnetIDByName failure scenario")
	}

	// Test non-existent resources
	_, err = mockClient.GetSubnetIDByName(ctx, "non-existent-subnet")
	if err == nil {
		t.Error("Expected error for non-existent subnet name")
	}

	_, err = mockClient.GetSubnetCIDRByID(ctx, "non-existent-subnet")
	if err == nil {
		t.Error("Expected error for non-existent subnet ID")
	}

	_, err = mockClient.GetSecurityGroupIDByName(ctx, "non-existent-sg")
	if err == nil {
		t.Error("Expected error for non-existent security group name")
	}
}
