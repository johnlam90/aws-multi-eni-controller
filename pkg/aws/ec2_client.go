package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/go-logr/logr"
)

// EC2ClientFacade is a facade that implements the EC2Interface by delegating to specialized components
type EC2ClientFacade struct {
	// Underlying EC2 client from AWS SDK v2
	ec2Client *ec2.Client
	// Logger for structured logging
	Logger logr.Logger

	// Component implementations
	eniManager            *EC2ENIManager
	eniDescriber          *EC2ENIDescriber
	subnetResolver        *EC2SubnetResolver
	securityGroupResolver *EC2SecurityGroupResolver
}

// NewEC2ClientFacade creates a new EC2ClientFacade using AWS SDK v2
func NewEC2ClientFacade(ctx context.Context, region string, logger logr.Logger) (*EC2ClientFacade, error) {
	// Create AWS config
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create EC2 client
	ec2Client := ec2.NewFromConfig(cfg)

	// Create component implementations
	eniManager := NewEC2ENIManager(ec2Client, logger)
	eniDescriber := NewEC2ENIDescriber(ec2Client, logger)
	subnetResolver := NewEC2SubnetResolver(ec2Client, logger)
	securityGroupResolver := NewEC2SecurityGroupResolver(ec2Client, logger)

	return &EC2ClientFacade{
		ec2Client:             ec2Client,
		Logger:                logger.WithName("aws-ec2-client-facade"),
		eniManager:            eniManager,
		eniDescriber:          eniDescriber,
		subnetResolver:        subnetResolver,
		securityGroupResolver: securityGroupResolver,
	}, nil
}

// CreateENI delegates to ENIManager
func (c *EC2ClientFacade) CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error) {
	return c.eniManager.CreateENI(ctx, subnetID, securityGroupIDs, description, tags)
}

// AttachENI delegates to ENIManager
func (c *EC2ClientFacade) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error) {
	return c.eniManager.AttachENI(ctx, eniID, instanceID, deviceIndex, deleteOnTermination)
}

// DetachENI delegates to ENIManager
func (c *EC2ClientFacade) DetachENI(ctx context.Context, attachmentID string, force bool) error {
	return c.eniManager.DetachENI(ctx, attachmentID, force)
}

// DeleteENI delegates to ENIManager
func (c *EC2ClientFacade) DeleteENI(ctx context.Context, eniID string) error {
	return c.eniManager.DeleteENI(ctx, eniID)
}

// DescribeENI delegates to ENIDescriber
func (c *EC2ClientFacade) DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error) {
	return c.eniDescriber.DescribeENI(ctx, eniID)
}

// WaitForENIDetachment delegates to ENIDescriber
func (c *EC2ClientFacade) WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error {
	return c.eniDescriber.WaitForENIDetachment(ctx, eniID, timeout)
}

// GetSubnetIDByName delegates to SubnetResolver
func (c *EC2ClientFacade) GetSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	return c.subnetResolver.GetSubnetIDByName(ctx, subnetName)
}

// GetSubnetCIDRByID delegates to SubnetResolver
func (c *EC2ClientFacade) GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error) {
	return c.subnetResolver.GetSubnetCIDRByID(ctx, subnetID)
}

// GetSecurityGroupIDByName delegates to SecurityGroupResolver
func (c *EC2ClientFacade) GetSecurityGroupIDByName(ctx context.Context, securityGroupName string) (string, error) {
	return c.securityGroupResolver.GetSecurityGroupIDByName(ctx, securityGroupName)
}
