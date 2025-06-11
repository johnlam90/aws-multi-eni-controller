// Package aws provides utilities for interacting with AWS services,
// particularly EC2 for managing Elastic Network Interfaces (ENIs).
//
// This package uses AWS SDK v2 for all AWS interactions, providing improved
// performance, better error handling, and more modern API design compared to v1.
package aws

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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

// NewEC2Client creates a new EC2 client using AWS SDK v2 with cloud-native authentication
func NewEC2Client(ctx context.Context, region string, logger logr.Logger) (*EC2Client, error) {
	log := logger.WithName("aws-ec2-client")
	log.Info("Creating AWS EC2 client with cloud-native authentication")

	// Create AWS config with multiple authentication strategies
	cfg, err := createCloudNativeAWSConfig(ctx, region, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create cloud-native AWS config: %v", err)
	}

	return &EC2Client{
		EC2:             ec2.NewFromConfig(cfg),
		Logger:          log,
		subnetCache:     make(map[string]string),
		subnetNameCache: make(map[string]string),
		sgCache:         make(map[string]string),
		cacheExpiration: 5 * time.Minute, // Cache expires after 5 minutes
		lastCacheUpdate: time.Now(),
	}, nil
}

// createCloudNativeAWSConfig creates AWS config with multiple authentication strategies
// This solves the IMDSv2 chicken-and-egg problem by using cloud-native approaches
func createCloudNativeAWSConfig(ctx context.Context, region string, log logr.Logger) (aws.Config, error) {
	log.Info("Attempting cloud-native AWS authentication", "region", region)

	// Strategy 1: Try IRSA (IAM Roles for Service Accounts) first
	// This is the most cloud-native approach and works without IMDS
	if cfg, err := tryIRSAAuthentication(ctx, region, log); err == nil {
		log.Info("Successfully authenticated using IRSA (IAM Roles for Service Accounts)")
		return cfg, nil
	} else {
		log.V(1).Info("IRSA authentication failed", "error", err.Error())
	}

	// Strategy 2: Try environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
	if cfg, err := tryEnvironmentAuthentication(ctx, region, log); err == nil {
		log.Info("Successfully authenticated using environment variables")
		return cfg, nil
	} else {
		log.V(1).Info("Environment variable authentication failed", "error", err.Error())
	}

	// Strategy 3: Try standard AWS config (includes IMDS with fallback)
	// This is the fallback that should work in most cases
	if cfg, err := tryStandardAuthentication(ctx, region, log); err == nil {
		log.Info("Successfully authenticated using standard AWS config")
		return cfg, nil
	} else {
		log.V(1).Info("Standard authentication failed", "error", err.Error())
	}

	// Strategy 4: Try IMDS with custom configuration (last resort)
	if cfg, err := tryCustomIMDSAuthentication(ctx, region, log); err == nil {
		log.Info("Successfully authenticated using custom IMDS configuration")
		return cfg, nil
	} else {
		log.V(1).Info("Custom IMDS authentication failed", "error", err.Error())
	}

	// Strategy 5: Fallback to basic config without credential testing
	// This allows the controller to start even if credentials are not immediately available
	log.Info("All authentication strategies with credential testing failed, falling back to basic config")
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load basic AWS config: %v", err)
	}

	log.Info("Using basic AWS config - credentials will be tested during first API call")
	return cfg, nil
}

// tryIRSAAuthentication attempts to authenticate using IRSA (IAM Roles for Service Accounts)
// This is the most cloud-native approach and works without IMDS
func tryIRSAAuthentication(ctx context.Context, region string, log logr.Logger) (aws.Config, error) {
	log.V(1).Info("Attempting IRSA authentication")

	// Check if we're running in a Kubernetes environment with IRSA
	tokenFile := os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")
	roleArn := os.Getenv("AWS_ROLE_ARN")

	if tokenFile == "" || roleArn == "" {
		return aws.Config{}, fmt.Errorf("IRSA environment variables not found (AWS_WEB_IDENTITY_TOKEN_FILE, AWS_ROLE_ARN)")
	}

	log.Info("Found IRSA configuration", "tokenFile", tokenFile, "roleArn", roleArn)

	// Create config with web identity token credentials
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithWebIdentityRoleCredentialOptions(func(options *stscreds.WebIdentityRoleOptions) {
			options.RoleARN = roleArn
			options.TokenRetriever = stscreds.IdentityTokenFile(tokenFile)
		}),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load IRSA config: %v", err)
	}

	// Test the credentials by making a simple STS call
	stsClient := sts.NewFromConfig(cfg)
	_, err = stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return aws.Config{}, fmt.Errorf("IRSA credentials test failed: %v", err)
	}

	log.Info("IRSA authentication successful")
	return cfg, nil
}

