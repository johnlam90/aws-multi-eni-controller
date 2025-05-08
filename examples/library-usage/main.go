// This example demonstrates how to use the AWS Multi-ENI Controller as a library
// to create and attach ENIs to EC2 instances.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/lib"
	"go.uber.org/zap"
)

func main() {
	// Create a logger
	zapLog, err := zap.NewDevelopment()
	if err != nil {
		fmt.Printf("Error creating logger: %v\n", err)
		os.Exit(1)
	}
	logger := zapr.NewLogger(zapLog)

	// Get AWS region from environment variable or use default
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}

	// Create a context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create an ENI manager
	eniManager, err := lib.NewENIManager(ctx, region, logger)
	if err != nil {
		logger.Error(err, "Failed to create ENI manager")
		os.Exit(1)
	}

	// Example: Create an ENI
	// Replace these values with your actual AWS resource IDs
	subnetID := os.Getenv("SUBNET_ID")
	securityGroupID := os.Getenv("SECURITY_GROUP_ID")
	instanceID := os.Getenv("INSTANCE_ID")

	if subnetID == "" || securityGroupID == "" || instanceID == "" {
		logger.Info("Please set SUBNET_ID, SECURITY_GROUP_ID, and INSTANCE_ID environment variables")
		os.Exit(1)
	}

	// Create ENI options
	options := lib.ENIOptions{
		SubnetID:           subnetID,
		SecurityGroupIDs:   []string{securityGroupID},
		Description:        "Example ENI created by library",
		DeviceIndex:        1,
		DeleteOnTermination: true,
		Tags: map[string]string{
			"Name":        "example-eni",
			"CreatedBy":   "aws-multi-eni-controller-library",
			"Environment": "example",
		},
	}

	// Create the ENI
	logger.Info("Creating ENI...", "subnetID", subnetID)
	eniID, err := eniManager.CreateENI(ctx, options)
	if err != nil {
		logger.Error(err, "Failed to create ENI")
		os.Exit(1)
	}
	logger.Info("Successfully created ENI", "eniID", eniID)

	// Attach the ENI to an instance
	logger.Info("Attaching ENI to instance...", "eniID", eniID, "instanceID", instanceID)
	err = eniManager.AttachENI(ctx, eniID, instanceID, options.DeviceIndex, options.DeleteOnTermination)
	if err != nil {
		logger.Error(err, "Failed to attach ENI")
		
		// Try to clean up the ENI
		logger.Info("Attempting to delete the ENI...", "eniID", eniID)
		if delErr := eniManager.DeleteENI(ctx, eniID); delErr != nil {
			logger.Error(delErr, "Failed to delete ENI during cleanup")
		}
		
		os.Exit(1)
	}
	logger.Info("Successfully attached ENI to instance", "eniID", eniID, "instanceID", instanceID)

	// Get ENIs attached to the instance
	logger.Info("Getting ENIs attached to instance...", "instanceID", instanceID)
	enis, err := eniManager.GetENIsByInstance(ctx, instanceID)
	if err != nil {
		logger.Error(err, "Failed to get ENIs for instance")
	} else {
		logger.Info("Instance has ENIs attached", "count", len(enis))
		for i, eni := range enis {
			logger.Info("ENI details", 
				"index", i,
				"id", eni.ID,
				"subnetID", eni.SubnetID,
				"privateIP", eni.PrivateIP,
				"deviceIndex", eni.DeviceIndex,
				"status", eni.Status)
		}
	}

	// Note: In a real application, you might want to detach and delete the ENI when done
	// This example leaves the ENI attached for demonstration purposes
	logger.Info("Example completed successfully")
}
