# Project Structure

This document describes the organization of the AWS Multi-ENI Controller project.

## Root Directory Structure

```bash
aws-multi-eni-controller/
├── README.md                    # Main project documentation
├── LICENSE                      # Apache 2.0 license
├── CHANGELOG.md                 # Version history and changes
├── CONTRIBUTING.md              # Contribution guidelines
├── SECURITY.md                  # Security policy
├── Makefile                     # Build automation
├── Dockerfile                   # Container image definition
├── go.mod                       # Go module definition
├── go.sum                       # Go module checksums
├── doc.go                       # Package documentation
├── .gitignore                   # Git ignore rules
│
├── cmd/                         # Application entry points
│   ├── main.go                  # Controller main entry point
│   └── eni-manager/             # ENI Manager DaemonSet
│       ├── main.go              # ENI Manager main entry point
│       └── *_test.go            # ENI Manager tests
│
├── pkg/                         # Reusable Go packages
│   ├── apis/                    # Kubernetes API definitions
│   ├── aws/                     # AWS SDK integration
│   ├── config/                  # Configuration management
│   ├── controller/              # Controller logic
│   ├── eni-manager/             # ENI Manager components
│   │   ├── coordinator/         # Main coordination logic
│   │   ├── dpdk/                # DPDK integration
│   │   ├── kubernetes/          # Kubernetes client
│   │   ├── network/             # Network management
│   │   ├── sriov/               # SR-IOV device plugin integration
│   │   └── testutil/            # Testing utilities
│   ├── lib/                     # Library interface
│   ├── mapping/                 # Interface mapping utilities
│   ├── metrics/                 # Metrics collection
│   ├── observability/           # Logging and monitoring
│   ├── test/                    # Test utilities
│   └── util/                    # Common utilities
│
├── deploy/                      # Kubernetes deployment manifests
│   ├── crds/                    # Custom Resource Definitions
│   ├── samples/                 # Example configurations
│   ├── sriov/                   # SR-IOV related configurations
│   ├── deployment.yaml          # Controller deployment
│   ├── eni-manager-daemonset.yaml # ENI Manager DaemonSet
│   └── dpdk-tools-configmap.yaml # DPDK tools configuration
│
├── charts/                      # Helm charts
│   └── aws-multi-eni-controller/
│       ├── Chart.yaml           # Helm chart metadata
│       ├── values.yaml          # Default values
│       ├── README.md            # Chart documentation
│       ├── crds/                # CRD templates
│       └── templates/           # Kubernetes resource templates
│
├── docs/                        # Documentation
│   ├── architecture.md          # Architecture overview
│   ├── configuration.md         # Configuration guide
│   ├── deployment.md            # Deployment guide
│   ├── dpdk.md                  # DPDK integration guide
│   ├── multus-integration.md    # Multus CNI integration
│   ├── troubleshooting.md       # Troubleshooting guide
│   ├── sriov-dpdk-troubleshooting.md # SR-IOV/DPDK troubleshooting
│   ├── diagrams/                # Architecture diagrams
│   └── images/                  # Documentation images
│
├── examples/                    # Usage examples
│   ├── library-usage/           # Library usage examples
│   └── nodeeni-with-dpdk.yaml   # DPDK configuration example
│
├── test/                        # Test files and configurations
│   ├── README.md                # Test documentation
│   ├── e2e/                     # End-to-end tests
│   ├── integration/             # Integration tests
│   └── test-nodeeni-dpdk-pci.yaml # Test configuration
│
├── build/                       # Build-related files
│   ├── entrypoint.sh            # Container entrypoint script
│   ├── dpdk-devbind.py          # DPDK device binding script
│   └── dpdk-scripts/            # DPDK setup scripts
│
├── hack/                        # Development and testing scripts
│   ├── check-status.sh          # Status checking script
│   ├── cleanup.sh               # Cleanup script
│   ├── deploy.sh                # Deployment script
│   ├── label-node.sh            # Node labeling script
│   ├── test-beta-image.sh       # Beta image testing
│   └── test-local.sh            # Local testing script
│
└── scripts/                     # Additional utility scripts
    └── test-sriov-integration.sh # SR-IOV integration testing
```

## Key Directories

### `/cmd`

Contains the main application entry points:

- `main.go`: The NodeENI Controller
- `eni-manager/`: The ENI Manager DaemonSet with comprehensive tests

