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
	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Custom error types for better error handling
type ENINotFoundError struct {
	ENIID string
}

func (e ENINotFoundError) Error() string {
	return fmt.Sprintf("ENI %s not found", e.ENIID)
}

type AttachmentNotFoundError struct {
	AttachmentID string
}

func (e AttachmentNotFoundError) Error() string {
	return fmt.Sprintf("attachment %s not found", e.AttachmentID)
}

type SubnetNotFoundError struct {
	SubnetName string
}

func (e SubnetNotFoundError) Error() string {
	return fmt.Sprintf("subnet %s not found", e.SubnetName)
}

// EC2ClientOptimized wraps the AWS EC2 client with additional functionality using SDK v2.
// It provides methods for creating, attaching, detaching, and deleting ENIs,
// as well as looking up subnet and security group IDs by name.
type EC2ClientOptimized struct {
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

	// Rate limiter for AWS API calls
	rateLimiter *rate.Limiter
}

// NewEC2ClientOptimized creates a new optimized EC2 client using AWS SDK v2
func NewEC2ClientOptimized(ctx context.Context, region string, logger logr.Logger) (*EC2ClientOptimized, error) {
	// Create AWS config
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &EC2ClientOptimized{
		EC2:             ec2.NewFromConfig(cfg),
		Logger:          logger.WithName("aws-ec2-optimized"),
		subnetCache:     make(map[string]string, 32), // Pre-allocate for typical usage
		subnetNameCache: make(map[string]string, 32), // Pre-allocate for typical usage
		sgCache:         make(map[string]string, 16), // Pre-allocate for typical usage
		cacheExpiration: 5 * time.Minute,             // Cache expires after 5 minutes
		lastCacheUpdate: time.Now(),
		rateLimiter:     rate.NewLimiter(rate.Limit(10), 20), // 10 requests per second, burst of 20
	}, nil
}

// waitForRateLimit waits for rate limiter before making AWS API calls
func (c *EC2ClientOptimized) waitForRateLimit(ctx context.Context) error {
	return c.rateLimiter.Wait(ctx)
}

// CreateENI creates a new ENI in AWS with context timeout
func (c *EC2ClientOptimized) CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := c.Logger.WithValues("subnetID", subnetID, "securityGroups", securityGroupIDs)
	log.Info("Creating ENI")

	// Convert tags map to AWS tags
	var tagSpecs []types.TagSpecification
	if len(tags) > 0 {
		awsTags := make([]types.Tag, 0, len(tags)) // Pre-allocate slice
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
		return "", fmt.Errorf("failed to create ENI in subnet %s with security groups %v: %w",
			subnetID, securityGroupIDs, err)
	}

	eniID := *result.NetworkInterface.NetworkInterfaceId
	log.Info("Successfully created ENI", "eniID", eniID)
	return eniID, nil
}

// AttachENI attaches an ENI to an EC2 instance with context timeout
func (c *EC2ClientOptimized) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := c.Logger.WithValues("eniID", eniID, "instanceID", instanceID, "deviceIndex", deviceIndex)
	log.Info("Attaching ENI to instance")

	input := &ec2.AttachNetworkInterfaceInput{
		DeviceIndex:        aws.Int32(int32(deviceIndex)),
		InstanceId:         aws.String(instanceID),
		NetworkInterfaceId: aws.String(eniID),
	}

	result, err := c.EC2.AttachNetworkInterface(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to attach ENI %s to instance %s at device index %d: %w",
			eniID, instanceID, deviceIndex, err)
	}

	attachmentID := *result.AttachmentId
	log.Info("Successfully attached ENI", "attachmentID", attachmentID)

	// Set delete on termination attribute if requested
	if deleteOnTermination {
		if err := c.setDeleteOnTermination(ctx, eniID, attachmentID); err != nil {
			// Log the error but don't fail the attachment
			log.Error(err, "Failed to set delete on termination, but ENI is still attached", "attachmentID", attachmentID)
			log.Info("WARNING: ENI will not be automatically deleted when the instance terminates", "eniID", eniID)
		} else {
			log.Info("Set delete on termination to true", "attachmentID", attachmentID)
		}
	}

	return attachmentID, nil
}

