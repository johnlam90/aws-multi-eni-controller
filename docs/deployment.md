# Deployment Guide

This document provides detailed instructions for deploying the AWS Multi-ENI Controller in various configurations.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Deployment Options](#deployment-options)
  - [Helm Installation (Recommended)](#helm-installation-recommended)
  - [Manual Deployment](#manual-deployment)
- [AWS Permissions](#aws-permissions)
- [Configuration Options](#configuration-options)

## Prerequisites

- Kubernetes cluster running on AWS (e.g., EKS)
- kubectl configured to access your cluster
- Helm 3.0+ (for Helm installation)
- Docker installed and configured (for building custom images)
- AWS CLI configured with appropriate permissions
- Go 1.22 or later (for development)

## Deployment Options

### Helm Installation (Recommended)

The easiest way to deploy the AWS Multi-ENI Controller is using Helm. You have two options for installing the chart:

#### Option 1: Install from OCI Registry (Recommended)

Helm charts are published to GitHub Container Registry (GHCR) as OCI artifacts:

1. Install directly from the OCI registry:

   ```bash
   # Install the latest version
   helm install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.3.0 \
     --namespace eni-controller-system --create-namespace

   # Or specify a specific version
   helm install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.3.0 \
     --namespace eni-controller-system --create-namespace
   ```

   > **Important**: Always specify the `--namespace eni-controller-system` flag and the `--create-namespace` flag when installing the chart to ensure all resources are created in the correct namespace.

2. Customize the installation with values:

   ```bash
   # Create a values.yaml file
   cat > values.yaml <<EOF
   # IMPORTANT: Keep this as eni-controller-system
   namespace: eni-controller-system
   awsRegion: us-east-1
   nodeSelector:
     ng: multi-eni
   EOF

   # Install with custom values
   helm install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.3.0 \
     --namespace eni-controller-system --create-namespace \
     -f values.yaml
   ```

3. Upgrade an existing installation:

   ```bash
   helm upgrade aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.3.0 \
     --namespace eni-controller-system \
     -f values.yaml
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
   helm install aws-multi-eni ./aws-multi-eni-controller-${CHART_VERSION}.tgz \
     --namespace eni-controller-system --create-namespace
   ```

3. Customize the installation with values:

   ```bash
   # Install with custom values
   helm install aws-multi-eni ./aws-multi-eni-controller-${CHART_VERSION}.tgz \
     --namespace eni-controller-system --create-namespace \
     -f values.yaml
   ```

For more information about the Helm chart and its configuration options, see the [Helm Chart README](../charts/aws-multi-eni-controller/README.md).

### Manual Deployment

If you prefer to deploy manually:

1. Use the pre-built image from GitHub Container Registry:

   ```bash
   # Use the latest image
   IMAGE=ghcr.io/johnlam90/aws-multi-eni-controller:latest

   # Or use a specific version
   # IMAGE=ghcr.io/johnlam90/aws-multi-eni-controller:v1.3.2
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

   # Apply the DPDK tools ConfigMap (required for DPDK functionality)
   kubectl apply -f deploy/dpdk-tools-configmap.yaml

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

### Using the Deploy Script

For convenience, you can use the provided deploy script:

```bash
# Build and push your own Docker image
./hack/deploy.sh

# Or use the pre-built GitHub Container Registry image
USE_GHCR=true ./hack/deploy.sh
```

The script will:

- Either use the pre-built GitHub Container Registry image or build your own
- Apply the CRDs to the cluster
- Deploy the controller and ENI Manager to the cluster

## ConfigMap Components

### DPDK Tools ConfigMap

The AWS Multi-ENI Controller includes a ConfigMap (`dpdk-tools-configmap.yaml`) that provides essential DPDK functionality:

**Purpose**: Contains the DPDK device binding script (`dpdk-devbind.py`) required for DPDK device management.

**Contents**:

- **dpdk-devbind.py**: Complete DPDK device binding script (751 lines) that handles:
  - Device discovery and status reporting
  - Binding/unbinding devices to/from DPDK drivers
  - Support for various DPDK drivers (vfio-pci, uio_pci_generic, igb_uio)
  - PCI device management and driver override functionality

**Deployment**: The ConfigMap is automatically mounted at `/opt/dpdk` in the ENI Manager DaemonSet containers with executable permissions (0755).

**Requirements**: This ConfigMap is required for any DPDK functionality. Without it, DPDK device binding operations will fail.

## AWS Permissions

The controller requires the following AWS permissions:

- `ec2:CreateNetworkInterface`
- `ec2:DeleteNetworkInterface`
- `ec2:DescribeNetworkInterfaces`
- `ec2:AttachNetworkInterface`
- `ec2:DetachNetworkInterface`
- `ec2:ModifyNetworkInterfaceAttribute`
- `ec2:DescribeSubnets` (if using subnet names)

### Setting up IAM permissions

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

## Pre-built Container Images

Pre-built container images are available on GitHub Container Registry:

```bash
# Pull the latest stable image
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:latest

# Or use a specific version
docker pull ghcr.io/johnlam90/aws-multi-eni-controller:v1.3.2
```

You can find all available tags at [GitHub Container Registry](https://github.com/johnlam90/aws-multi-eni-controller/pkgs/container/aws-multi-eni-controller).

### Beta Images from Sandbox Branches

For development and testing, beta images are automatically built from sandbox branches:

- **Branch-specific tags**: `beta-{branch-name}` (e.g., `beta-sandbox-testv0.1`)
- **Latest beta**: `beta-latest` always points to the most recent beta build
- **Commit-specific tags**: `beta-{branch-name}.{commit-sha}` for precise version tracking

## Configuration Options

The AWS Multi-ENI Controller can be configured through various methods depending on your deployment approach:

### Helm Chart Configuration

When using Helm, you can customize the deployment through a `values.yaml` file. Key configuration options include:

- **namespace**: Target namespace for deployment (default: `eni-controller-system`)
- **awsRegion**: AWS region for ENI operations (default: `us-west-2`)
- **nodeSelector**: Node selection criteria for pod placement
- **image.repository**: Container image repository
- **image.tag**: Container image tag
- **resources**: CPU and memory resource limits/requests

### Environment Variables

The controller supports the following environment variables:

- **AWS_REGION**: AWS region for API calls (default: `us-west-2`)
- **MAX_CONCURRENT_ENI_CLEANUP**: Number of concurrent ENI cleanup operations (default: `3`)
- **MAX_CONCURRENT_RECONCILES**: Number of concurrent NodeENI reconciles (default: `5`)

### NodeENI Custom Resource Configuration

Configure individual ENI attachments through NodeENI custom resources. See the [Configuration Guide](configuration.md) for detailed examples and options.
