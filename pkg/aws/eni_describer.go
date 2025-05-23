package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/go-logr/logr"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/aws/retry"
	"golang.org/x/time/rate"
)

// EC2ENIDescriber implements the ENIDescriber interface
type EC2ENIDescriber struct {
	// EC2 is the underlying AWS SDK v2 EC2 client
	EC2 *ec2.Client
	// Logger is used for structured logging
	Logger logr.Logger
	// Rate limiter for AWS API calls
	rateLimiter *rate.Limiter
}

// NewEC2ENIDescriber creates a new EC2ENIDescriber
func NewEC2ENIDescriber(ec2Client *ec2.Client, logger logr.Logger) *EC2ENIDescriber {
	return &EC2ENIDescriber{
		EC2:         ec2Client,
		Logger:      logger.WithName("ec2-eni-describer"),
		rateLimiter: rate.NewLimiter(rate.Limit(10), 20), // 10 requests per second, burst of 20
	}
}

// waitForRateLimit waits for rate limiter before making AWS API calls
func (d *EC2ENIDescriber) waitForRateLimit(ctx context.Context) error {
	return d.rateLimiter.Wait(ctx)
}

// DescribeENI describes an ENI
func (d *EC2ENIDescriber) DescribeENI(ctx context.Context, eniID string) (*EC2v2NetworkInterface, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Wait for rate limiter
	if err := d.waitForRateLimit(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait failed: %w", err)
	}

	log := d.Logger.WithValues("eniID", eniID)
	log.V(1).Info("Describing ENI")

	input := &ec2.DescribeNetworkInterfacesInput{
		NetworkInterfaceIds: []string{eniID},
	}

	var result *ec2.DescribeNetworkInterfacesOutput
	operation := fmt.Sprintf("describe ENI %s", eniID)
	err := retry.WithExponentialBackoff(
		ctx,
		log,
		operation,
		func() (bool, error) {
			var err error
			result, err = d.EC2.DescribeNetworkInterfaces(ctx, input)
			if err != nil {
				return false, err
			}
			return true, nil
		},
		retry.DefaultRetryableErrors,
		retry.DefaultBackoff(),
	)

	if err != nil {
		if strings.Contains(err.Error(), "InvalidNetworkInterfaceID.NotFound") {
			return nil, ENINotFoundError{ENIID: eniID}
		}
		return nil, err
	}

	if len(result.NetworkInterfaces) == 0 {
		return nil, ENINotFoundError{ENIID: eniID}
	}

	// Convert to our internal type
	eni := &EC2v2NetworkInterface{
		NetworkInterfaceID: *result.NetworkInterfaces[0].NetworkInterfaceId,
		Status:             EC2v2NetworkInterfaceStatus(result.NetworkInterfaces[0].Status),
	}

	// Add attachment information if available
	if result.NetworkInterfaces[0].Attachment != nil {
		eni.Attachment = &EC2v2NetworkInterfaceAttachment{
			AttachmentID:        *result.NetworkInterfaces[0].Attachment.AttachmentId,
			InstanceID:          *result.NetworkInterfaces[0].Attachment.InstanceId,
			DeviceIndex:         *result.NetworkInterfaces[0].Attachment.DeviceIndex,
			DeleteOnTermination: *result.NetworkInterfaces[0].Attachment.DeleteOnTermination,
			Status:              string(result.NetworkInterfaces[0].Attachment.Status),
		}
	}

	return eni, nil
}

// WaitForENIDetachment waits for an ENI to be detached
func (d *EC2ENIDescriber) WaitForENIDetachment(ctx context.Context, eniID string, timeout time.Duration) error {
	log := d.Logger.WithValues("eniID", eniID)
	log.Info("Waiting for ENI detachment to complete", "timeout", timeout)

	// Use exponential backoff for checking detachment status
	backoff := retry.DefaultBackoff()
	backoff.Duration = timeout / 5 // Divide the total timeout into steps
	backoff.Factor = 1.5

	operation := fmt.Sprintf("wait for ENI %s detachment", eniID)
	err := retry.WithExponentialBackoff(
		ctx,
		log,
		operation,
		func() (bool, error) {
			eniInterface, err := d.DescribeENI(ctx, eniID)
			if err != nil {
				// If the ENI is not found, it's considered detached
				if _, ok := err.(ENINotFoundError); ok {
					log.Info("ENI no longer exists")
					return true, nil
				}
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

			log.Info("ENI is still attached, waiting...",
				"status", eniInterface.Status,
				"attachmentID", eniInterface.Attachment.AttachmentID)
			return false, nil
		},
		retry.DefaultRetryableErrors,
		backoff,
	)

	if err != nil {
		return fmt.Errorf("failed to wait for ENI detachment: %w", err)
	}

	log.Info("ENI detachment completed")
	return nil
}
