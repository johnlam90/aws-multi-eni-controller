// Package aws provides utilities for interacting with AWS services,
// particularly EC2 for managing Elastic Network Interfaces (ENIs).
//
// This package uses AWS SDK v2 for all AWS interactions, providing improved
// performance, better error handling, and more modern API design compared to v1.
package aws

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
)

// EC2Client wraps the AWS EC2 client with additional functionality using SDK v2.
// It provides methods for creating, attaching, detaching, and deleting ENIs,
// as well as looking up subnet and security group IDs by name.
type EC2Client struct {
	// EC2 is the underlying AWS SDK v2 EC2 client
	EC2 *ec2.Client
	// Logger is used for structured logging
	Logger logr.Logger

	// Cache for subnet information
	subnetCache     map[string]string // subnetID -> CIDR
	subnetNameCache map[string]string // subnetName -> subnetID
	subnetMutex     sync.RWMutex

	// Cache for security group information
	sgCache map[string]string // sgName -> sgID
	sgMutex sync.RWMutex

	// Cache expiration
	cacheExpiration time.Duration
	lastCacheUpdate time.Time
}

// NewEC2Client creates a new EC2 client using AWS SDK v2
func NewEC2Client(ctx context.Context, region string, logger logr.Logger) (*EC2Client, error) {
	// Create AWS config
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %v", err)
	}

	return &EC2Client{
		EC2:             ec2.NewFromConfig(cfg),
		Logger:          logger.WithName("aws-ec2"),
		subnetCache:     make(map[string]string),
		subnetNameCache: make(map[string]string),
		sgCache:         make(map[string]string),
		cacheExpiration: 5 * time.Minute, // Cache expires after 5 minutes
		lastCacheUpdate: time.Now(),
	}, nil
}

// CreateENI creates a new ENI in AWS
func (c *EC2Client) CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error) {
	log := c.Logger.WithValues("subnetID", subnetID)
	log.Info("Creating ENI")

	// Convert tags map to AWS tags
	var tagSpecs []types.TagSpecification
	if len(tags) > 0 {
		var awsTags []types.Tag
		for k, v := range tags {
			awsTags = append(awsTags, types.Tag{
				Key:   aws.String(k),
				Value: aws.String(v),
			})
		}
		tagSpecs = append(tagSpecs, types.TagSpecification{
			ResourceType: types.ResourceTypeNetworkInterface,
			Tags:         awsTags,
		})
	}

	input := &ec2.CreateNetworkInterfaceInput{
		Description:       aws.String(description),
		SubnetId:          aws.String(subnetID),
		Groups:            securityGroupIDs,
		TagSpecifications: tagSpecs,
	}

	result, err := c.EC2.CreateNetworkInterface(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to create ENI: %v", err)
	}

	eniID := *result.NetworkInterface.NetworkInterfaceId
	log.Info("Successfully created ENI", "eniID", eniID)
	return eniID, nil
}

// AttachENI attaches an ENI to an EC2 instance
func (c *EC2Client) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error) {
	log := c.Logger.WithValues("eniID", eniID, "instanceID", instanceID, "deviceIndex", deviceIndex)
	log.Info("Attaching ENI to instance")

	input := &ec2.AttachNetworkInterfaceInput{
		DeviceIndex:        aws.Int32(int32(deviceIndex)),
		InstanceId:         aws.String(instanceID),
		NetworkInterfaceId: aws.String(eniID),
	}

	result, err := c.EC2.AttachNetworkInterface(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to attach ENI: %v", err)
	}

	attachmentID := *result.AttachmentId
	log.Info("Successfully attached ENI", "attachmentID", attachmentID)

	// Set delete on termination attribute if requested
	if deleteOnTermination {
		_, err = c.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: aws.String(eniID),
			Attachment: &types.NetworkInterfaceAttachmentChanges{
				AttachmentId:        aws.String(attachmentID),
				DeleteOnTermination: aws.Bool(true),
			},
		})
		if err != nil {
			// Log the error but don't fail the attachment
			log.Error(err, "Failed to set delete on termination, but ENI is still attached", "attachmentID", attachmentID)
			log.Info("WARNING: ENI will not be automatically deleted when the instance terminates", "eniID", eniID)
			// Continue without returning an error
		} else {
			log.Info("Set delete on termination to true", "attachmentID", attachmentID)
		}
	}

	return attachmentID, nil
}

