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
- `lib/`: Public library interface for programmatic usage

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

Test configurations and utilities for development and CI/CD

## Build Artifacts

The following directories are generated during build and are excluded from version control:

- `bin/`: Compiled binaries
- `dist/`: Distribution packages
- `vendor/`: Go vendor dependencies (if used)

## Development Files

Files excluded from version control (see `.gitignore`):

- IDE configuration files (`.vscode/`, `.idea/`)
- Temporary analysis and improvement documents
- Build artifacts and test outputs
- Environment-specific configuration files
