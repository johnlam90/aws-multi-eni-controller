# ENI Manager Refactoring Migration Guide

## Overview

This document provides a comprehensive guide for migrating from the monolithic ENI Manager architecture (6700+ lines in `main.go`) to the new modular architecture.

## What Changed

### Before: Monolithic Architecture
```
cmd/eni-manager/
└── main.go (6700+ lines)
    ├── DPDK operations
    ├── SR-IOV management  
    ├── Network interface operations
    ├── Kubernetes API interactions
    ├── Configuration management
    └── Main orchestration logic
```

### After: Modular Architecture
```
cmd/eni-manager/
├── main.go (200 lines - orchestration only)
└── main_old.go (backup of original)

pkg/eni-manager/
├── coordinator/
│   └── manager.go (main orchestration)
├── dpdk/
│   ├── manager.go (device binding)
│   ├── coordinator.go (NodeENI processing)
│   ├── circuit_breaker.go (reliability)
│   └── errors.go (error handling)
├── network/
│   ├── manager.go (interface operations)
│   ├── validation.go (input validation)
│   └── retry.go (retry logic)
├── kubernetes/
│   └── client.go (K8s API interactions)
├── sriov/
│   └── manager.go (device plugin config)
└── testutil/
    └── mock.go (testing utilities)
```

## Backward Compatibility

### ✅ What Remains the Same
- **Command-line flags**: All existing flags work unchanged
- **Environment variables**: Same environment variable support
- **Configuration files**: Existing configs continue to work
- **Deployment**: Same container images and Helm charts
- **Functionality**: All features work exactly as before
- **Performance**: Same or better performance characteristics

### ✅ No Breaking Changes
- **API compatibility**: All existing APIs preserved
- **Behavior**: Identical behavior in all scenarios
- **Error handling**: Same error messages and codes
- **Logging**: Same log format and levels

## File Mapping

### Core Functionality Migration

| Original Location (main.go) | New Location | Purpose |
|------------------------------|--------------|---------|
| `main()` function | `cmd/eni-manager/main.go` | Simplified orchestration |
| DPDK binding functions | `pkg/eni-manager/dpdk/manager.go` | Device binding operations |
| NodeENI DPDK processing | `pkg/eni-manager/dpdk/coordinator.go` | NodeENI-specific logic |
| Network interface operations | `pkg/eni-manager/network/manager.go` | Interface management |
| Kubernetes API calls | `pkg/eni-manager/kubernetes/client.go` | K8s interactions |
| SR-IOV configuration | `pkg/eni-manager/sriov/manager.go` | Device plugin config |
| Main processing loop | `pkg/eni-manager/coordinator/manager.go` | Orchestration logic |

### Function Migration Examples

#### DPDK Functions
```go
// Before (in main.go)
func bindPCIDeviceToDPDK(pciAddress, driver string) error { ... }
func updateDPDKBindingFromNodeENI(nodeENI NodeENI) error { ... }

// After (in pkg/eni-manager/dpdk/)
func (m *Manager) BindPCIDeviceToDPDK(pciAddress, driver string) error { ... }
func (c *Coordinator) ProcessNodeENIBindings(ctx context.Context, nodeENIs []NodeENI) error { ... }
```

#### Network Functions
```go
// Before (in main.go)
func bringUpInterface(ifaceName string) error { ... }
func setMTU(ifaceName string, mtu int) error { ... }

// After (in pkg/eni-manager/network/)
func (m *Manager) BringUpInterface(ifaceName string) error { ... }
func (m *Manager) SetMTU(ifaceName string, mtu int) error { ... }
```

## New Features Added

### 1. Circuit Breaker Pattern
```go
// Automatic fault tolerance for DPDK operations
manager := dpdk.NewManager(cfg)
// Circuit breaker automatically handles failures and recovery
err := manager.BindPCIDeviceToDPDK(pciAddr, driver)
```

### 2. Structured Error Handling
```go
// Before: Generic errors
err := fmt.Errorf("bind failed: %v", err)

// After: Structured errors with context
err := dpdk.NewDPDKError("bind", pciAddr, driver, err)
if dpdkErr, ok := err.(*dpdk.DPDKError); ok {
    if dpdkErr.IsRetryable() {
        // Handle retryable error
    }
}
```

### 3. Enhanced Validation
```go
// Comprehensive input validation
err := network.ValidateNetworkConfiguration(ifaceName, mtu, deviceIndex, pciAddress)
if err != nil {
    // Handle validation error with detailed context
}
```

### 4. Retry Logic
```go
// Automatic retry with exponential backoff
err := networkManager.BringUpInterfaceWithRetry(ctx, ifaceName)
```

### 5. Better Testing Support
```go
// Mock implementations for testing
mockNetwork := testutil.NewMockNetworkManager()
mockDPDK := testutil.NewMockDPDKManager()
```

## Development Workflow Changes

### For New Features

#### Before: Monolithic Development
```go
// Add feature to main.go (6700+ lines)
// Risk of conflicts and difficult testing
```