// DetachENI detaches an ENI from an EC2 instance
func (c *EC2Client) DetachENI(ctx context.Context, attachmentID string, force bool) error {
	log := c.Logger.WithValues("attachmentID", attachmentID)

	// If force is false, we're just checking if the attachment exists
	// This is used by verifyENIAttachments to check if an ENI is still attached
	if !force {
		log.V(1).Info("Checking if ENI attachment exists")
	} else {
		log.Info("Detaching ENI")
	}

	input := &ec2.DetachNetworkInterfaceInput{
		AttachmentId: aws.String(attachmentID),
		Force:        aws.Bool(force),
	}

	// If force is false, we're just checking if the attachment exists
	// We don't actually want to detach it, so we'll use DryRun mode
	if !force {
		input.DryRun = aws.Bool(true)
	}

	// Use exponential backoff for API rate limiting
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}

	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		_, err := c.EC2.DetachNetworkInterface(ctx, input)
		if err != nil {
			// Check if the error indicates the attachment doesn't exist
			// This can happen if the ENI was manually detached outside of our control
			if strings.Contains(err.Error(), "InvalidAttachmentID.NotFound") {
				log.Info("ENI attachment no longer exists, considering detachment successful", "error", err.Error())
				return true, nil
			}

			// If we're in DryRun mode and get a DryRunOperation error, the attachment exists
			if !force && strings.Contains(err.Error(), "DryRunOperation") {
				log.V(1).Info("ENI attachment exists (dry run succeeded)")
				lastErr = fmt.Errorf("attachment exists but not detaching due to dry run")
				return false, lastErr
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying", "error", err.Error())
				lastErr = err
				return false, nil // Return nil error to continue retrying
			}

			// For other errors, fail immediately
			lastErr = err
			return false, err
		}
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to detach ENI after retries: %v", lastErr)
	}

	log.Info("Successfully detached ENI")
	return nil
}

// DeleteENI deletes an ENI
func (c *EC2Client) DeleteENI(ctx context.Context, eniID string) error {
	log := c.Logger.WithValues("eniID", eniID)
	log.Info("Deleting ENI")

	input := &ec2.DeleteNetworkInterfaceInput{
		NetworkInterfaceId: aws.String(eniID),
	}

	// Use exponential backoff for API rate limiting
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}

	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		_, err := c.EC2.DeleteNetworkInterface(ctx, input)
		if err != nil {
			// Check if the error indicates the ENI doesn't exist
			// This can happen if the ENI was manually deleted outside of our control
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				log.Info("ENI no longer exists, considering deletion successful", "error", err.Error())
				return true, nil
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying", "error", err.Error())
				lastErr = err
				return false, nil // Return nil error to continue retrying
			}

			// For other errors, fail immediately
			lastErr = err
			return false, err
		}
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("failed to delete ENI after retries: %v", lastErr)
	}

	log.Info("Successfully deleted ENI")
	return nil
}

// DescribeENI describes an ENI
func (c *EC2Client) DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error) {
	log := c.Logger.WithValues("eniID", eniID)
	log.V(1).Info("Describing ENI")

	input := &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	}

	// Use exponential backoff for API rate limiting
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}

	var result *ec2.DescribeNetworkInterfacesOutput
	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		var err error
		result, err = c.EC2.DescribeNetworkInterfaces(ctx, input)
		if err != nil {
			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying", "error", err.Error())
				lastErr = err
				return false, nil // Return nil error to continue retrying
			}

			// For other errors, fail immediately
			lastErr = err
			return false, err
		}
		return true, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to describe ENI after retries: %v", lastErr)
	}

	if len(result.NetworkInterfaces) == 0 {
		return nil, nil // ENI not found
	}

	// Convert to our internal type
	eni := &EC2v2NetworkInterface{
		NetworkInterfaceID: *result.NetworkInterfaces[0].NetworkInterfaceId,
		Status:             EC2v2NetworkInterfaceStatus(result.NetworkInterfaces[0].Status),
	}

	// Add attachment if it exists and is attached
	// Note: We need to check both the existence of the Attachment field AND the status
	// An ENI can have an Attachment field but still be in the "available" state if it was manually detached
	if result.NetworkInterfaces[0].Attachment != nil &&
		result.NetworkInterfaces[0].Status != "available" &&
		result.NetworkInterfaces[0].Attachment.Status != "detached" {
		eni.Attachment = &EC2v2NetworkInterfaceAttachment{
			AttachmentID:        *result.NetworkInterfaces[0].Attachment.AttachmentId,
			DeleteOnTermination: *result.NetworkInterfaces[0].Attachment.DeleteOnTermination,
			DeviceIndex:         *result.NetworkInterfaces[0].Attachment.DeviceIndex,
			InstanceID:          *result.NetworkInterfaces[0].Attachment.InstanceId,
			Status:              string(result.NetworkInterfaces[0].Attachment.Status),
		}
	} else {
		// Explicitly set Attachment to nil to indicate it's not attached
		// This ensures we don't rely on the AWS response structure which might include
		// attachment info even for detached ENIs
		eni.Attachment = nil

		// If the ENI has an attachment field but is in the available state,
		// it means it was manually detached
		if result.NetworkInterfaces[0].Attachment != nil &&
			(result.NetworkInterfaces[0].Status == "available" ||
				result.NetworkInterfaces[0].Attachment.Status == "detached") {
			log.Info("ENI has attachment info but is in available state or detached status, considering it detached")
		}
	}

	return eni, nil
}

