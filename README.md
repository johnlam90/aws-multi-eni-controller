# AWS Multi-ENI Controller for Kubernetes

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Report Card](https://img.shields.io/badge/Go%20Report-A%2B-brightgreen?logo=go)](https://github.com/johnlam90/aws-multi-eni-controller/actions/workflows/go-report.yml)
[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](https://go.dev/)

A Kubernetes controller that automatically creates and attaches AWS Elastic Network Interfaces (ENIs) to nodes based on node labels. This controller is useful for workloads that require multiple network interfaces, such as networking plugins, security tools, or specialized applications.

## Overview

The ENI Controller watches for NodeENI custom resources and nodes with matching labels. When a node matches the selector in a NodeENI resource, the controller creates an ENI in the specified subnet with the specified security groups and attaches it to the node at the specified device index.

When a node no longer matches the selector or when the NodeENI resource is deleted, the controller automatically detaches and deletes the ENI, ensuring proper cleanup of AWS resources.

## Features

- **Dynamic ENI Management**: Automatically creates and attaches ENIs to nodes based on labels
- **Proper Cleanup**: Uses finalizers to ensure ENIs are properly detached and deleted when no longer needed
- **Parallel ENI Cleanup**: Efficiently cleans up multiple ENIs in parallel for improved performance on larger instances
- **Configurable**: Supports custom subnet, security groups, device index, and more
- **Cloud-Native**: Follows Kubernetes patterns for resource management
- **Region Aware**: Works in any AWS region with configurable region settings
- **Subnet Flexibility**: Supports both subnet IDs and subnet names (via AWS tags)
- **Multi-Subnet Support**: Can attach ENIs from different subnets to the same or different nodes
- **AWS SDK v2**: Uses the latest AWS SDK Go v2 for improved performance and features
- **Optimized Image**: Lightweight container image (22MB) for fast deployments
- **Library Support**: Can be used as a Go library in other projects for programmatic ENI management

## Building and Deploying

### Prerequisites

- Docker installed and configured
- Access to a Kubernetes cluster (e.g., EKS)
- AWS CLI configured with appropriate permissions
- kubectl installed and configured
- Go 1.22 or later (for development)
- Helm 3.0+ (for Helm installation method)

### Pre-built Container Images

Pre-built container images are available on GitHub Container Registry:

```bash
# Pull the latest stable image
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:latest

# Or use a specific version
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:v1.0.0

# For beta/development images from sandbox branches
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:beta-sandbox-testv0.1
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:beta-latest
```

You can find all available tags at [GitHub Container Registry](https://github.com/johnlam90/aws-multi-eni-controller/pkgs/container/aws-multi-eni-controller).

#### Beta Images from Sandbox Branches

For development and testing, beta images are automatically built from sandbox branches:

- **Branch-specific tags**: `beta-{branch-name}` (e.g., `beta-sandbox-testv0.1`)
- **Latest beta**: `beta-latest` always points to the most recent beta build
- **Commit-specific tags**: `beta-{branch-name}.{commit-sha}` for precise version tracking

Beta images are ideal for testing new features before they're merged into the main branch. To use a beta image in your deployment:

```yaml
# In your deployment YAML
spec:
  template:
    spec:
      containers:
      - name: controller
        image: ghcr.io/johnlam90/aws-multi-eni-controller:beta-sandbox-testv0.1
```

Or when using Helm:

```bash
helm install my-release ./aws-multi-eni-controller-chart.tgz --set image.tag=beta-sandbox-testv0.1
```

#### Making the Container Image Public

After the GitHub Actions workflow builds and pushes your container image, you'll need to manually make it public:

1. Go to your GitHub repository
2. Click on "Packages" in the right sidebar
3. Find and click on your container package (aws-multi-eni-controller)
4. Click on "Package settings" (⚙️ icon) at the bottom of the page
5. Under "Danger Zone", find "Change visibility"
6. Select "Public" and confirm the change

Once you've made the package public, anyone can pull it without authentication:

```bash
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:latest
```

### Required AWS Permissions

The controller requires the following AWS permissions:

- `ec2:CreateNetworkInterface`
- `ec2:DeleteNetworkInterface`
- `ec2:DescribeNetworkInterfaces`
- `ec2:AttachNetworkInterface`
- `ec2:DetachNetworkInterface`
- `ec2:ModifyNetworkInterfaceAttribute`
- `ec2:DescribeSubnets` (if using subnet names)

#### Setting up IAM permissions

For EKS clusters, you can use IAM roles for service accounts (IRSA):

1. Create an IAM policy with the required permissions:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Action": [
           "ec2:CreateNetworkInterface",
           "ec2:DeleteNetworkInterface",
           "ec2:DescribeNetworkInterfaces",
           "ec2:AttachNetworkInterface",
           "ec2:DetachNetworkInterface",
           "ec2:ModifyNetworkInterfaceAttribute",
           "ec2:DescribeSubnets"
         ],
         "Resource": "*"
       }
     ]
   }
   ```

2. Create an IAM role and attach the policy

3. Associate the IAM role with the service account used by the controller

### Building the Controller

1. Clone the repository:

   ```bash
   git clone https://github.com/johnlam90/aws-multi-eni-controller.git
   cd aws-multi-eni-controller
   ```

2. Deploy using the script:

   ```bash
   # Build and push your own Docker image
   ./hack/deploy.sh

   # Or use the pre-built GitHub Container Registry image
   USE_GHCR=true ./hack/deploy.sh
   ```

   The script will:
   - Either use the pre-built GitHub Container Registry image or build your own
   - Apply the CRDs to the cluster
   - Deploy the controller and ENI manager to the cluster

### Helm Installation (Recommended)

The easiest way to deploy the AWS Multi-ENI Controller is using Helm. You have two options for installing the chart:

#### Option 1: Install from OCI Registry (Recommended)

Helm charts are published to GitHub Container Registry (GHCR) as OCI artifacts:

1. Install directly from the OCI registry:

   ```bash
   # Install the latest version
   helm install my-release oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 0.1.0

   # Or specify a specific version
   helm install my-release oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.1.1
   ```

2. Customize the installation with values:

   ```bash
   # Create a values.yaml file
   cat > values.yaml <<EOF
   awsRegion: us-east-1
   nodeSelector:
     ng: multi-eni
   EOF

   # Install with custom values
   helm install my-release oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 0.1.0 -f values.yaml
   ```

3. Upgrade an existing installation:

   ```bash
   helm upgrade my-release oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 0.1.0 -f values.yaml
   ```

#### Option 2: Install from GitHub Release

Alternatively, you can download the chart from GitHub releases:

1. Download the Helm chart from the latest release:

   ```bash
   # Get the latest chart version
   CHART_VERSION=$(curl -s https://api.github.com/repos/johnlam90/aws-multi-eni-controller/releases | grep "helm-chart-" | grep "tag_name" | head -n 1 | cut -d'"' -f4 | cut -d'-' -f3)

   # Download the chart
   wget https://github.com/johnlam90/aws-multi-eni-controller/releases/download/helm-chart-${CHART_VERSION}/aws-multi-eni-controller-${CHART_VERSION}.tgz
   ```

2. Install the chart:

   ```bash
   helm install my-release ./aws-multi-eni-controller-${CHART_VERSION}.tgz
   ```

3. Customize the installation with values:

   ```bash
   # Install with custom values
   helm install my-release ./aws-multi-eni-controller-${CHART_VERSION}.tgz -f values.yaml
   ```

For more information about the Helm chart and its configuration options, see the [Helm Chart README](charts/aws-multi-eni-controller/README.md).

### Manual Deployment Steps

If you prefer to deploy manually:

1. Use the pre-built image from GitHub Container Registry:

   ```bash
   # Use the latest image
   IMAGE=ghcr.io/johnlam90/aws-multi-eni-controller:latest

   # Or use a specific version
   # IMAGE=ghcr.io/johnlam90/aws-multi-eni-controller:v1.0.0
   ```

   Alternatively, build and push your own Docker image:

   ```bash
   # Set a unique tag
   TAG=$(date +%Y%m%d%H%M%S)
   docker build -t yourrepo/aws-multi-eni:v1-$TAG .
   docker push yourrepo/aws-multi-eni:v1-$TAG

   IMAGE=yourrepo/aws-multi-eni:v1-$TAG
   ```

2. Update the image in the deployment YAMLs:

   ```bash
   # Replace the image in deployment files
   sed -i '' "s|\${UNIFIED_IMAGE}|$IMAGE|g" deploy/deployment.yaml
   sed -i '' "s|\${UNIFIED_IMAGE}|$IMAGE|g" deploy/eni-manager-daemonset.yaml
   ```

3. Apply the CRDs and deploy the components:

   ```bash
   kubectl apply -f deploy/crds/networking.k8s.aws_nodeenis_crd.yaml
   kubectl apply -f deploy/deployment.yaml
   kubectl apply -f deploy/eni-manager-daemonset.yaml
   ```

4. Configure the AWS region and other options (optional):

   By default, the controller uses the `us-west-2` region. To use a different region or configure other options, edit the deployment:

   ```bash
   kubectl edit deployment -n eni-controller-system eni-controller
   ```

   Update the environment variables as needed:

   ```yaml
   env:
   - name: AWS_REGION
     value: "your-preferred-region"  # e.g., eu-west-1, ap-southeast-1, etc.
   - name: MAX_CONCURRENT_ENI_CLEANUP
     value: "3"  # Number of concurrent ENI cleanup operations (default: 3)
   - name: MAX_CONCURRENT_RECONCILES
     value: "5"  # Number of concurrent NodeENI reconciles (default: 5)
   ```

   The `MAX_CONCURRENT_ENI_CLEANUP` setting controls how many ENIs can be cleaned up in parallel when a NodeENI resource is deleted or when nodes no longer match the selector. This is particularly useful for larger instances with many ENIs, as it can significantly reduce cleanup time.

   The `MAX_CONCURRENT_RECONCILES` setting controls how many NodeENI resources can be reconciled in parallel. This is particularly useful for larger deployments with many nodes and ENIs, as it can significantly improve performance and responsiveness.

## Using the Controller

### Creating a NodeENI Resource

1. Create a YAML file for your NodeENI resource:

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

### Using Multiple Subnets

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
     deleteOnTermination: true
     description: "ENI with multiple subnets"
   ```

You can also use subnet names instead of IDs:

   ```yaml
   apiVersion: networking.k8s.aws/v1alpha1
   kind: NodeENI
   metadata:
     name: multi-subnet-name-eni
   spec:
     nodeSelector:
       ng: multi-eni
     # Specify multiple subnet names - ENIs will be created in ALL subnets
     subnetNames:
     - eks-private-subnet-1
     - eks-private-subnet-2
     securityGroupIDs:
     - sg-05da196f3314d4af8  # Replace with your security group ID
     deviceIndex: 2  # This is the base device index, will be incremented for additional ENIs
     deleteOnTermination: true
     description: "ENI with multiple subnet names"
   ```

The controller will automatically increment the device index for each additional ENI. For example, if you specify a device index of 1 and three subnets, the ENIs will be attached at device indices 1, 2, and 3.

### Automatically Bringing Up Secondary Interfaces

When AWS attaches a secondary ENI to an EC2 instance, the interface is visible to the operating system but typically in a DOWN state. To automatically bring these interfaces up:

1. Deploy the ENI Manager DaemonSet:

   ```bash
   kubectl apply -f deploy/eni-manager-daemonset.yaml
   ```

The ENI Manager is a lightweight Go application that:

- Runs on all nodes with the `ng=multi-eni` label
- Monitors for newly attached network interfaces in DOWN state
- Automatically brings them up when detected
- Verifies interfaces are properly UP

The ENI Manager ensures your secondary interfaces are ready to use as soon as they're attached, without requiring any manual intervention.

#### Cross-Distribution Compatibility

The ENI Manager is designed to work across all Linux distributions:

- **Auto-detection**: Automatically detects the primary interface by examining the default route
- **Fallback mechanisms**: If the primary method fails, falls back to using the standard `ip` command
- **No assumptions**: Makes no distribution-specific assumptions about network configuration

This makes it compatible with Amazon Linux, Ubuntu, Debian, CentOS, RHEL, and other Linux distributions commonly used in Kubernetes environments.

### Alternative Configuration Options

You can use a subnet name instead of a subnet ID:

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
  - sg-05da196f3314d4af8  # Use your security group ID
  deviceIndex: 2
  deleteOnTermination: true
  description: "ENI using subnet name instead of ID"
```

### Deploying Your Configuration

1. Apply the NodeENI resource:

   ```bash
   kubectl apply -f your-nodeeni.yaml
   ```

2. Label a node to match the selector:

   ```bash
   kubectl label node your-node-name ng=multi-eni
   ```

### Multi-Subnet Configuration

You can configure nodes to receive ENIs from multiple subnets using one of these approaches:

1. **Multiple NodeENI Resources**: Create multiple NodeENI resources with different subnet IDs and device indices:

   ```bash
   kubectl apply -f deploy/samples/multi-subnet-example.yaml
   ```

2. **Subnet Selection via Node Labels**: Use node labels to determine which nodes get ENIs from which subnets:

   ```bash
   # Label nodes for specific subnets
   kubectl label node node1 ng=multi-eni subnet=a
   kubectl label node node2 ng=multi-eni subnet=b

   # Apply the NodeENI resources that use these labels
   kubectl apply -f deploy/samples/multi-subnet-example.yaml
   ```

For detailed examples, see the [multi-subnet sample configuration](deploy/samples/multi-subnet-example.yaml) and the [architecture documentation](docs/architecture.md).

### Verifying ENI Creation and Attachment

1. Check the status of the NodeENI resource:

   ```bash
   kubectl get nodeeni multus-eni-config -o yaml
   ```

2. Verify the ENI has been created and attached using AWS CLI:

   ```bash
   # Get the ENI ID from the NodeENI status
   ENI_ID=$(kubectl get nodeeni multus-eni-config -o jsonpath='{.status.attachments[0].eniID}')

   # Describe the ENI
   aws ec2 describe-network-interfaces --network-interface-ids $ENI_ID

   # Check the instance attachments
   INSTANCE_ID=$(kubectl get nodeeni multus-eni-config -o jsonpath='{.status.attachments[0].instanceID}')
   aws ec2 describe-instances --instance-ids $INSTANCE_ID --query "Reservations[*].Instances[*].NetworkInterfaces[*].[NetworkInterfaceId,Attachment.DeviceIndex,Status,SubnetId]" --output table
   ```

### Testing Cleanup

1. Remove the label from the node:

   ```bash
   kubectl label node your-node-name ng-
   ```

2. Verify the ENI has been detached:

   ```bash
   aws ec2 describe-instances --instance-ids $INSTANCE_ID --query "Reservations[*].Instances[*].NetworkInterfaces[*].[NetworkInterfaceId,Attachment.DeviceIndex,Status,SubnetId]" --output table
   ```

3. Delete the NodeENI resource:

   ```bash
   kubectl delete nodeeni multus-eni-config
   ```

4. Verify the ENI has been deleted:

   ```bash
   aws ec2 describe-network-interfaces --network-interface-ids $ENI_ID
   # Should return an error indicating the ENI doesn't exist
   ```

## Troubleshooting

### Common Issues

1. **ENI not being created**:
   - Check if the controller pod is running: `kubectl get pods -n eni-controller-system`
   - Check the controller logs: `kubectl logs -n eni-controller-system deployment/eni-controller`
   - Verify the node has the correct label: `kubectl get nodes --show-labels | grep your-label`
   - Ensure the subnet and security group IDs are correct

2. **ENI not being deleted**:
   - Check if the finalizer is present on the NodeENI resource: `kubectl get nodeeni -o yaml`
   - Check the controller logs for any errors during deletion
   - Verify AWS permissions for the controller to delete ENIs

3. **Controller pod not starting**:
   - Check the pod status: `kubectl describe pod -n eni-controller-system eni-controller-xxx`
   - Verify RBAC permissions are correctly configured
   - Check if the Docker image is accessible

### Debugging

1. Enable more verbose logging by editing the controller deployment:

   ```bash
   kubectl edit deployment -n eni-controller-system eni-controller
   # Add --v=5 to the command args
   ```

2. Check AWS API calls using CloudTrail:

   ```bash
   aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=CreateNetworkInterface
   aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventName,AttributeValue=DeleteNetworkInterface
   ```

3. Manually verify AWS permissions:

   ```bash
   # Test EC2 permissions
   aws ec2 describe-instances
   aws ec2 describe-network-interfaces
   ```

## AWS Region Configuration

The controller needs to know which AWS region to use for creating and managing ENIs. There are several ways to configure this:

1. **Environment Variable**: Set the `AWS_REGION` environment variable in the deployment (default method)
2. **Instance Metadata**: When running on EC2, the controller can use the instance's region
3. **EKS Annotation**: For EKS clusters, you can use the cluster's region annotation

The default region is `us-west-2` if not specified. See the deployment instructions for how to change this.

## Architecture

The ENI Controller follows the Kubernetes operator pattern:

1. **Custom Resource Definition (CRD)**: Defines the NodeENI resource
2. **Controller**: Watches for NodeENI resources and nodes with matching labels
3. **Reconciliation Loop**: Creates, attaches, detaches, and deletes ENIs as needed
4. **Finalizers**: Ensures proper cleanup of AWS resources when NodeENI resources are deleted
5. **ENI Manager**: Brings secondary interfaces up automatically

For a detailed architecture diagram and workflow, see [Architecture Documentation](docs/architecture.md).

### AWS SDK v2 Integration

The controller uses AWS SDK Go v2, which provides several advantages:

- **Improved Performance**: More efficient API calls and better resource utilization
- **Context Support**: Full support for Go contexts for better timeout and cancellation handling
- **Modular Design**: Only imports the specific AWS services needed (EC2 in this case)
- **Retry Mechanisms**: Enhanced retry logic for improved reliability
- **Error Handling**: More detailed error information for better troubleshooting

The AWS SDK v2 integration ensures the controller is using the latest AWS best practices and provides a foundation for future AWS feature support.

### Unified Image Architecture

The project uses a unified Docker image approach:

1. **Single Image**: Both the ENI Controller and ENI Manager components are packaged in a single Docker image
2. **Component Selection**: The image determines which component to run based on the `COMPONENT` environment variable
3. **Deployment Separation**: The controller runs as a Deployment, while the ENI Manager runs as a DaemonSet on labeled nodes
4. **Shared Codebase**: Both components share common code and dependencies, ensuring they stay in sync
5. **Optimized Size**: The image is highly optimized (22MB) for fast deployments while maintaining full functionality

This approach simplifies maintenance, reduces image storage requirements, and ensures consistent versioning across components.

### Optimized Container Image

The Docker image is optimized for size and performance:

- **Small Footprint**: Only 22MB in size, compared to typical Go-based images of 100MB+
- **Fast Deployment**: Smaller image means faster pulls and container starts
- **Binary Optimization**: Uses Go build flags and UPX compression for size reduction
- **Alpine Base**: Uses Alpine Linux for a minimal base image
- **No Performance Impact**: Optimizations do not affect runtime performance

The optimized image is ideal for production environments where deployment speed and resource efficiency are important.

### Controller Logic

1. When a NodeENI resource is created:
   - Add a finalizer to the resource
   - Find nodes matching the selector
   - Create and attach ENIs to matching nodes
   - Update the NodeENI status with attachment information

2. When a node no longer matches the selector:
   - Detach and delete the ENI
   - Update the NodeENI status

3. When a NodeENI resource is deleted:
   - Detach and delete all ENIs created by this resource
   - Remove the finalizer to allow the resource to be deleted

## Using as a Library

The AWS Multi-ENI Controller can also be used as a Go library in your own projects for programmatic ENI management. This is useful for applications that need to create, attach, and manage ENIs without using the Kubernetes controller.

### Installation

```bash
go get github.com/johnlam90/aws-multi-eni-controller
```

### Basic Usage

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/go-logr/logr"
    "github.com/go-logr/zapr"
    "github.com/johnlam90/aws-multi-eni-controller/pkg/lib"
    "go.uber.org/zap"
)

func main() {
    // Create a logger
    zapLog, _ := zap.NewDevelopment()
    logger := zapr.NewLogger(zapLog)

    // Create a context with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // Create an ENI manager
    eniManager, err := lib.NewENIManager(ctx, "us-east-1", logger)
    if err != nil {
        log.Fatalf("Failed to create ENI manager: %v", err)
    }

    // Create an ENI
    options := lib.ENIOptions{
        SubnetID:           "subnet-12345678",
        SecurityGroupIDs:   []string{"sg-12345678"},
        Description:        "Example ENI",
        DeviceIndex:        1,
        DeleteOnTermination: true,
        Tags: map[string]string{
            "Name": "example-eni",
        },
    }

    eniID, err := eniManager.CreateENI(ctx, options)
    if err != nil {
        log.Fatalf("Failed to create ENI: %v", err)
    }

    // Attach the ENI to an instance
    err = eniManager.AttachENI(ctx, eniID, "i-12345678", options.DeviceIndex, options.DeleteOnTermination)
    if err != nil {
        log.Fatalf("Failed to attach ENI: %v", err)
    }
}
```

For more detailed examples and documentation, see the [Library Documentation](pkg/lib/README.md) and the [example code](examples/library-usage/main.go).

## Reference

The repository contains the following key components:

- `pkg/apis/networking/v1alpha1/nodeeni_types.go`: NodeENI CRD definition
- `pkg/controller/nodeeni_controller.go`: Controller implementation
- `pkg/lib/eni.go`: Library for programmatic ENI management
- `deploy/crds/networking.k8s.aws_nodeenis_crd.yaml`: CRD YAML
- `deploy/deployment.yaml`: Controller deployment (includes RBAC)
- `deploy/samples/`: Sample NodeENI resources
- `examples/library-usage/`: Examples of using the project as a library

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details on how to contribute to this project.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
