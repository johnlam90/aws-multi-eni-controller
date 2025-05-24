# Multi-Subnet NodeENI Configuration - Clarification and Correct Usage

## ❗ Important Clarification

The multi-subnet feature in NodeENI is often misunderstood. This document clarifies how it actually works and provides correct usage examples.

## How Multi-Subnet Actually Works

### ✅ What Happens

When you specify multiple subnets in `subnetIDs`:

```yaml
spec:
  subnetIDs:
  - subnet-a
  - subnet-b
  - subnet-c
  deviceIndex: 1
```

**The controller creates ONE ENI per subnet, per matching node:**

- Each node gets **3 ENIs** (one in each subnet)
- Device indices are assigned as: 1, 2, 3
- All ENIs use the same security groups
- All ENIs are attached to the same node

### ❌ Common Misconceptions

#### Misconception 1: "High Availability"

```yaml
# This is NOT HA - this creates multiple ENIs per node
subnetIDs:
- subnet-1a-private  # AZ-1a
- subnet-1b-private  # AZ-1b
- subnet-1c-private  # AZ-1c
```

**Reality**: Each node gets 3 ENIs. If the node fails, all 3 ENIs are lost.

#### Misconception 2: "Subnet Selection"

```yaml
# This is NOT "pick one subnet" - it's "use ALL subnets"
subnetIDs:
- subnet-option-1
- subnet-option-2
```

**Reality**: Creates ENIs in BOTH subnets on EVERY matching node.

#### Misconception 3: "Load Balancing"

```yaml
# This does NOT distribute nodes across subnets
subnetIDs:
- subnet-east
- subnet-west
```

**Reality**: Every node gets ENIs in BOTH east AND west subnets.

## Correct Use Cases

### 1. Network Segmentation

**Use Case**: Separate network traffic by function

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: network-segmentation
spec:
  nodeSelector:
    tier: application
  subnetIDs:
  - subnet-frontend     # Web traffic
  - subnet-backend      # API traffic
  - subnet-database     # DB traffic
  deviceIndex: 1        # Creates ENIs at indices 1, 2, 3
```

**Result**: Each application node gets 3 ENIs for different traffic types.

### 2. Multi-Tenant Networking

**Use Case**: Isolate traffic for different tenants

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multi-tenant
spec:
  nodeSelector:
    workload: multi-tenant
  subnetIDs:
  - subnet-tenant-a
  - subnet-tenant-b
  deviceIndex: 2        # Creates ENIs at indices 2, 3 (eth0 is primary)
```

**Result**: Each node can serve multiple tenants with isolated networks.

### 3. Performance Multi-Homing

**Use Case**: Aggregate bandwidth across multiple networks

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: bandwidth-aggregation
spec:
  nodeSelector:
    performance: high
  subnetIDs:
  - subnet-performance-1
  - subnet-performance-2
  deviceIndex: 1        # Creates ENIs at indices 1, 2
  mtu: 9000            # Jumbo frames for performance
```

**Result**: Each node gets 2 high-performance ENIs for bandwidth aggregation.

## Device Index Assignment

The controller assigns device indices automatically:

```yaml
deviceIndex: 1  # Base index
subnetIDs:
- subnet-a      # Gets device index 1
- subnet-b      # Gets device index 2  
- subnet-c      # Gets device index 3
```

**Network Interface Mapping**:

- `eth0`: Primary ENI (AWS managed)
- `eth1`: First additional ENI (subnet-a)
- `eth2`: Second additional ENI (subnet-b)
- `eth3`: Third additional ENI (subnet-c)

## Real High Availability Patterns

If you want actual HA, use these patterns instead:

### Pattern 1: Multiple NodeENI Resources

```yaml
# NodeENI for AZ-1a nodes
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-az-1a
spec:
  nodeSelector:
    topology.kubernetes.io/zone: us-east-1a
  subnetID: subnet-1a-private
---
# NodeENI for AZ-1b nodes  
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-az-1b
spec:
  nodeSelector:
    topology.kubernetes.io/zone: us-east-1b
  subnetID: subnet-1b-private
```

### Pattern 2: Node Pool Based Distribution

```yaml
# NodeENI for primary node pool
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-primary-pool
spec:
  nodeSelector:
    node-pool: primary
  subnetID: subnet-primary
---
# NodeENI for secondary node pool
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-secondary-pool
spec:
  nodeSelector:
    node-pool: secondary
  subnetID: subnet-secondary
```

## DPDK Considerations with Multi-Subnet

When using DPDK with multiple subnets:

```yaml
spec:
  subnetIDs:
  - subnet-dpdk-1
  - subnet-dpdk-2
  enableDPDK: true
  dpdkPCIAddress: "0000:00:06.0"  # Only binds ONE ENI to DPDK
```

**Important**:

- Only ONE ENI can be bound to DPDK per PCI address
- The first ENI (lowest device index) typically gets DPDK binding
- Other ENIs remain in kernel mode

## Performance Implications

### Resource Usage

```yaml
# 3 subnets × 10 nodes = 30 ENIs total
subnetIDs:
- subnet-a
- subnet-b  
- subnet-c
```

**Consider**:

- ENI limits per instance type
- IP address consumption (each ENI gets private IPs)
- Security group rule complexity
- Network performance impact

### Best Practices

1. **Limit subnet count**: More ENIs = more complexity
2. **Plan IP addressing**: Each ENI consumes subnet IPs
3. **Monitor ENI limits**: Check AWS instance ENI limits
4. **Test performance**: Multiple ENIs can impact performance

## Migration from Incorrect Usage

If you have configurations that were intended for HA but actually create multiple ENIs:

### Before (Incorrect)

```yaml
spec:
  subnetIDs:
  - subnet-1a-private
  - subnet-1b-private
  - subnet-1c-private
```

### After (Correct for HA)

```yaml
# Option 1: Use single subnet with AZ-aware node selection
spec:
  nodeSelector:
    topology.kubernetes.io/zone: us-east-1a
  subnetID: subnet-1a-private
---
# Option 2: Keep multi-subnet if you actually want multi-homing
spec:
  nodeSelector:
    multi-homing: enabled
  subnetIDs:
  - subnet-frontend
  - subnet-backend
```

## Summary

- **Multi-subnet = Multi-ENI per node**, not HA
- **Use cases**: Network segmentation, multi-tenancy, performance
- **For HA**: Use multiple NodeENI resources with different node selectors
- **DPDK**: Only one ENI per PCI address can use DPDK
- **Plan carefully**: Consider resource limits and performance impact
