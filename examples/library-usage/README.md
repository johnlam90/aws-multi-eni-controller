# AWS Multi-ENI Controller Library Example

This example demonstrates how to use the AWS Multi-ENI Controller as a Go library to programmatically create and attach ENIs to EC2 instances.

## Prerequisites

- Go 1.22 or later
- AWS credentials configured (via environment variables, AWS configuration files, or IAM roles)
- A subnet ID, security group ID, and (optionally) an instance ID to attach the ENI to

## Running the Example

1. Set the required environment variables:

```bash
export AWS_REGION=us-east-1  # Optional, defaults to us-east-1
export SUBNET_ID=subnet-12345678  # Required
export SECURITY_GROUP_ID=sg-12345678  # Required
export INSTANCE_ID=i-12345678  # Optional, if you want to attach the ENI
```

2. Run the example:

```bash
go run main.go
```

## What the Example Does

1. Creates an ENI in the specified subnet with the specified security group
2. If an instance ID is provided, attaches the ENI to the instance
3. Logs the results and provides AWS CLI commands to verify the ENI was created and attached

## Cleaning Up

The example does not automatically clean up the ENI. To clean up:

1. If the ENI is attached to an instance, detach it:

```bash
aws ec2 detach-network-interface --attachment-id <attachment-id>
```

2. Delete the ENI:

```bash
aws ec2 delete-network-interface --network-interface-id <eni-id>
```

## Using in Your Own Project

To use the AWS Multi-ENI Controller library in your own project:

1. Add the dependency:

```bash
go get github.com/johnlam90/aws-multi-eni-controller
```

2. Import the library:

```go
import "github.com/johnlam90/aws-multi-eni-controller/pkg/lib"
```

3. Create an ENI manager and use it to manage ENIs:

```go
eniManager, err := lib.NewENIManager(ctx, "us-east-1", logger)
if err != nil {
    // Handle error
}

// Create an ENI
eniID, err := eniManager.CreateENI(ctx, options)
if err != nil {
    // Handle error
}

// Attach the ENI to an instance
err = eniManager.AttachENI(ctx, eniID, instanceID, deviceIndex, deleteOnTermination)
if err != nil {
    // Handle error
}

// Detach the ENI
err = eniManager.DetachENI(ctx, attachmentID)
if err != nil {
    // Handle error
}

// Delete the ENI
err = eniManager.DeleteENI(ctx, eniID)
if err != nil {
    // Handle error
}
```

For more details, see the [Library Documentation](../../pkg/lib/README.md).
