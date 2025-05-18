# Cloud-Native DPDK Integration for AWS Multi-ENI Controller

This guide explains how to implement a cloud-native DPDK integration for the AWS Multi-ENI Controller using Kubernetes-native patterns and resources.

## Architecture

The cloud-native DPDK integration consists of the following components:

1. **DPDK Operator**: A Kubernetes operator that manages DPDK configuration across the cluster
2. **DPDK Driver Loader**: A DaemonSet that loads the required kernel modules on each node
3. **SRIOV Device Plugin**: A DaemonSet that exposes DPDK-bound devices to pods
4. **DPDK Controller**: A deployment that watches DPDKConfig resources and updates NodeENI resources accordingly
5. **DPDKConfig CRD**: A custom resource definition for configuring DPDK across the cluster

This architecture follows cloud-native principles:
- Declarative configuration using custom resources
- Kubernetes-native management of resources
- Automatic reconciliation of desired state
- No direct host access required from outside the cluster

## Prerequisites

- Kubernetes cluster with the AWS Multi-ENI Controller installed
- Nodes with SR-IOV capable network interfaces
- Kubernetes version 1.16 or later

## Installation

### Step 1: Install the DPDK Operator

```bash
kubectl apply -f dpdk-crd.yaml
kubectl apply -f dpdk-operator.yaml
kubectl apply -f dpdk-controller.yaml
```

### Step 2: Create a DPDKConfig Resource

The DPDKConfig resource defines how DPDK should be configured across the cluster:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: DPDKConfig
metadata:
  name: default-dpdk-config
spec:
  nodeSelector:
    ng: multi-eni
  driverOptions:
    enableUnsafeNoIOMMU: true
    writeCombing: true
  resourceMapping:
  - deviceIndex: 2
    resourceName: intel.com/intel_sriov_netdevice_2
    driver: vfio-pci
```

Apply this configuration:

```bash
kubectl apply -f dpdk-config.yaml
```

### Step 3: Verify the Installation

Check that the DPDK Operator components are running:

```bash
kubectl get pods -n dpdk-operator
```

Check that nodes have been labeled for DPDK:

```bash
kubectl get nodes -l dpdk.aws.k8s/ready=true
```

Check the status of the DPDKConfig resource:

```bash
kubectl get dpdkconfigs
```

## How It Works

### DPDK Driver Loader

The DPDK Driver Loader DaemonSet runs on all nodes that match the node selector in the DPDKConfig resource. It:

1. Runs an init container that loads the required kernel modules (vfio, vfio-pci)
2. Configures the modules for DPDK (enables unsafe NOIOMMU mode if needed)
3. Labels the node as `dpdk.aws.k8s/ready=true` when the setup is complete
4. Runs a monitor container that ensures the modules remain loaded

### DPDK Controller

The DPDK Controller watches DPDKConfig resources and:

1. Identifies nodes that match the node selector in the DPDKConfig
2. Labels these nodes for DPDK
3. Finds NodeENI resources that apply to these nodes and have matching device indices
4. Updates the NodeENI resources to enable DPDK with the specified driver and resource name
5. Updates the status of the DPDKConfig resource

### SRIOV Device Plugin

The SRIOV Device Plugin runs on nodes labeled as `dpdk.aws.k8s/ready=true` and:

1. Discovers DPDK-bound devices on the node
2. Exposes these devices as Kubernetes resources
3. Allows pods to request these resources

### Integration with AWS Multi-ENI Controller

The AWS Multi-ENI Controller continues to work as before, but now:

1. NodeENI resources are automatically updated with DPDK configuration
2. The ENI Manager detects the DPDK configuration and binds interfaces to the specified driver
3. The SRIOV Device Plugin exposes the DPDK-bound interfaces as Kubernetes resources

## Using DPDK in Pods

To use DPDK in your pods, request the DPDK resource in your pod spec:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dpdk-app
spec:
  containers:
  - name: dpdk-app
    image: dpdk-app:latest
    resources:
      limits:
        intel.com/intel_sriov_netdevice_2: 1
    securityContext:
      privileged: true
```

## Troubleshooting

### Checking DPDK Driver Loader Logs

```bash
kubectl logs -n dpdk-operator -l app=dpdk-driver-loader -c driver-loader
```

### Checking DPDK Controller Logs

```bash
kubectl logs -n dpdk-operator -l app=dpdk-controller
```

### Checking SRIOV Device Plugin Logs

```bash
kubectl logs -n dpdk-operator -l app=sriov-device-plugin
```

### Checking Node Status

```bash
kubectl describe node <node-name>
```

Look for the DPDK-related labels and resource allocations.

### Checking DPDKConfig Status

```bash
kubectl describe dpdkconfig <config-name>
```

## Conclusion

This cloud-native approach to DPDK integration with the AWS Multi-ENI Controller provides:

1. **Declarative Configuration**: Define your DPDK configuration as Kubernetes resources
2. **Automatic Management**: The operator handles all the details of setting up DPDK
3. **Scalability**: Works across any number of nodes in the cluster
4. **Reliability**: Continuously monitors and ensures DPDK is properly configured
5. **Cloud-Native**: No direct host access or manual intervention required

By following this approach, you can leverage DPDK's performance benefits while maintaining cloud-native principles and practices.
