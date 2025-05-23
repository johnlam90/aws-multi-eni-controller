# AWS Multi-ENI Controller Test Suite

This directory contains tests for the AWS Multi-ENI Controller. The test suite is designed to be run both locally and in CI/CD environments, with proper handling of AWS credentials.

## Test Categories

The test suite is organized into the following categories:

1. **Unit Tests**: Tests that don't require AWS credentials or a Kubernetes cluster. These tests use mock implementations of AWS services and Kubernetes clients.

2. **Integration Tests**: Tests that require AWS credentials but not a Kubernetes cluster. These tests interact with real AWS services.

3. **End-to-End Tests**: Tests that require both AWS credentials and a Kubernetes cluster. These tests deploy the controller to a real cluster and verify its functionality.

## Running Tests

### Prerequisites

- Go 1.22 or higher
- AWS credentials (for integration and E2E tests)
- Access to a Kubernetes cluster (for E2E tests)

### Running Unit Tests

Unit tests can be run without AWS credentials:

```bash
go test -v ./pkg/... -short
```

### Running Integration Tests

Integration tests require AWS credentials and specific environment variables:

```bash
export AWS_REGION=us-east-1
export TEST_SUBNET_ID=subnet-xxxxxxxx
export TEST_SECURITY_GROUP_ID=sg-xxxxxxxx
export TEST_SECURITY_GROUP_NAME=default

go test -v ./pkg/aws -tags=integration
```

### Running End-to-End Tests

End-to-end tests require AWS credentials and a Kubernetes cluster:

```bash
export AWS_REGION=us-east-1
export TEST_SUBNET_ID=subnet-xxxxxxxx
export TEST_SECURITY_GROUP_ID=sg-xxxxxxxx
export KUBECONFIG=/path/to/kubeconfig

go test -v ./test/e2e -tags=e2e
```

### Running All Tests

To run all tests (requires AWS credentials and a Kubernetes cluster):

```bash
make test-all
```

## Test Skipping Logic

The test suite includes logic to automatically skip tests that require AWS credentials or a Kubernetes cluster when those resources are not available:

- Tests that require AWS credentials check for the presence of `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (or `AWS_PROFILE`) environment variables.
- Tests that require a Kubernetes cluster check for the presence of the `KUBECONFIG` environment variable or in-cluster configuration.

## Adding New Tests

### Adding Unit Tests

1. Create a new test file with the suffix `_test.go` in the appropriate package.
2. Use the mock implementations in `pkg/test` for AWS services and Kubernetes clients.
3. Ensure the test can run without AWS credentials or a Kubernetes cluster.

Example:

```go
func TestMyFunction(t *testing.T) {
    // Create mock clients
    mockEC2Client := test.CreateMockEC2Client()
    
    // Test your function
    result := MyFunction(mockEC2Client)
    
    // Assert the result
    if result != expected {
        t.Errorf("Expected %v, got %v", expected, result)
    }
}
```

### Adding Integration Tests

1. Create a new test file with the suffix `_integration_test.go` in the appropriate package.
2. Add the build tag `// +build integration` at the top of the file.
3. Use the `test.SkipIfNoAWSCredentials` function to skip the test when AWS credentials are not available.
4. Use the `test.CreateTestEC2Client` function to create a real EC2 client.

Example:

```go
// +build integration

func TestIntegration_MyFunction(t *testing.T) {
    // Skip if no AWS credentials
    test.SkipIfNoAWSCredentials(t)
    
    // Create a real EC2 client
    client := test.CreateTestEC2Client(t)
    
    // Test your function with the real client
    result := MyFunction(client)
    
    // Assert the result
    if result != expected {
        t.Errorf("Expected %v, got %v", expected, result)
    }
}
```

### Adding End-to-End Tests

1. Create a new test file in the `test/e2e` directory.
2. Add the build tag `// +build e2e` at the top of the file.
3. Use the `test.SkipIfNoAWSCredentials` and `test.SkipIfNoKubernetesCluster` functions to skip the test when resources are not available.
4. Use the Kubernetes client-go library to interact with the cluster.

Example:

```go
// +build e2e

func TestE2E_MyFeature(t *testing.T) {
    // Skip if no AWS credentials or Kubernetes cluster
    test.SkipIfNoAWSCredentials(t)
    test.SkipIfNoKubernetesCluster(t)
    
    // Create a Kubernetes client
    config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
    if err != nil {
        t.Fatalf("Failed to create Kubernetes config: %v", err)
    }
    
    clientset, err := kubernetes.NewForConfig(config)
    if err != nil {
        t.Fatalf("Failed to create Kubernetes client: %v", err)
    }
    
    // Test your feature
    // ...
}
```

## CI/CD Integration

The test suite is designed to work with GitHub Actions. The workflow is configured to:

1. Run unit tests on every pull request and push to main
2. Run integration tests only when AWS credentials are available (using GitHub secrets)
3. Run end-to-end tests only when both AWS credentials and a Kubernetes cluster are available

See the `.github/workflows/go-tests.yml` file for the workflow configuration.
