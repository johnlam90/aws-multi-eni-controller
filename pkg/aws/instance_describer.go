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
	"k8s.io/apimachinery/pkg/util/wait"
)

// EC2InstanceDescriber handles EC2 instance description operations
type EC2InstanceDescriber struct {
	ec2Client *ec2.Client
	logger    logr.Logger
}

// NewEC2InstanceDescriber creates a new EC2InstanceDescriber
func NewEC2InstanceDescriber(ec2Client *ec2.Client, logger logr.Logger) *EC2InstanceDescriber {
	return &EC2InstanceDescriber{
		ec2Client: ec2Client,
		logger:    logger.WithName("instance-describer"),
	}
}

// DescribeInstance describes an EC2 instance
func (d *EC2InstanceDescriber) DescribeInstance(ctx context.Context, instanceID string) (*EC2Instance, error) {
	log := d.logger.WithValues("instanceID", instanceID)
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
		result, err = d.ec2Client.DescribeInstances(ctx, input)
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

// GetInstanceENIs gets all ENIs attached to an instance
func (d *EC2InstanceDescriber) GetInstanceENIs(ctx context.Context, instanceID string) (map[int]string, error) {
	log := d.logger.WithValues("instanceID", instanceID)
	log.V(1).Info("Getting all ENIs attached to instance")

	// Use DescribeNetworkInterfaces with a filter to get all ENIs attached to this instance
	input := &ec2.DescribeNetworkInterfacesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.instance-id"),
				Values: []string{instanceID},
			},
			{
				Name:   aws.String("attachment.status"),
				Values: []string{"attached"},
			},
		},
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
		result, err = d.ec2Client.DescribeNetworkInterfaces(ctx, input)
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
		return nil, fmt.Errorf("failed to describe network interfaces after retries: %v", lastErr)
	}

	// Build a map of device index to ENI ID
	eniMap := make(map[int]string)
	for _, eni := range result.NetworkInterfaces {
		if eni.Attachment != nil && eni.Attachment.DeviceIndex != nil {
			deviceIndex := int(*eni.Attachment.DeviceIndex)
			eniID := *eni.NetworkInterfaceId
			eniMap[deviceIndex] = eniID
			log.V(1).Info("Found attached ENI", "eniID", eniID, "deviceIndex", deviceIndex)
		}
	}

	log.Info("Retrieved all ENIs attached to instance", "count", len(eniMap), "eniMap", eniMap)
	return eniMap, nil
}
