package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/aws/retry"
	"golang.org/x/time/rate"
)

// EC2ENIManager implements the ENIManager interface
type EC2ENIManager struct {
	// EC2 is the underlying AWS SDK v2 EC2 client
	EC2 *ec2.Client
	// Logger is used for structured logging
	Logger logr.Logger
	// Rate limiter for AWS API calls
	rateLimiter *rate.Limiter
}

// NewEC2ENIManager creates a new EC2ENIManager
func NewEC2ENIManager(ec2Client *ec2.Client, logger logr.Logger) *EC2ENIManager {
	return &EC2ENIManager{
		EC2:         ec2Client,
		Logger:      logger.WithName("ec2-eni-manager"),
		rateLimiter: rate.NewLimiter(rate.Limit(10), 20), // 10 requests per second, burst of 20
	}
}

// waitForRateLimit waits for rate limiter before making AWS API calls
func (m *EC2ENIManager) waitForRateLimit(ctx context.Context) error {
	return m.rateLimiter.Wait(ctx)
}

// CreateENI creates a new ENI in AWS
func (m *EC2ENIManager) CreateENI(ctx context.Context, subnetID string, securityGroupIDs []string, description string, tags map[string]string) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := m.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := m.Logger.WithValues("subnetID", subnetID, "securityGroups", securityGroupIDs)
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

	result, err := m.EC2.CreateNetworkInterface(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to create ENI in subnet %s with security groups %v: %w",
			subnetID, securityGroupIDs, err)
	}

	eniID := *result.NetworkInterface.NetworkInterfaceId
	log.Info("Successfully created ENI", "eniID", eniID)
	return eniID, nil
}

// AttachENI attaches an ENI to an EC2 instance
func (m *EC2ENIManager) AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := m.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := m.Logger.WithValues("eniID", eniID, "instanceID", instanceID, "deviceIndex", deviceIndex)
	log.Info("Attaching ENI to instance")

	input := &ec2.AttachNetworkInterfaceInput{
		DeviceIndex:        aws.Int32(int32(deviceIndex)),
		InstanceId:         aws.String(instanceID),
		NetworkInterfaceId: aws.String(eniID),
	}

	result, err := m.EC2.AttachNetworkInterface(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to attach ENI %s to instance %s at device index %d: %w",
			eniID, instanceID, deviceIndex, err)
	}

	attachmentID := *result.AttachmentId
	log.Info("Successfully attached ENI", "attachmentID", attachmentID)

	// Set delete on termination if requested
	if deleteOnTermination {
		if err := m.setDeleteOnTermination(ctx, eniID, attachmentID); err != nil {
			log.Error(err, "Failed to set delete on termination", "eniID", eniID, "attachmentID", attachmentID)
			// Continue despite this error, as the ENI is already attached
		}
	}

	return attachmentID, nil
}

// setDeleteOnTermination sets the delete on termination attribute for an ENI attachment
func (m *EC2ENIManager) setDeleteOnTermination(ctx context.Context, eniID, attachmentID string) error {
	// Wait for rate limiter
	if err := m.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

	_, err := m.EC2.ModifyNetworkInterfaceAttribute(ctx, &ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eniID),
		Attachment: &types.NetworkInterfaceAttachmentChanges{
			AttachmentId:        aws.String(attachmentID),
			DeleteOnTermination: aws.Bool(true),
		},
	})
	return err
}

// DetachENI detaches an ENI from an EC2 instance
func (m *EC2ENIManager) DetachENI(ctx context.Context, attachmentID string, force bool) error {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := m.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := m.Logger.WithValues("attachmentID", attachmentID, "force", force)
	log.Info("Detaching ENI")

	input := &ec2.DetachNetworkInterfaceInput{
		AttachmentId: aws.String(attachmentID),
		Force:        aws.Bool(force),
	}

	// Use the retry package for exponential backoff
	operation := fmt.Sprintf("detach ENI with attachment ID %s", attachmentID)
	err := retry.WithExponentialBackoff(
		ctx,
		log,
		operation,
		func() (bool, error) {
			_, err := m.EC2.DetachNetworkInterface(ctx, input)
			if err != nil {
				// Check if the error indicates the attachment doesn't exist
				if strings.Contains(err.Error(), "InvalidAttachmentID.NotFound") {
					log.Info("ENI attachment no longer exists, considering detachment successful")
					return true, nil
				}
				return false, err
			}
			return true, nil
		},
		retry.DefaultRetryableErrors,
		retry.DefaultBackoff(),
	)

	if err != nil {
		return err
	}

	log.Info("Successfully detached ENI")
	return nil
}

// DeleteENI deletes an ENI
func (m *EC2ENIManager) DeleteENI(ctx context.Context, eniID string) error {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := m.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := m.Logger.WithValues("eniID", eniID)
	log.Info("Deleting ENI")

	input := &ec2.DeleteNetworkInterfaceInput{
		NetworkInterfaceId: aws.String(eniID),
	}

	// Use the retry package for exponential backoff
	operation := fmt.Sprintf("delete ENI %s", eniID)
	err := retry.WithExponentialBackoff(
		ctx,
		log,
		operation,
		func() (bool, error) {
			_, err := m.EC2.DeleteNetworkInterface(ctx, input)
			if err != nil {
				// Check if the error indicates the ENI doesn't exist
				if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
					log.Info("ENI no longer exists, considering deletion successful")
					return true, nil
				}
				return false, err
			}
			return true, nil
		},
		retry.DefaultRetryableErrors,
		retry.DefaultBackoff(),
	)

	if err != nil {
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			return ENINotFoundError{ENIID: eniID}
		}
		return err
	}

	log.Info("Successfully deleted ENI")
	return nil
}
