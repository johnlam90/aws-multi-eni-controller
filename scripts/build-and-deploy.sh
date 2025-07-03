#!/bin/bash

# AWS Multi-ENI Controller Build, Tag, Push, and Update Script
# This script automates the entire build and deployment process

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEFAULT_REGISTRY="johnlam90"
DEFAULT_REPOSITORY="aws-multi-eni-controller"
DEFAULT_PLATFORMS="linux/amd64"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
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

# Help function
show_help() {
    cat << EOF
AWS Multi-ENI Controller Build and Deploy Script

Usage: $0 [OPTIONS]

OPTIONS:
    -t, --tag TAG           Docker image tag (default: auto-generated from git)
    -r, --registry REGISTRY Docker registry (default: ${DEFAULT_REGISTRY})
    -n, --name NAME         Repository name (default: ${DEFAULT_REPOSITORY})
    -p, --platforms PLATFORMS Target platforms (default: ${DEFAULT_PLATFORMS}, use 'multi' for multi-arch)
    --skip-tests           Skip running tests during build
    --skip-push            Skip pushing to registry
    --skip-update          Skip updating YAML files
    --dry-run              Show what would be done without executing
    --helm-only            Only update Helm chart values
    --yaml-only            Only update YAML deployment files
    --build-only           Only build the image, skip push and updates
    -h, --help             Show this help message

EXAMPLES:
    # Build with auto-generated tag and update all files
    $0

    # Build with specific tag
    $0 --tag v1.4.0

    # Build for specific registry
    $0 --registry docker.io/myuser --name my-eni-controller

    # Build multi-platform (requires buildx setup)
    $0 --platforms linux/amd64,linux/arm64

    # Dry run to see what would happen
    $0 --dry-run

    # Only update Helm chart
    $0 --tag v1.4.0 --helm-only --skip-push --build-only

    # Build and push but don't update files
    $0 --tag v1.4.0 --skip-update

EOF
}

# Parse command line arguments
REGISTRY="${DEFAULT_REGISTRY}"
REPOSITORY="${DEFAULT_REPOSITORY}"
TAG=""
PLATFORMS="${DEFAULT_PLATFORMS}"
SKIP_TESTS=false
SKIP_PUSH=false
SKIP_UPDATE=false
DRY_RUN=false
HELM_ONLY=false
YAML_ONLY=false
BUILD_ONLY=false

while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -r|--registry)
            REGISTRY="$2"
            shift 2
            ;;
        -n|--name)
            REPOSITORY="$2"
            shift 2
            ;;
        -p|--platforms)
            PLATFORMS="$2"
            shift 2
            ;;
        --skip-tests)
            SKIP_TESTS=true
            shift
            ;;
        --skip-push)
            SKIP_PUSH=true
            shift
            ;;
        --skip-update)
            SKIP_UPDATE=true
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --helm-only)
            HELM_ONLY=true
            shift
            ;;
        --yaml-only)
            YAML_ONLY=true
            shift
            ;;
        --build-only)
            BUILD_ONLY=true
            shift
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

# Change to project root
cd "${PROJECT_ROOT}"

# Generate tag if not provided
if [[ -z "${TAG}" ]]; then
    # Get git commit hash
    GIT_COMMIT=$(git rev-parse --short HEAD)
    # Get current branch name
    GIT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
    # Get current timestamp
    TIMESTAMP=$(date +%Y%m%d-%H%M%S)
    
    if [[ "${GIT_BRANCH}" == "main" ]]; then
        TAG="v1.4.0-${GIT_COMMIT}"
    else
        # Sanitize branch name for Docker tag
        CLEAN_BRANCH=$(echo "${GIT_BRANCH}" | sed 's/[^a-zA-Z0-9._-]/-/g')
        TAG="${CLEAN_BRANCH}-${GIT_COMMIT}"
    fi
    
    log_info "Auto-generated tag: ${TAG}"
fi

# Full image name
FULL_IMAGE="${REGISTRY}/${REPOSITORY}:${TAG}"