#### After: Modular Development
```go
// Add feature to specific package
// Example: New DPDK driver support
// 1. Add to pkg/eni-manager/dpdk/manager.go
// 2. Add tests to pkg/eni-manager/dpdk/manager_test.go
// 3. Update coordinator if needed
```

### For Bug Fixes

#### Before: Monolithic Debugging
```go
// Search through 6700+ lines
// Risk of side effects in unrelated code
```

#### After: Modular Debugging
```go
// Isolate issue to specific package
// Fix in focused, testable component
// Verify with package-specific tests
```

## Testing Strategy

### Unit Testing
```bash
# Test specific components
go test ./pkg/eni-manager/dpdk -v
go test ./pkg/eni-manager/network -v
go test ./pkg/eni-manager/kubernetes -v

# Test all components
go test ./pkg/eni-manager/... -v
```

### Integration Testing
```go
// Use mock implementations
cfg := testutil.TestConfig()
manager, err := coordinator.NewManager(cfg)
// Test with controlled environment
```

### Performance Testing
```bash
# Benchmark specific operations
go test -bench=. ./pkg/eni-manager/dpdk
go test -bench=. ./pkg/eni-manager/network
```

## Deployment Migration

### Container Images
- **No changes required**: Same Dockerfile and build process
- **Same image tags**: Existing deployment scripts work unchanged
- **Same entry point**: Container starts with same command

### Kubernetes Deployment
```yaml
# No changes to deployment YAML
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: eni-manager
spec:
  template:
    spec:
      containers:
      - name: eni-manager
        image: ghcr.io/johnlam90/aws-multi-eni-controller:v1.3.0
        # Same configuration, same behavior
```

### Helm Charts
- **No changes required**: Existing Helm values work unchanged
- **Same configuration**: All existing settings preserved
- **Same behavior**: Identical functionality

## Monitoring and Observability

### Enhanced Status Endpoints
```go
// New status information available
status := manager.GetManagerStatus()
// Returns detailed component status:
// - Circuit breaker state
// - Bound interfaces count
// - Active operations
// - Error statistics
```

### Better Logging
```go
// Component-specific logging
log.Printf("[DPDK] Binding PCI device %s to driver %s", pciAddr, driver)
log.Printf("[Network] Bringing up interface %s", ifaceName)
log.Printf("[K8s] Updating NodeENI %s status", nodeENI.Name)
```

### Metrics Collection
```go
// Circuit breaker metrics
cbStats := manager.GetCircuitBreakerStats()
// Component health metrics
healthStatus := manager.GetHealthStatus()
```

## Troubleshooting

### Common Issues and Solutions

#### 1. Import Path Changes
```go
// If you have custom code importing the old main package
// Before:
// import "github.com/johnlam90/aws-multi-eni-controller/cmd/eni-manager"

// After:
import "github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/dpdk"
import "github.com/johnlam90/aws-multi-eni-controller/pkg/eni-manager/network"
```

#### 2. Function Signature Changes
```go
// Functions are now methods on manager structs
// Before: bindPCIDeviceToDPDK(pciAddr, driver)
// After: manager.BindPCIDeviceToDPDK(pciAddr, driver)
```

#### 3. Error Handling Updates
```go
// Enhanced error information available
if dpdkErr, ok := err.(*dpdk.DPDKError); ok {
    log.Printf("DPDK operation %s failed for PCI %s: %v", 
        dpdkErr.Operation, dpdkErr.PCIAddr, dpdkErr.Err)
}
```

## Rollback Plan

### If Issues Arise
1. **Immediate rollback**: Use `main_old.go` (original monolithic version)
2. **Container rollback**: Use previous image tag
3. **Configuration rollback**: No configuration changes needed

### Rollback Steps
```bash
# 1. Replace main.go with original version
cd cmd/eni-manager
mv main.go main_new.go
mv main_old.go main.go

# 2. Rebuild and deploy
go build -o bin/eni-manager cmd/eni-manager/main.go

# 3. Update container image if needed
docker build -t eni-manager:rollback .
```

## Validation Checklist

### Pre-Migration
- [ ] Backup current deployment
- [ ] Document current configuration
- [ ] Prepare rollback plan
- [ ] Test in staging environment

### Post-Migration
- [ ] Verify all NodeENI resources are processed
- [ ] Check DPDK bindings are working
- [ ] Validate network interface operations
- [ ] Monitor for errors or performance issues
- [ ] Verify SR-IOV device plugin configuration

### Success Criteria
- [ ] All existing functionality works unchanged
- [ ] No performance degradation
- [ ] No new errors in logs
- [ ] All tests pass
- [ ] Circuit breaker is in closed state
- [ ] Component status endpoints respond correctly

## Support and Resources

### Documentation
- [Modular Architecture Overview](./MODULAR_ARCHITECTURE.md)
- [Component API Documentation](./API_REFERENCE.md)
- [Testing Guide](./TESTING_GUIDE.md)

### Getting Help
- Check component-specific logs for detailed error information
- Use status endpoints for component health
- Review circuit breaker state for DPDK issues
- Consult package-specific documentation

### Contributing
- Follow modular development practices
- Add tests for new functionality
- Update documentation for changes
- Use appropriate package for new features