// tryEnvironmentAuthentication attempts to authenticate using environment variables
func tryEnvironmentAuthentication(ctx context.Context, region string, log logr.Logger) (aws.Config, error) {
	log.V(1).Info("Attempting environment variable authentication")

	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if accessKey == "" || secretKey == "" {
		return aws.Config{}, fmt.Errorf("AWS environment variables not found (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)")
	}

	log.Info("Found AWS environment variables")

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load environment config: %v", err)
	}

	// Test the credentials
	stsClient := sts.NewFromConfig(cfg)
	_, err = stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return aws.Config{}, fmt.Errorf("environment credentials test failed: %v", err)
	}

	log.Info("Environment variable authentication successful")
	return cfg, nil
}

// tryStandardAuthentication attempts standard AWS authentication
func tryStandardAuthentication(ctx context.Context, region string, log logr.Logger) (aws.Config, error) {
	log.V(1).Info("Attempting standard AWS authentication")

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load standard config: %v", err)
	}

	// Test the credentials
	stsClient := sts.NewFromConfig(cfg)
	_, err = stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return aws.Config{}, fmt.Errorf("standard credentials test failed: %v", err)
	}

	log.Info("Standard authentication successful")
	return cfg, nil
}

// tryCustomIMDSAuthentication attempts IMDS authentication with custom configuration
func tryCustomIMDSAuthentication(ctx context.Context, region string, log logr.Logger) (aws.Config, error) {
	log.V(1).Info("Attempting custom IMDS authentication")

	// Try with different IMDS configurations
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithEC2IMDSClientEnableState(imds.ClientEnabled),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load custom IMDS config: %v", err)
	}

	// Test the credentials
	stsClient := sts.NewFromConfig(cfg)
	_, err = stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return aws.Config{}, fmt.Errorf("custom IMDS credentials test failed: %v", err)
	}

	log.Info("Custom IMDS authentication successful")
	return cfg, nil
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

// ConfigureIMDSHopLimit automatically configures the IMDS hop limit for the current instance
// to ensure IMDS requests work from containerized environments
func (c *EC2Client) ConfigureIMDSHopLimit(ctx context.Context) error {
	// Check if auto-configuration is enabled
	autoConfigureStr := os.Getenv("IMDS_AUTO_CONFIGURE_HOP_LIMIT")
	if autoConfigureStr != "true" {
		c.Logger.V(1).Info("IMDS hop limit auto-configuration is disabled")
		return nil
	}

	// Get the desired hop limit from environment variable
	hopLimitStr := os.Getenv("IMDS_HOP_LIMIT")
	if hopLimitStr == "" {
		hopLimitStr = "2" // Default to 2 for container environments
	}

	hopLimit, err := strconv.Atoi(hopLimitStr)
	if err != nil {
		return fmt.Errorf("invalid IMDS_HOP_LIMIT value '%s': %v", hopLimitStr, err)
	}

	c.Logger.Info("Starting IMDS hop limit configuration", "desiredHopLimit", hopLimit)

	// Try to configure IMDS hop limit using multiple strategies
	return c.configureIMDSWithFallback(ctx, int32(hopLimit))
}