log_info "Starting build and deploy process..."
log_info "Registry: ${REGISTRY}"
log_info "Repository: ${REPOSITORY}"
log_info "Tag: ${TAG}"
log_info "Full image: ${FULL_IMAGE}"
log_info "Platforms: ${PLATFORMS}"

# Validate git status
if ! git diff-index --quiet HEAD --; then
    log_warning "Working directory has uncommitted changes"
    if [[ "${DRY_RUN}" == "false" ]]; then
        read -p "Continue anyway? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_error "Aborted by user"
            exit 1
        fi
    fi
fi

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

# Build Docker image
if [[ "${BUILD_ONLY}" == "false" ]] || [[ "${SKIP_PUSH}" == "true" ]]; then
    log_info "Building Docker image..."

    # Prepare build arguments
    BUILD_ARGS=""
    if [[ "${SKIP_TESTS}" == "true" ]]; then
        BUILD_ARGS="${BUILD_ARGS} --build-arg SKIP_TESTS=true"
    fi

    # Check if multi-platform build is requested
    if [[ "${PLATFORMS}" == *","* ]] || [[ "${PLATFORMS}" == "multi" ]]; then
        # Multi-platform build requires buildx
        log_info "Building multi-platform image..."

        # Set platforms for multi-arch build
        if [[ "${PLATFORMS}" == "multi" ]]; then
            PLATFORMS="linux/amd64,linux/arm64"
        fi

        log_info "Platforms: ${PLATFORMS}"

        # Check if buildx builder exists and is available
        if ! docker buildx inspect >/dev/null 2>&1; then
            log_warning "Docker buildx not available or not configured"
            log_info "Creating buildx builder..."
            execute_or_show "docker buildx create --use --name multiarch-builder"
        fi

        BUILDX_CMD="docker buildx build --platform ${PLATFORMS} ${BUILD_ARGS} -t ${FULL_IMAGE}"
        if [[ "${SKIP_PUSH}" == "false" ]]; then
            BUILDX_CMD="${BUILDX_CMD} --push"
        else
            BUILDX_CMD="${BUILDX_CMD} --load"
        fi
        BUILDX_CMD="${BUILDX_CMD} ."
        execute_or_show "${BUILDX_CMD}"
    else
        # Single-platform build
        log_info "Building single-platform image for: ${PLATFORMS}"

        # Remove platform flag if it's just "linux/amd64" to avoid issues
        PLATFORM_ARG=""
        if [[ "${PLATFORMS}" != "linux/amd64" ]]; then
            PLATFORM_ARG="--platform ${PLATFORMS}"
        fi

        BUILD_CMD="docker build ${PLATFORM_ARG} ${BUILD_ARGS} -t ${FULL_IMAGE} ."
        execute_or_show "${BUILD_CMD}"

        # Push single-platform image
        if [[ "${SKIP_PUSH}" == "false" ]]; then
            log_info "Pushing Docker image..."
            execute_or_show "docker push ${FULL_IMAGE}"
        fi
    fi

    if [[ "${DRY_RUN}" == "false" ]]; then
        log_success "Docker image built successfully: ${FULL_IMAGE}"
    fi
fi

