# AWS Multi-ENI Controller Helm Chart

This Helm chart deploys the AWS Multi-ENI Controller to your Kubernetes cluster.

## Introduction

The AWS Multi-ENI Controller manages multiple Elastic Network Interfaces (ENIs) for AWS nodes in Kubernetes. It allows you to attach additional ENIs to your nodes based on node selectors.

## Prerequisites

- Kubernetes 1.16+
- Helm 3.0+
- AWS credentials with permissions to manage ENIs

## Installing the Chart

### Option 1: Install from OCI Registry (Recommended)

```bash
# Install the latest version
helm install my-release oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 0.1.0

# Or specify a specific version
helm install my-release oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.1.1
```

### Option 2: Install from GitHub Release

```bash
# Get the latest chart version
CHART_VERSION=$(curl -s https://api.github.com/repos/johnlam90/aws-multi-eni-controller/releases | grep "helm-chart-" | grep "tag_name" | head -n 1 | cut -d'"' -f4 | cut -d'-' -f3)

# Download the chart
wget https://github.com/johnlam90/aws-multi-eni-controller/releases/download/helm-chart-${CHART_VERSION}/aws-multi-eni-controller-${CHART_VERSION}.tgz

# Install the chart
helm install my-release ./aws-multi-eni-controller-${CHART_VERSION}.tgz
```

### Option 3: Install from Local Directory

If you've cloned the repository:

```bash
helm install my-release ./charts/aws-multi-eni-controller
```

## Configuration

The following table lists the configurable parameters of the AWS Multi-ENI Controller chart and their default values.

| Parameter | Description | Default |
| --------- | ----------- | ------- |
| `image.repository` | Image repository | `ghcr.io/johnlam90/aws-multi-eni-controller` |
| `image.tag` | Image tag | `v1.1.1` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `namespace` | Namespace to deploy the controller | `eni-controller-system` |
| `awsRegion` | AWS Region | `us-east-1` |
| `controller.maxConcurrentENICleanup` | Maximum number of concurrent ENI cleanup operations | `3` |
| `resources.controller.limits.cpu` | CPU limits for the controller | `500m` |
| `resources.controller.limits.memory` | Memory limits for the controller | `512Mi` |
| `resources.controller.requests.cpu` | CPU requests for the controller | `100m` |
| `resources.controller.requests.memory` | Memory requests for the controller | `128Mi` |
| `resources.manager.limits.cpu` | CPU limits for the manager | `500m` |
| `resources.manager.limits.memory` | Memory limits for the manager | `512Mi` |
| `resources.manager.requests.cpu` | CPU requests for the manager | `100m` |
| `resources.manager.requests.memory` | Memory requests for the manager | `128Mi` |
| `nodeSelector` | Node selector for the ENI manager daemonset | `{}` |
| `securityContext.runAsNonRoot` | Run as non-root | `true` |
| `securityContext.runAsUser` | User ID to run as | `1000` |
| `securityContext.fsGroup` | Group ID for filesystem | `2000` |
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.name` | Service account name | `eni-controller` |
| `serviceAccount.annotations` | Service account annotations | `{}` |
| `rbac.create` | Create RBAC resources | `true` |
| `podAnnotations` | Pod annotations | `{}` |
| `podLabels` | Pod labels | `{}` |
| `tolerations` | Tolerations for the ENI manager daemonset | `[]` |
| `affinity` | Affinity for the controller deployment | `{}` |
| `metrics.enabled` | Enable metrics server | `false` |
| `metrics.port` | Metrics port | `8080` |
| `metrics.service.type` | Metrics service type | `ClusterIP` |
| `metrics.service.port` | Metrics service port | `8080` |
| `logLevel` | Log level | `info` |

## Example: Installing with Custom Values

Create a `values.yaml` file:

```yaml
namespace: custom-namespace
awsRegion: us-west-2
controller:
  maxConcurrentENICleanup: 5  # Increase for larger instances with many ENIs
nodeSelector:
  role: worker
  ng: multi-eni
```

Then install the chart:

```bash
helm install my-release ./charts/aws-multi-eni-controller -f values.yaml
```

## Creating a NodeENI Resource

After installing the chart, you can create a NodeENI resource:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: example-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-xxxxxxxx
  securityGroupIDs:
  - sg-xxxxxxxx
  deviceIndex: 2
  deleteOnTermination: true
  description: "Example ENI"
```

Apply it with:

```bash
kubectl apply -f your-file.yaml
```

## Using Security Group Names

You can use security group names instead of IDs:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: example-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetID: subnet-xxxxxxxx
  securityGroupNames:
  - your-security-group-name
  deviceIndex: 2
  deleteOnTermination: true
  description: "Example ENI"
```

## Using Subnet Names

You can use subnet names instead of IDs:

```yaml
apiVersion: networking.k8s.aws/v1alpha1
kind: NodeENI
metadata:
  name: example-eni
spec:
  nodeSelector:
    ng: multi-eni
  subnetName: your-subnet-name
  securityGroupIDs:
  - sg-xxxxxxxx
  deviceIndex: 2
  deleteOnTermination: true
  description: "Example ENI"
```

## Using Multiple Subnets

You can specify multiple subnets to create ENIs in all specified subnets with the same security group:

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
  - subnet-xxxxxxxx
  - subnet-yyyyyyyy
  securityGroupIDs:
  - sg-xxxxxxxx
  deviceIndex: 2  # This is the base device index, will be incremented for additional ENIs
  deleteOnTermination: true
  description: "ENI with multiple subnets"
```

You can also use multiple subnet names:

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
  - subnet-name-1
  - subnet-name-2
  securityGroupIDs:
  - sg-xxxxxxxx
  deviceIndex: 2  # This is the base device index, will be incremented for additional ENIs
  deleteOnTermination: true
  description: "ENI with multiple subnet names"
```

The controller will automatically increment the device index for each additional ENI. For example, if you specify a device index of 2 and three subnets, the ENIs will be attached at device indices 2, 3, and 4.
