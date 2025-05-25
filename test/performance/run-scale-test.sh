#!/bin/bash

# AWS Multi-ENI Controller Scale Performance Test Runner
# This script orchestrates comprehensive performance testing for enterprise scale deployment

set -euo pipefail

# Make script executable
chmod +x "$0"

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TEST_NAMESPACE="eni-controller-system"
PERFORMANCE_NAMESPACE="performance-test"

# Default test parameters
NODE_COUNT=${PERF_TEST_NODE_COUNT:-10}
ENI_PER_NODE=${PERF_TEST_ENI_PER_NODE:-10}
MAX_CONCURRENT_RECONCILES=${PERF_TEST_MAX_CONCURRENT_RECONCILES:-15}
MAX_CONCURRENT_CLEANUP=${PERF_TEST_MAX_CONCURRENT_CLEANUP:-8}
TEST_TIMEOUT=${PERF_TEST_TIMEOUT:-30m}
USE_REAL_AWS=${PERF_TEST_USE_REAL_AWS:-false}
SUBNET_ID=${PERF_TEST_SUBNET_ID:-subnet-12345}
SECURITY_GROUP_ID=${PERF_TEST_SECURITY_GROUP_ID:-sg-12345}

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

# Function to check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed or not in PATH"
        exit 1
    fi

    # Check if helm is available
    if ! command -v helm &> /dev/null; then
        log_error "helm is not installed or not in PATH"
        exit 1
    fi

    # Check if go is available
    if ! command -v go &> /dev/null; then
        log_error "go is not installed or not in PATH"
        exit 1
    fi

    # Check Kubernetes cluster connectivity
    if ! kubectl cluster-info &> /dev/null; then
        log_error "Cannot connect to Kubernetes cluster"
        exit 1
    fi

    # Check AWS credentials if using real AWS
    if [[ "${USE_REAL_AWS}" == "true" ]]; then
        if ! aws sts get-caller-identity &> /dev/null; then
            log_error "AWS credentials not configured or invalid"
            exit 1
        fi
        log_info "AWS credentials validated"
    fi

    log_success "Prerequisites check passed"
}

# Function to setup test environment
setup_test_environment() {
    log_info "Setting up test environment..."

    # Create performance test namespace
    kubectl create namespace "${PERFORMANCE_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

    # Label nodes for testing if they exist
    if kubectl get nodes -l ng=multi-eni &> /dev/null; then
        log_info "Found existing nodes with ng=multi-eni label"
    else
        log_warning "No nodes found with ng=multi-eni label. Creating simulated environment."
    fi

    # Deploy controller with performance configuration
    log_info "Deploying AWS Multi-ENI Controller with performance configuration..."
    helm upgrade --install aws-multi-eni-controller \
        "${PROJECT_ROOT}/charts/aws-multi-eni-controller" \
        --namespace "${TEST_NAMESPACE}" \
        --create-namespace \
        --values "${SCRIPT_DIR}/values-scale-test.yaml" \
        --set controller.maxConcurrentReconciles="${MAX_CONCURRENT_RECONCILES}" \
        --set controller.maxConcurrentENICleanup="${MAX_CONCURRENT_CLEANUP}" \
        --set metrics.enabled=true \
        --wait --timeout=10m

    # Wait for controller to be ready
    kubectl wait --for=condition=available deployment/eni-controller \
        -n "${TEST_NAMESPACE}" --timeout=300s

    log_success "Test environment setup completed"
}

