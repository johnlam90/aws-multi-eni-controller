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

- Go 1.19 or higher
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
   go test ./...
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

## Coding Standards

- Follow Go best practices and style guidelines
- Use meaningful variable and function names
- Write clear comments and documentation
- Write unit tests for your code

## License

By contributing to this project, you agree that your contributions will be licensed under the project's [Apache 2.0 License](LICENSE).
