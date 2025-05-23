package aws

import (
	"context"
	"time"
)

// ENIManager defines the interface for ENI management operations
type ENIManager interface {
	// CreateENI creates a new ENI in AWS
	CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error)

	// AttachENI attaches an ENI to an EC2 instance
	AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error)

	// DetachENI detaches an ENI from an EC2 instance
	DetachENI(ctx context.Context, attachmentID string, force bool) error

	// DeleteENI deletes an ENI
	DeleteENI(ctx context.Context, eniID string) error
}

// ENIDescriber defines the interface for ENI description operations
type ENIDescriber interface {
	// DescribeENI describes an ENI
	// Returns nil, nil if the ENI doesn't exist
	DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error)

	// WaitForENIDetachment waits for an ENI to be detached
	WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error
}

// SubnetResolver defines the interface for subnet resolution operations
type SubnetResolver interface {
	// GetSubnetIDByName looks up a subnet ID by its Name tag
	GetSubnetIDByName(ctx context.Context, subnetName string) (string, error)

	// GetSubnetCIDRByID looks up a subnet CIDR by its ID
	GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error)
}

// SecurityGroupResolver defines the interface for security group resolution operations
type SecurityGroupResolver interface {
	// GetSecurityGroupIDByName looks up a security group ID by its Name or GroupName
	GetSecurityGroupIDByName(ctx context.Context, securityGroupName string) (string, error)
}

// EC2Interface defines the combined interface for all EC2 operations using AWS SDK v2
// This is a facade that combines all the specialized interfaces
type EC2Interface interface {
	ENIManager
	ENIDescriber
	SubnetResolver
	SecurityGroupResolver
}
