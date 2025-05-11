package aws

import (
	"context"
	"time"
)

// EC2Interface defines the interface for EC2 operations using AWS SDK v2
type EC2Interface interface {
	// CreateENI creates a new ENI in AWS
	CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error)

	// AttachENI attaches an ENI to an EC2 instance
	AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error)

	// DetachENI detaches an ENI from an EC2 instance
	DetachENI(ctx context.Context, attachmentID string, force bool) error

	// DeleteENI deletes an ENI
	DeleteENI(ctx context.Context, eniID string) error

	// DescribeENI describes an ENI
	// Returns nil, nil if the ENI doesn't exist
	DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error)

	// GetSubnetIDByName looks up a subnet ID by its Name tag
	GetSubnetIDByName(ctx context.Context, subnetName string) (string, error)

	// GetSubnetCIDRByID looks up a subnet CIDR by its ID
	GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error)

	// GetSecurityGroupIDByName looks up a security group ID by its Name or GroupName
	GetSecurityGroupIDByName(ctx context.Context, securityGroupName string) (string, error)

	// WaitForENIDetachment waits for an ENI to be detached
	WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error
}
