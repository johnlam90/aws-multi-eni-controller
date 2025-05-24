// Package aws provides utilities for interacting with AWS services,
// particularly EC2 for managing Elastic Network Interfaces (ENIs).
package aws

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockEC2Client implements the EC2Interface for testing purposes
// It also implements all the specialized interfaces (ENIManager, ENIDescriber, etc.)
type MockEC2Client struct {
	// Mocked resources
	ENIs                 map[string]*EC2v2NetworkInterface
	Attachments          map[string]string            // attachmentID -> eniID
	Subnets              map[string]string            // subnetID -> CIDR
	SubnetNames          map[string]string            // subnetName -> subnetID
	SecurityGroups       map[string]string            // sgName -> sgID
	InstanceENIs         map[string][]string          // instanceID -> list of attached ENI IDs
	Instances            map[string]*EC2Instance      // instanceID -> instance
	ENITags              map[string]map[string]string // eniID -> tags
	FailureScenarios     map[string]bool              // operation -> should fail
	DetachmentWaitTime   time.Duration                // time to wait for detachment
	CreationWaitTime     time.Duration                // time to wait for creation
	AttachmentWaitTime   time.Duration                // time to wait for attachment
	DeletionWaitTime     time.Duration                // time to wait for deletion
	DescribeWaitTime     time.Duration                // time to wait for describe
	SubnetLookupWaitTime time.Duration                // time to wait for subnet lookup
	SGLookupWaitTime     time.Duration                // time to wait for security group lookup
	mutex                sync.RWMutex
}

// NewMockEC2Client creates a new mock EC2 client for testing
func NewMockEC2Client() *MockEC2Client {
	return &MockEC2Client{
		ENIs:                 make(map[string]*EC2v2NetworkInterface),
		Attachments:          make(map[string]string),
		Subnets:              make(map[string]string),
		SubnetNames:          make(map[string]string),
		SecurityGroups:       make(map[string]string),
		InstanceENIs:         make(map[string][]string),
		Instances:            make(map[string]*EC2Instance),
		ENITags:              make(map[string]map[string]string),
		FailureScenarios:     make(map[string]bool),
		DetachmentWaitTime:   0,
		CreationWaitTime:     0,
		AttachmentWaitTime:   0,
		DeletionWaitTime:     0,
		DescribeWaitTime:     0,
		SubnetLookupWaitTime: 0,
		SGLookupWaitTime:     0,
	}
}

// SetFailureScenario sets whether a specific operation should fail
func (m *MockEC2Client) SetFailureScenario(operation string, shouldFail bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.FailureScenarios[operation] = shouldFail
}

// AddSubnet adds a subnet to the mock client
func (m *MockEC2Client) AddSubnet(subnetID, cidr string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Subnets[subnetID] = cidr
}

// AddSubnetName adds a subnet name mapping to the mock client
func (m *MockEC2Client) AddSubnetName(subnetName, subnetID string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.SubnetNames[subnetName] = subnetID
}

// AddSecurityGroup adds a security group to the mock client
func (m *MockEC2Client) AddSecurityGroup(sgName, sgID string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.SecurityGroups[sgName] = sgID
}

// AddInstance adds an instance to the mock client
func (m *MockEC2Client) AddInstance(instanceID, state string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Instances[instanceID] = &EC2Instance{
		InstanceID: instanceID,
		State:      state,
	}
}

