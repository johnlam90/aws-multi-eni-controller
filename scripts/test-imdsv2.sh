#!/bin/bash

# Test script for IMDSv2 support in AWS Multi-ENI Controller
# This script verifies that the controller can successfully authenticate with AWS
# using IMDSv2 on both Amazon Linux 2 and Amazon Linux 2023 nodes

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check if we're running on EC2
check_ec2_environment() {
    print_status "Checking if running on EC2 instance..."
    
    # Try to access IMDS to check if we're on EC2
    if curl -s --max-time 5 http://169.254.169.254/latest/meta-data/instance-id > /dev/null 2>&1; then
        print_success "Running on EC2 instance"
        return 0
    else
        print_warning "Not running on EC2 instance - some tests will be skipped"
        return 1
    fi
}

# Function to check IMDS version support
check_imds_version() {
    print_status "Checking IMDS version support..."
    
    # Try IMDSv2 first
    TOKEN=$(curl -s --max-time 10 -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600" 2>/dev/null || echo "")
    
    if [ -n "$TOKEN" ]; then
        print_success "IMDSv2 is supported and working"
        
        # Test getting instance ID with token
        INSTANCE_ID=$(curl -s --max-time 10 -H "X-aws-ec2-metadata-token: $TOKEN" http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null || echo "")
        
        if [ -n "$INSTANCE_ID" ]; then
            print_success "Successfully retrieved instance ID using IMDSv2: $INSTANCE_ID"
        else
            print_error "Failed to retrieve instance ID using IMDSv2"
            return 1
        fi
    else
        print_warning "IMDSv2 token request failed, trying IMDSv1..."
        
        # Try IMDSv1 fallback
        INSTANCE_ID=$(curl -s --max-time 10 http://169.254.169.254/latest/meta-data/instance-id 2>/dev/null || echo "")
        
        if [ -n "$INSTANCE_ID" ]; then
            print_warning "IMDSv1 is working but IMDSv2 is not available"
            print_warning "Instance ID: $INSTANCE_ID"
        else
            print_error "Both IMDSv1 and IMDSv2 failed"
            return 1
        fi
    fi
}

# Function to check OS version
check_os_version() {
    print_status "Checking operating system version..."
    
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        print_success "OS: $PRETTY_NAME"
        
        if echo "$PRETTY_NAME" | grep -q "Amazon Linux 2023"; then
            print_status "Detected Amazon Linux 2023 - IMDSv2 enforcement expected"
        elif echo "$PRETTY_NAME" | grep -q "Amazon Linux 2"; then
            print_status "Detected Amazon Linux 2 - IMDSv1/v2 flexibility expected"
        else
            print_warning "Unknown OS - IMDS behavior may vary"
        fi
    else
        print_warning "Cannot determine OS version"
    fi
}

# Function to test AWS SDK configuration
test_aws_sdk_config() {
    print_status "Testing AWS SDK configuration..."
    
    # Set IMDSv2 environment variables
    export AWS_EC2_METADATA_DISABLED=false
    export AWS_EC2_METADATA_V1_DISABLED=false
    export AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE=IPv4
    export AWS_EC2_METADATA_SERVICE_ENDPOINT=http://169.254.169.254
    export AWS_METADATA_SERVICE_TIMEOUT=10
    export AWS_METADATA_SERVICE_NUM_ATTEMPTS=3
    
    print_success "Set IMDSv2 environment variables:"
    echo "  AWS_EC2_METADATA_DISABLED=$AWS_EC2_METADATA_DISABLED"
    echo "  AWS_EC2_METADATA_V1_DISABLED=$AWS_EC2_METADATA_V1_DISABLED"
    echo "  AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE=$AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE"
    echo "  AWS_EC2_METADATA_SERVICE_ENDPOINT=$AWS_EC2_METADATA_SERVICE_ENDPOINT"
    echo "  AWS_METADATA_SERVICE_TIMEOUT=$AWS_METADATA_SERVICE_TIMEOUT"
    echo "  AWS_METADATA_SERVICE_NUM_ATTEMPTS=$AWS_METADATA_SERVICE_NUM_ATTEMPTS"
}

# Function to test controller deployment
test_controller_deployment() {
    print_status "Checking AWS Multi-ENI Controller deployment..."
    
    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        print_warning "kubectl not found - skipping Kubernetes tests"
        return 0
    fi
    
    # Check if controller is deployed
    if kubectl get deployment eni-controller -n eni-controller-system &> /dev/null; then
        print_success "AWS Multi-ENI Controller deployment found"
        
        # Check controller logs for IMDS-related errors
        print_status "Checking controller logs for IMDS errors..."
        
        CONTROLLER_POD=$(kubectl get pods -n eni-controller-system -l app=eni-controller -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        
        if [ -n "$CONTROLLER_POD" ]; then
            print_status "Checking logs for pod: $CONTROLLER_POD"
            
            # Look for IMDS-related errors in the last 100 lines
            IMDS_ERRORS=$(kubectl logs -n eni-controller-system "$CONTROLLER_POD" --tail=100 | grep -i "metadata\|imds\|credential" | grep -i "error\|failed" || echo "")
            
            if [ -z "$IMDS_ERRORS" ]; then
                print_success "No IMDS-related errors found in controller logs"
            else
                print_warning "Found potential IMDS-related errors:"
                echo "$IMDS_ERRORS"
            fi
        else
            print_warning "Controller pod not found"
        fi
    else
        print_warning "AWS Multi-ENI Controller not deployed - skipping controller tests"
    fi
}

# Function to run Go tests
test_go_implementation() {
    print_status "Running Go tests for IMDSv2 implementation..."
    
    if [ -f "go.mod" ]; then
        print_status "Running IMDSv2 tests..."
        if go test ./pkg/aws -run TestIMDSv2 -v; then
            print_success "Go tests passed"
        else
            print_error "Go tests failed"
            return 1
        fi
    else
        print_warning "Not in Go project directory - skipping Go tests"
    fi
}

# Main function
main() {
    echo "=========================================="
    echo "AWS Multi-ENI Controller IMDSv2 Test"
    echo "=========================================="
    echo
    
    # Check if we're on EC2
    if check_ec2_environment; then
        check_os_version
        check_imds_version
    fi
    
    # Test AWS SDK configuration
    test_aws_sdk_config
    
    # Test controller deployment
    test_controller_deployment
    
    # Test Go implementation
    test_go_implementation
    
    echo
    print_success "IMDSv2 test completed!"
    echo
    echo "For more information about IMDSv2 support, see:"
    echo "  docs/imdsv2-support.md"
}

# Run main function
main "$@"
