# Multus CNI Integration Guide

This document provides comprehensive guidance on integrating AWS Multi-ENI Controller with Multus CNI for advanced networking capabilities in Kubernetes on AWS.

## Overview

The AWS Multi-ENI Controller was specifically designed to work with Multus CNI to enable multi-network support for Kubernetes pods running on AWS. This integration solves the challenge of dynamically provisioning and managing ENIs for Multus CNI without complex infrastructure templates.

## Prerequisites

Before integrating AWS Multi-ENI Controller with Multus CNI, ensure you have:

1. A Kubernetes cluster running on AWS (e.g., EKS)
2. AWS Multi-ENI Controller installed in your cluster
3. Multus CNI installed in your cluster
4. Appropriate IAM permissions for EC2 ENI operations

## How It Works

The integration between AWS Multi-ENI Controller and Multus CNI follows this workflow:

1. AWS Multi-ENI Controller watches for NodeENI resources
2. When a matching node is found, the controller creates an ENI in AWS
3. The controller attaches the ENI to the node at the specified device index
4. The ENI Manager DaemonSet configures the network interface on the node
5. Multus CNI uses this interface to provide additional networks to pods
6. Pods use these additional networks through NetworkAttachmentDefinition resources

## Installation

### 1. Install AWS Multi-ENI Controller

```bash
helm install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.3.0 \
  --namespace eni-controller-system --create-namespace
```

### 2. Install Multus CNI

```bash
kubectl apply -f https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/master/deployments/multus-daemonset.yml
```

## Configuration

### 1. Create NodeENI Resources

Create a NodeENI resource to provision an ENI:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-config
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2  # This will create eth2 on the node
  mtu: 9001
  deleteOnTermination: true
  description: "Multus ENI for secondary network interfaces"
```

### 2. Create NetworkAttachmentDefinition

Create a NetworkAttachmentDefinition that uses this interface:

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: secondary-network
spec:
  config: '{
    "cniVersion": "0.3.1",
    "type": "ipvlan",
    "master": "eth2",
    "mode": "l2",
    "ipam": {
      "type": "host-local",
      "subnet": "192.168.1.0/24",
      "rangeStart": "192.168.1.200",
      "rangeEnd": "192.168.1.250"
    }
  }'
```

### 3. Deploy Pods with Multiple Networks

Deploy pods that use this network:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: multinet-pod
  annotations:
    k8s.v1.cni.cncf.io/networks: secondary-network
spec:
  containers:
  - name: multinet-container
    image: nginx
```

## Advanced Configurations

### Multiple Subnets

For applications that need to connect to multiple subnets:

```yaml
# First NodeENI for subnet1
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-subnet1
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2
---
# Second NodeENI for subnet2
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-subnet2
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-abcdef1234567890
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 3
```

### DPDK Integration

For high-performance networking applications:

```yaml
# NodeENI with DPDK enabled
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: dpdk-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-0f59b4f14737be9ad
  securityGroupIDs:
  - sg-05da196f3314d4af8
  deviceIndex: 2
  dpdkEnabled: true
  dpdkDriver: "vfio-pci"
  dpdkResourceName: "intel.com/intel_sriov_netdevice"
```

Create a NetworkAttachmentDefinition for DPDK:

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: dpdk-network
spec:
  config: '{
    "cniVersion": "0.3.1",
    "type": "host-device",
    "device": "0000:00:06.0",
    "vlan": 1000,
    "ipam": {
      "type": "host-local",
      "subnet": "192.168.1.0/24",
      "rangeStart": "192.168.1.200",
      "rangeEnd": "192.168.1.250"
    }
  }'
```

Deploy a pod that uses DPDK:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dpdk-pod
  annotations:
    k8s.v1.cni.cncf.io/networks: dpdk-network
spec:
  containers:
  - name: dpdk-container
    image: dpdk-app:latest
    resources:
      limits:
        intel.com/intel_sriov_netdevice: 1
```

## Common Use Cases

### Network Isolation

Isolate network traffic between different applications or tenants:

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: isolated-network
  namespace: tenant-a
spec:
  config: '{
    "cniVersion": "0.3.1",
    "type": "ipvlan",
    "master": "eth2",
    "mode": "l2",
    "ipam": {
      "type": "host-local",
      "subnet": "10.10.0.0/24"
    }
  }'
```

### Multi-VPC Connectivity

Connect pods to multiple VPCs:

```yaml
# Create NodeENI resources for different VPCs
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: vpc1-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-vpc1
  securityGroupIDs:
  - sg-vpc1
  deviceIndex: 2
---
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: vpc2-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-vpc2
  securityGroupIDs:
  - sg-vpc2
  deviceIndex: 3
```

### Network Security Appliances

Deploy network security appliances that need to inspect traffic:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: network-firewall
  annotations:
    k8s.v1.cni.cncf.io/networks: ingress-network,egress-network
spec:
  containers:
  - name: firewall
    image: network-firewall:latest
    securityContext:
      capabilities:
        add: ["NET_ADMIN"]
```

## Best Practices

### Device Index Management

Maintain consistent device indices across your cluster:

- eth0: Primary ENI (managed by AWS)
- eth1: Reserved for AWS CNI (if using)
- eth2: First Multus network
- eth3: Second Multus network
- eth4: Third Multus network

### Network Attachment Definition Naming Conventions

Use a consistent naming convention for NetworkAttachmentDefinitions:

- Format: `<purpose>-<subnet>-<index>`
- Example: `app-subnet1-eth2`

### Resource Requests for Multi-Network Pods

Always specify resource requests for pods with multiple networks to ensure proper scheduling.

### Monitoring ENI Attachments

Create a monitoring solution to track ENI attachments and ensure they are properly configured.

## Troubleshooting

### Common Issues

1. **Pods can't access secondary networks**:
   - Verify the NetworkAttachmentDefinition references the correct interface name
   - Check Multus logs: `kubectl logs -n kube-system daemonset/kube-multus-ds`
   - Verify the interface is up on the node: `ip link show eth2`

2. **Wrong IP assignment on secondary networks**:
   - Check IPAM configuration in the NetworkAttachmentDefinition
   - Verify subnet configuration matches the actual ENI subnet

3. **DPDK binding issues**:
   - Verify DPDK kernel modules are loaded: `lsmod | grep vfio`
   - Check SRIOV device plugin is running: `kubectl get pods -n kube-system | grep sriov`

## References

- [Multus CNI GitHub Repository](https://github.com/k8snetworkplumbingwg/multus-cni)
- [Multus CNI Documentation](https://github.com/k8snetworkplumbingwg/multus-cni/blob/master/docs/quickstart.md)
- [AWS Multi-ENI Controller Documentation](../README.md)
- [DPDK Integration Guide](dpdk.md)
