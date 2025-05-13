/*
Package lib provides a clean API for using AWS Multi-ENI Controller functionality
as a library in other Go projects.

This package abstracts away the implementation details of the AWS Multi-ENI Controller
and provides a simple interface for managing AWS Elastic Network Interfaces (ENIs).

Basic usage:

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
	}

	eniID, err := eniManager.CreateENI(ctx, options)
	if err != nil {
		log.Fatalf("Failed to create ENI: %v", err)
	}

	// Attach the ENI to an instance
	err = eniManager.AttachENI(ctx, eniID, "i-12345678", 1, true)
	if err != nil {
		log.Fatalf("Failed to attach ENI: %v", err)
	}

	// Detach the ENI
	err = eniManager.DetachENI(ctx, "eni-attach-12345678")
	if err != nil {
		log.Fatalf("Failed to detach ENI: %v", err)
	}

	// Delete the ENI
	err = eniManager.DeleteENI(ctx, eniID)
	if err != nil {
		log.Fatalf("Failed to delete ENI: %v", err)
	}

For more examples, see the examples/library-usage directory.
*/
package lib