// DescribeInstance describes an EC2 instance
func (c *EC2Client) DescribeInstance(ctx context.Context, instanceID string) (*EC2Instance, error) {
	log := c.Logger.WithValues("instanceID", instanceID)
	log.V(1).Info("Describing EC2 instance")

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	// Use exponential backoff for API rate limiting
	backoff := wait.Backoff{
		Steps:    5,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}

	var result *ec2.DescribeInstancesOutput
	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		var err error
		result, err = c.EC2.DescribeInstances(ctx, input)
		if err != nil {
			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying", "error", err.Error())
				lastErr = err
				return false, nil // Return nil error to continue retrying
			}

			// For other errors, fail immediately
			lastErr = err
			return false, err
		}
		return true, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to describe instance after retries: %v", lastErr)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return nil, nil // Instance not found
	}

	instance := result.Reservations[0].Instances[0]

	// Convert to our internal type
	ec2Instance := &EC2Instance{
		InstanceID: *instance.InstanceId,
		State:      string(instance.State.Name),
	}

	log.V(1).Info("Found EC2 instance", "instanceID", ec2Instance.InstanceID, "state", ec2Instance.State)
	return ec2Instance, nil
}

// GetSubnetIDByName looks up a subnet ID by its Name tag
func (c *EC2Client) GetSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	log := c.Logger.WithValues("subnetName", subnetName)
	log.Info("Looking up subnet ID by name")

	// Check cache first
	c.subnetMutex.RLock()
	if subnetID, ok := c.subnetNameCache[subnetName]; ok {
		// Check if cache is still valid
		if time.Since(c.lastCacheUpdate) < c.cacheExpiration {
			log.Info("Using cached subnet ID", "subnetName", subnetName, "subnetID", subnetID)
			c.subnetMutex.RUnlock()
			return subnetID, nil
		}
	}
	c.subnetMutex.RUnlock()

	// Cache miss or expired, fetch from AWS
	input := &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{subnetName},
			},
		},
	}

	result, err := c.EC2.DescribeSubnets(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe subnets: %v", err)
	}

	if len(result.Subnets) == 0 {
		return "", fmt.Errorf("no subnet found with name: %s", subnetName)
	}

	if len(result.Subnets) > 1 {
		log.Info("Multiple subnets found with the same name, using the first one")
	}

	subnetID := *result.Subnets[0].SubnetId
	log.Info("Found subnet ID", "subnetID", subnetID)

	// Update cache
	c.subnetMutex.Lock()
	c.subnetNameCache[subnetName] = subnetID
	// Also cache the CIDR if available
	if result.Subnets[0].CidrBlock != nil {
		c.subnetCache[subnetID] = *result.Subnets[0].CidrBlock
	}
	c.lastCacheUpdate = time.Now()
	c.subnetMutex.Unlock()

	return subnetID, nil
}

// GetSubnetCIDRByID looks up a subnet CIDR by its ID
func (c *EC2Client) GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error) {
	log := c.Logger.WithValues("subnetID", subnetID)
	log.V(1).Info("Looking up subnet CIDR by ID")

	// Check cache first
	c.subnetMutex.RLock()
	if cidr, ok := c.subnetCache[subnetID]; ok {
		// Check if cache is still valid
		if time.Since(c.lastCacheUpdate) < c.cacheExpiration {
			log.V(1).Info("Using cached subnet CIDR", "subnetID", subnetID, "cidrBlock", cidr)
			c.subnetMutex.RUnlock()
			return cidr, nil
		}
	}
	c.subnetMutex.RUnlock()

	// Cache miss or expired, fetch from AWS
	input := &ec2.DescribeSubnetsInput{
		SubnetIds: []string{subnetID},
	}

	result, err := c.EC2.DescribeSubnets(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe subnet: %v", err)
	}

	if len(result.Subnets) == 0 {
		return "", fmt.Errorf("no subnet found with ID: %s", subnetID)
	}

	cidrBlock := *result.Subnets[0].CidrBlock
	log.V(1).Info("Found subnet CIDR", "subnetID", subnetID, "cidrBlock", cidrBlock)

	// Update cache
	c.subnetMutex.Lock()
	c.subnetCache[subnetID] = cidrBlock
	c.lastCacheUpdate = time.Now()
	c.subnetMutex.Unlock()

	return cidrBlock, nil
}

