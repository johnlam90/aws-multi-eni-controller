# Changelog

All notable changes to the AWS Multi-ENI Controller will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.3.5] - 2025-05-28

### Fixed

- **SR-IOV Configuration Generation**: Fixed ENI Manager incorrectly generating SR-IOV device plugin configuration for regular ENI configurations (Case 1) when it should only do so for configurations where SR-IOV is explicitly requested via `dpdkPCIAddress` field
- **PCI Address Mapping**: Fixed interface-to-NodeENI mapping logic to prioritize PCI address matching over device index matching for SR-IOV configurations, resolving PCI address mismatches in SR-IOV device plugin configuration
- **Resource Name Mapping**: Resolved issue where PCI addresses were incorrectly mapped to SR-IOV resource names (e.g., PCI `0000:00:08.0` incorrectly mapped to `sriov_kernel_1` instead of `sriov_kernel_2`)

### Added

- **Enhanced Logging**: Added detailed logging for interface mapping decisions to help troubleshoot configuration issues
- **Test Coverage**: Added comprehensive test coverage for interface-to-NodeENI mapping scenarios including `TestPCIAddressMapping` and `TestRegularENINoSRIOVConfiguration`
- **Documentation**: Updated GitHub issue #22 with detailed root cause analysis and solution documentation

### Changed

- **Interface Mapping Logic**: Modified `findNodeENIForInterface` method to check PCI address first for SR-IOV configurations, then fall back to device index for regular ENI configurations
- **SR-IOV Logic**: Updated `updateSRIOVConfiguration` method to only process interfaces where `dpdkPCIAddress` is explicitly specified, ensuring proper separation between regular ENI and SR-IOV functionality

### Technical Details

This release addresses critical issues in SR-IOV configuration generation:

1. **Issue 1**: Regular ENI configurations (no DPDK fields) were incorrectly generating SR-IOV device plugin configuration
2. **Issue 2**: PCI address mapping was using device index first, causing incorrect associations between interfaces and NodeENI resources

The fixes ensure that:

- Regular ENI configurations work as intended (basic ENI attachment only)
- SR-IOV configurations correctly map PCI addresses to resource names
- Cleaner separation of concerns between regular ENI and SR-IOV functionality

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
