# Contributing to AWS Multi-ENI Controller

Thank you for your interest in contributing to the AWS Multi-ENI Controller! This document provides guidelines and instructions for contributing to this project.

## Code of Conduct

By participating in this project, you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

## How to Contribute

### Reporting Bugs

If you find a bug in the project, please create an issue on GitHub with the following information:

1. A clear, descriptive title for the issue
2. A description of the problem, including steps to reproduce
3. The expected behavior
4. Screenshots, if applicable
5. Your environment (Kubernetes version, AWS region, etc.)
6. Any additional context that might be helpful

### Suggesting Enhancements

If you have an idea for a new feature or enhancement, please create an issue with the following information:

1. A clear, descriptive title
2. A detailed description of the proposed enhancement
3. The motivation behind this enhancement
4. Any potential implementation details
5. Any potential drawbacks or concerns

### Reporting Security Vulnerabilities

Please do not report security vulnerabilities through public GitHub issues. Instead, follow the security vulnerability reporting process outlined in our [Security Policy](SECURITY.md).

### Pull Requests

We welcome pull requests! To submit a pull request:

1. Fork the repository
2. Create a new branch for your changes
3. Make your changes
4. Write or update tests as necessary
5. Update documentation as necessary
6. Submit a pull request

#### Pull Request Guidelines

- Keep your changes focused. If you're fixing a bug, focus on that bug. If you're adding a feature, focus on that feature.
- Write clear, descriptive commit messages
- Include tests for your changes
- Update documentation as necessary
- Make sure all tests pass before submitting
- Reference any relevant issues in your pull request description

## Development Setup

### Prerequisites

- Go 1.23 or higher
- Docker
- Access to a Kubernetes cluster (e.g., EKS)
- AWS CLI configured with appropriate permissions
- kubectl installed and configured

### Building and Testing Locally

1. Clone the repository:

   ```bash
   git clone https://github.com/johnlam90/aws-multi-eni-controller.git
   cd aws-multi-eni-controller
   ```

2. Build the controller:

   ```bash
   go build -o bin/manager cmd/main.go
   ```

3. Run tests:

   ```bash
   # Run unit tests only
   go test -v ./pkg/... -short

   # Run integration tests (requires AWS credentials)
   export AWS_REGION=us-east-1
   export TEST_SUBNET_ID=subnet-xxxxxxxx
   export TEST_SECURITY_GROUP_ID=sg-xxxxxxxx
   go test -v ./pkg/aws -tags=integration
   ```

4. Build and push the Docker image:

   ```bash
   # The deploy.sh script builds and pushes the Docker image
   ./hack/deploy.sh
   ```

## Project Structure

- `cmd/`: Contains the main entry point for the controller
- `pkg/`: Contains the core controller logic
  - `apis/`: Contains the API definitions
  - `controller/`: Contains the controller implementation
- `deploy/`: Contains Kubernetes deployment manifests
  - `crds/`: Contains the Custom Resource Definitions
  - `samples/`: Contains sample NodeENI resources
- `hack/`: Contains scripts for development and deployment
- `test/`: Contains test utilities and integration tests
- `docs/`: Contains project documentation

## Contribution Requirements

### Code Standards

- Follow Go best practices and style guidelines
- Maximum cyclomatic complexity of 15 per function
- Use meaningful variable and function names
- Follow the project's existing code structure and patterns
- Ensure context.Context is always the first parameter of a function
- Avoid underscores in Go package names
- Use Go modules for dependency management
- Format code with `gofmt` before submitting

### Testing Requirements

- All new features must include unit tests
- Maintain or improve the current test coverage percentage
- Tests should cover both success and error paths
- Include integration tests for AWS-dependent functionality
- Skip AWS-dependent tests when credentials are not available
- Use mocks for external dependencies in unit tests

### Documentation Requirements

- Update relevant documentation for any new features or changes
- Include examples for new functionality
- Document any new configuration options
- Update the README.md if necessary
- Add comments to exported functions and types

### Security Considerations

- Do not include hardcoded credentials or sensitive information
- Validate all user inputs and file paths
- Follow secure coding practices for shell command execution
- Document security implications of your changes if applicable
- Use proper error handling that doesn't leak sensitive information
- Follow the principle of least privilege for AWS IAM permissions

## Test Policy

The AWS Multi-ENI Controller project requires tests for all new functionality:

1. **Unit Tests**: All new functions and methods must have corresponding unit tests
2. **Integration Tests**: Features that interact with AWS services must have integration tests
3. **End-to-End Tests**: Major features should include end-to-end tests where appropriate

### Test Coverage Requirements

- New code should maintain or improve the current test coverage percentage
- Tests should cover both success and error paths
- Edge cases should be tested where applicable

### Running Tests

```bash
# Run unit tests
go test -v ./pkg/... -short

# Run integration tests (requires AWS credentials)
export AWS_REGION=us-east-1
export TEST_SUBNET_ID=subnet-xxxxxxxx
export TEST_SECURITY_GROUP_ID=sg-xxxxxxxx
go test -v ./pkg/aws -tags=integration
```

This policy is enforced during code review. Pull requests that add new functionality without corresponding tests will not be merged.

## License

By contributing to this project, you agree that your contributions will be licensed under the project's [Apache 2.0 License](LICENSE).
