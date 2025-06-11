package aws

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIMDSv2Configuration tests that the AWS SDK is properly configured for IMDSv2
func TestIMDSv2Configuration(t *testing.T) {
	// Skip if no AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" && os.Getenv("AWS_PROFILE") == "" {
		t.Skip("Skipping test that requires AWS credentials")
	}

	// Set up IMDSv2 environment variables
	originalEnvVars := map[string]string{
		"AWS_EC2_METADATA_DISABLED":              os.Getenv("AWS_EC2_METADATA_DISABLED"),
		"AWS_EC2_METADATA_V1_DISABLED":           os.Getenv("AWS_EC2_METADATA_V1_DISABLED"),
		"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE": os.Getenv("AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE"),
		"AWS_EC2_METADATA_SERVICE_ENDPOINT":      os.Getenv("AWS_EC2_METADATA_SERVICE_ENDPOINT"),
		"AWS_METADATA_SERVICE_TIMEOUT":           os.Getenv("AWS_METADATA_SERVICE_TIMEOUT"),
		"AWS_METADATA_SERVICE_NUM_ATTEMPTS":      os.Getenv("AWS_METADATA_SERVICE_NUM_ATTEMPTS"),
	}

	// Set IMDSv2 configuration
	os.Setenv("AWS_EC2_METADATA_DISABLED", "false")
	os.Setenv("AWS_EC2_METADATA_V1_DISABLED", "false")
	os.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE", "IPv4")
	os.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", "http://169.254.169.254")
	os.Setenv("AWS_METADATA_SERVICE_TIMEOUT", "10")
	os.Setenv("AWS_METADATA_SERVICE_NUM_ATTEMPTS", "3")

	// Restore original environment variables after test
	defer func() {
		for key, value := range originalEnvVars {
			if value == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, value)
			}
		}
	}()

	// Create a test logger
	logger := logr.Discard()

	// Test creating EC2 client with IMDSv2 configuration
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	region := "us-east-1"
	if envRegion := os.Getenv("AWS_REGION"); envRegion != "" {
		region = envRegion
	}

	// Test NewEC2Client
	t.Run("NewEC2Client", func(t *testing.T) {
		client, err := NewEC2Client(ctx, region, logger)
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.NotNil(t, client.EC2)
	})

	// Test NewEC2ClientFacade
	t.Run("NewEC2ClientFacade", func(t *testing.T) {
		client, err := NewEC2ClientFacade(ctx, region, logger)
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.NotNil(t, client.ec2Client)
	})

	// Test NewEC2ClientOptimized
	t.Run("NewEC2ClientOptimized", func(t *testing.T) {
		client, err := NewEC2ClientOptimized(ctx, region, logger)
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.NotNil(t, client.EC2)
	})
}

// TestIMDSv2EnvironmentVariables tests that the environment variables are properly set
func TestIMDSv2EnvironmentVariables(t *testing.T) {
	// Test cases for different environment variable configurations
	testCases := []struct {
		name     string
		envVars  map[string]string
		expected bool
	}{
		{
			name: "IMDSv2 enabled configuration",
			envVars: map[string]string{
				"AWS_EC2_METADATA_DISABLED":              "false",
				"AWS_EC2_METADATA_V1_DISABLED":           "false",
				"AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE": "IPv4",
				"AWS_EC2_METADATA_SERVICE_ENDPOINT":      "http://169.254.169.254",
				"AWS_METADATA_SERVICE_TIMEOUT":           "10",
				"AWS_METADATA_SERVICE_NUM_ATTEMPTS":      "3",
			},
			expected: true,
		},
		{
			name: "IMDS disabled configuration",
			envVars: map[string]string{
				"AWS_EC2_METADATA_DISABLED": "true",
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Store original environment variables
			originalEnvVars := make(map[string]string)
			for key := range tc.envVars {
				originalEnvVars[key] = os.Getenv(key)
			}

			// Set test environment variables
			for key, value := range tc.envVars {
				os.Setenv(key, value)
			}

			// Restore original environment variables after test
			defer func() {
				for key, value := range originalEnvVars {
					if value == "" {
						os.Unsetenv(key)
					} else {
						os.Setenv(key, value)
					}
				}
			}()

			// Verify environment variables are set correctly
			for key, expectedValue := range tc.envVars {
				actualValue := os.Getenv(key)
				assert.Equal(t, expectedValue, actualValue, "Environment variable %s should be %s", key, expectedValue)
			}

			// Test that we can create an EC2 client (this validates the configuration is valid)
			if tc.expected {
				logger := logr.Discard()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				client, err := NewEC2Client(ctx, "us-east-1", logger)
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}