# Update configuration files
if [[ "${SKIP_UPDATE}" == "false" ]]; then
    log_info "Updating configuration files..."

    # Function to update image in file
    update_image_in_file() {
        local file="$1"
        local search_pattern="$2"
        local replacement="$3"

        if [[ ! -f "${file}" ]]; then
            log_warning "File not found: ${file}"
            return 1
        fi

        if [[ "${DRY_RUN}" == "true" ]]; then
            log_info "[DRY RUN] Would update ${file}"
            log_info "[DRY RUN] Pattern: ${search_pattern}"
            log_info "[DRY RUN] Replacement: ${replacement}"
            return 0
        fi

        # Create backup
        cp "${file}" "${file}.backup"

        # Update the file
        if sed -i.tmp "${search_pattern}" "${file}"; then
            rm -f "${file}.tmp"
            log_success "Updated ${file}"

            # Show the change
            log_info "Changed to: ${replacement}"
        else
            # Restore backup if sed failed
            mv "${file}.backup" "${file}"
            log_error "Failed to update ${file}"
            return 1
        fi
    }

    # Update Helm chart values.yaml
    if [[ "${YAML_ONLY}" == "false" ]]; then
        log_info "Updating Helm chart values..."
        HELM_VALUES_FILE="charts/aws-multi-eni-controller/values.yaml"

        # Update repository
        update_image_in_file "${HELM_VALUES_FILE}" \
            "s|repository: .*|repository: ${REGISTRY}/${REPOSITORY}|g" \
            "repository: ${REGISTRY}/${REPOSITORY}"

        # Update tag
        update_image_in_file "${HELM_VALUES_FILE}" \
            "s|tag: .*|tag: ${TAG}|g" \
            "tag: ${TAG}"
    fi

    # Update deployment YAML files
    if [[ "${HELM_ONLY}" == "false" ]]; then
        log_info "Updating deployment YAML files..."

        # Update controller deployment
        DEPLOYMENT_FILE="deploy/deployment.yaml"
        update_image_in_file "${DEPLOYMENT_FILE}" \
            "s|image: .*aws-multi-eni-controller:.*|image: ${FULL_IMAGE}|g" \
            "image: ${FULL_IMAGE}"

        # Update ENI manager daemonset
        DAEMONSET_FILE="deploy/eni-manager-daemonset.yaml"
        update_image_in_file "${DAEMONSET_FILE}" \
            "s|image: .*aws-multi-eni-controller:.*|image: ${FULL_IMAGE}|g" \
            "image: ${FULL_IMAGE}"
    fi

    # Update Helm chart templates if they exist
    if [[ "${YAML_ONLY}" == "false" ]]; then
        log_info "Checking Helm chart templates..."

        CONTROLLER_TEMPLATE="charts/aws-multi-eni-controller/templates/controller-deployment.yaml"
        MANAGER_TEMPLATE="charts/aws-multi-eni-controller/templates/manager-daemonset.yaml"

        # These files use templating, so we just verify they exist
        if [[ -f "${CONTROLLER_TEMPLATE}" ]]; then
            log_info "Helm controller template exists: ${CONTROLLER_TEMPLATE}"
        fi

        if [[ -f "${MANAGER_TEMPLATE}" ]]; then
            log_info "Helm manager template exists: ${MANAGER_TEMPLATE}"
        fi
    fi
fi

# Show summary
log_info "Build and deploy process summary:"
log_info "  Image: ${FULL_IMAGE}"
log_info "  Platforms: ${PLATFORMS}"
log_info "  Tests skipped: ${SKIP_TESTS}"
log_info "  Push skipped: ${SKIP_PUSH}"
log_info "  Update skipped: ${SKIP_UPDATE}"
log_info "  Dry run: ${DRY_RUN}"

if [[ "${DRY_RUN}" == "false" ]]; then
    log_success "Build and deploy process completed successfully!"

    # Show next steps
    echo
    log_info "Next steps:"
    if [[ "${SKIP_PUSH}" == "false" ]]; then
        log_info "  1. Image has been pushed to: ${FULL_IMAGE}"
    fi

    if [[ "${SKIP_UPDATE}" == "false" ]]; then
        log_info "  2. Configuration files have been updated"
        log_info "  3. Review changes with: git diff"
        log_info "  4. Commit changes: git add . && git commit -m 'Update image to ${TAG}'"
    fi

    log_info "  5. Deploy with Helm:"
    log_info "     helm upgrade --install aws-multi-eni oci://ghcr.io/johnlam90/charts/aws-multi-eni-controller \\"
    log_info "       --version 1.3.0 --namespace eni-controller-system --create-namespace"

    log_info "  6. Or deploy with kubectl:"
    log_info "     kubectl apply -f deploy/"

    echo
    log_info "To verify deployment:"
    log_info "  kubectl get pods -n eni-controller-system"
    log_info "  kubectl logs -n eni-controller-system deployment/eni-controller"
    log_info "  kubectl logs -n eni-controller-system daemonset/eni-manager"
else
    log_info "Dry run completed. Use without --dry-run to execute."
fi
