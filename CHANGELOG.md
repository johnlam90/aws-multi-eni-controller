# Changelog

All notable changes to the AWS Multi-ENI Controller will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.3.4] - 2025-05-26

### Added

- **Modular Architecture**: Complete refactoring from monolithic 6700+ line main.go to focused, maintainable packages
  - `pkg/eni-manager/coordinator/` - Main orchestration logic and integration tests
  - `pkg/eni-manager/dpdk/` - DPDK device binding operations with circuit breaker pattern
  - `pkg/eni-manager/network/` - Network interface management with validation
  - `pkg/eni-manager/kubernetes/` - Kubernetes API interactions and NodeENI handling
  - `pkg/eni-manager/sriov/` - SR-IOV device plugin configuration management
  - `pkg/eni-manager/testutil/` - Testing utilities and mock implementations
- **Enhanced SR-IOV Management**: Improved device plugin restart isolation and tracking
- **NodeENI Deletion Detection**: Automatic cleanup and tracking for deleted NodeENI resources
- **Batched SR-IOV Updates**: Prevention of configuration overwrites during concurrent operations
- **Comprehensive Test Suite**: Organized test structure with 16+ test files moved to appropriate packages
- **Circuit Breaker Pattern**: Fault tolerance for DPDK operations to prevent cascading failures
- **Input Validation Framework**: Comprehensive validation with detailed error messages
- **Exponential Backoff Retry**: Resilient network operations with configurable retry logic

### Changed

- **Architecture Transformation**: Reduced main.go from 6700+ lines to ~300 lines per focused package
- **Improved Maintainability**: Clear separation of concerns with single responsibility principle
- **Enhanced Testability**: Mock implementations and comprehensive unit tests for each component
- **Better Observability**: Enhanced status reporting and component health metrics
- **Optimized Performance**: Same or better performance with improved resource utilization
- **Organized Test Structure**: Moved all test files to appropriate package directories with fixed declarations

### Fixed

- **SR-IOV Resource Cleanup**: Fixed cleanup issues for non-DPDK interfaces
- **Device Plugin Restart Loops**: Prevented unnecessary restart cycles with improved change detection
- **DPDK Status Reporting**: Enhanced accuracy of DPDK binding status in NodeENI resources
- **Configuration Conflicts**: Resolved SR-IOV placeholder resource conflicts with updated CRD group
- **Race Condition Protection**: Enhanced protection against concurrent operation conflicts

### Security

- **Improved Error Handling**: Enhanced error recovery and timeout protection for all operations
- **Resource Lifecycle Management**: Proper cleanup and resource management across all components
- **Graceful Shutdown**: Enhanced shutdown handling for all modular components

### Documentation

- **Modular Architecture Guide**: Comprehensive documentation for new architecture (docs/MODULAR_ARCHITECTURE.md)
- **Migration Guide**: Detailed guide for developers working with the new structure (docs/REFACTORING_MIGRATION.md)
- **Updated README**: Enhanced architecture overview and component descriptions

**Addresses**: GitHub issue #20 - Complete modular architecture refactoring
**Backward Compatibility**: 100% backward compatible - no breaking changes

## [v1.3.3] - 2025-01-27

### Added

- **Cloud-Native SR-IOV Device Plugin Restart**: Implemented native Kubernetes API-based restart functionality for SR-IOV device plugins
- **Enhanced RBAC Permissions**: Added comprehensive pod management permissions for SR-IOV device plugin operations
- **Multiple SR-IOV Plugin Support**: Support for various SR-IOV device plugin naming conventions and deployment patterns
- **Performance Testing Framework**: Added comprehensive performance testing suite for enterprise-scale deployments
- **Timeout Protection**: Enhanced error handling with configurable timeouts for SR-IOV operations

### Changed

- **Replaced kubectl Dependency**: Eliminated external kubectl binary dependency in favor of native Kubernetes client-go APIs
- **Improved SR-IOV Integration**: Enhanced reliability and error handling for DPDK binding/unbinding operations
- **Enhanced Device Detection**: Improved DPDK device detection and status reporting in ENI manager
- **Optimized Concurrent Operations**: Better handling of concurrent ENI operations and reconciliation

### Fixed

- **SR-IOV Device Plugin Restart Reliability**: Fixed issues with SR-IOV device plugin restart when DPDK binding changes occur
- **DPDK Status Reporting**: Improved accuracy of DPDK binding status in NodeENI resources
- **Race Condition Handling**: Enhanced protection against race conditions in concurrent operations

### Security

- **Principle of Least Privilege**: Refined RBAC permissions to follow security best practices
- **Enhanced Pod Security**: Improved security context and capabilities management for privileged operations

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
