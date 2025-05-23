package aws

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
)

// EC2SubnetResolver implements the SubnetResolver interface
type EC2SubnetResolver struct {
	// EC2 is the underlying AWS SDK v2 EC2 client
	EC2 *ec2.Client
	// Logger is used for structured logging
	Logger logr.Logger
	// Rate limiter for AWS API calls
	rateLimiter *rate.Limiter

	// Cache for subnet information
	subnetCache     map[string]string // subnetID -> CIDR
	subnetNameCache map[string]string // subnetName -> subnetID
	subnetMutex     sync.RWMutex

	// Cache expiration
	cacheExpiration time.Duration
	lastCacheUpdate time.Time
}

// NewEC2SubnetResolver creates a new EC2SubnetResolver
func NewEC2SubnetResolver(ec2Client *ec2.Client, logger logr.Logger) *EC2SubnetResolver {
	return &EC2SubnetResolver{
		EC2:             ec2Client,
		Logger:          logger.WithName("ec2-subnet-resolver"),
		rateLimiter:     rate.NewLimiter(rate.Limit(10), 20), // 10 requests per second, burst of 20
		subnetCache:     make(map[string]string, 32),         // Pre-allocate for typical usage
		subnetNameCache: make(map[string]string, 32),         // Pre-allocate for typical usage
		cacheExpiration: 5 * time.Minute,                     // Cache expires after 5 minutes
		lastCacheUpdate: time.Now(),
	}
}

// waitForRateLimit waits for rate limiter before making AWS API calls
func (r *EC2SubnetResolver) waitForRateLimit(ctx context.Context) error {
	return r.rateLimiter.Wait(ctx)
}

// GetSubnetIDByName looks up a subnet ID by its Name tag
func (r *EC2SubnetResolver) GetSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log := r.Logger.WithValues("subnetName", subnetName)
	log.Info("Looking up subnet ID by name")

	// Check cache first
	r.subnetMutex.RLock()
	if subnetID, ok := r.subnetNameCache[subnetName]; ok {
		// Check if cache is still valid
		if time.Since(r.lastCacheUpdate) < r.cacheExpiration {
			log.Info("Using cached subnet ID", "subnetID", subnetID)
			r.subnetMutex.RUnlock()
			return subnetID, nil
		}
	}
	r.subnetMutex.RUnlock()

	// Wait for rate limiter
	if err := r.waitForRateLimit(ctx); err != nil {
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

	result, err := r.EC2.DescribeSubnets(ctx, input)
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
	cidr := *result.Subnets[0].CidrBlock
	log.Info("Found subnet ID", "subnetID", subnetID, "cidr", cidr)

	// Update cache
	r.subnetMutex.Lock()
	r.subnetNameCache[subnetName] = subnetID
	r.subnetCache[subnetID] = cidr
	r.lastCacheUpdate = time.Now()
	r.subnetMutex.Unlock()

	return subnetID, nil
}

// GetSubnetCIDRByID looks up a subnet CIDR by its ID
func (r *EC2SubnetResolver) GetSubnetCIDRByID(ctx context.Context, subnetID string) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log := r.Logger.WithValues("subnetID", subnetID)
	log.Info("Looking up subnet CIDR by ID")

	// Check cache first
	r.subnetMutex.RLock()
	if cidr, ok := r.subnetCache[subnetID]; ok {
		// Check if cache is still valid
		if time.Since(r.lastCacheUpdate) < r.cacheExpiration {
			log.Info("Using cached subnet CIDR", "cidr", cidr)
			r.subnetMutex.RUnlock()
			return cidr, nil
		}
	}
	r.subnetMutex.RUnlock()

	// Wait for rate limiter
	if err := r.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

	// Cache miss or expired, fetch from AWS
	input := &ec2.DescribeSubnetsInput{
		SubnetIds: []string{subnetID},
	}

	result, err := r.EC2.DescribeSubnets(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe subnet %s: %w", subnetID, err)
	}

	if len(result.Subnets) == 0 {
		return "", fmt.Errorf("no subnet found with ID: %s", subnetID)
	}

	cidr := *result.Subnets[0].CidrBlock
	log.Info("Found subnet CIDR", "cidr", cidr)

	// Update cache
	r.subnetMutex.Lock()
	r.subnetCache[subnetID] = cidr
	r.lastCacheUpdate = time.Now()
	r.subnetMutex.Unlock()

	return cidr, nil
}
