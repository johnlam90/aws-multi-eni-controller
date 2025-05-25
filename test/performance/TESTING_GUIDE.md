# AWS Multi-ENI Controller Performance Testing Guide

## Overview

This guide provides comprehensive instructions for executing performance tests on the AWS Multi-ENI Controller to validate its behavior at enterprise scale (10 nodes × 10 ENIs = 100 total ENIs).

## Quick Start

### 1. Basic Performance Test (Mock AWS)

```bash
# Run with default settings (10 nodes × 10 ENIs)
cd test/performance
./run-scale-test.sh
```

### 2. Real AWS Environment Test

```bash
# Set up AWS credentials and run with real AWS APIs
export AWS_REGION=us-east-1
export PERF_TEST_SUBNET_ID=subnet-xxxxxxxxx
export PERF_TEST_SECURITY_GROUP_ID=sg-xxxxxxxxx

./run-scale-test.sh --use-real-aws --subnet-id $PERF_TEST_SUBNET_ID --security-group-id $PERF_TEST_SECURITY_GROUP_ID
```

### 3. Custom Scale Test

```bash
# Test with 15 nodes and 8 ENIs per node
./run-scale-test.sh --node-count 15 --eni-per-node 8
```

## Test Scenarios

### Scenario 1: Gradual Scale-Up
- **Purpose**: Validate controller performance during gradual scaling
- **Method**: Incrementally add nodes and ENIs
- **Metrics**: Creation time, attachment time, resource usage
- **Expected**: Linear performance degradation, no memory leaks

### Scenario 2: Burst Creation
- **Purpose**: Test controller under sudden load spikes
- **Method**: Create all NodeENI resources simultaneously
- **Metrics**: Throughput, error rate, recovery time
- **Expected**: Graceful handling of burst load, circuit breaker activation if needed

### Scenario 3: Mixed Operations
- **Purpose**: Simulate real-world workload patterns
- **Method**: Concurrent create, update, delete operations
- **Metrics**: Operation success rate, coordination conflicts
- **Expected**: Stable performance under mixed load

### Scenario 4: Failure Recovery
- **Purpose**: Validate resilience and recovery mechanisms
- **Method**: Introduce AWS API failures, network issues
- **Metrics**: Recovery time, circuit breaker behavior
- **Expected**: Automatic recovery, no data loss

### Scenario 5: Scale-Down Performance
- **Purpose**: Test cleanup efficiency
- **Method**: Delete all NodeENI resources
- **Metrics**: Cleanup time, resource deallocation
- **Expected**: Complete cleanup within timeout

## Configuration Optimization

### Recommended Settings for Scale Testing

```yaml
# values-scale-test.yaml
controller:
  maxConcurrentReconciles: 15      # Increased from default 5
  maxConcurrentENICleanup: 8       # Increased from default 3

resources:
  controller:
    limits:
      cpu: 1000m                   # Increased from 500m
      memory: 1Gi                  # Increased from 512Mi
    requests:
      cpu: 200m                    # Increased from 100m
      memory: 256Mi                # Increased from 128Mi

eniManager:
  checkInterval: 15s               # Reduced from 30s for faster response
```

### Environment Variables

```bash
# Test Configuration
export PERF_TEST_NODE_COUNT=10
export PERF_TEST_ENI_PER_NODE=10
export PERF_TEST_MAX_CONCURRENT_RECONCILES=15
export PERF_TEST_MAX_CONCURRENT_CLEANUP=8
export PERF_TEST_TIMEOUT=30m

# AWS Configuration (for real AWS testing)
export AWS_REGION=us-east-1
export PERF_TEST_SUBNET_ID=subnet-xxxxxxxxx
export PERF_TEST_SECURITY_GROUP_ID=sg-xxxxxxxxx
export PERF_TEST_USE_REAL_AWS=true

# Circuit Breaker Configuration
export CIRCUIT_BREAKER_ENABLED=true
export CIRCUIT_BREAKER_FAILURE_THRESHOLD=5
export CIRCUIT_BREAKER_SUCCESS_THRESHOLD=3
export CIRCUIT_BREAKER_TIMEOUT=30s
```

## Performance Thresholds

### Acceptable Performance Targets

| Metric | Target | Warning | Critical |
|--------|--------|---------|----------|
| ENI Creation Time | < 30s | > 45s | > 60s |
| ENI Attachment Time | < 15s | > 25s | > 35s |
| Reconciliation Time | < 5m | > 8m | > 10m |
| Controller CPU | < 200m | > 300m | > 400m |
| Controller Memory | < 256Mi | > 384Mi | > 512Mi |
| Error Rate | < 1% | > 2% | > 5% |
| Success Rate | > 99% | < 98% | < 95% |

## Monitoring and Observability

