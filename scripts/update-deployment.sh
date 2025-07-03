#!/bin/bash

# Update Deployment Script for AWS Multi-ENI Controller
# Updates existing Kubernetes deployments with new image tags

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

show_help() {
    cat << EOF
Update Deployment Script for AWS Multi-ENI Controller

Usage: $0 [OPTIONS]

OPTIONS:
    -t, --tag TAG           New image tag to deploy
    -n, --namespace NS      Kubernetes namespace (default: eni-controller-system)
    -i, --image IMAGE       Full image name (overrides tag)
    --helm                  Update Helm deployment
    --kubectl               Update kubectl deployment
    --dry-run               Show what would be updated without applying
    --wait                  Wait for rollout to complete
    --timeout TIMEOUT       Timeout for rollout (default: 300s)
    -h, --help              Show this help message

EXAMPLES:
    # Update with specific tag
    $0 --tag v1.4.0

    # Update Helm deployment only
    $0 --tag v1.4.0 --helm

    # Update kubectl deployment only
    $0 --tag v1.4.0 --kubectl

    # Dry run to see what would be updated
    $0 --tag v1.4.0 --dry-run

    # Update with full image name
    $0 --image ghcr.io/johnlam90/aws-multi-eni-controller:v1.4.0

EOF
}

# Default values
TAG=""
NAMESPACE="eni-controller-system"
IMAGE=""
UPDATE_HELM=false
UPDATE_KUBECTL=false
DRY_RUN=false
WAIT_FOR_ROLLOUT=false
TIMEOUT="300s"

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -n|--namespace)
            NAMESPACE="$2"
            shift 2
            ;;
        -i|--image)
            IMAGE="$2"
            shift 2
            ;;
        --helm)
            UPDATE_HELM=true
            shift
            ;;
        --kubectl)
            UPDATE_KUBECTL=true
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --wait)
            WAIT_FOR_ROLLOUT=true
            shift
            ;;
        --timeout)
            TIMEOUT="$2"
            shift 2
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Validate inputs
if [[ -z "${TAG}" && -z "${IMAGE}" ]]; then
    log_error "Either --tag or --image must be specified"
    show_help
    exit 1
fi

# If neither helm nor kubectl specified, try to detect
if [[ "${UPDATE_HELM}" == "false" && "${UPDATE_KUBECTL}" == "false" ]]; then
    log_info "Auto-detecting deployment type..."
    
    # Check if Helm deployment exists
    if helm list -n "${NAMESPACE}" | grep -q "aws-multi-eni"; then
        log_info "Detected Helm deployment"
        UPDATE_HELM=true
    fi
    
    # Check if kubectl deployment exists
    if kubectl get deployment eni-controller -n "${NAMESPACE}" >/dev/null 2>&1; then
        log_info "Detected kubectl deployment"
        UPDATE_KUBECTL=true
    fi
    
    if [[ "${UPDATE_HELM}" == "false" && "${UPDATE_KUBECTL}" == "false" ]]; then
        log_error "No existing deployment found in namespace ${NAMESPACE}"
        exit 1
    fi
fi

# Build full image name if not provided
if [[ -z "${IMAGE}" ]]; then
    IMAGE="ghcr.io/johnlam90/aws-multi-eni-controller:${TAG}"
fi

log_info "Updating deployment with image: ${IMAGE}"
log_info "Namespace: ${NAMESPACE}"

# Function to execute or show command
execute_or_show() {
    local cmd="$1"
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "[DRY RUN] Would execute: ${cmd}"
    else
        log_info "Executing: ${cmd}"
        eval "${cmd}"
    fi
}