// configureIMDSWithFallback attempts to configure IMDS hop limit using multiple strategies
// This method now uses cloud-native authentication to solve the chicken-and-egg problem
func (c *EC2Client) configureIMDSWithFallback(ctx context.Context, hopLimit int32) error {
	c.Logger.Info("Starting cloud-native IMDS configuration", "hopLimit", hopLimit)

	// Strategy 1: Use cloud-native authentication to configure IMDS for current instance
	if err := c.tryCloudNativeIMDSConfiguration(ctx, hopLimit); err == nil {
		c.Logger.Info("Successfully configured IMDS hop limit using cloud-native authentication")
		return nil
	}

	c.Logger.Info("Cloud-native IMDS configuration failed, trying fallback methods")

	// Strategy 2: Try the standard approach (works if IMDS is already accessible)
	if err := c.tryStandardIMDSConfiguration(ctx, hopLimit); err == nil {
		c.Logger.Info("Successfully configured IMDS hop limit using standard method")
		return nil
	}

	// Strategy 3: Use private IP lookup to find instance ID
	if err := c.tryPrivateIPBasedConfiguration(ctx, hopLimit); err == nil {
		c.Logger.Info("Successfully configured IMDS hop limit using private IP lookup")
		return nil
	}

	// Strategy 4: Configure all instances in the current VPC (last resort)
	if err := c.tryVPCWideConfiguration(ctx, hopLimit); err == nil {
		c.Logger.Info("Successfully configured IMDS hop limit using VPC-wide approach")
		return nil
	}

	c.Logger.Info("All IMDS configuration strategies failed, continuing without IMDS configuration")
	return nil // Don't fail controller startup
}

// tryCloudNativeIMDSConfiguration uses cloud-native authentication to configure IMDS
// This solves the chicken-and-egg problem by using IRSA or other non-IMDS authentication
func (c *EC2Client) tryCloudNativeIMDSConfiguration(ctx context.Context, hopLimit int32) error {
	c.Logger.Info("Attempting cloud-native IMDS configuration")

	// Create a separate EC2 client with cloud-native authentication
	// This bypasses the IMDS chicken-and-egg problem
	cfg, err := createCloudNativeAWSConfig(ctx, "us-east-1", c.Logger)
	if err != nil {
		return fmt.Errorf("failed to create cloud-native AWS config: %v", err)
	}

	// Create a new EC2 client with the cloud-native config
	cloudNativeEC2 := ec2.NewFromConfig(cfg)

	// Strategy 1: Try to get current instance ID using private IP lookup
	privateIP, err := c.getPrivateIPFromNetworkInterface()
	if err != nil {
		return fmt.Errorf("failed to get private IP: %v", err)
	}

	// Find instance by private IP using cloud-native authentication
	instanceID, err := c.findInstanceByPrivateIPWithClient(ctx, cloudNativeEC2, privateIP)
	if err != nil {
		return fmt.Errorf("failed to find instance by private IP: %v", err)
	}

	c.Logger.Info("Found current instance using cloud-native authentication", "instanceID", instanceID, "privateIP", privateIP)

	// Configure IMDS hop limit for this instance
	return c.configureInstanceIMDSWithClient(ctx, cloudNativeEC2, instanceID, hopLimit)
}

// findInstanceByPrivateIPWithClient finds an instance by private IP using a specific EC2 client
func (c *EC2Client) findInstanceByPrivateIPWithClient(ctx context.Context, ec2Client *ec2.Client, privateIP string) (string, error) {
	c.Logger.V(1).Info("Looking up instance by private IP using cloud-native client", "privateIP", privateIP)

	// Use EC2 API to find instance with this private IP
	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("private-ip-address"),
				Values: []string{privateIP},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending"},
			},
		},
	}

	result, err := ec2Client.DescribeInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe instances by private IP: %v", err)
	}

	// Find the instance
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				c.Logger.V(1).Info("Found instance by private IP using cloud-native client", "instanceID", *instance.InstanceId, "privateIP", privateIP)
				return *instance.InstanceId, nil
			}
		}
	}

	return "", fmt.Errorf("no instance found with private IP %s", privateIP)
}