// GetSecurityGroupIDByName looks up a security group ID by its Name or GroupName
func (c *EC2Client) GetSecurityGroupIDByName(ctx context.Context, securityGroupName string) (string, error) {
	log := c.Logger.WithValues("securityGroupName", securityGroupName)
	log.Info("Looking up security group ID by name")

	// Check cache first
	c.sgMutex.RLock()
	if sgID, ok := c.sgCache[securityGroupName]; ok {
		// Check if cache is still valid
		if time.Since(c.lastCacheUpdate) < c.cacheExpiration {
			log.Info("Using cached security group ID", "securityGroupName", securityGroupName, "sgID", sgID)
			c.sgMutex.RUnlock()
			return sgID, nil
		}
	}
	c.sgMutex.RUnlock()

	// Cache miss or expired, fetch from AWS
	// Try with group-name first
	input := &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("group-name"),
				Values: []string{securityGroupName},
			},
		},
	}

	result, err := c.EC2.DescribeSecurityGroups(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe security groups: %v", err)
	}

	// If not found, try with the Name tag
	if len(result.SecurityGroups) == 0 {
		input = &ec2.DescribeSecurityGroupsInput{
			Filters: []types.Filter{
				{
					Name:   aws.String("tag:Name"),
					Values: []string{securityGroupName},
				},
			},
		}

		result, err = c.EC2.DescribeSecurityGroups(ctx, input)
		if err != nil {
			return "", fmt.Errorf("failed to describe security groups by Name tag: %v", err)
		}

		if len(result.SecurityGroups) == 0 {
			return "", fmt.Errorf("no security group found with name or Name tag: %s", securityGroupName)
		}
	}

	if len(result.SecurityGroups) > 1 {
		log.Info("Multiple security groups found with the same name, using the first one")
	}

	sgID := *result.SecurityGroups[0].GroupId
	log.Info("Found security group ID", "securityGroupID", sgID)

	// Update cache
	c.sgMutex.Lock()
	c.sgCache[securityGroupName] = sgID
	c.lastCacheUpdate = time.Now()
	c.sgMutex.Unlock()

	return sgID, nil
}

// WaitForENIDetachment waits for an ENI to be detached
func (c *EC2Client) WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error {
	log := c.Logger.WithValues("eniID", eniID)
	log.Info("Waiting for ENI detachment to complete", "timeout", timeout)

	// Use exponential backoff for checking detachment status
	backoff := wait.Backoff{
		Steps:    5,
		Duration: timeout / 5, // Divide the total timeout into steps
		Factor:   1.5,
		Jitter:   0.1,
	}

	var lastErr error
	err := wait.ExponentialBackoff(backoff, func() (bool, error) {
		// Check the ENI status
		eniInterface, err := c.DescribeENI(ctx, eniID)
		if err != nil {
			// Check if the error indicates the ENI doesn't exist
			// This can happen if the ENI was manually deleted outside of our control
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				log.Info("ENI no longer exists when checking detachment status, considering detachment successful", "error", err.Error())
				return true, nil
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying", "error", err.Error())
				lastErr = err
				return false, nil // Return nil error to continue retrying
			}

			// For other errors, fail immediately
			lastErr = err
			return false, err
		}

		if eniInterface == nil {
			log.Info("ENI no longer exists")
			return true, nil
		}

		// Check if the ENI is detached
		if eniInterface.Attachment == nil || eniInterface.Status == EC2v2NetworkInterfaceStatusAvailable {
			log.Info("ENI is now detached")
			return true, nil
		}

		// ENI is still attached, continue waiting
		log.Info("ENI is still attached, continuing to wait", "status", eniInterface.Status)
		lastErr = fmt.Errorf("ENI is still attached: %s", eniInterface.Status)
		return false, nil
	})

	if err != nil {
		if err == wait.ErrWaitTimeout {
			return fmt.Errorf("timed out waiting for ENI detachment: %v", lastErr)
		}
		return fmt.Errorf("failed to check ENI detachment status: %v", lastErr)
	}

	log.Info("ENI detachment confirmed")
	return nil
}
