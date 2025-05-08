// Package lib provides a clean API for using AWS Multi-ENI Controller functionality
// as a library in other Go projects. It abstracts away the implementation details
// and provides a simple interface for managing ENIs.
package lib

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// ENIManager provides methods for managing AWS Elastic Network Interfaces.
type ENIManager struct {
	ec2Client *awsutil.EC2Client
	config    *config.ControllerConfig
	logger    logr.Logger
}

// ENIOptions contains options for creating a new ENI.
type ENIOptions struct {
	// SubnetID is the ID of the subnet to create the ENI in.
	SubnetID string
	// SecurityGroupIDs is a list of security group IDs to attach to the ENI.
	SecurityGroupIDs []string
	// Description is an optional description for the ENI.
	Description string
	// DeviceIndex is the device index to use when attaching the ENI.
	DeviceIndex int
	// DeleteOnTermination indicates whether the ENI should be deleted when the instance is terminated.
	DeleteOnTermination bool
	// Tags is a map of tags to apply to the ENI.
	Tags map[string]string
}

// NewENIManager creates a new ENIManager with the given configuration.
func NewENIManager(ctx context.Context, region string, logger logr.Logger) (*ENIManager, error) {
	if region == "" {
		return nil, fmt.Errorf("region cannot be empty")
	}

	ec2Client, err := awsutil.NewEC2Client(ctx, region, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create EC2 client: %w", err)
	}

	cfg := config.DefaultControllerConfig()
	cfg.AWSRegion = region

	return &ENIManager{
		ec2Client: ec2Client,
		config:    cfg,
		logger:    logger,
	}, nil
}

// NewENIManagerWithConfig creates a new ENIManager with the given configuration.
func NewENIManagerWithConfig(ctx context.Context, cfg *config.ControllerConfig, logger logr.Logger) (*ENIManager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if cfg.AWSRegion == "" {
		return nil, fmt.Errorf("AWS region cannot be empty")
	}

	ec2Client, err := awsutil.NewEC2Client(ctx, cfg.AWSRegion, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create EC2 client: %w", err)
	}

	return &ENIManager{
		ec2Client: ec2Client,
		config:    cfg,
		logger:    logger,
	}, nil
}

// CreateENI creates a new ENI with the given options and returns its ID.
func (m *ENIManager) CreateENI(ctx context.Context, options ENIOptions) (string, error) {
	if options.SubnetID == "" {
		return "", fmt.Errorf("subnet ID cannot be empty")
	}

	if len(options.SecurityGroupIDs) == 0 {
		return "", fmt.Errorf("at least one security group ID is required")
	}

	description := options.Description
	if description == "" {
		description = "Created by AWS Multi-ENI Controller Library"
	}

	eniID, err := m.ec2Client.CreateNetworkInterface(ctx, options.SubnetID, options.SecurityGroupIDs, description, options.Tags)
	if err != nil {
		return "", fmt.Errorf("failed to create network interface: %w", err)
	}

	return eniID, nil
}

// AttachENI attaches an ENI to an instance.
func (m *ENIManager) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) error {
	if eniID == "" {
		return fmt.Errorf("ENI ID cannot be empty")
	}

	if instanceID == "" {
		return fmt.Errorf("instance ID cannot be empty")
	}

	if deviceIndex < 0 {
		deviceIndex = m.config.DefaultDeviceIndex
	}

	attachmentID, err := m.ec2Client.AttachNetworkInterface(ctx, eniID, instanceID, deviceIndex, deleteOnTermination)
	if err != nil {
		return fmt.Errorf("failed to attach network interface: %w", err)
	}

	m.logger.Info("Successfully attached ENI", "eniID", eniID, "instanceID", instanceID, "attachmentID", attachmentID)
	return nil
}

// DetachENI detaches an ENI from an instance.
func (m *ENIManager) DetachENI(ctx context.Context, attachmentID string) error {
	if attachmentID == "" {
		return fmt.Errorf("attachment ID cannot be empty")
	}

	err := m.ec2Client.DetachNetworkInterface(ctx, attachmentID, m.config.DetachmentTimeout)
	if err != nil {
		return fmt.Errorf("failed to detach network interface: %w", err)
	}

	return nil
}

// DeleteENI deletes an ENI.
func (m *ENIManager) DeleteENI(ctx context.Context, eniID string) error {
	if eniID == "" {
		return fmt.Errorf("ENI ID cannot be empty")
	}

	err := m.ec2Client.DeleteNetworkInterface(ctx, eniID)
	if err != nil {
		return fmt.Errorf("failed to delete network interface: %w", err)
	}

	return nil
}

// GetENIsByInstance gets all ENIs attached to an instance.
func (m *ENIManager) GetENIsByInstance(ctx context.Context, instanceID string) ([]awsutil.NetworkInterfaceInfo, error) {
	if instanceID == "" {
		return nil, fmt.Errorf("instance ID cannot be empty")
	}

	enis, err := m.ec2Client.GetNetworkInterfacesByInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	return enis, nil
}

// GetSubnetsByVPC gets all subnets in a VPC.
func (m *ENIManager) GetSubnetsByVPC(ctx context.Context, vpcID string) ([]awsutil.SubnetInfo, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("VPC ID cannot be empty")
	}

	subnets, err := m.ec2Client.GetSubnetsByVPC(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("failed to get subnets: %w", err)
	}

	return subnets, nil
}

// GetSecurityGroupsByVPC gets all security groups in a VPC.
func (m *ENIManager) GetSecurityGroupsByVPC(ctx context.Context, vpcID string) ([]awsutil.SecurityGroupInfo, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("VPC ID cannot be empty")
	}

	securityGroups, err := m.ec2Client.GetSecurityGroupsByVPC(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("failed to get security groups: %w", err)
	}

	return securityGroups, nil
}