// configureInstanceIMDSWithClient configures IMDS hop limit for a specific instance using a specific EC2 client
func (c *EC2Client) configureInstanceIMDSWithClient(ctx context.Context, ec2Client *ec2.Client, instanceID string, hopLimit int32) error {
	c.Logger.Info("Configuring IMDS for instance using cloud-native client", "instanceID", instanceID, "hopLimit", hopLimit)

	// Check current IMDS configuration
	currentHopLimit, err := c.getCurrentIMDSHopLimitWithClient(ctx, ec2Client, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get current IMDS hop limit: %v", err)
	}

	// Only modify if the current hop limit is different from desired
	if currentHopLimit == hopLimit {
		c.Logger.Info("IMDS hop limit is already correctly configured", "instanceID", instanceID, "hopLimit", currentHopLimit)
		return nil
	}

	c.Logger.Info("Updating IMDS hop limit for container compatibility",
		"instanceID", instanceID,
		"currentHopLimit", currentHopLimit,
		"newHopLimit", hopLimit)

	// Modify the instance metadata options
	err = c.modifyInstanceMetadataOptionsWithClient(ctx, ec2Client, instanceID, hopLimit)
	if err != nil {
		return fmt.Errorf("failed to modify IMDS hop limit: %v", err)
	}

	c.Logger.Info("Successfully updated IMDS hop limit using cloud-native client", "instanceID", instanceID, "hopLimit", hopLimit)
	return nil
}

// getCurrentIMDSHopLimitWithClient gets the current IMDS hop limit using a specific EC2 client
func (c *EC2Client) getCurrentIMDSHopLimitWithClient(ctx context.Context, ec2Client *ec2.Client, instanceID string) (int32, error) {
	// Use DescribeInstances to get the metadata options
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := ec2Client.DescribeInstances(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to describe instance: %v", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return 0, fmt.Errorf("instance not found: %s", instanceID)
	}

	instance := result.Reservations[0].Instances[0]
	if instance.MetadataOptions == nil {
		return 1, nil // Default hop limit
	}

	hopLimit := instance.MetadataOptions.HttpPutResponseHopLimit
	if hopLimit == nil {
		return 1, nil // Default hop limit
	}

	return *hopLimit, nil
}

// modifyInstanceMetadataOptionsWithClient modifies instance metadata options using a specific EC2 client
func (c *EC2Client) modifyInstanceMetadataOptionsWithClient(ctx context.Context, ec2Client *ec2.Client, instanceID string, hopLimit int32) error {
	input := &ec2.ModifyInstanceMetadataOptionsInput{
		InstanceId:              aws.String(instanceID),
		HttpPutResponseHopLimit: aws.Int32(hopLimit),
		HttpTokens:              types.HttpTokensStateRequired, // Enforce IMDSv2
		HttpEndpoint:            types.InstanceMetadataEndpointStateEnabled,
		InstanceMetadataTags:    types.InstanceMetadataTagsStateDisabled,
	}

	_, err := ec2Client.ModifyInstanceMetadataOptions(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to modify instance metadata options: %v", err)
	}

	return nil
}

// tryStandardIMDSConfiguration attempts the standard IMDS configuration approach
func (c *EC2Client) tryStandardIMDSConfiguration(ctx context.Context, hopLimit int32) error {
	// Get the current instance ID from IMDS
	instanceID, err := c.getCurrentInstanceID(ctx)
	if err != nil {
		return fmt.Errorf("failed to get instance ID: %v", err)
	}

	return c.configureInstanceIMDS(ctx, instanceID, hopLimit)
}

// tryKubernetesBasedConfiguration attempts to configure IMDS using Kubernetes node metadata
func (c *EC2Client) tryKubernetesBasedConfiguration(_ context.Context, _ int32) error {
	// This would require Kubernetes API access, which we don't have in the EC2Client
	// For now, we'll skip this strategy
	return fmt.Errorf("Kubernetes-based configuration not implemented")
}

// tryPrivateIPBasedConfiguration attempts to configure IMDS using private IP lookup
func (c *EC2Client) tryPrivateIPBasedConfiguration(ctx context.Context, hopLimit int32) error {
	// Get the private IP of this instance from the network interface
	privateIP, err := c.getPrivateIPFromNetworkInterface()
	if err != nil {
		return fmt.Errorf("failed to get private IP: %v", err)
	}

	// Find instance by private IP
	instanceID, err := c.findInstanceByPrivateIP(ctx)
	if err != nil {
		return fmt.Errorf("failed to find instance by private IP: %v", err)
	}

	c.Logger.Info("Found instance ID using private IP lookup", "instanceID", instanceID, "privateIP", privateIP)
	return c.configureInstanceIMDS(ctx, instanceID, hopLimit)
}