# Update Helm deployment
if [[ "${UPDATE_HELM}" == "true" ]]; then
    log_info "Updating Helm deployment..."
    
    # Get current Helm release
    RELEASE_NAME=$(helm list -n "${NAMESPACE}" -o json | jq -r '.[] | select(.chart | contains("aws-multi-eni")) | .name' | head -1)
    
    if [[ -z "${RELEASE_NAME}" ]]; then
        log_error "No Helm release found for aws-multi-eni-controller in namespace ${NAMESPACE}"
        exit 1
    fi
    
    log_info "Found Helm release: ${RELEASE_NAME}"
    
    # Extract registry and tag from image
    REGISTRY=$(echo "${IMAGE}" | cut -d'/' -f1-2)
    REPO_TAG=$(echo "${IMAGE}" | cut -d'/' -f3)
    REPOSITORY=$(echo "${REPO_TAG}" | cut -d':' -f1)
    TAG_ONLY=$(echo "${REPO_TAG}" | cut -d':' -f2)
    
    HELM_CMD="helm upgrade ${RELEASE_NAME} oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller"
    HELM_CMD="${HELM_CMD} --namespace ${NAMESPACE}"
    HELM_CMD="${HELM_CMD} --set image.repository=${REGISTRY}/${REPOSITORY}"
    HELM_CMD="${HELM_CMD} --set image.tag=${TAG_ONLY}"
    HELM_CMD="${HELM_CMD} --reuse-values"
    
    execute_or_show "${HELM_CMD}"
    
    if [[ "${DRY_RUN}" == "false" ]]; then
        log_success "Helm deployment updated"
    fi
fi

# Update kubectl deployment
if [[ "${UPDATE_KUBECTL}" == "true" ]]; then
    log_info "Updating kubectl deployment..."
    
    # Update controller deployment
    CONTROLLER_CMD="kubectl set image deployment/eni-controller manager=${IMAGE} -n ${NAMESPACE}"
    execute_or_show "${CONTROLLER_CMD}"
    
    # Update manager daemonset
    MANAGER_CMD="kubectl set image daemonset/eni-manager eni-manager=${IMAGE} -n ${NAMESPACE}"
    execute_or_show "${MANAGER_CMD}"
    
    # Update init container in daemonset
    INIT_CMD="kubectl set image daemonset/eni-manager dpdk-setup=${IMAGE} -n ${NAMESPACE}"
    execute_or_show "${INIT_CMD}"
    
    if [[ "${DRY_RUN}" == "false" ]]; then
        log_success "kubectl deployment updated"
    fi
fi

# Wait for rollout if requested
if [[ "${WAIT_FOR_ROLLOUT}" == "true" && "${DRY_RUN}" == "false" ]]; then
    log_info "Waiting for rollout to complete (timeout: ${TIMEOUT})..."
    
    # Wait for controller deployment
    if kubectl get deployment eni-controller -n "${NAMESPACE}" >/dev/null 2>&1; then
        log_info "Waiting for controller deployment rollout..."
        kubectl rollout status deployment/eni-controller -n "${NAMESPACE}" --timeout="${TIMEOUT}"
    fi
    
    # Wait for manager daemonset
    if kubectl get daemonset eni-manager -n "${NAMESPACE}" >/dev/null 2>&1; then
        log_info "Waiting for manager daemonset rollout..."
        kubectl rollout status daemonset/eni-manager -n "${NAMESPACE}" --timeout="${TIMEOUT}"
    fi
    
    log_success "Rollout completed successfully"
fi

# Show status
if [[ "${DRY_RUN}" == "false" ]]; then
    echo
    log_info "Deployment status:"
    
    # Show pod status
    kubectl get pods -n "${NAMESPACE}" -l app=eni-controller -o wide
    kubectl get pods -n "${NAMESPACE}" -l app=eni-manager -o wide
    
    echo
    log_info "To check logs:"
    log_info "  Controller: kubectl logs -n ${NAMESPACE} deployment/eni-controller"
    log_info "  Manager:    kubectl logs -n ${NAMESPACE} daemonset/eni-manager"
    
    echo
    log_success "Update completed successfully!"
else
    log_info "Dry run completed. Use without --dry-run to execute."
fi
