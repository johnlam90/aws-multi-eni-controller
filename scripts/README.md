# AWS Multi-ENI Controller Build Scripts

This directory contains automated scripts for building, tagging, pushing, and deploying the AWS Multi-ENI Controller.

## Scripts Overview

### 1. `build-and-deploy.sh` - Complete Build and Deploy Pipeline

The main script that handles the entire build and deployment process.

**Features:**
- Automatic tag generation from git branch/commit
- Docker image building (single or multi-platform)
- Image pushing to registry
- Automatic update of Helm values and YAML files
- Comprehensive logging and error handling
- Dry-run mode for testing

**Basic Usage:**
```bash
# Build with auto-generated tag and update all files
./scripts/build-and-deploy.sh

# Build with specific tag
./scripts/build-and-deploy.sh --tag v1.4.0

# Dry run to see what would happen
./scripts/build-and-deploy.sh --dry-run

# Build for Docker Hub instead of default registry
./scripts/build-and-deploy.sh --registry docker.io/myuser

# Skip tests for faster builds
./scripts/build-and-deploy.sh --skip-tests

# Build multi-platform (requires buildx setup)
./scripts/build-and-deploy.sh --platforms linux/amd64,linux/arm64
```

**Advanced Options:**
- `--skip-push`: Build but don't push to registry
- `--skip-update`: Don't update YAML/Helm files
- `--helm-only`: Only update Helm chart values
- `--yaml-only`: Only update deployment YAML files
- `--build-only`: Only build, skip push and updates

### 2. `quick-build.sh` - Fast Development Builds

Simplified script for rapid development cycles.

**Features:**
- Fast single-platform builds
- Skips tests and UPX compression
- Auto-updates Helm values.yaml
- Minimal configuration

**Usage:**
```bash
# Quick build with auto-generated tag
./scripts/quick-build.sh

# Quick build with specific tag
./scripts/quick-build.sh --tag dev-feature-x

# Build but don't push
./scripts/quick-build.sh --no-push
```

### 3. `update-deployment.sh` - Update Existing Deployments

Updates running Kubernetes deployments with new image tags.

**Features:**
- Auto-detects Helm vs kubectl deployments
- Updates controller and manager components
- Waits for rollout completion
- Shows deployment status

**Usage:**
```bash
# Update with specific tag
./scripts/update-deployment.sh --tag v1.4.0

# Update Helm deployment only
./scripts/update-deployment.sh --tag v1.4.0 --helm

# Update and wait for rollout
./scripts/update-deployment.sh --tag v1.4.0 --wait

# Dry run to see what would be updated
./scripts/update-deployment.sh --tag v1.4.0 --dry-run
```

## Configuration

### Default Settings

- **Registry**: `johnlam90` (Docker Hub)
- **Repository**: `aws-multi-eni-controller`
- **Platform**: `linux/amd64` (single platform by default)
- **Namespace**: `eni-controller-system`

### Multi-Platform Builds

To build for multiple architectures:

1. **Setup buildx** (one-time setup):
   ```bash
   docker buildx create --use --name multiarch-builder
   ```

2. **Build multi-platform**:
   ```bash
   ./scripts/build-and-deploy.sh --platforms linux/amd64,linux/arm64
   # or
   ./scripts/build-and-deploy.sh --platforms multi
   ```

## Tag Generation

When no tag is specified, tags are auto-generated:

- **Main branch**: `v1.4.0-{commit-hash}`
- **Feature branches**: `{branch-name}-{commit-hash}`

Branch names are sanitized for Docker compatibility.

## File Updates

The scripts automatically update:

1. **Helm Chart** (`charts/aws-multi-eni-controller/values.yaml`):
   - `image.repository`
   - `image.tag`

2. **Deployment YAML** (`deploy/deployment.yaml`):
   - Controller image

3. **DaemonSet YAML** (`deploy/eni-manager-daemonset.yaml`):
   - Manager image
   - Init container image

## Examples

### Complete Development Workflow

```bash
# 1. Make code changes
git add .
git commit -m "Fix resource conflicts in cleanup"

# 2. Build and push new image
./scripts/build-and-deploy.sh --tag v1.4.0

# 3. Deploy to cluster
helm upgrade --install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller \
  --version 1.3.0 --namespace eni-controller-system --create-namespace

# 4. Verify deployment
kubectl get pods -n eni-controller-system
```

### Quick Development Iteration

```bash
# Fast build for testing
./scripts/quick-build.sh --tag dev-test

# Update running deployment
./scripts/update-deployment.sh --tag dev-test --wait
```

### Production Release

```bash
# Build with full testing and multi-platform
./scripts/build-and-deploy.sh --tag v1.4.0 --platforms multi

# Update production deployment
./scripts/update-deployment.sh --tag v1.4.0 --helm --wait --timeout 600s
```

## Troubleshooting

### Multi-Platform Build Issues

If you see "multiple platforms feature is currently not supported":

```bash
# Setup buildx
docker buildx create --use --name multiarch-builder

# Or use single platform
./scripts/build-and-deploy.sh --platforms linux/amd64
```

### Registry Authentication

For private registries:

```bash
# Login to registry
docker login ghcr.io

# Or for Docker Hub
docker login
```

### Permission Issues

Make scripts executable:

```bash
chmod +x scripts/*.sh
```

## Integration with CI/CD

These scripts are designed to work in CI/CD pipelines:

```yaml
# Example GitHub Actions step
- name: Build and Push
  run: |
    ./scripts/build-and-deploy.sh \
      --tag ${{ github.ref_name }} \
      --skip-tests \
      --platforms linux/amd64,linux/arm64
```