// tryVPCWideConfiguration attempts to configure IMDS for all instances in the VPC (last resort)
func (c *EC2Client) tryVPCWideConfiguration(ctx context.Context, hopLimit int32) error {
	// Check if aggressive configuration is enabled
	aggressiveConfig := os.Getenv("IMDS_AGGRESSIVE_CONFIGURATION")
	if aggressiveConfig != "true" {
		c.Logger.Info("Aggressive IMDS configuration is disabled, skipping VPC-wide configuration")
		return fmt.Errorf("aggressive configuration disabled")
	}

	// This is a last resort strategy - configure IMDS for all instances that might need it
	// We'll look for instances that have hop limit 1 and are in running state

	c.Logger.Info("Attempting VPC-wide IMDS configuration as last resort")

	// Get all running instances in the region
	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running"},
			},
		},
	}

	result, err := c.EC2.DescribeInstances(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to describe instances: %v", err)
	}

	var configuredCount int
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId == nil {
				continue
			}

			instanceID := *instance.InstanceId

			// Check if this instance needs IMDS configuration
			currentHopLimit, err := c.getCurrentIMDSHopLimit(ctx, instanceID)
			if err != nil {
				c.Logger.V(1).Info("Failed to get IMDS hop limit for instance", "instanceID", instanceID, "error", err.Error())
				continue
			}

			if currentHopLimit == hopLimit {
				continue // Already configured
			}

			// Configure this instance
			if err := c.configureInstanceIMDS(ctx, instanceID, hopLimit); err != nil {
				c.Logger.V(1).Info("Failed to configure IMDS for instance", "instanceID", instanceID, "error", err.Error())
				continue
			}

			configuredCount++
			c.Logger.Info("Configured IMDS hop limit for instance", "instanceID", instanceID, "hopLimit", hopLimit)

			// Limit the number of instances we configure to avoid excessive API calls
			if configuredCount >= 10 {
				c.Logger.Info("Configured IMDS for maximum number of instances, stopping")
				break
			}
		}
		if configuredCount >= 10 {
			break
		}
	}

	if configuredCount == 0 {
		return fmt.Errorf("no instances were configured")
	}

	c.Logger.Info("VPC-wide IMDS configuration completed", "configuredCount", configuredCount)
	return nil
}

// configureInstanceIMDS configures IMDS hop limit for a specific instance
func (c *EC2Client) configureInstanceIMDS(ctx context.Context, instanceID string, hopLimit int32) error {
	c.Logger.Info("Configuring IMDS for instance", "instanceID", instanceID, "hopLimit", hopLimit)

	// Check current IMDS configuration
	currentHopLimit, err := c.getCurrentIMDSHopLimit(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get current IMDS hop limit: %v", err)
	}

	// Only modify if the current hop limit is different from desired
	if currentHopLimit == hopLimit {
		c.Logger.Info("IMDS hop limit is already correctly configured", "instanceID", instanceID, "hopLimit", currentHopLimit)
		return nil
	}

	c.Logger.Info("Updating IMDS hop limit for container compatibility",
		"instanceID", instanceID,
		"currentHopLimit", currentHopLimit,
		"newHopLimit", hopLimit)

	// Modify the instance metadata options
	err = c.modifyInstanceMetadataOptions(ctx, instanceID, hopLimit)
	if err != nil {
		return fmt.Errorf("failed to modify IMDS hop limit: %v", err)
	}

	c.Logger.Info("Successfully updated IMDS hop limit", "instanceID", instanceID, "hopLimit", hopLimit)
	return nil
}

// getCurrentInstanceID retrieves the current instance ID from IMDS or environment
func (c *EC2Client) getCurrentInstanceID(ctx context.Context) (string, error) {
	// Check if instance ID is available in environment (some container platforms set this)
	if instanceID := os.Getenv("EC2_INSTANCE_ID"); instanceID != "" {
		c.Logger.V(1).Info("Using instance ID from environment variable", "instanceID", instanceID)
		return instanceID, nil
	}

	// Try to get instance ID from node name if running in Kubernetes
	if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
		// Extract instance ID from node name if it follows AWS pattern
		if strings.HasPrefix(nodeName, "ip-") && strings.HasSuffix(nodeName, ".ec2.internal") {
			c.Logger.V(1).Info("Detected Kubernetes environment, but cannot determine instance ID from node name alone")
			// We would need to query Kubernetes API to get the provider ID
			// For now, fall back to IMDS
		}
	}

	// Try IMDS with multiple strategies for node replacement scenarios
	return c.getInstanceIDWithFallback(ctx)
}

