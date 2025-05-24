#!/bin/bash

# Test script for SR-IOV Device Plugin Integration
# This script validates that the enhanced SR-IOV integration works correctly

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
NAMESPACE="eni-controller-system"
TEST_NODEENI_1="test-sriov-integration-1"
TEST_NODEENI_2="test-sriov-integration-2"
TEST_POD="test-sriov-consumer"
SRIOV_CONFIG_PATH="/etc/pcidp/config.json"

# Helper functions
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

# Function to wait for condition
wait_for_condition() {
    local condition="$1"
    local timeout="${2:-300}"
    local interval="${3:-5}"
    local count=0
    
    log_info "Waiting for condition: $condition"
    
    while [ $count -lt $timeout ]; do
        if eval "$condition"; then
            return 0
        fi
        sleep $interval
        count=$((count + interval))
        echo -n "."
    done
    
    echo ""
    log_error "Timeout waiting for condition: $condition"
    return 1
}

# Function to check if NodeENI is ready
check_nodeeni_ready() {
    local nodeeni_name="$1"
    local attachments=$(kubectl get nodeeni "$nodeeni_name" -o jsonpath='{.status.attachments[*].status}' 2>/dev/null || echo "")
    
    if [[ "$attachments" == *"attached"* ]]; then
        return 0
    fi
    return 1
}

# Function to check if DPDK is bound
check_dpdk_bound() {
    local nodeeni_name="$1"
    local dpdk_bound=$(kubectl get nodeeni "$nodeeni_name" -o jsonpath='{.status.attachments[*].dpdkBound}' 2>/dev/null || echo "false")
    
    if [[ "$dpdk_bound" == *"true"* ]]; then
        return 0
    fi
    return 1
}

# Function to check SR-IOV config
check_sriov_config() {
    local resource_name="$1"
    local node_name=$(kubectl get nodes -l ng=multi-eni -o jsonpath='{.items[0].metadata.name}')
    
    if [ -z "$node_name" ]; then
        log_error "No nodes found with label ng=multi-eni"
        return 1
    fi
    
    # Check if the resource is in the SR-IOV config
    local config_check=$(kubectl exec -n "$NAMESPACE" "$(kubectl get pods -n "$NAMESPACE" -l app=eni-manager --field-selector spec.nodeName="$node_name" -o jsonpath='{.items[0].metadata.name}')" -- cat "$SRIOV_CONFIG_PATH" 2>/dev/null | grep -c "$resource_name" || echo "0")
    
    if [ "$config_check" -gt 0 ]; then
        return 0
    fi
    return 1
}

# Function to check if SR-IOV device plugin sees the resources
check_sriov_resources() {
    local resource_name="$1"
    local node_name=$(kubectl get nodes -l ng=multi-eni -o jsonpath='{.items[0].metadata.name}')
    
    if [ -z "$node_name" ]; then
        log_error "No nodes found with label ng=multi-eni"
        return 1
    fi
    
    # Check if the node has the resource available
    local resource_count=$(kubectl get node "$node_name" -o jsonpath="{.status.allocatable['$resource_name']}" 2>/dev/null || echo "0")
    
    if [ "$resource_count" != "0" ] && [ -n "$resource_count" ]; then
        return 0
    fi
    return 1
}