// setDeleteOnTermination sets the delete on termination attribute for an ENI attachment
func (c *EC2ClientOptimized) setDeleteOnTermination(ctx context.Context, eniID, attachmentID string) error {
	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

	_, err := c.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eniID),
		Attachment: &types.NetworkInterfaceAttachmentChanges{
			AttachmentId:        aws.String(attachmentID),
			DeleteOnTermination: aws.Bool(true),
		},
	})
	return err
}

// DetachENI detaches an ENI from an EC2 instance with context timeout
func (c *EC2ClientOptimized) DetachENI(ctx context.Context, attachmentID string, force bool) error {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := c.Logger.WithValues("attachmentID", attachmentID, "force", force)
	log.Info("Detaching ENI")

	input := &ec2.DetachNetworkInterfaceInput{
		AttachmentId: aws.String(attachmentID),
		Force:        aws.Bool(force),
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
			if strings.Contains(err.Error(), "InvalidAttachmentID.NotFound") {
				log.Info("ENI attachment no longer exists, considering detachment successful")
				return true, nil
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying")
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
		if strings.Contains(lastErr.Error(), "InvalidAttachmentID.NotFound") {
			return AttachmentNotFoundError{AttachmentID: attachmentID}
		}
		return fmt.Errorf("failed to detach ENI attachment %s after retries: %w", attachmentID, lastErr)
	}

	log.Info("Successfully detached ENI")
	return nil
}

// DeleteENI deletes an ENI with context timeout
func (c *EC2ClientOptimized) DeleteENI(ctx context.Context, eniID string) error {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

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
			if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
				log.Info("ENI no longer exists, considering deletion successful")
				return true, nil
			}

			// Check if this is a rate limit error
			if strings.Contains(err.Error(), "RequestLimitExceeded") ||
				strings.Contains(err.Error(), "Throttling") {
				log.Info("AWS API rate limit exceeded, retrying")
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
		if strings.Contains(lastErr.Error(), "InvalidNetworkInterfaceID.NotFound") {
			return ENINotFoundError{ENIID: eniID}
		}
		return fmt.Errorf("failed to delete ENI %s after retries: %w", eniID, lastErr)
	}

	log.Info("Successfully deleted ENI")
	return nil
}

// DescribeENI describes an ENI with context timeout
func (c *EC2ClientOptimized) DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

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
				log.Info("AWS API rate limit exceeded, retrying")
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
		if strings.Contains(lastErr.Error(), "InvalidNetworkInterfaceID.NotFound") {
			return nil, ENINotFoundError{ENIID: eniID}
		}
		return nil, fmt.Errorf("failed to describe ENI %s after retries: %w", eniID, lastErr)
	}

	if len(result.NetworkInterfaces) == 0 {
		return nil, ENINotFoundError{ENIID: eniID}
	}

	// Convert to our internal type
	eni := &EC2v2NetworkInterface{
		NetworkInterfaceID: *result.NetworkInterfaces[0].NetworkInterfaceId,
		Status:             EC2v2NetworkInterfaceStatus(result.NetworkInterfaces[0].Status),
	}

	// Add attachment if it exists and is attached
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
		eni.Attachment = nil
		if result.NetworkInterfaces[0].Attachment != nil &&
			(result.NetworkInterfaces[0].Status == "available" ||
				result.NetworkInterfaces[0].Attachment.Status == "detached") {
			log.Info("ENI has attachment info but is in available state or detached status, considering it detached")
		}
	}

	return eni, nil
}

// GetSubnetIDByName looks up a subnet ID by its Name tag with caching
func (c *EC2ClientOptimized) GetSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log := c.Logger.WithValues("subnetName", subnetName)
	log.Info("Looking up subnet ID by name")

	// Check cache first
	c.subnetMutex.RLock()
	if subnetID, ok := c.subnetNameCache[subnetName]; ok {
		// Check if cache is still valid
		if time.Since(c.lastCacheUpdate) < c.cacheExpiration {
			log.Info("Using cached subnet ID", "subnetID", subnetID)
			c.subnetMutex.RUnlock()
			return subnetID, nil
		}
	}
	c.subnetMutex.RUnlock()

	// Wait for rate limiter
	if err := c.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

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
		return "", fmt.Errorf("failed to describe subnets for name %s: %w", subnetName, err)
	}

	if len(result.Subnets) == 0 {
		return "", SubnetNotFoundError{SubnetName: subnetName}
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
