# Troubleshooting Guide

This document provides detailed troubleshooting steps for common issues with the AWS Multi-ENI Controller.

## Table of Contents

- [Common Issues](#common-issues)
  - [ENI Not Being Created](#eni-not-being-created)
  - [ENI Not Being Deleted](#eni-not-being-deleted)
  - [Interface Not Coming Up](#interface-not-coming-up)
  - [MTU Not Being Applied](#mtu-not-being-applied)
  - [Controller Pod Not Starting](#controller-pod-not-starting)
- [Debugging Techniques](#debugging-techniques)
  - [Checking Controller Logs](#checking-controller-logs)
  - [Checking ENI Manager Logs](#checking-eni-manager-logs)
  - [Checking AWS API Calls](#checking-aws-api-calls)
  - [Verifying AWS Permissions](#verifying-aws-permissions)
  - [Enabling Verbose Logging](#enabling-verbose-logging)
- [Verifying ENI Status](#verifying-eni-status)
- [Recovering from Failures](#recovering-from-failures)

## Common Issues

### ENI Not Being Created

If ENIs are not being created when you expect them to be:

1. **Check if the controller pod is running**:
   ```bash
   kubectl get pods -n eni-controller-system
   ```

2. **Check the controller logs**:
   ```bash
   kubectl logs -n eni-controller-system deployment/eni-controller
   ```

3. **Verify the node has the correct label**:
   ```bash
   kubectl get nodes --show-labels | grep your-label
   ```

4. **Ensure the subnet and security group IDs are correct**:
   ```bash
   # Check the NodeENI resource
   kubectl get nodeeni your-nodeeni-name -o yaml
   
   # Verify the subnet exists
   aws ec2 describe-subnets --subnet-ids subnet-12345678
   
   # Verify the security group exists
   aws ec2 describe-security-groups --group-ids sg-12345678
   ```

5. **Check AWS permissions**:
   Ensure the controller has permissions to create ENIs. See [AWS Permissions](#aws-permissions) for details.

### ENI Not Being Deleted

If ENIs are not being deleted when you expect them to be:

1. **Check if the finalizer is present on the NodeENI resource**:
   ```bash
   kubectl get nodeeni your-nodeeni-name -o yaml | grep finalizers -A 3
   ```

2. **Check the controller logs for any errors during deletion**:
   ```bash
   kubectl logs -n eni-controller-system deployment/eni-controller | grep "delete"
   ```

3. **Verify AWS permissions for the controller to delete ENIs**:
   ```bash
   # Test EC2 permissions
   aws ec2 describe-network-interfaces
   ```

4. **Check if the ENI is still attached to an instance**:
   ```bash
   # Get the ENI ID from the NodeENI status
   ENI_ID=$(kubectl get nodeeni your-nodeeni-name -o jsonpath='{.status.attachments[0].eniID}')
   
   # Describe the ENI
   aws ec2 describe-network-interfaces --network-interface-ids $ENI_ID
   ```

### Interface Not Coming Up

If the ENI is created and attached but the interface is not coming up:

1. **Check if the ENI Manager is running on the node**:
   ```bash
   kubectl get pods -n eni-controller-system -l app=eni-manager -o wide
   ```

2. **Check the ENI Manager logs**:
   ```bash
   kubectl logs -n eni-controller-system -l app=eni-manager
   ```

3. **Verify the interface is visible to the operating system**:
   ```bash
   # Get the node name
   NODE_NAME=$(kubectl get nodeeni your-nodeeni-name -o jsonpath='{.status.attachments[0].nodeID}')
   
   # Run a command on the node to check interfaces
   kubectl debug node/$NODE_NAME -it --image=busybox -- ip link
   ```

4. **Check if the interface is in DOWN state**:
   ```bash
   kubectl debug node/$NODE_NAME -it --image=busybox -- ip link | grep DOWN
   ```

### MTU Not Being Applied

If the MTU is not being applied to the interface:

1. **Ensure MTU is set in the NodeENI resource**:
   ```bash
   kubectl get nodeeni your-nodeeni-name -o yaml | grep mtu
   ```

2. **Check ENI Manager logs for MTU configuration issues**:
   ```bash
   kubectl logs -n eni-controller-system -l app=eni-manager | grep MTU
   ```

3. **Verify the interface MTU on the node**:
   ```bash
   kubectl debug node/$NODE_NAME -it --image=busybox -- ip link | grep mtu
   ```

4. **Check if the interface is using a different naming pattern**:
   The ENI Manager looks for interfaces with names like `eth*`, `ens*`, etc. If your interfaces use a different naming pattern, you may need to update the ENI Manager configuration.

### Controller Pod Not Starting

If the controller pod is not starting:

1. **Check the pod status**:
   ```bash
   kubectl describe pod -n eni-controller-system eni-controller-xxx
   ```

2. **Verify RBAC permissions are correctly configured**:
   ```bash
   kubectl get clusterrole,clusterrolebinding -l app=eni-controller
   ```

3. **Check if the Docker image is accessible**:
   ```bash
   # Pull the image locally to verify it's accessible
   docker pull ghcr.io/johnlam90/aws-multi-eni-controller:latest
   ```

4. **Check for resource constraints**:
   ```bash
   kubectl describe node your-node-name | grep -A 10 Allocated
   ```

## Debugging Techniques

### Checking Controller Logs

```bash
# Get basic logs
kubectl logs -n eni-controller-system deployment/eni-controller

# Follow logs in real-time
kubectl logs -n eni-controller-system deployment/eni-controller -f

# Get logs with timestamps
kubectl logs -n eni-controller-system deployment/eni-controller --timestamps

# Get logs for a specific pod if there are multiple replicas
kubectl logs -n eni-controller-system eni-controller-pod-name
```

### Checking ENI Manager Logs

```bash
# Get logs for all ENI Manager pods
kubectl logs -n eni-controller-system -l app=eni-manager

# Get logs for a specific ENI Manager pod
kubectl logs -n eni-controller-system eni-manager-pod-name

# Follow logs in real-time
kubectl logs -n eni-controller-system -l app=eni-manager -f
```

### Checking AWS API Calls

You can use CloudTrail to check AWS API calls made by the controller:

```bash
# Check CreateNetworkInterface calls
aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=CreateNetworkInterface

# Check DeleteNetworkInterface calls
aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=DeleteNetworkInterface

# Check AttachNetworkInterface calls
aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=AttachNetworkInterface
```

### Verifying AWS Permissions

```bash
# Test EC2 permissions
aws ec2 describe-instances
aws ec2 describe-network-interfaces
aws ec2 describe-subnets
```

### Enabling Verbose Logging

You can enable more verbose logging by editing the controller deployment:

```bash
kubectl edit deployment -n eni-controller-system eni-controller
```

Add `--v=5` to the command args:

```yaml
args:
- --v=5  # Add this line for verbose logging
```

## Verifying ENI Status

### Checking NodeENI Status

```bash
# Get the NodeENI resource
kubectl get nodeeni your-nodeeni-name -o yaml

# Check the status section for attachments
kubectl get nodeeni your-nodeeni-name -o jsonpath='{.status.attachments}'
```

### Checking ENI Status in AWS

```bash
# Get the ENI ID from the NodeENI status
ENI_ID=$(kubectl get nodeeni your-nodeeni-name -o jsonpath='{.status.attachments[0].eniID}')

# Describe the ENI
aws ec2 describe-network-interfaces --network-interface-ids $ENI_ID

# Check the instance attachments
INSTANCE_ID=$(kubectl get nodeeni your-nodeeni-name -o jsonpath='{.status.attachments[0].instanceID}')
aws ec2 describe-instances --instance-ids $INSTANCE_ID --query "Reservations[*].Instances[*].NetworkInterfaces[*].[NetworkInterfaceId,Attachment.DeviceIndex,Status,SubnetId]" --output table
```

## Recovering from Failures

### Manually Detaching an ENI

If an ENI is stuck in a detaching state:

```bash
# Get the attachment ID
ATTACHMENT_ID=$(aws ec2 describe-network-interfaces --network-interface-ids $ENI_ID --query "NetworkInterfaces[0].Attachment.AttachmentId" --output text)

# Force detach the ENI
aws ec2 detach-network-interface --attachment-id $ATTACHMENT_ID --force
```

### Manually Deleting an ENI

If an ENI is stuck and cannot be deleted by the controller:

```bash
# First, ensure the ENI is detached
aws ec2 detach-network-interface --attachment-id $ATTACHMENT_ID --force

# Then delete the ENI
aws ec2 delete-network-interface --network-interface-id $ENI_ID
```

### Removing a Finalizer

If a NodeENI resource is stuck in a deleting state due to a finalizer:

```bash
# Edit the NodeENI resource
kubectl edit nodeeni your-nodeeni-name

# Remove the finalizer from the metadata section
# metadata:
#   finalizers:
#   - networking.k8s.aws/finalizer  # Remove this line
```

### Restarting the Controller

If the controller is in a bad state, you can restart it:

```bash
kubectl rollout restart deployment -n eni-controller-system eni-controller
```