# Function to run performance tests
run_performance_tests() {
    log_info "Starting performance tests..."

    # Set environment variables for tests
    export PERF_TEST_NODE_COUNT="${NODE_COUNT}"
    export PERF_TEST_ENI_PER_NODE="${ENI_PER_NODE}"
    export PERF_TEST_MAX_CONCURRENT_RECONCILES="${MAX_CONCURRENT_RECONCILES}"
    export PERF_TEST_MAX_CONCURRENT_CLEANUP="${MAX_CONCURRENT_CLEANUP}"
    export PERF_TEST_TIMEOUT="${TEST_TIMEOUT}"
    export PERF_TEST_USE_REAL_AWS="${USE_REAL_AWS}"
    export PERF_TEST_SUBNET_ID="${SUBNET_ID}"
    export PERF_TEST_SECURITY_GROUP_ID="${SECURITY_GROUP_ID}"

    # Create test results directory
    RESULTS_DIR="${SCRIPT_DIR}/results/$(date +%Y%m%d_%H%M%S)"
    mkdir -p "${RESULTS_DIR}"

    log_info "Test results will be saved to: ${RESULTS_DIR}"

    # Start metrics collection in background
    start_metrics_collection "${RESULTS_DIR}" &
    METRICS_PID=$!

    # Run the actual performance tests
    cd "${PROJECT_ROOT}"

    log_info "Running scale-up performance test..."
    go test -v -tags=performance ./test/performance -run TestScaleUp \
        -timeout="${TEST_TIMEOUT}" \
        2>&1 | tee "${RESULTS_DIR}/scale_up_test.log"

    log_info "Running burst creation performance test..."
    go test -v -tags=performance ./test/performance -run TestBurstCreation \
        -timeout="${TEST_TIMEOUT}" \
        2>&1 | tee "${RESULTS_DIR}/burst_creation_test.log"

    log_info "Running mixed operations performance test..."
    go test -v -tags=performance ./test/performance -run TestMixedOperations \
        -timeout="${TEST_TIMEOUT}" \
        2>&1 | tee "${RESULTS_DIR}/mixed_operations_test.log"

    # Stop metrics collection
    kill $METRICS_PID 2>/dev/null || true

    log_success "Performance tests completed"
}

# Function to start metrics collection
start_metrics_collection() {
    local results_dir="$1"
    local metrics_file="${results_dir}/metrics.json"

    log_info "Starting metrics collection..."

    while true; do
        # Collect controller metrics
        kubectl top pod -n "${TEST_NAMESPACE}" --no-headers 2>/dev/null | \
            grep eni-controller >> "${results_dir}/resource_usage.log" || true

        # Collect Prometheus metrics if available
        if kubectl get service eni-controller-metrics -n "${TEST_NAMESPACE}" &>/dev/null; then
            kubectl port-forward -n "${TEST_NAMESPACE}" service/eni-controller-metrics 8080:8080 &
            PORT_FORWARD_PID=$!
            sleep 2

            curl -s http://localhost:8080/metrics >> "${results_dir}/prometheus_metrics.txt" 2>/dev/null || true

            kill $PORT_FORWARD_PID 2>/dev/null || true
        fi

        sleep 30
    done
}

# Function to collect test results
collect_results() {
    local results_dir="$1"

    log_info "Collecting test results..."

    # Collect controller logs
    kubectl logs -n "${TEST_NAMESPACE}" deployment/eni-controller \
        --tail=1000 > "${results_dir}/controller_logs.txt" 2>/dev/null || true

    # Collect ENI manager logs
    kubectl logs -n "${TEST_NAMESPACE}" daemonset/eni-manager \
        --tail=1000 > "${results_dir}/eni_manager_logs.txt" 2>/dev/null || true

    # Collect NodeENI resources
    kubectl get nodeeni -o yaml > "${results_dir}/nodeeni_resources.yaml" 2>/dev/null || true

    # Collect events
    kubectl get events -n "${TEST_NAMESPACE}" \
        --sort-by='.lastTimestamp' > "${results_dir}/events.txt" 2>/dev/null || true

    # Generate summary report
    generate_summary_report "${results_dir}"

    log_success "Results collected in: ${results_dir}"
}

# Function to generate summary report
generate_summary_report() {
    local results_dir="$1"
    local report_file="${results_dir}/summary_report.txt"

    cat > "${report_file}" << EOF
AWS Multi-ENI Controller Performance Test Summary
================================================

Test Configuration:
- Node Count: ${NODE_COUNT}
- ENIs per Node: ${ENI_PER_NODE}
- Total ENIs: $((NODE_COUNT * ENI_PER_NODE))
- Max Concurrent Reconciles: ${MAX_CONCURRENT_RECONCILES}
- Max Concurrent Cleanup: ${MAX_CONCURRENT_CLEANUP}
- Test Timeout: ${TEST_TIMEOUT}
- Use Real AWS: ${USE_REAL_AWS}
- Test Date: $(date)

Test Results:
$(analyze_test_results "${results_dir}")

Resource Usage:
$(analyze_resource_usage "${results_dir}")

Recommendations:
$(generate_recommendations "${results_dir}")

EOF

    log_info "Summary report generated: ${report_file}"
}

