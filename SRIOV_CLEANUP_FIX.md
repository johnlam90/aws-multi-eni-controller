# SR-IOV Resource Cleanup Fix

## Problem Description

After successfully fixing the SR-IOV resource name mismatch issue, a cleanup problem was identified:

**Working Behavior:**
- ✅ Non-DPDK SR-IOV resources are created correctly with proper resource names
- ✅ SR-IOV device plugin pod restarts properly when resources are added
- ✅ Resources show correctly in `kubectl describe node`
- ✅ Test pods can successfully request and use the SR-IOV resources

**Issue to Fix:**
When NodeENI resources with non-DPDK SR-IOV configuration are deleted:
1. ❌ The `pcidp config.json` file still contained the SR-IOV resource entries (they were not being removed)
2. ❌ The SR-IOV device plugin pod was not being restarted to reflect the configuration changes
3. ❌ Stale SR-IOV resources remained available on the node even after the NodeENI was deleted

## Root Cause Analysis

The cleanup logic was incomplete in several areas:

1. **Missing Resource Tracking**: No mechanism to track which non-DPDK SR-IOV resources belong to which NodeENI
2. **Incomplete Cleanup Function**: `findSRIOVResourcesForNodeENI` returned an empty list (simplified implementation)
3. **DPDK-Only Cleanup**: `performSRIOVCleanup` only handled DPDK interfaces, not non-DPDK SR-IOV resources
4. **No State Management**: The system couldn't determine what to clean up when a NodeENI was deleted

## Solution Implemented

### 1. Added Resource Tracking System

**New Data Structure:**
```go
// SRIOVResourceInfo tracks SR-IOV resource information for cleanup
type SRIOVResourceInfo struct {
    PCIAddress     string
    ResourceName   string
    ResourcePrefix string
    Driver         string
    NodeENIName    string
}

// Added to Manager struct
nodeENISRIOVResources map[string][]SRIOVResourceInfo // Track SR-IOV resources by NodeENI name
```

### 2. Resource Tracking During Creation

**Updated `updateSRIOVConfiguration` method:**
```go
// Track this resource for cleanup purposes
m.trackSRIOVResource(nodeENI.Name, SRIOVResourceInfo{
    PCIAddress:     iface.PCIAddress,
    ResourceName:   resourceName,
    ResourcePrefix: resourcePrefix,
    Driver:         "ena",
    NodeENIName:    nodeENI.Name,
})
```

**New Helper Function:**
```go
func (m *Manager) trackSRIOVResource(nodeENIName string, resourceInfo SRIOVResourceInfo) {
    // Thread-safe tracking with duplicate detection
    // Maintains a map of NodeENI name -> list of SR-IOV resources
}
```

### 3. Implemented Proper Resource Discovery

**Fixed `findSRIOVResourcesForNodeENI`:**
```go
func (m *Manager) findSRIOVResourcesForNodeENI(nodeENIName string) ([]string, error) {
    m.mutex.RLock()
    defer m.mutex.RUnlock()
    
    resources, exists := m.nodeENISRIOVResources[nodeENIName]
    if !exists || len(resources) == 0 {
        return []string{}, nil
    }
    
    var pciAddresses []string
    for _, resource := range resources {
        pciAddresses = append(pciAddresses, resource.PCIAddress)
    }
    
    return pciAddresses, nil
}
```

### 4. Enhanced Cleanup Process

**Updated `performSRIOVCleanup`:**
```go
func (m *Manager) performSRIOVCleanup(dpdkInterfacesToCleanup []string, nodeENIName string) bool {
    configModified := false
    
    // Clean up DPDK interfaces (existing logic)
    for _, pciAddr := range dpdkInterfacesToCleanup {
        if m.cleanupSingleDPDKInterface(pciAddr, nodeENIName) {
            configModified = true
        }
    }
    
    // NEW: Clean up non-DPDK SR-IOV resources
    if m.cleanupNonDPDKSRIOVResources(nodeENIName) {
        configModified = true
    }
    
    return configModified
}
```

**New Cleanup Function:**
```go
func (m *Manager) cleanupNonDPDKSRIOVResources(nodeENIName string) bool {
    // 1. Get tracked resources for the NodeENI
    // 2. Remove each PCI address from SR-IOV configuration
    // 3. Clean up tracking data
    // 4. Return whether configuration was modified
}
```

## Files Modified

1. **`pkg/eni-manager/coordinator/manager.go`**
   - Added `SRIOVResourceInfo` struct for resource tracking
   - Added `nodeENISRIOVResources` map to Manager struct
   - Added `trackSRIOVResource` helper method
   - Updated `updateSRIOVConfiguration` to track resources during creation
   - Initialized tracking map in `NewManager`

2. **`pkg/eni-manager/coordinator/cleanup.go`**
   - Fixed `findSRIOVResourcesForNodeENI` to actually find tracked resources
   - Updated `performSRIOVCleanup` to handle both DPDK and non-DPDK resources
   - Added `cleanupNonDPDKSRIOVResources` function for non-DPDK cleanup

3. **`pkg/eni-manager/coordinator/cleanup_fix_test.go`** (new file)
   - Added comprehensive tests for resource tracking and cleanup
   - Tests for duplicate handling and proper cleanup behavior

## Verification

The fix has been verified with:

1. **Unit Tests**: All resource tracking and cleanup scenarios pass
2. **Compilation**: Code compiles without errors
3. **Resource Lifecycle**: Complete tracking from creation to cleanup
4. **Thread Safety**: Proper mutex usage for concurrent access

## Expected Behavior After Fix

### During NodeENI Creation:
1. Non-DPDK SR-IOV resources are created with correct names (existing functionality)
2. **NEW**: Resources are tracked in `nodeENISRIOVResources` map for future cleanup
3. SR-IOV device plugin restarts to pick up new resources (existing functionality)

### During NodeENI Deletion:
1. **NEW**: System finds all tracked SR-IOV resources for the deleted NodeENI
2. **NEW**: Each PCI address is removed from the `pcidp config.json` file
3. **NEW**: Resource tracking data is cleaned up
4. **NEW**: SR-IOV device plugin restarts to reflect the configuration changes
5. **NEW**: Stale resources are no longer available on the node

### Example Cleanup Flow:
```
NodeENI "test-sriov-nondpdk-1" deleted
├── Find tracked resources: ["0000:00:07.0"] 
├── Remove PCI 0000:00:07.0 from pcidp config.json
├── Clean up tracking for "test-sriov-nondpdk-1"
├── Restart SR-IOV device plugin
└── Resource "intel.com/sriov_kernel_1" no longer available
```

## Impact

- ✅ **Fixed**: Non-DPDK SR-IOV resource cleanup when NodeENI is deleted
- ✅ **Enhanced**: Complete resource lifecycle management (creation + cleanup)
- ✅ **Preserved**: All existing DPDK functionality remains intact
- ✅ **Improved**: Proper state management and resource tracking
- ✅ **Tested**: Comprehensive test coverage for the cleanup functionality

The fix ensures that the AWS Multi-ENI Controller properly manages the complete lifecycle of non-DPDK SR-IOV resources, from creation through cleanup, resolving the stale resource issue and completing the SR-IOV functionality.