// CreateENI creates a new ENI in the mock AWS environment
func (m *MockEC2Client) CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Simulate creation delay
	if m.CreationWaitTime > 0 {
		time.Sleep(m.CreationWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["CreateENI"] {
		return "", fmt.Errorf("simulated CreateENI failure")
	}

	// Check if subnet exists
	if _, ok := m.Subnets[subnetID]; !ok {
		return "", fmt.Errorf("subnet %s not found", subnetID)
	}

	// Generate a mock ENI ID
	eniID := fmt.Sprintf("eni-%s", randomString(17))

	// Create the ENI
	m.ENIs[eniID] = &EC2v2NetworkInterface{
		NetworkInterfaceID: eniID,
		Status:             EC2v2NetworkInterfaceStatusAvailable,
	}

	// Store the tags
	if tags != nil {
		m.ENITags[eniID] = make(map[string]string)
		for k, v := range tags {
			m.ENITags[eniID][k] = v
		}
	}

	return eniID, nil
}

// AttachENI attaches an ENI to an EC2 instance in the mock AWS environment
func (m *MockEC2Client) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Simulate attachment delay
	if m.AttachmentWaitTime > 0 {
		time.Sleep(m.AttachmentWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["AttachENI"] {
		return "", fmt.Errorf("simulated AttachENI failure")
	}

	// Check if ENI exists
	eni, ok := m.ENIs[eniID]
	if !ok {
		return "", fmt.Errorf("ENI %s not found", eniID)
	}

	// Check if ENI is already attached
	if eni.Status == EC2v2NetworkInterfaceStatusInUse {
		return "", fmt.Errorf("ENI %s is already attached", eniID)
	}

	// Generate a mock attachment ID
	attachmentID := fmt.Sprintf("eni-attach-%s", randomString(17))

	// Update ENI status
	eni.Status = EC2v2NetworkInterfaceStatusInUse
	eni.Attachment = &EC2v2NetworkInterfaceAttachment{
		AttachmentID:        attachmentID,
		InstanceID:          instanceID,
		DeviceIndex:         int32(deviceIndex),
		DeleteOnTermination: deleteOnTermination,
		Status:              "attached",
	}

	// Store the attachment
	m.Attachments[attachmentID] = eniID

	// Track ENIs attached to this instance
	if _, ok := m.InstanceENIs[instanceID]; !ok {
		m.InstanceENIs[instanceID] = []string{}
	}
	m.InstanceENIs[instanceID] = append(m.InstanceENIs[instanceID], eniID)

	return attachmentID, nil
}

// DetachENI detaches an ENI from an EC2 instance in the mock AWS environment
func (m *MockEC2Client) DetachENI(ctx context.Context, attachmentID string, force bool) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Simulate detachment delay
	if m.DetachmentWaitTime > 0 {
		time.Sleep(m.DetachmentWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["DetachENI"] {
		return fmt.Errorf("simulated DetachENI failure")
	}

	// Check if attachment exists
	eniID, ok := m.Attachments[attachmentID]
	if !ok {
		return fmt.Errorf("attachment %s not found", attachmentID)
	}

	// Check if ENI exists
	eni, ok := m.ENIs[eniID]
	if !ok {
		return fmt.Errorf("ENI %s not found", eniID)
	}

	// Get the instance ID before we clear the attachment
	var instanceID string
	if eni.Attachment != nil {
		instanceID = eni.Attachment.InstanceID
	}

	// Update ENI status
	eni.Status = EC2v2NetworkInterfaceStatusAvailable
	eni.Attachment = nil

	// Remove the attachment
	delete(m.Attachments, attachmentID)

	// Remove the ENI from the instance's list of ENIs
	if instanceID != "" {
		if enis, ok := m.InstanceENIs[instanceID]; ok {
			for i, id := range enis {
				if id == eniID {
					// Remove this ENI from the list
					m.InstanceENIs[instanceID] = append(enis[:i], enis[i+1:]...)
					break
				}
			}
		}
	}

	return nil
}

// DeleteENI deletes an ENI in the mock AWS environment
func (m *MockEC2Client) DeleteENI(ctx context.Context, eniID string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Simulate deletion delay
	if m.DeletionWaitTime > 0 {
		time.Sleep(m.DeletionWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["DeleteENI"] {
		return fmt.Errorf("simulated DeleteENI failure")
	}

	// Check if ENI exists
	eni, ok := m.ENIs[eniID]
	if !ok {
		return fmt.Errorf("ENI %s not found", eniID)
	}

	// Check if ENI is still attached
	if eni.Status == "in-use" {
		return fmt.Errorf("ENI %s is still attached", eniID)
	}

	// Delete the ENI
	delete(m.ENIs, eniID)

	// Delete the tags
	delete(m.ENITags, eniID)

	return nil
}

// DescribeENI describes an ENI in the mock AWS environment
func (m *MockEC2Client) DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Simulate describe delay
	if m.DescribeWaitTime > 0 {
		time.Sleep(m.DescribeWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["DescribeENI"] {
		return nil, fmt.Errorf("simulated DescribeENI failure")
	}

	// Check if ENI exists
	eni, ok := m.ENIs[eniID]
	if !ok {
		return nil, nil // Return nil, nil to simulate ENI not found
	}

	// Create a deep copy to avoid modifying the original
	result := &EC2v2NetworkInterface{
		NetworkInterfaceID: eni.NetworkInterfaceID,
		Status:             eni.Status,
	}

	// Copy attachment if it exists
	if eni.Attachment != nil {
		result.Attachment = &EC2v2NetworkInterfaceAttachment{
			AttachmentID:        eni.Attachment.AttachmentID,
			DeleteOnTermination: eni.Attachment.DeleteOnTermination,
			DeviceIndex:         eni.Attachment.DeviceIndex,
			InstanceID:          eni.Attachment.InstanceID,
			Status:              eni.Attachment.Status,
		}
	}

	return result, nil
}

// DescribeInstance describes an EC2 instance in the mock AWS environment
func (m *MockEC2Client) DescribeInstance(ctx context.Context, instanceID string) (*EC2Instance, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Simulate describe delay
	if m.DescribeWaitTime > 0 {
		time.Sleep(m.DescribeWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["DescribeInstance"] {
		return nil, fmt.Errorf("simulated DescribeInstance failure")
	}

	// Check if instance exists
	instance, ok := m.Instances[instanceID]
	if !ok {
		return nil, nil // Return nil, nil to simulate instance not found
	}

	// Create a copy to avoid modifying the original
	result := &EC2Instance{
		InstanceID: instance.InstanceID,
		State:      instance.State,
	}

	return result, nil
}

// GetSubnetIDByName looks up a subnet ID by its Name tag in the mock AWS environment
func (m *MockEC2Client) GetSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Simulate lookup delay
	if m.SubnetLookupWaitTime > 0 {
		time.Sleep(m.SubnetLookupWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["GetSubnetIDByName"] {
		return "", fmt.Errorf("simulated GetSubnetIDByName failure")
	}

	// Check if subnet name exists
	subnetID, ok := m.SubnetNames[subnetName]
	if !ok {
		return "", fmt.Errorf("subnet with name %s not found", subnetName)
	}

	return subnetID, nil
}

// GetSubnetCIDRByID looks up a subnet CIDR by its ID in the mock AWS environment
func (m *MockEC2Client) GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Simulate lookup delay
	if m.SubnetLookupWaitTime > 0 {
		time.Sleep(m.SubnetLookupWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["GetSubnetCIDRByID"] {
		return "", fmt.Errorf("simulated GetSubnetCIDRByID failure")
	}

	// Check if subnet exists
	cidr, ok := m.Subnets[subnetID]
	if !ok {
		return "", fmt.Errorf("subnet %s not found", subnetID)
	}

	return cidr, nil
}

// GetSecurityGroupIDByName looks up a security group ID by its Name in the mock AWS environment
func (m *MockEC2Client) GetSecurityGroupIDByName(ctx context.Context, securityGroupName string) (string, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Simulate lookup delay
	if m.SGLookupWaitTime > 0 {
		time.Sleep(m.SGLookupWaitTime)
	}

	// Check if we should simulate a failure
	if m.FailureScenarios["GetSecurityGroupIDByName"] {
		return "", fmt.Errorf("simulated GetSecurityGroupIDByName failure")
	}

	// Check if security group name exists
	sgID, ok := m.SecurityGroups[securityGroupName]
	if !ok {
		return "", fmt.Errorf("security group with name %s not found", securityGroupName)
	}

	return sgID, nil
}

// WaitForENIDetachment waits for an ENI to be detached in the mock AWS environment
func (m *MockEC2Client) WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Check if we should simulate a failure
	if m.FailureScenarios["WaitForENIDetachment"] {
		return fmt.Errorf("simulated WaitForENIDetachment failure")
	}

	// Check if ENI exists
	eni, ok := m.ENIs[eniID]
	if !ok {
		return fmt.Errorf("ENI %s not found", eniID)
	}

	// Check if ENI is already detached
	if eni.Status == "available" {
		return nil
	}

	return fmt.Errorf("ENI %s is still attached", eniID)
}

// Helper function to generate random strings for IDs
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[i%len(charset)]
	}
	return string(result)
}
