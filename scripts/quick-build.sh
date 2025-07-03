#!/bin/bash

# Quick Build Script for AWS Multi-ENI Controller
# This is a simplified version for rapid development cycles

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

# Default values
REGISTRY="ghcr.io/johnlam90"
REPOSITORY="aws-multi-eni-controller"
TAG=""
PUSH=true

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        --no-push)
            PUSH=false
            shift
            ;;
        -h|--help)
            echo "Quick Build Script"
            echo "Usage: $0 [--tag TAG] [--no-push]"
            echo "  --tag TAG    Specify image tag (default: auto-generated)"
            echo "  --no-push    Don't push to registry"
            echo "  --help       Show this help"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

cd "${PROJECT_ROOT}"

# Generate tag if not provided
if [[ -z "${TAG}" ]]; then
    GIT_COMMIT=$(git rev-parse --short HEAD)
    GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
    
    if [[ "${GIT_BRANCH}" == "main" ]]; then
        TAG="latest-${GIT_COMMIT}"
    else
        CLEAN_BRANCH=$(echo "${GIT_BRANCH}" | sed 's/[^a-zA-Z0-9._-]/-/g')
        TAG="${CLEAN_BRANCH}-${GIT_COMMIT}"
    fi
fi

FULL_IMAGE="${REGISTRY}/${REPOSITORY}:${TAG}"

log_info "Quick building: ${FULL_IMAGE}"

# Check if Docker is running
if ! docker info >/dev/null 2>&1; then
    log_warning "Docker is not running or not accessible"
    exit 1
fi

# Build image
log_info "Building Docker image..."
docker build \
    --build-arg SKIP_TESTS=true \
    --build-arg SKIP_UPX=true \
    -t "${FULL_IMAGE}" \
    .

log_success "Build completed: ${FULL_IMAGE}"

# Push if requested
if [[ "${PUSH}" == "true" ]]; then
    log_info "Pushing to registry..."
    docker push "${FULL_IMAGE}"
    log_success "Push completed: ${FULL_IMAGE}"
fi

# Update values.yaml quickly
log_info "Updating Helm values.yaml..."
HELM_VALUES="charts/aws-multi-eni-controller/values.yaml"

if [[ -f "${HELM_VALUES}" ]]; then
    # Create backup
    cp "${HELM_VALUES}" "${HELM_VALUES}.backup"
    
    # Update tag
    sed -i.tmp "s|tag: .*|tag: ${TAG}|g" "${HELM_VALUES}"
    rm -f "${HELM_VALUES}.tmp"
    
    log_success "Updated ${HELM_VALUES} with tag: ${TAG}"
else
    log_warning "Helm values file not found: ${HELM_VALUES}"
fi

echo
log_success "Quick build completed!"
log_info "Image: ${FULL_IMAGE}"
log_info "To deploy: helm upgrade --install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller --version 1.3.0 --namespace eni-controller-system --create-namespace"