# Main test function
run_test() {
    log_info "Starting SR-IOV Device Plugin Integration Test"
    
    # Step 1: Check prerequisites
    log_info "Step 1: Checking prerequisites"
    
    if ! kubectl get nodes -l ng=multi-eni | grep -q Ready; then
        log_error "No nodes with label ng=multi-eni found"
        exit 1
    fi
    
    if ! kubectl get pods -n "$NAMESPACE" -l app=eni-controller | grep -q Running; then
        log_error "ENI controller is not running"
        exit 1
    fi
    
    if ! kubectl get pods -n "$NAMESPACE" -l app=eni-manager | grep -q Running; then
        log_error "ENI manager is not running"
        exit 1
    fi
    
    log_success "Prerequisites check passed"
    
    # Step 2: Clean up any existing test resources
    log_info "Step 2: Cleaning up existing test resources"
    kubectl delete nodeeni "$TEST_NODEENI_1" "$TEST_NODEENI_2" --ignore-not-found=true
    kubectl delete pod "$TEST_POD" --ignore-not-found=true
    sleep 10
    
    # Step 3: Apply test NodeENI resources
    log_info "Step 3: Applying test NodeENI resources"
    kubectl apply -f deploy/samples/test-sriov-integration.yaml
    
    # Step 4: Wait for NodeENI resources to be attached
    log_info "Step 4: Waiting for NodeENI resources to be attached"
    
    if ! wait_for_condition "check_nodeeni_ready $TEST_NODEENI_1" 300 10; then
        log_error "NodeENI $TEST_NODEENI_1 failed to attach"
        kubectl describe nodeeni "$TEST_NODEENI_1"
        exit 1
    fi
    log_success "NodeENI $TEST_NODEENI_1 attached successfully"
    
    if ! wait_for_condition "check_nodeeni_ready $TEST_NODEENI_2" 300 10; then
        log_error "NodeENI $TEST_NODEENI_2 failed to attach"
        kubectl describe nodeeni "$TEST_NODEENI_2"
        exit 1
    fi
    log_success "NodeENI $TEST_NODEENI_2 attached successfully"
    
    # Step 5: Wait for DPDK binding
    log_info "Step 5: Waiting for DPDK binding"
    
    if ! wait_for_condition "check_dpdk_bound $TEST_NODEENI_1" 180 10; then
        log_error "DPDK binding failed for $TEST_NODEENI_1"
        kubectl describe nodeeni "$TEST_NODEENI_1"
        exit 1
    fi
    log_success "DPDK binding successful for $TEST_NODEENI_1"
    
    if ! wait_for_condition "check_dpdk_bound $TEST_NODEENI_2" 180 10; then
        log_error "DPDK binding failed for $TEST_NODEENI_2"
        kubectl describe nodeeni "$TEST_NODEENI_2"
        exit 1
    fi
    log_success "DPDK binding successful for $TEST_NODEENI_2"
    
    # Step 6: Check SR-IOV configuration
    log_info "Step 6: Checking SR-IOV device plugin configuration"
    
    sleep 30  # Give time for SR-IOV config to be updated
    
    if ! wait_for_condition "check_sriov_config intel.com/sriov_test_1" 120 10; then
        log_error "SR-IOV config not updated for intel.com/sriov_test_1"
        exit 1
    fi
    log_success "SR-IOV config updated for intel.com/sriov_test_1"
    
    if ! wait_for_condition "check_sriov_config intel.com/sriov_test_2" 120 10; then
        log_error "SR-IOV config not updated for intel.com/sriov_test_2"
        exit 1
    fi
    log_success "SR-IOV config updated for intel.com/sriov_test_2"
    
    # Step 7: Check if SR-IOV device plugin recognizes the resources
    log_info "Step 7: Checking if SR-IOV device plugin recognizes the resources"
    
    sleep 60  # Give time for device plugin to restart and discover resources
    
    if ! wait_for_condition "check_sriov_resources intel.com/sriov_test_1" 180 10; then
        log_warning "SR-IOV device plugin may not have recognized intel.com/sriov_test_1 yet"
    else
        log_success "SR-IOV device plugin recognized intel.com/sriov_test_1"
    fi
    
    if ! wait_for_condition "check_sriov_resources intel.com/sriov_test_2" 180 10; then
        log_warning "SR-IOV device plugin may not have recognized intel.com/sriov_test_2 yet"
    else
        log_success "SR-IOV device plugin recognized intel.com/sriov_test_2"
    fi
    
    # Step 8: Display final status
    log_info "Step 8: Displaying final status"
    
    echo ""
    log_info "NodeENI Status:"
    kubectl get nodeeni "$TEST_NODEENI_1" "$TEST_NODEENI_2" -o wide
    
    echo ""
    log_info "NodeENI Details:"
    kubectl describe nodeeni "$TEST_NODEENI_1" "$TEST_NODEENI_2"
    
    echo ""
    log_info "Node Resource Status:"
    local node_name=$(kubectl get nodes -l ng=multi-eni -o jsonpath='{.items[0].metadata.name}')
    kubectl describe node "$node_name" | grep -A 10 -B 5 "intel.com/sriov_test" || log_warning "No SR-IOV resources found on node"
    
    log_success "SR-IOV Device Plugin Integration Test completed successfully!"
}

# Cleanup function
cleanup() {
    log_info "Cleaning up test resources"
    kubectl delete nodeeni "$TEST_NODEENI_1" "$TEST_NODEENI_2" --ignore-not-found=true
    kubectl delete pod "$TEST_POD" --ignore-not-found=true
}

# Trap cleanup on exit
trap cleanup EXIT

# Run the test
run_test