// getInstanceIDWithFallback attempts to get instance ID with multiple fallback strategies
func (c *EC2Client) getInstanceIDWithFallback(ctx context.Context) (string, error) {
	// Strategy 1: Try IMDS with current configuration
	instanceID, err := c.tryIMDSInstanceID(ctx)
	if err == nil {
		c.Logger.V(1).Info("Retrieved instance ID from IMDS", "instanceID", instanceID)
		return instanceID, nil
	}

	c.Logger.Info("Failed to get instance ID from IMDS, trying alternative methods", "error", err.Error())

	// Strategy 2: Try to configure IMDS hop limit first, then retry
	if c.tryConfigureIMDSForNewNode(ctx) {
		instanceID, err := c.tryIMDSInstanceID(ctx)
		if err == nil {
			c.Logger.Info("Retrieved instance ID after configuring IMDS hop limit", "instanceID", instanceID)
			return instanceID, nil
		}
	}

	// Strategy 3: Use EC2 API to find instance by private IP (if available)
	if instanceID, err := c.findInstanceByPrivateIP(ctx); err == nil {
		c.Logger.Info("Retrieved instance ID by private IP lookup", "instanceID", instanceID)
		return instanceID, nil
	}

	return "", fmt.Errorf("failed to determine instance ID using all available methods")
}

// tryIMDSInstanceID attempts to get instance ID from IMDS
func (c *EC2Client) tryIMDSInstanceID(ctx context.Context) (string, error) {
	// Use AWS SDK's built-in IMDS client to get the instance ID
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config for IMDS: %v", err)
	}

	// Create an IMDS client using the same configuration
	imdsClient := imds.NewFromConfig(cfg)

	// Get the instance ID from IMDS
	result, err := imdsClient.GetMetadata(ctx, &imds.GetMetadataInput{
		Path: "instance-id",
	})
	if err != nil {
		return "", fmt.Errorf("failed to get instance ID from IMDS: %v", err)
	}

	// Read the content from the response
	defer result.Content.Close()
	content, err := io.ReadAll(result.Content)
	if err != nil {
		return "", fmt.Errorf("failed to read instance ID from IMDS response: %v", err)
	}

	instanceID := strings.TrimSpace(string(content))
	return instanceID, nil
}

// tryConfigureIMDSForNewNode attempts to configure IMDS hop limit for a new node
// This is used when the controller detects it's running on a new instance
func (c *EC2Client) tryConfigureIMDSForNewNode(ctx context.Context) bool {
	c.Logger.Info("Attempting to configure IMDS hop limit for new node")

	// Try to get instance ID using EC2 API instead of IMDS
	instanceID, err := c.findInstanceByPrivateIP(ctx)
	if err != nil {
		c.Logger.Info("Could not determine instance ID for IMDS configuration", "error", err.Error())
		return false
	}

	// Get the desired hop limit from environment variable
	hopLimitStr := os.Getenv("IMDS_HOP_LIMIT")
	if hopLimitStr == "" {
		hopLimitStr = "2" // Default to 2 for container environments
	}

	hopLimit, err := strconv.Atoi(hopLimitStr)
	if err != nil {
		c.Logger.Error(err, "Invalid IMDS_HOP_LIMIT value", "value", hopLimitStr)
		return false
	}

	// Check current IMDS configuration
	currentHopLimit, err := c.getCurrentIMDSHopLimit(ctx, instanceID)
	if err != nil {
		c.Logger.Error(err, "Failed to get current IMDS hop limit")
		return false
	}

	// Only modify if the current hop limit is different from desired
	if currentHopLimit == int32(hopLimit) {
		c.Logger.Info("IMDS hop limit is already correctly configured", "instanceID", instanceID, "hopLimit", currentHopLimit)
		return true
	}

	c.Logger.Info("Updating IMDS hop limit for new node",
		"instanceID", instanceID,
		"currentHopLimit", currentHopLimit,
		"newHopLimit", hopLimit)

	// Modify the instance metadata options
	err = c.modifyInstanceMetadataOptions(ctx, instanceID, int32(hopLimit))
	if err != nil {
		c.Logger.Error(err, "Failed to modify IMDS hop limit for new node")
		return false
	}

	c.Logger.Info("Successfully updated IMDS hop limit for new node", "instanceID", instanceID, "hopLimit", hopLimit)
	return true
}

