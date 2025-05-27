# SR-IOV Resource Name Mismatch Fix

## Problem Description

The AWS Multi-ENI Controller had a resource name mismatch issue in the SR-IOV (non-DPDK) configuration:

- **Test file** `deploy/samples/test-sriov-nondpdk.yaml` was requesting SR-IOV resources with names:
  - `intel.com/sriov_kernel_1`
  - `intel.com/sriov_kernel_2`

- **PCIDP config** was creating resources with the hardcoded name:
  - `intel.com/sriov_net`

This caused the test pods to fail because they couldn't find the requested SR-IOV resources.

## Root Cause

In `pkg/eni-manager/coordinator/manager.go`, the `updateSRIOVConfiguration` method was hardcoding the resource name and prefix for non-DPDK interfaces:

```go
// OLD CODE (BROKEN)
update := sriov.ResourceUpdate{
    PCIAddress:     iface.PCIAddress,
    Driver:         "ena",
    ResourceName:   "sriov_net",        // HARDCODED
    ResourcePrefix: "intel.com",        // HARDCODED
    Action:         "add",
}
```

The code was not using the `dpdkResourceName` field from the NodeENI specification when `enableDPDK` was false.

## Solution

### 1. Added Resource Name Parsing Function

Added a new helper method `parseResourceName` to the Manager struct that correctly parses resource names in the format `prefix/name`:

```go
func (m *Manager) parseResourceName(resourceName string) (string, string) {
    if resourceName == "" {
        // Return default values if no resource name is specified
        return "intel.com", "sriov_net"
    }
    
    // Split by "/" to separate prefix and name
    parts := strings.Split(resourceName, "/")
    if len(parts) == 2 {
        return parts[0], parts[1]
    }
    
    // If no "/" found, treat the whole string as the resource name with default prefix
    return "intel.com", resourceName
}
```

### 2. Updated SR-IOV Configuration Logic

Modified the `updateSRIOVConfiguration` method to use the parsed resource name from the NodeENI specification:

```go
// NEW CODE (FIXED)
// Parse resource name from NodeENI spec or use default
resourcePrefix, resourceName := m.parseResourceName(nodeENI.Spec.DPDKResourceName)

update := sriov.ResourceUpdate{
    PCIAddress:     iface.PCIAddress,
    Driver:         "ena",
    ResourceName:   resourceName,      // FROM NODEENI SPEC
    ResourcePrefix: resourcePrefix,    // FROM NODEENI SPEC
    Action:         "add",
}
```

### 3. Added Comprehensive Testing

Created unit tests to verify the parsing logic handles all cases correctly:

- Full resource names with prefix: `intel.com/sriov_kernel_1` → `intel.com`, `sriov_kernel_1`
- Resource names without prefix: `sriov_test` → `intel.com`, `sriov_test`
- Empty resource names: `""` → `intel.com`, `sriov_net`
- Different prefixes: `amazon.com/ena_kernel_test` → `amazon.com`, `ena_kernel_test`

## Files Modified

1. **`pkg/eni-manager/coordinator/manager.go`**
   - Added `parseResourceName` helper method
   - Updated `updateSRIOVConfiguration` to use parsed resource names
   - Added test helper method `ParseResourceNameForTest`

2. **`pkg/eni-manager/coordinator/manager_test.go`** (new file)
   - Added comprehensive unit tests for resource name parsing

3. **`test/integration/sriov_resource_name_test.go`** (new file)
   - Added integration tests for end-to-end verification

## Verification

The fix has been verified with:

1. **Unit Tests**: All parsing scenarios pass
2. **Compilation**: Code compiles without errors
3. **Backward Compatibility**: Existing DPDK functionality remains unaffected

## Expected Behavior After Fix

With this fix, when the test file `deploy/samples/test-sriov-nondpdk.yaml` is applied:

1. NodeENI resources specify:
   - `dpdkResourceName: "intel.com/sriov_kernel_1"`
   - `dpdkResourceName: "intel.com/sriov_kernel_2"`

2. The ENI Manager will create SR-IOV resources:
   - `intel.com/sriov_kernel_1`
   - `intel.com/sriov_kernel_2`

3. The test pod can successfully request and use these resources:
   ```yaml
   resources:
     requests:
       intel.com/sriov_kernel_1: "1"
       intel.com/sriov_kernel_2: "1"
   ```

## Impact

- ✅ **Fixed**: Non-DPDK SR-IOV resource name mismatch
- ✅ **Preserved**: All existing DPDK functionality
- ✅ **Enhanced**: Better resource name flexibility and parsing
- ✅ **Tested**: Comprehensive test coverage for the fix

The fix ensures that the AWS Multi-ENI Controller correctly honors the `dpdkResourceName` field from NodeENI resources for both DPDK and non-DPDK scenarios, resolving the resource name mismatch issue.
