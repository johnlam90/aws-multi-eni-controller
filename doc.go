// Package awsmultienicontroller is a Kubernetes controller that automatically creates
// and attaches AWS Elastic Network Interfaces (ENIs) to nodes based on node labels.
// Version: v1.2.9
//
// This package can be used in two ways:
//
// 1. As a standalone Kubernetes controller (see cmd/main.go)
// 2. As a library for managing AWS ENIs programmatically (see pkg/lib)
//
// For library usage, import the lib package:
//
//	import "github.com/johnlam90/aws-multi-eni-controller/pkg/lib"
//
// Then use the ENIManager to create, attach, detach, and delete ENIs:
//
//	// Create a logger
//	zapLog, _ := zap.NewDevelopment()
//	logger := zapr.NewLogger(zapLog)
//
//	// Create an ENI manager
//	eniManager, err := lib.NewENIManager(ctx, "us-east-1", logger)
//	if err != nil {
//	    log.Fatalf("Failed to create ENI manager: %v", err)
//	}
//
//	// Create an ENI
//	options := lib.ENIOptions{
//	    SubnetID:           "subnet-12345678",
//	    SecurityGroupIDs:   []string{"sg-12345678"},
//	    Description:        "Example ENI",
//	    DeviceIndex:        1,
//	    DeleteOnTermination: true,
//	}
//
//	eniID, err := eniManager.CreateENI(ctx, options)
//
// For more information, see the documentation in pkg/lib/README.md.
package awsmultienicontroller
