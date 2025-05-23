# DPDK Device Binding in AWS Multi-ENI Controller

The AWS Multi-ENI Controller now supports binding ENIs to DPDK drivers for high-performance networking applications. This feature allows you to use the Data Plane Development Kit (DPDK) with your AWS ENIs, enabling packet acceleration and bypassing the kernel network stack for improved performance.

## Overview

DPDK (Data Plane Development Kit) is a set of libraries and drivers for fast packet processing. It is designed to run on any processor and works by bypassing the kernel network stack to process packets directly in user space. This results in higher throughput, lower latency, and reduced CPU usage for network-intensive applications.

The AWS Multi-ENI Controller can now automatically bind ENIs to DPDK drivers after they are attached to the node. This integration allows you to use DPDK with your Kubernetes workloads without manual configuration.

## Prerequisites

To use DPDK with the AWS Multi-ENI Controller, you need:

1. A Kubernetes cluster with the AWS Multi-ENI Controller installed
2. Nodes with the DPDK drivers installed (vfio-pci is recommended)
3. The DPDK device binding script (`dpdk-devbind.py`) installed on the nodes
4. The SRIOV Device Plugin installed in your cluster (for exposing DPDK devices to pods)

## Configuration

### Enabling DPDK in the ENI Manager

To enable DPDK device binding in the ENI Manager, you need to set the following environment variables:

```yaml
- name: ENABLE_DPDK
  value: "true"
- name: DEFAULT_DPDK_DRIVER
  value: "vfio-pci"
- name: DPDK_BINDING_SCRIPT
  value: "/opt/dpdk/dpdk-devbind.py"
- name: SRIOV_DP_CONFIG_PATH
  value: "/etc/pcidp/config.json"
```

If you're using the Helm chart, you can enable DPDK by setting the following values:

```yaml
eniManager:
  dpdk:
    enabled: true
    driver: "vfio-pci"
    bindingScript: "/opt/dpdk/dpdk-devbind.py"
    sriovDPConfigPath: "/etc/pcidp/config.json"
```

### Enabling DPDK for a NodeENI Resource

To enable DPDK for a specific NodeENI resource, add the following fields to the NodeENI spec:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: multus-eni-with-dpdk
spec:
  # ... other fields ...

  # Enable DPDK binding for this ENI
  enableDPDK: true

  # DPDK driver to use (default: vfio-pci)
  dpdkDriver: "vfio-pci"

  # Resource name to use for the DPDK interface in SRIOV device plugin
  dpdkResourceName: "intel.com/intel_sriov_netdevice_1"

  # PCI address of the device to bind to DPDK (recommended method)
  # If specified, this will be used instead of device index mapping
  dpdkPCIAddress: "0000:00:06.0"
```

**Note**: The `dpdkPCIAddress` field is the recommended method for DPDK binding as it provides direct control over which PCI device gets bound to DPDK, avoiding potential issues with device index mapping.

## How It Works

When DPDK is enabled, the ENI Manager will:

1. **Setup DPDK Environment**: Load required kernel modules (vfio, vfio-pci) and configure NOIOMMU mode
2. **Attach ENIs**: Attach ENIs to the node as usual using AWS APIs
3. **Interface Configuration**: Bring up the network interfaces and configure MTU
4. **DPDK Binding**: Bind the interfaces to the specified DPDK driver using either:
   - **PCI Address Method**: Direct binding using the `dpdkPCIAddress` field (recommended)
   - **Device Index Method**: Fallback method using device index to determine interface name
5. **SRIOV Integration**: Update the SRIOV Device Plugin configuration to expose DPDK devices to pods
6. **Status Tracking**: Update the NodeENI status with DPDK binding information

The DPDK binding process includes robust error handling and concurrent operation protection to ensure reliable binding across multiple interfaces.

## SRIOV Device Plugin Integration

The ENI Manager integrates with the SRIOV Device Plugin by updating its configuration file (`/etc/pcidp/config.json`) with the DPDK-bound devices. This allows pods to request DPDK devices using Kubernetes resource requests.

The SRIOV Device Plugin configuration is updated with the following information:

```json
{
  "resourceList": [
    {
      "resourceName": "intel.com/intel_sriov_netdevice_1",
      "deviceType": "netdevice",
      "devices": [
        {
          "pciAddress": "0000:00:06.0",
          "driver": "vfio-pci"
        }
      ]
    }
  ]
}
```

## Using DPDK in Pods

To use DPDK in your pods, you need to request the DPDK devices using Kubernetes resource requests:

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
        intel.com/intel_sriov_netdevice_1: 1
```

The SRIOV Device Plugin will expose the DPDK device to the pod, allowing your application to use DPDK for packet processing.

## Troubleshooting

If you encounter issues with DPDK device binding, check the following:

### Basic Checks

1. **Verify DPDK drivers are loaded**:
   ```bash
   # Check if vfio-pci module is loaded
   lsmod | grep vfio

   # Check DPDK binding script status
   kubectl exec -n eni-controller-system daemonset/eni-manager -- dpdk-devbind.py --status
   ```

2. **Check ENI Manager logs**:
   ```bash
   kubectl logs -n eni-controller-system daemonset/eni-manager -c eni-manager
   ```

3. **Verify SRIOV Device Plugin**:
   ```bash
   kubectl get pods -n kube-system | grep sriov
   kubectl logs -n kube-system <sriov-device-plugin-pod>
   ```

### Common Issues and Solutions

- **Missing DPDK drivers**: Ensure vfio-pci module is loaded on the host
- **PCI address not found**: Use `lspci` to verify the correct PCI address
- **Binding script not found**: Check that `/opt/dpdk/dpdk-devbind.py` exists and is executable
- **Insufficient permissions**: Ensure the ENI Manager runs with privileged security context
- **Concurrent binding operations**: The controller includes mutex protection to prevent race conditions
- **Device already bound**: Check if the device is already bound to another driver

## Best Practices

### PCI Address Method (Recommended)

Always use the `dpdkPCIAddress` field when possible for more reliable DPDK binding:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: dpdk-eni-example
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-12345678
  securityGroupIDs:
    - sg-12345678
  enableDPDK: true
  dpdkDriver: "vfio-pci"
  dpdkResourceName: "intel.com/sriov_dpdk"
  dpdkPCIAddress: "0000:00:06.0"  # Direct PCI targeting
```

### Resource Naming Convention

Use consistent resource naming for SRIOV device plugin integration:

- `intel.com/sriov_dpdk_1`, `intel.com/sriov_dpdk_2` for Intel devices
- `amazon.com/ena_dpdk_1`, `amazon.com/ena_dpdk_2` for AWS ENA devices

### Monitoring DPDK Status

Check the NodeENI status to verify DPDK binding:

```bash
kubectl get nodeeni -o yaml | grep -A 10 -B 5 dpdk
```

## Current Features

- **Concurrent Operation Protection**: Mutex-based locking prevents race conditions during binding
- **PCI Address Targeting**: Direct PCI device binding for reliable operation
- **Automatic Module Loading**: Init container handles vfio-pci module setup
- **SRIOV Integration**: Automatic device plugin configuration updates
- **Status Tracking**: Real-time DPDK binding status in NodeENI resources
- **Error Recovery**: Robust error handling and retry mechanisms

## Limitations

- DPDK device binding requires privileged access to the node
- Not all network cards support DPDK (AWS ENA adapters are supported)
- DPDK applications must be specifically designed to use DPDK libraries
- Once an interface is bound to DPDK, it cannot be used by the kernel network stack
- NOIOMMU mode is used for compatibility, which has security implications