// findInstanceByPrivateIP attempts to find the current instance ID by looking up the private IP
func (c *EC2Client) findInstanceByPrivateIP(ctx context.Context) (string, error) {
	// Get the private IP of this instance from the network interface
	privateIP, err := c.getPrivateIPFromNetworkInterface()
	if err != nil {
		return "", fmt.Errorf("failed to get private IP: %v", err)
	}

	if privateIP == "" {
		return "", fmt.Errorf("no private IP found")
	}

	c.Logger.V(1).Info("Looking up instance by private IP", "privateIP", privateIP)

	// Use EC2 API to find instance with this private IP
	input := &ec2.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("private-ip-address"),
				Values: []string{privateIP},
			},
			{
				Name:   aws.String("instance-state-name"),
				Values: []string{"running", "pending"},
			},
		},
	}

	result, err := c.EC2.DescribeInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe instances by private IP: %v", err)
	}

	// Find the instance
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				c.Logger.V(1).Info("Found instance by private IP", "instanceID", *instance.InstanceId, "privateIP", privateIP)
				return *instance.InstanceId, nil
			}
		}
	}

	return "", fmt.Errorf("no instance found with private IP %s", privateIP)
}

// getPrivateIPFromNetworkInterface gets the private IP address of the current instance
func (c *EC2Client) getPrivateIPFromNetworkInterface() (string, error) {
	// Try to get the private IP from the default network interface
	// This is a simple approach that works for most cases

	// First, try to get it from the environment if available
	if privateIP := os.Getenv("PRIVATE_IP"); privateIP != "" {
		c.Logger.V(1).Info("Using private IP from environment variable", "privateIP", privateIP)
		return privateIP, nil
	}

	// Try to read from the network interface files
	// This is a fallback method that works in most Linux environments
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces: %v", err)
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					// This is an IPv4 address
					ip := ipnet.IP.String()
					// Check if this is a private IP (AWS instances use private IPs)
					if c.isPrivateIP(ip) {
						c.Logger.V(1).Info("Found private IP from network interface", "privateIP", ip, "interface", iface.Name)
						return ip, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no private IP address found")
}

// isPrivateIP checks if an IP address is in a private range
func (c *EC2Client) isPrivateIP(ip string) bool {
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}

	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return false
	}

	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ipAddr) {
			return true
		}
	}

	return false
}

// getCurrentIMDSHopLimit gets the current IMDS hop limit for an instance
func (c *EC2Client) getCurrentIMDSHopLimit(ctx context.Context, instanceID string) (int32, error) {
	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}

	result, err := c.EC2.DescribeInstances(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("failed to describe instance: %v", err)
	}

	if len(result.Reservations) == 0 || len(result.Reservations[0].Instances) == 0 {
		return 0, fmt.Errorf("instance not found: %s", instanceID)
	}

	instance := result.Reservations[0].Instances[0]
	if instance.MetadataOptions == nil {
		return 1, nil // Default hop limit is 1
	}

	return *instance.MetadataOptions.HttpPutResponseHopLimit, nil
}

// modifyInstanceMetadataOptions modifies the IMDS hop limit for an instance
func (c *EC2Client) modifyInstanceMetadataOptions(ctx context.Context, instanceID string, hopLimit int32) error {
	input := &ec2.ModifyInstanceMetadataOptionsInput{
		InstanceId:              aws.String(instanceID),
		HttpPutResponseHopLimit: aws.Int32(hopLimit),
	}

	_, err := c.EC2.ModifyInstanceMetadataOptions(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to modify instance metadata options: %v", err)
	}

	return nil
}
