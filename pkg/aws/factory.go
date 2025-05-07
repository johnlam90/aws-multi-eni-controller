package aws

import (
	"context"

	"github.com/go-logr/logr"
)

// CreateEC2Client creates a new EC2 client
func CreateEC2Client(ctx context.Context, region string, logger logr.Logger) (EC2Interface, error) {
	return NewEC2Client(ctx, region, logger)
}
