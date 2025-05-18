# Cloud-Native DPDK Integration for AWS Multi-ENI Controller

This guide explains how to implement a cloud-native DPDK (Data Plane Development Kit) integration for the AWS Multi-ENI Controller. This integration enables high-performance networking for Kubernetes workloads using DPDK-accelerated interfaces.

## Architecture

The cloud-native DPDK integration consists of the following components:

1. **DPDK Feature Discovery**: A DaemonSet that detects DPDK capabilities on nodes and labels them accordingly
2. **DPDK Module Installer**: A DaemonSet that installs and loads the required kernel modules on nodes
3. **DPDK CRD**: A Custom Resource Definition for declarative DPDK configuration
4. **DPDK Operator**: A controller that watches DPDKConfig resources and updates NodeENI resources
5. **SRIOV Device Plugin**: A DaemonSet that exposes DPDK-bound devices to pods

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

### Step 1: Install the DPDK CRD

```bash
kubectl apply -f dpdk-crd.yaml
```

This creates the DPDKConfig custom resource definition, which allows you to declaratively configure DPDK across your cluster.

### Step 2: Deploy the DPDK Feature Discovery

```bash
kubectl apply -f dpdk-feature-discovery.yaml
```

The DPDK Feature Discovery DaemonSet runs on all nodes and:
- Detects if DPDK kernel modules are available
- Detects if DPDK kernel modules are loaded
- Labels nodes accordingly with `dpdk.aws.k8s/available` and `dpdk.aws.k8s/loaded`

### Step 3: Deploy the DPDK Module Installer

```bash
kubectl apply -f dpdk-module-installer.yaml
```

The DPDK Module Installer DaemonSet runs on nodes where DPDK modules are not available and:
- Builds and installs the required kernel modules (vfio, vfio-pci)
- Loads the modules and configures them for DPDK
- Labels nodes as `dpdk.aws.k8s/ready=true` when setup is complete

### Step 4: Deploy the DPDK Operator

```bash
kubectl apply -f dpdk-operator-deployment.yaml
```

The DPDK Operator watches DPDKConfig resources and:
- Identifies nodes that match the node selector in the DPDKConfig
- Finds NodeENI resources that apply to these nodes
- Updates the NodeENI resources to enable DPDK with the specified driver and resource name

### Step 5: Create the SRIOV Device Plugin Configuration

```bash
kubectl create configmap sriov-device-plugin-config -n kube-system --from-file=config.json=sriov-config.json
```

This creates a ConfigMap with the SRIOV Device Plugin configuration, which defines how DPDK-bound devices are exposed to pods.

### Step 6: Deploy the SRIOV Device Plugin

```bash
kubectl apply -f sriov-device-plugin.yaml
```

The SRIOV Device Plugin runs on nodes labeled as `dpdk.aws.k8s/loaded=true` and:
- Discovers DPDK-bound devices on the node
- Exposes these devices as Kubernetes resources
- Allows pods to request these resources

### Step 7: Patch the ENI Manager

```bash
kubectl patch daemonset eni-manager -n eni-controller-system --patch "$(cat eni-manager-patch.yaml)"
```

This patches the ENI Manager to use the device index from the NodeENI resource when selecting interfaces for DPDK binding.

### Step 8: Create a DPDKConfig Resource

```bash
kubectl apply -f dpdk-config.yaml
```

This creates a DPDKConfig resource that defines:
- Which nodes should have DPDK enabled (via node selector)
- DPDK driver options (e.g., enableUnsafeNoIOMMU, writeCombining)
- Resource mapping for DPDK-bound devices

## Verification

### Check Node Labels

```bash
kubectl get nodes --show-labels | grep dpdk
```

Look for nodes labeled with:
- `dpdk.aws.k8s/available=true`: DPDK modules are available
- `dpdk.aws.k8s/loaded=true`: DPDK modules are loaded
- `dpdk.aws.k8s/ready=true`: DPDK setup is complete
- `dpdk.aws.k8s/enabled=true`: Node is selected for DPDK by a DPDKConfig

### Check DPDKConfig Status

```bash
kubectl get dpdkconfigs
```

### Check NodeENI Resources

```bash
kubectl get nodeeni
kubectl describe nodeeni <nodeeni-name>
```

Look for:
- `enableDPDK: true`
- `dpdkDriver: vfio-pci`
- `dpdkResourceName: intel_sriov_netdevice_2`

### Check DPDK Binding

```bash
kubectl logs -n eni-controller-system -l app=eni-manager
```

Look for messages like:
```
Successfully bound interface eth2 (PCI: 0000:00:07.0) to DPDK driver vfio-pci
Successfully updated SRIOV device plugin config for interface eth2 with resource name intel.com/intel_sriov_netdevice_2
```

### Check DPDK Device Status

```bash
kubectl exec -n eni-controller-system <eni-manager-pod> -- /opt/dpdk/dpdk-devbind.py --status
```

### Check SRIOV Device Plugin

```bash
kubectl logs -n kube-system -l app=sriov-device-plugin
```

### Check Node Resources

```bash
kubectl get node <node-name> -o json | jq '.status.capacity'
```

Look for the DPDK resource: `"intel.com/intel_sriov_netdevice_2": "2"`

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

### DPDK Feature Discovery Issues

```bash
kubectl logs -n eni-controller-system -l app=dpdk-feature-discovery
```

### DPDK Module Installer Issues

```bash
kubectl logs -n eni-controller-system -l app=dpdk-module-installer
```

### DPDK Operator Issues

```bash
kubectl logs -n eni-controller-system -l app=dpdk-operator
```

### SRIOV Device Plugin Issues

```bash
kubectl logs -n kube-system -l app=sriov-device-plugin
```

### ENI Manager Issues

```bash
kubectl logs -n eni-controller-system -l app=eni-manager
```

## Configuration Reference

### DPDKConfig CRD

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: DPDKConfig
metadata:
  name: default-dpdk-config
spec:
  nodeSelector:
    kubernetes.io/hostname: ip-10-0-2-246.ec2.internal
  driverOptions:
    enableUnsafeNoIOMMU: true
    writeCombining: true
  resourceMapping:
  - deviceIndex: 2
    resourceName: intel_sriov_netdevice_2
    driver: vfio-pci
```

- `nodeSelector`: Selects nodes where DPDK should be enabled
- `driverOptions`: Configures DPDK driver options
- `resourceMapping`: Maps device indices to resource names and drivers

### SRIOV Device Plugin Configuration

```json
{
  "resourceList": [
    {
      "resourceName": "intel_sriov_netdevice_2",
      "selectors": {
        "vendors": ["1d0f"],
        "devices": ["ec20"],
        "drivers": ["vfio-pci"]
      }
    }
  ]
}
```

- `resourceName`: Name of the resource to expose
- `selectors`: Criteria for selecting devices (vendors, devices, drivers)

## Conclusion

This cloud-native DPDK integration with the AWS Multi-ENI Controller provides:

1. **Declarative Configuration**: Define your DPDK configuration as Kubernetes resources
2. **Automatic Management**: The operator handles all the details of setting up DPDK
3. **Scalability**: Works across any number of nodes in the cluster
4. **Reliability**: Continuously monitors and ensures DPDK is properly configured
5. **Cloud-Native**: No direct host access or manual intervention required
