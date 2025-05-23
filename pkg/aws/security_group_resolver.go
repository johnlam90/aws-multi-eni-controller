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

// EC2SecurityGroupResolver implements the SecurityGroupResolver interface
type EC2SecurityGroupResolver struct {
	// EC2 is the underlying AWS SDK v2 EC2 client
	EC2 *ec2.Client
	// Logger is used for structured logging
	Logger logr.Logger
	// Rate limiter for AWS API calls
	rateLimiter *rate.Limiter

	// Cache for security group information
	sgCache map[string]string // sgName -> sgID
	sgMutex sync.RWMutex

	// Cache expiration
	cacheExpiration time.Duration
	lastCacheUpdate time.Time
}

// NewEC2SecurityGroupResolver creates a new EC2SecurityGroupResolver
func NewEC2SecurityGroupResolver(ec2Client *ec2.Client, logger logr.Logger) *EC2SecurityGroupResolver {
	return &EC2SecurityGroupResolver{
		EC2:             ec2Client,
		Logger:          logger.WithName("ec2-sg-resolver"),
		rateLimiter:     rate.NewLimiter(rate.Limit(10), 20), // 10 requests per second, burst of 20
		sgCache:         make(map[string]string, 16),         // Pre-allocate for typical usage
		cacheExpiration: 5 * time.Minute,                     // Cache expires after 5 minutes
		lastCacheUpdate: time.Now(),
	}
}

// waitForRateLimit waits for rate limiter before making AWS API calls
func (r *EC2SecurityGroupResolver) waitForRateLimit(ctx context.Context) error {
	return r.rateLimiter.Wait(ctx)
}

// GetSecurityGroupIDByName looks up a security group ID by its Name or GroupName
func (r *EC2SecurityGroupResolver) GetSecurityGroupIDByName(ctx context.Context, securityGroupName string) (string, error) {
	// Add context timeout for AWS operations
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	log := r.Logger.WithValues("securityGroupName", securityGroupName)
	log.Info("Looking up security group ID by name")

	// Check cache first
	r.sgMutex.RLock()
	if sgID, ok := r.sgCache[securityGroupName]; ok {
		// Check if cache is still valid
		if time.Since(r.lastCacheUpdate) < r.cacheExpiration {
			log.Info("Using cached security group ID", "securityGroupID", sgID)
			r.sgMutex.RUnlock()
			return sgID, nil
		}
	}
	r.sgMutex.RUnlock()

	// Wait for rate limiter
	if err := r.waitForRateLimit(ctx); err != nil {
		return "", fmt.Errorf("rate limit wait failed: %w", err)
	}

	// Cache miss or expired, fetch from AWS
	// Try both Name tag and GroupName
	input := &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("group-name"),
				Values: []string{securityGroupName},
			},
		},
	}

	result, err := r.EC2.DescribeSecurityGroups(ctx, input)
	if err != nil {
		return "", fmt.Errorf("failed to describe security groups for name %s: %w", securityGroupName, err)
	}

	// If not found by group-name, try tag:Name
	if len(result.SecurityGroups) == 0 {
		input = &ec2.DescribeSecurityGroupsInput{
			Filters: []types.Filter{
				{
					Name:   aws.String("tag:Name"),
					Values: []string{securityGroupName},
				},
			},
		}

		result, err = r.EC2.DescribeSecurityGroups(ctx, input)
		if err != nil {
			return "", fmt.Errorf("failed to describe security groups for tag:Name %s: %w", securityGroupName, err)
		}

		if len(result.SecurityGroups) == 0 {
			return "", fmt.Errorf("no security group found with name: %s", securityGroupName)
		}
	}

	if len(result.SecurityGroups) > 1 {
		log.Info("Multiple security groups found with the same name, using the first one")
	}

	sgID := *result.SecurityGroups[0].GroupId
	log.Info("Found security group ID", "securityGroupID", sgID)

	// Update cache
	r.sgMutex.Lock()
	r.sgCache[securityGroupName] = sgID
	r.lastCacheUpdate = time.Now()
	r.sgMutex.Unlock()

	return sgID, nil
}
