package aws

import (
	"context"
	"testing"
)

// TestMockEC2Client tests the mock EC2 client implementation
func TestMockEC2Client(t *testing.T) {
	// Create a mock EC2 client
	mockClient := NewMockEC2Client()

	// Add test data
	mockClient.AddSubnet("subnet-123", "10.0.0.0/24")
	mockClient.AddSubnetName("test-subnet", "subnet-123")
	mockClient.AddSecurityGroup("test-sg", "sg-123")

	// Test CreateENI
	ctx := context.Background()
	eniID, err := mockClient.CreateENI(ctx, "subnet-123", []string{"sg-123"}, "Test ENI", map[string]string{"Name": "test-eni"})
	if err != nil {
		t.Fatalf("Failed to create ENI: %v", err)
	}
	if eniID == "" {
		t.Fatal("Expected non-empty ENI ID")
	}

	// Test DescribeENI
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

	// Verify ENI is attached
	eni, err = mockClient.DescribeENI(ctx, eniID)
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

	// Test DetachENI
	err = mockClient.DetachENI(ctx, attachmentID, false)
	if err != nil {
		t.Fatalf("Failed to detach ENI: %v", err)
	}

	// Verify ENI is detached
	eni, err = mockClient.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni.Status != EC2v2NetworkInterfaceStatusAvailable {
		t.Errorf("Expected status 'available', got %s", eni.Status)
	}
	if eni.Attachment != nil {
		t.Error("Expected nil attachment")
	}

	// Test DeleteENI
	err = mockClient.DeleteENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to delete ENI: %v", err)
	}

	// Verify ENI is deleted
	eni, err = mockClient.DescribeENI(ctx, eniID)
	if err != nil {
		t.Fatalf("Failed to describe ENI: %v", err)
	}
	if eni != nil {
		t.Error("Expected nil ENI (deleted)")
	}

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

	// Test failure scenarios
	mockClient.SetFailureScenario("CreateENI", true)
	_, err = mockClient.CreateENI(ctx, "subnet-123", []string{"sg-123"}, "Test ENI", nil)
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
