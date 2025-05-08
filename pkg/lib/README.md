# AWS Multi-ENI Controller Library

This package provides a Go library for managing AWS Elastic Network Interfaces (ENIs) programmatically. It's extracted from the AWS Multi-ENI Controller project and provides a clean API for creating, attaching, detaching, and deleting ENIs.

## Installation

```bash
go get github.com/johnlam90/aws-multi-eni-controller
```

## Usage

### Basic Example

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/go-logr/logr"
    "github.com/go-logr/zapr"
    "github.com/johnlam90/aws-multi-eni-controller/pkg/lib"
    "go.uber.org/zap"
)

func main() {
    // Create a logger
    zapLog, _ := zap.NewDevelopment()
    logger := zapr.NewLogger(zapLog)

    // Create a context with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // Create an ENI manager
    eniManager, err := lib.NewENIManager(ctx, "us-east-1", logger)
    if err != nil {
        log.Fatalf("Failed to create ENI manager: %v", err)
    }

    // Create an ENI
    options := lib.ENIOptions{
        SubnetID:           "subnet-12345678",
        SecurityGroupIDs:   []string{"sg-12345678"},
        Description:        "Example ENI",
        DeviceIndex:        1,
        DeleteOnTermination: true,
        Tags: map[string]string{
            "Name": "example-eni",
        },
    }

    eniID, err := eniManager.CreateENI(ctx, options)
    if err != nil {
        log.Fatalf("Failed to create ENI: %v", err)
    }

    // Attach the ENI to an instance
    err = eniManager.AttachENI(ctx, eniID, "i-12345678", options.DeviceIndex, options.DeleteOnTermination)
    if err != nil {
        log.Fatalf("Failed to attach ENI: %v", err)
    }

    // Later, detach and delete the ENI when done
    // ...
}
```

### Advanced Configuration

You can create an ENI manager with custom configuration:

```go
import (
    "github.com/johnlam90/aws-multi-eni-controller/pkg/config"
    "github.com/johnlam90/aws-multi-eni-controller/pkg/lib"
)

// Create a custom configuration
cfg := config.DefaultControllerConfig()
cfg.AWSRegion = "us-west-2"
cfg.DetachmentTimeout = 30 * time.Second
cfg.DefaultDeviceIndex = 2

// Create an ENI manager with custom configuration
eniManager, err := lib.NewENIManagerWithConfig(ctx, cfg, logger)
```

## API Reference

### Types

#### `ENIManager`

The main type for managing ENIs.

#### `ENIOptions`

Options for creating a new ENI:

- `SubnetID` (string): The ID of the subnet to create the ENI in.
- `SecurityGroupIDs` ([]string): A list of security group IDs to attach to the ENI.
- `Description` (string): An optional description for the ENI.
- `DeviceIndex` (int): The device index to use when attaching the ENI.
- `DeleteOnTermination` (bool): Whether the ENI should be deleted when the instance is terminated.
- `Tags` (map[string]string): A map of tags to apply to the ENI.

### Methods

#### `NewENIManager(ctx context.Context, region string, logger logr.Logger) (*ENIManager, error)`

Creates a new ENI manager with default configuration for the specified region.

#### `NewENIManagerWithConfig(ctx context.Context, cfg *config.ControllerConfig, logger logr.Logger) (*ENIManager, error)`

Creates a new ENI manager with custom configuration.

#### `CreateENI(ctx context.Context, options ENIOptions) (string, error)`

Creates a new ENI with the specified options and returns its ID.

#### `AttachENI(ctx context.Context, eniID, instanceID string, deviceIndex int, deleteOnTermination bool) error`

Attaches an ENI to an instance.

#### `DetachENI(ctx context.Context, attachmentID string) error`

Detaches an ENI from an instance.

#### `DeleteENI(ctx context.Context, eniID string) error`

Deletes an ENI.

#### `GetENIsByInstance(ctx context.Context, instanceID string) ([]NetworkInterfaceInfo, error)`

Gets all ENIs attached to an instance. Note: This method is currently not fully implemented and will return an error.

#### `GetSubnetsByVPC(ctx context.Context, vpcID string) ([]SubnetInfo, error)`

Gets all subnets in a VPC. Note: This method is currently not fully implemented and will return an error.

#### `GetSecurityGroupsByVPC(ctx context.Context, vpcID string) ([]SecurityGroupInfo, error)`

Gets all security groups in a VPC. Note: This method is currently not fully implemented and will return an error.

## Error Handling

All methods return meaningful error messages that can be used to diagnose issues. It's recommended to check errors and handle them appropriately in your application.

## AWS Credentials

The library uses the default AWS SDK credential chain. Make sure your application has the necessary AWS credentials configured through environment variables, AWS configuration files, or IAM roles.

## Examples

See the `examples/library-usage` directory for complete examples of how to use this library.

## License

This library is licensed under the same license as the AWS Multi-ENI Controller project.
