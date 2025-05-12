# Configuration Guide

This document provides detailed information about configuring the AWS Multi-ENI Controller.

## Table of Contents

- [NodeENI Resource Configuration](#nodeeni-resource-configuration)
- [Subnet Configuration](#subnet-configuration)
- [Security Group Configuration](#security-group-configuration)
- [MTU Configuration](#mtu-configuration)
- [Performance Configuration](#performance-configuration)
- [AWS Region Configuration](#aws-region-configuration)
- [Helm Chart Configuration](#helm-chart-configuration)

## NodeENI Resource Configuration

The NodeENI custom resource defines which nodes should get ENIs and how those ENIs should be configured.

### Basic Configuration

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-config
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad  # Use your subnet ID
  securityGroupIDs:
  - sg-05da196f3314d4af8  # Use your security group ID
  deviceIndex: 2
  deleteOnTermination: true
  description: "Multus ENI for secondary network interfaces"
```

### Configuration Fields

| Field | Description | Default | Required |
|-------|-------------|---------|----------|
| `nodeSelector` | Labels to select nodes that should have ENIs attached | N/A | Yes |
| `subnetID` | AWS Subnet ID where the ENI should be created | N/A | One of `subnetID`, `subnetIDs`, `subnetName`, or `subnetNames` is required |
| `subnetIDs` | List of AWS Subnet IDs where ENIs should be created | N/A | One of `subnetID`, `subnetIDs`, `subnetName`, or `subnetNames` is required |
| `subnetName` | AWS Subnet Name where the ENI should be created | N/A | One of `subnetID`, `subnetIDs`, `subnetName`, or `subnetNames` is required |
| `subnetNames` | List of AWS Subnet Names where ENIs should be created | N/A | One of `subnetID`, `subnetIDs`, `subnetName`, or `subnetNames` is required |
| `securityGroupIDs` | AWS Security Group IDs to attach to the ENI | N/A | One of `securityGroupIDs` or `securityGroupNames` is required |
| `securityGroupNames` | AWS Security Group Names to attach to the ENI | N/A | One of `securityGroupIDs` or `securityGroupNames` is required |
| `deviceIndex` | Device index for the ENI | 1 | No |
| `deleteOnTermination` | Whether to delete the ENI when the node is terminated | true | No |
| `description` | Description for the ENI | N/A | No |
| `mtu` | Maximum Transmission Unit (MTU) for the ENI | System default | No |

## Subnet Configuration

### Using Subnet IDs

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-subnet-id
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad  # Use your subnet ID
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2
```

### Using Subnet Names

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-subnet-name
spec:
  nodeSelector:
    ng: multi-eni
  subnetName: my-subnet-name  # Subnet with this Name tag will be used
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2
```

### Multiple Subnets

You can specify multiple subnets in your NodeENI resource. The controller will create ENIs in ALL specified subnets, using the same security group(s):

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multi-subnet-eni
spec:
  nodeSelector:
    ng: multi-eni
  # Specify multiple subnet IDs - ENIs will be created in ALL subnets
  subnetIDs:
  - subnet-0f59b4f14737be9ad  # Replace with your subnet ID
  - subnet-abcdef1234567890  # Replace with your subnet ID
  securityGroupIDs:
  - sg-05da196f3314d4af8  # Replace with your security group ID
  deviceIndex: 1  # This is the base device index, will be incremented for additional ENIs
```

The controller will automatically increment the device index for each additional ENI. For example, if you specify a device index of 1 and three subnets, the ENIs will be attached at device indices 1, 2, and 3.

## Security Group Configuration

### Using Security Group IDs

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-sg-id
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad
  securityGroupIDs:
  - sg-05da196f3314d4af8  # Use your security group ID
  deviceIndex: 2
```

### Using Security Group Names

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-sg-name
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad
  securityGroupNames:
  - my-security-group  # Security group with this name will be used
  deviceIndex: 2
```

## MTU Configuration

The controller supports configuring custom MTU values for ENIs, which is useful for enabling jumbo frames (9001 bytes) or other specialized network configurations:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: jumbo-frames-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2
  mtu: 9001  # Set MTU to 9001 for jumbo frames
```

## Performance Configuration

The controller includes configuration options to optimize performance in larger deployments:

### Controller Concurrency

Control how many NodeENI resources can be reconciled in parallel:

```yaml
# In Helm values.yaml
controller:
  maxConcurrentReconciles: 10  # Default: 5
```

Or set the environment variable in the deployment:

```yaml
env:
- name: MAX_CONCURRENT_RECONCILES
  value: "10"
```

### ENI Cleanup Concurrency

Control how many ENIs can be cleaned up in parallel:

```yaml
# In Helm values.yaml
controller:
  maxConcurrentENICleanup: 5  # Default: 3
```

Or set the environment variable in the deployment:

```yaml
env:
- name: MAX_CONCURRENT_ENI_CLEANUP
  value: "5"
```

## AWS Region Configuration

The controller needs to know which AWS region to use for creating and managing ENIs. There are several ways to configure this:

1. **Environment Variable**: Set the `AWS_REGION` environment variable in the deployment (default method)
2. **Instance Metadata**: When running on EC2, the controller can use the instance's region
3. **EKS Annotation**: For EKS clusters, you can use the cluster's region annotation

The default region is `us-west-2` if not specified.

```yaml
# In Helm values.yaml
awsRegion: us-east-1
```

Or set the environment variable in the deployment:

```yaml
env:
- name: AWS_REGION
  value: "us-east-1"
```

## Helm Chart Configuration

The Helm chart provides a number of configuration options. Here are the most important ones:

```yaml
# Image configuration
image:
  repository: ghcr.io/johnlam90/aws-multi-eni-controller
  tag: v1.2.7
  pullPolicy: IfNotPresent

# Namespace to deploy the controller
namespace: eni-controller-system

# AWS Region configuration
awsRegion: us-east-1

# Controller configuration
controller:
  # Maximum number of concurrent ENI cleanup operations
  maxConcurrentENICleanup: 3
  # Maximum number of concurrent reconciles
  maxConcurrentReconciles: 5

# ENI Manager configuration
eniManager:
  # Default MTU to set on ENIs (0 to use MTU from NodeENI resources)
  defaultMTU: 0

# Node selector for the ENI manager daemonset
nodeSelector:
  # Empty by default, users should specify their own node selector
  # Example: ng: multi-eni
```

For a complete list of configuration options, see the [Helm Chart README](../charts/aws-multi-eni-controller/README.md).