### `/pkg`

Reusable Go packages organized by functionality:

- `apis/`: Kubernetes API types and CRDs
- `aws/`: AWS SDK integration and EC2 operations
- `controller/`: Core controller logic and reconciliation
- `eni-manager/`: ENI Manager DaemonSet components
  - `coordinator/`: Main coordination and orchestration logic
  - `dpdk/`: DPDK device binding and management
  - `kubernetes/`: Kubernetes API client wrapper
  - `network/`: Network interface management and validation
  - `sriov/`: SR-IOV device plugin integration
  - `testutil/`: Mock implementations and test utilities
- `lib/`: Public library interface for programmatic usage
- `test/`: Common test utilities and helpers

### `/deploy`

Kubernetes deployment manifests:

- `crds/`: Custom Resource Definitions
- `samples/`: Example NodeENI configurations
- Core deployment files for controller and DaemonSet

### `/charts`

Helm chart for easy deployment with customizable values

### `/docs`

Comprehensive documentation including architecture, configuration, and troubleshooting guides

### `/test`

Test configurations and utilities for development and CI/CD:

- `e2e/`: End-to-end tests requiring full Kubernetes cluster
- `integration/`: Integration tests with AWS services
- `performance/`: Performance and scalability tests
- Test utilities with proper environment detection and skipping

## Testing Strategy

The project implements a comprehensive testing strategy with proper environment detection:

### Test Categories

1. **Unit Tests**: Fast, isolated tests that don't require external dependencies
   - Located alongside source code (`*_test.go` files)
   - Run in all environments including CI/CD
   - Use mock implementations from `pkg/eni-manager/testutil/`

2. **Integration Tests**: Tests that require external services (AWS, Kubernetes)
   - Automatically skip when dependencies are unavailable
   - Use `test.SkipIfNoKubernetesCluster(t)` and `test.SkipIfNoAWSCredentials(t)`
   - Located in `test/integration/` and package-specific test files

3. **End-to-End Tests**: Full system tests requiring complete environment
   - Located in `test/e2e/`
   - Require both Kubernetes cluster and AWS credentials
   - Test complete workflows and user scenarios

### Test Environment Detection

Tests automatically detect available resources and skip appropriately:

- **Kubernetes**: Checks for `KUBECONFIG` or in-cluster service account
- **AWS**: Checks for AWS credentials and region configuration
- **CI/CD Compatibility**: All tests pass in environments without external dependencies

### Test Coverage

Current test coverage by package:

- `pkg/eni-manager/coordinator`: 1.9%
- `pkg/eni-manager/dpdk`: 19.3%
- `pkg/eni-manager/network`: 25.6%
- `pkg/eni-manager/sriov`: 40.5%

## Build Artifacts

The following directories are generated during build and are excluded from version control:

- `bin/`: Compiled binaries
- `dist/`: Distribution packages
- `vendor/`: Go vendor dependencies (if used)

## Recent Improvements (v1.3.4)

### Test Infrastructure Enhancements

- **Fixed CI/CD Test Failures**: Resolved test failures in GitHub Actions by implementing proper environment detection
- **Smart Test Skipping**: Tests automatically skip when required dependencies (Kubernetes, AWS) are unavailable
- **Improved Test Coverage**: Enhanced test organization with dedicated mock implementations
- **Cross-Environment Compatibility**: Tests now work in local development, CI/CD, and production environments

### ENI Manager Architecture

- **Modular Design**: Restructured ENI Manager into focused components (coordinator, dpdk, network, sriov)
- **Enhanced Error Handling**: Improved error messages and logging throughout the system
- **Better Resource Management**: Enhanced cleanup and resource tracking capabilities
- **DPDK Integration**: Robust DPDK device binding with proper error handling and verification

### Code Quality Improvements

- **Comprehensive Testing**: Added unit tests, integration tests, and failure scenario tests
- **Mock Implementations**: Created reusable mock components for testing
- **Documentation Updates**: Enhanced documentation with current architecture and testing strategies
- **Maintainability**: Improved code organization and separation of concerns

## Development Files

Files excluded from version control (see `.gitignore`):

- IDE configuration files (`.vscode/`, `.idea/`)
- Temporary analysis and improvement documents
- Build artifacts and test outputs
- Environment-specific configuration files
- Performance optimization tracking documents