# Function to analyze test results
analyze_test_results() {
    local results_dir="$1"

    # Analyze test logs for success/failure patterns
    local total_tests=0
    local passed_tests=0
    local failed_tests=0

    for log_file in "${results_dir}"/*_test.log; do
        if [[ -f "$log_file" ]]; then
            total_tests=$((total_tests + 1))
            if grep -q "PASS" "$log_file"; then
                passed_tests=$((passed_tests + 1))
            else
                failed_tests=$((failed_tests + 1))
            fi
        fi
    done

    echo "- Total Tests: ${total_tests}"
    echo "- Passed Tests: ${passed_tests}"
    echo "- Failed Tests: ${failed_tests}"
    echo "- Success Rate: $(( passed_tests * 100 / total_tests ))%"
}

# Function to analyze resource usage
analyze_resource_usage() {
    local results_dir="$1"

    if [[ -f "${results_dir}/resource_usage.log" ]]; then
        echo "- Peak CPU Usage: $(awk '{print $2}' "${results_dir}/resource_usage.log" | sort -n | tail -1)"
        echo "- Peak Memory Usage: $(awk '{print $3}' "${results_dir}/resource_usage.log" | sort -n | tail -1)"
    else
        echo "- Resource usage data not available"
    fi
}

# Function to generate recommendations
generate_recommendations() {
    local results_dir="$1"

    echo "- Monitor AWS API rate limits and adjust accordingly"
    echo "- Consider increasing controller resources if CPU/Memory usage is high"
    echo "- Review error logs for optimization opportunities"
    echo "- Validate network performance between controller and AWS APIs"
}

# Function to cleanup test environment
cleanup_test_environment() {
    log_info "Cleaning up test environment..."

    # Delete all NodeENI resources
    kubectl delete nodeeni --all --timeout=300s 2>/dev/null || true

    # Uninstall Helm chart
    helm uninstall aws-multi-eni-controller -n "${TEST_NAMESPACE}" 2>/dev/null || true

    # Delete namespaces
    kubectl delete namespace "${PERFORMANCE_NAMESPACE}" --timeout=300s 2>/dev/null || true

    log_success "Cleanup completed"
}

# Main execution function
main() {
    log_info "Starting AWS Multi-ENI Controller Scale Performance Test"
    log_info "Configuration: ${NODE_COUNT} nodes × ${ENI_PER_NODE} ENIs = $((NODE_COUNT * ENI_PER_NODE)) total ENIs"

    # Create results directory
    RESULTS_DIR="${SCRIPT_DIR}/results/$(date +%Y%m%d_%H%M%S)"
    mkdir -p "${RESULTS_DIR}"

    # Trap to ensure cleanup on exit
    trap 'cleanup_test_environment' EXIT

    check_prerequisites
    setup_test_environment
    run_performance_tests
    collect_results "${RESULTS_DIR}"

    log_success "Performance testing completed successfully!"
    log_info "Results available in: ${RESULTS_DIR}"
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --node-count)
            NODE_COUNT="$2"
            shift 2
            ;;
        --eni-per-node)
            ENI_PER_NODE="$2"
            shift 2
            ;;
        --use-real-aws)
            USE_REAL_AWS="true"
            shift
            ;;
        --subnet-id)
            SUBNET_ID="$2"
            shift 2
            ;;
        --security-group-id)
            SECURITY_GROUP_ID="$2"
            shift 2
            ;;
        --help)
            echo "Usage: $0 [options]"
            echo "Options:"
            echo "  --node-count <num>         Number of nodes to test (default: 10)"
            echo "  --eni-per-node <num>       Number of ENIs per node (default: 10)"
            echo "  --use-real-aws             Use real AWS APIs instead of mocks"
            echo "  --subnet-id <id>           AWS subnet ID for testing"
            echo "  --security-group-id <id>   AWS security group ID for testing"
            echo "  --help                     Show this help message"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Run main function
main "$@"
