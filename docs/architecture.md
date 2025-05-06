# AWS Multi-ENI Controller Architecture

The AWS Multi-ENI Controller automatically creates and attaches AWS Elastic Network Interfaces (ENIs) to Kubernetes nodes based on node labels.

## Architecture Diagram

```ascii
                                +-------------------+
                                |                   |
                                | AWS Multi-ENI     |
                                | Controller        |
                                |                   |
                                +--------+----------+
                                         |
                 +------------------------|------------------------+
                 |                        |                        |
                 v                        v                        v
        +----------------+      +-----------------+      +------------------+
        |                |      |                 |      |                  |
        | Kubernetes API |      | ENI 1, ENI 2    |      | ENI 3            |
        |                |      | (subnet-a)      |      | (subnet-b)       |
        |                |      |                 |      |                  |
        +-------+--------+      +--------+--------+      +---------+--------+
                |                     /  \                          |
                |                    /    \                         |
                v                   /      \                        v
        +----------------+         /        \              +------------------+
        |                |        /          \             |                  |
        | Node 1         |<------+            +----------->| Node 2           |
        | (labeled)      |                                 | (labeled)        |
        | subnet: a,b    |                                 | subnet: b        |
        +----------------+                                 +------------------+
```

## How It Works

1. **Watch**: The controller watches for nodes with specific labels (e.g., `ng: multi-eni`)
2. **Create**: When a matching node is found, the controller creates ENIs in the specified subnets
3. **Attach**: The controller attaches the ENIs to the nodes at the specified device indices
4. **Update**: The controller updates the NodeENI status with attachment information
5. **Cleanup**: When nodes no longer match the selector, the controller detaches and deletes the ENIs

### Multi-Subnet Support

The controller supports multiple subnets in two ways:

1. **Multiple NodeENI Resources**: Create multiple NodeENI resources with different subnet IDs and device indices
2. **Subnet Selection via Node Labels**: Use node labels to determine which nodes get ENIs from which subnets

#### Example: Multiple NodeENI Resources

```yaml
# First NodeENI for subnet-a
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-subnet-a
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad  # Subnet A
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 1
  deleteOnTermination: true
  description: "ENI for subnet A"
```

```yaml
# Second NodeENI for subnet-b
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-subnet-b
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-abcdef1234567890  # Subnet B
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2
  deleteOnTermination: true
  description: "ENI for subnet B"
```

With this approach, each node matching the selector would get two ENIs - one from subnet A attached at device index 1, and one from subnet B attached at device index 2.

#### Example: Subnet Selection Based on Node Labels

```yaml
# NodeENI for subnet-a targeting nodes with label subnet=a
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-subnet-a
spec:
  nodeSelector:
    ng: multi-eni
    subnet: a
  subnetID: subnet-0f59b4f14737be9ad  # Subnet A
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 1
  deleteOnTermination: true
  description: "ENI for subnet A nodes"
```

```yaml
# NodeENI for subnet-b targeting nodes with label subnet=b
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: nodeeni-subnet-b
spec:
  nodeSelector:
    ng: multi-eni
    subnet: b
  subnetID: subnet-abcdef1234567890  # Subnet B
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 1
  deleteOnTermination: true
  description: "ENI for subnet B nodes"
```

With this approach, you would label your nodes accordingly:
```bash
# For nodes that should get an ENI from subnet A
kubectl label node node1 ng=multi-eni subnet=a

# For nodes that should get an ENI from subnet B
kubectl label node node2 ng=multi-eni subnet=b
```

## Components

- **AWS Multi-ENI Controller**: Kubernetes controller that manages ENI lifecycle
- **NodeENI Custom Resource**: Defines which nodes should get ENIs and their configuration
- **AWS ENIs**: Network interfaces created and attached to EC2 instances
- **Kubernetes Nodes**: EC2 instances running Kubernetes workloads

## Workflow

1. User creates a NodeENI custom resource specifying:
   - Node selector (e.g., `ng: multi-eni`)
   - ENI configurations (subnet, security groups, etc.)
   - Device index for attachment

2. Controller watches for:
   - New NodeENI resources
   - Changes to existing NodeENI resources
   - Nodes matching the selector

3. When a node matches the selector:
   - Controller creates ENIs as specified
   - Attaches ENIs to the node
   - Updates NodeENI status with attachment details

4. When a node no longer matches:
   - Controller detaches ENIs
   - Deletes ENIs
   - Updates NodeENI status

5. When a NodeENI is deleted:
   - Controller ensures all associated ENIs are detached and deleted
