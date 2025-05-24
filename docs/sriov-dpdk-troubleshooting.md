# SR-IOV and DPDK Troubleshooting Guide

This guide provides comprehensive troubleshooting information for SR-IOV and DPDK functionality in the AWS Multi-ENI Controller.

## Table of Contents

1. [Common Issues](#common-issues)
2. [Diagnostic Commands](#diagnostic-commands)
3. [Configuration Validation](#configuration-validation)
4. [Performance Issues](#performance-issues)
5. [Error Messages](#error-messages)
6. [Best Practices](#best-practices)

## Common Issues

### 1. DPDK Binding Failures

#### Symptoms
- Interface fails to bind to DPDK driver
- Error messages about device being in use
- Permission denied errors

#### Causes and Solutions

**Device in Use**
```bash
# Check if interface is being used by the system
ip route show | grep eth1
systemctl status NetworkManager
```

**Solution**: Ensure the interface is not configured in NetworkManager or systemd-networkd.

**Permission Issues**
```bash
# Check if running as root
whoami
# Check DPDK script permissions
ls -la /opt/dpdk/dpdk-devbind.py
```

**Solution**: Ensure the ENI manager is running with sufficient privileges.

**Driver Not Available**
```bash
# Check if DPDK drivers are loaded
lsmod | grep vfio
lsmod | grep uio
```

**Solution**: Load the required DPDK drivers:
```bash
modprobe vfio-pci
modprobe uio_pci_generic
```

### 2. SR-IOV Configuration Issues

#### Symptoms
- SR-IOV device plugin not recognizing devices
- Pods cannot access SR-IOV resources
- Configuration file corruption

#### Causes and Solutions

**Invalid Configuration Format**
```bash
# Validate SR-IOV configuration
cat /etc/pcidp/config.json | jq .
```

**Solution**: Ensure the configuration follows the correct format:
```json
{
  "resourceList": [
    {
      "resourceName": "intel.com/sriov_dpdk",
      "deviceType": "netdevice",
      "devices": [
        {
          "pciAddress": "0000:00:07.0",
          "driver": "vfio-pci"
        }
      ]
    }
  ]
}
```

**PCI Address Mismatch**
```bash
# List all PCI devices
lspci | grep Ethernet
# Check device details
lspci -vvv -s 0000:00:07.0
```

**Solution**: Verify PCI addresses match actual hardware.

### 3. AWS ENA Driver Issues

#### Symptoms
- Incorrect driver specified in SR-IOV configuration
- Network connectivity issues after DPDK binding

#### Causes and Solutions

**Wrong Driver Configuration**
```bash
# Check current driver
lspci -k -s 0000:00:07.0
```

**Solution**: For AWS ENA devices, ensure the driver is set to 'ena' in non-DPDK configurations:
```yaml
spec:
  dpdkDriver: "ena"  # For non-DPDK SR-IOV
```

## Diagnostic Commands

### System Information
```bash
# Check node information
kubectl get nodes -o wide

# Check ENI manager pods
kubectl get pods -n kube-system -l app=aws-multi-eni-controller

# Check ENI manager logs
kubectl logs -n kube-system -l app=aws-multi-eni-controller --tail=100
```

### DPDK Status
```bash
# Check DPDK device status
/opt/dpdk/dpdk-devbind.py --status

# Check bound devices
ls -la /sys/bus/pci/drivers/vfio-pci/

# Check DPDK processes
ps aux | grep dpdk
```

### SR-IOV Status
```bash
# Check SR-IOV device plugin status
kubectl get pods -n kube-system -l app=sriov-device-plugin

# Check SR-IOV resources
kubectl describe node <node-name> | grep -A 10 "Allocatable:"

# Check SR-IOV configuration
cat /etc/pcidp/config.json
```

### Network Interface Status
```bash
# List all network interfaces
ip link show

# Check interface details
ethtool -i eth1

# Check PCI device information
lspci -vvv | grep -A 20 Ethernet
```

## Configuration Validation

### NodeENI Resource Validation
```bash
# Check NodeENI resources
kubectl get nodeenis -o yaml

# Validate NodeENI configuration
kubectl describe nodeeni <nodeeni-name>
```

### SR-IOV Configuration Validation
```bash
# Validate JSON syntax
cat /etc/pcidp/config.json | jq empty

# Check for duplicate PCI addresses
cat /etc/pcidp/config.json | jq '.resourceList[].devices[].pciAddress' | sort | uniq -d

# Validate PCI address format
cat /etc/pcidp/config.json | jq '.resourceList[].devices[].pciAddress' | grep -E '^"[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]"$'
```

## Performance Issues

### DPDK Binding Performance
```bash
# Monitor DPDK binding operations
kubectl logs -n kube-system -l app=aws-multi-eni-controller | grep "DPDK-BIND"

# Check for concurrent operations
kubectl logs -n kube-system -l app=aws-multi-eni-controller | grep "concurrent"
```

### Memory Usage
```bash
# Check ENI manager memory usage
kubectl top pods -n kube-system -l app=aws-multi-eni-controller

# Check for memory leaks in logs
kubectl logs -n kube-system -l app=aws-multi-eni-controller | grep "memory leak"
```

## Error Messages

### DPDK Binding Errors

**"DPDK binding script not found"**
- **Cause**: DPDK binding script is missing or path is incorrect
- **Solution**: Verify the script exists at the configured path and has execute permissions

**"Another DPDK operation is already in progress"**
- **Cause**: Concurrent DPDK operations on the same PCI device
- **Solution**: Wait for the current operation to complete or check for stuck operations

**"Failed to bind PCI device: device is in use"**
- **Cause**: Network interface is actively being used by the system
- **Solution**: Ensure the interface is not configured in network management services

### SR-IOV Configuration Errors

**"Config file has no resources defined"**
- **Cause**: Empty or invalid SR-IOV configuration
- **Solution**: Ensure at least one resource is defined in the configuration

**"Invalid PCI address format"**
- **Cause**: PCI address doesn't match the expected format
- **Solution**: Use the format XXXX:XX:XX.X (e.g., 0000:00:07.0)

**"Resource name validation failed"**
- **Cause**: Invalid resource name format
- **Solution**: Use the format domain/resource (e.g., intel.com/sriov_dpdk)

## Best Practices

### Configuration Management
1. **Always backup configurations** before making changes
2. **Use consistent naming conventions** for resources
3. **Validate configurations** before applying them
4. **Monitor logs** for errors and warnings

### DPDK Operations
1. **Avoid concurrent operations** on the same device
2. **Use appropriate drivers** for your hardware
3. **Monitor resource usage** to prevent memory leaks
4. **Test in non-production** environments first

### SR-IOV Setup
1. **Use correct driver names** (ena for AWS ENA devices)
2. **Verify PCI addresses** match actual hardware
3. **Ensure device plugin compatibility** with your configuration
4. **Monitor resource allocation** in Kubernetes

### Monitoring and Alerting
1. **Set up metrics collection** for DPDK and SR-IOV operations
2. **Create alerts** for failed operations
3. **Monitor performance** metrics regularly
4. **Review logs** for patterns and issues

## Getting Help

If you encounter issues not covered in this guide:

1. **Check the logs** for detailed error messages
2. **Verify your configuration** against the examples
3. **Test with minimal configurations** to isolate issues
4. **Collect diagnostic information** using the commands above
5. **Report issues** with complete logs and configuration details

### Log Collection Script
```bash
#!/bin/bash
# Collect diagnostic information
echo "=== Node Information ===" > debug.log
kubectl get nodes -o wide >> debug.log

echo "=== ENI Manager Pods ===" >> debug.log
kubectl get pods -n kube-system -l app=aws-multi-eni-controller -o wide >> debug.log

echo "=== ENI Manager Logs ===" >> debug.log
kubectl logs -n kube-system -l app=aws-multi-eni-controller --tail=200 >> debug.log

echo "=== NodeENI Resources ===" >> debug.log
kubectl get nodeenis -o yaml >> debug.log

echo "=== SR-IOV Configuration ===" >> debug.log
cat /etc/pcidp/config.json >> debug.log 2>/dev/null || echo "SR-IOV config not found" >> debug.log

echo "=== DPDK Status ===" >> debug.log
/opt/dpdk/dpdk-devbind.py --status >> debug.log 2>/dev/null || echo "DPDK script not found" >> debug.log

echo "=== Network Interfaces ===" >> debug.log
ip link show >> debug.log

echo "=== PCI Devices ===" >> debug.log
lspci | grep Ethernet >> debug.log

echo "Debug information collected in debug.log"
```