### Key Metrics to Monitor

1. **ENI Operations**
   - `eni_operations_total` - Total ENI operations
   - `eni_operation_duration_seconds` - Operation duration
   - `eni_operation_errors_total` - Operation errors

2. **AWS API Performance**
   - `aws_api_calls_total` - Total API calls
   - `aws_api_call_duration_seconds` - API call duration
   - `aws_throttling_events_total` - Throttling events

3. **Controller Performance**
   - `circuit_breaker_state` - Circuit breaker status
   - `coordination_conflicts_total` - Resource conflicts
   - `cleanup_operations_total` - Cleanup operations

### Grafana Dashboard

Import the provided Grafana dashboard (`monitoring/grafana-dashboard.json`) to visualize:
- Real-time performance metrics
- Resource utilization trends
- Error rates and patterns
- AWS API performance

### Prometheus Queries

```promql
# Average ENI creation time
histogram_quantile(0.95, rate(eni_operation_duration_seconds_bucket{operation="create"}[5m]))

# Error rate percentage
rate(eni_operation_errors_total[5m]) / rate(eni_operations_total[5m]) * 100

# AWS API throttling rate
rate(aws_throttling_events_total[5m])

# Controller CPU usage
rate(container_cpu_usage_seconds_total{pod=~"eni-controller.*"}[5m])
```

## Test Environment Setup

### Prerequisites

1. **Kubernetes Cluster**
   - EKS cluster with worker nodes
   - kubectl configured and accessible
   - Sufficient resources for testing

2. **AWS Permissions**
   - EC2 permissions for ENI operations
   - VPC permissions for subnet access
   - IAM permissions for service operations

3. **Tools**
   - Go 1.22+
   - Helm 3.x
   - kubectl
   - AWS CLI (for real AWS testing)

### Simulated Environment

For testing without real AWS resources:

```bash
# Use mock AWS clients
export PERF_TEST_USE_REAL_AWS=false

# Run tests with simulated environment
./run-scale-test.sh
```

### Real AWS Environment

For production-like testing:

```bash
# Configure AWS credentials
aws configure

# Set required environment variables
export AWS_REGION=us-east-1
export PERF_TEST_SUBNET_ID=subnet-xxxxxxxxx
export PERF_TEST_SECURITY_GROUP_ID=sg-xxxxxxxxx

# Run tests with real AWS
./run-scale-test.sh --use-real-aws
```

## Interpreting Results

### Success Criteria

- ✅ All ENIs created and attached successfully
- ✅ Performance within acceptable thresholds
- ✅ No memory leaks or resource exhaustion
- ✅ Proper cleanup of all resources
- ✅ Error rate < 1%

### Warning Signs

- ⚠️ Performance degradation beyond warning thresholds
- ⚠️ Increasing error rates
- ⚠️ Memory usage growth
- ⚠️ AWS API throttling events

### Failure Indicators

- ❌ Test timeouts
- ❌ Resource exhaustion
- ❌ Persistent errors
- ❌ Circuit breaker constantly open
- ❌ Incomplete cleanup

### Optimization Recommendations

Based on test results, consider:

1. **Increase Concurrency**: Raise `maxConcurrentReconciles` if CPU allows
2. **Resource Scaling**: Increase controller CPU/memory limits
3. **Rate Limiting**: Adjust AWS API rate limits
4. **Circuit Breaker**: Tune failure/success thresholds
5. **Monitoring**: Enhance observability for bottlenecks

## Troubleshooting

### Common Issues

1. **Test Timeouts**
   - Increase `PERF_TEST_TIMEOUT`
   - Check AWS API rate limits
   - Verify network connectivity

2. **High Error Rates**
   - Check AWS permissions
   - Verify subnet/security group configuration
   - Review controller logs

3. **Resource Exhaustion**
   - Increase controller resource limits
   - Check node capacity
   - Monitor memory usage patterns

4. **AWS API Throttling**
   - Reduce concurrent operations
   - Implement exponential backoff
   - Contact AWS for limit increases

### Debug Commands

```bash
# Check controller status
kubectl get pods -n eni-controller-system

# View controller logs
kubectl logs -n eni-controller-system deployment/eni-controller

# Check NodeENI resources
kubectl get nodeeni -o wide

# Monitor resource usage
kubectl top pods -n eni-controller-system

# Check events
kubectl get events -n eni-controller-system --sort-by='.lastTimestamp'
```

## Next Steps

1. **Baseline Testing**: Establish performance baselines
2. **Regression Testing**: Integrate into CI/CD pipeline
3. **Load Testing**: Test with higher scales (20+ nodes)
4. **Chaos Engineering**: Introduce failures and test recovery
5. **Production Monitoring**: Deploy monitoring in production
