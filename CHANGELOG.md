# Changelog

All notable changes to the AWS Multi-ENI Controller will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.3.2] - 2025-05-23

### Added
- **OpenSSF Best Practices Badge Compliance**: Added comprehensive security policy (SECURITY.md) with vulnerability reporting process
- **Enhanced Contribution Guidelines**: Detailed coding standards, testing requirements, and security considerations
- **DPDK Support**: Full Data Plane Development Kit (DPDK) integration for high-performance networking
  - DPDK device binding and unbinding capabilities
  - PCI address targeting for direct device management
  - SRIOV device plugin integration
  - Configurable DPDK drivers (vfio-pci, uio_pci_generic, etc.)
- **Automated Dependency Management**: Dependabot configuration for security updates
- **Test Policy Documentation**: Formal testing requirements and coverage guidelines

### Changed
- Updated Go version requirement from 1.19 to 1.23 in contribution guidelines
- Enhanced security documentation with vulnerability remediation timelines
- Improved test documentation with detailed instructions for unit and integration tests

### Security
- Established formal vulnerability reporting process with 14-day response time
- Added security design principles and common vulnerability mitigations
- Documented secure coding practices for contributors
- Implemented principle of least privilege for AWS IAM permissions

## [v1.3.0] - 2025-05-13

### Added
- Device index tracking in ENI attachments for better reconciliation
- Consistent device index to subnet mapping across all nodes
- Automatic cleanup of manually detached ENIs to prevent resource leakage

### Changed
- Refactored code to reduce cyclomatic complexity
- Enhanced controller stability and reliability
- Improved logging for ENI attachment operations

### Fixed
- Fixed issue where manually detached ENIs were left in the available state
- Fixed device index type conversion in ENI attachment status

## [v1.2.7] - 2025-05-15

### Added
- Added subnet CIDR information to ENI attachment status
- Enhanced caching for subnet and security group information

### Changed
- Optimized AWS API calls with improved caching mechanisms
- Improved parallel cleanup implementation with worker pool pattern
- Streamlined MTU configuration logic
- Reduced verbose logging in performance-critical paths

### Fixed
- Enhanced error handling in ENI cleanup operations
- Fixed race conditions in concurrent ENI operations

## [v1.2.6] - 2025-05-12

### Added
- Added `MAX_CONCURRENT_RECONCILES` parameter for better scaling with many nodes
- Enhanced interface detection for different naming patterns (eth*, ens*, etc.)

### Changed
- Improved MTU configuration for all network interfaces
- Optimized Docker build process for faster builds

### Fixed
- Fixed MTU application to interfaces not explicitly mapped to ENIs
- Reduced cyclomatic complexity in code
- Improved error handling and logging

## [v1.2.5] - 2025-04-15

### Added
- Added configurable MTU option for ENIs in NodeENI resources
- Implemented MTU configuration in ENI Manager
- Updated ENI attachment status to include MTU information

### Fixed
- Fixed code quality issues and reduced cyclomatic complexity
- Improved error handling and logging for MTU configuration

## [v1.2.0] - 2025-03-10

### Added
- Added support for subnet names via AWS tags
- Implemented multi-subnet support
- Added support for security group names

### Changed
- Improved cleanup with finalizers
- Enhanced AWS SDK v2 integration

## [v1.0.0] - 2025-02-01

### Added
- Initial stable release
- Dynamic ENI management
- Proper cleanup with finalizers
- AWS SDK v2 integration
- Optimized container image (22MB)
