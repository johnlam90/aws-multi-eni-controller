# IMDSv2 Support in AWS Multi-ENI Controller

This document explains the Instance Metadata Service Version 2 (IMDSv2) support implemented in the AWS Multi-ENI Controller to ensure compatibility with Amazon Linux 2023 nodes and other environments that enforce IMDSv2.

## Overview

Amazon EC2 Instance Metadata Service Version 2 (IMDSv2) is a more secure version of IMDS that uses session tokens to access instance metadata. Amazon Linux 2023 nodes enforce IMDSv2 by default (`HttpTokens: required`), which can cause credential retrieval failures in applications that don't properly support IMDSv2.

The AWS Multi-ENI Controller has been updated to fully support IMDSv2 while maintaining backward compatibility with Amazon Linux 2 nodes.

## Implementation

### AWS SDK Configuration

The controller uses AWS SDK for Go v2, which automatically supports IMDSv2 by default. The SDK will:

1. **Attempt IMDSv2 first**: Try to obtain a session token and use it for metadata requests
2. **Fallback to IMDSv1**: If IMDSv2 fails due to non-retryable errors (HTTP 403, 404, 405), fall back to IMDSv1
3. **Respect environment variables**: Use environment variables to configure IMDS behavior

### Environment Variables

The following environment variables are configured in both the controller deployment and ENI manager daemonset:

```yaml
# Enable/disable IMDS entirely
- name: AWS_EC2_METADATA_DISABLED
  value: "false"

# Control IMDSv1 fallback behavior
- name: AWS_EC2_METADATA_V1_DISABLED
  value: "false"

# Set IMDS endpoint mode (IPv4 or IPv6)
- name: AWS_EC2_METADATA_SERVICE_ENDPOINT_MODE
  value: "IPv4"

# Explicitly set IMDS endpoint
- name: AWS_EC2_METADATA_SERVICE_ENDPOINT
  value: "http://169.254.169.254"

# Configure timeout for IMDS requests
- name: AWS_METADATA_SERVICE_TIMEOUT
  value: "10"

# Configure retry attempts for IMDS requests
- name: AWS_METADATA_SERVICE_NUM_ATTEMPTS
  value: "3"
```

### Code Changes

The AWS client creation functions have been updated with comments explaining the IMDSv2 support:

```go
// Create AWS config - IMDSv2 configuration is handled via environment variables
// The AWS SDK v2 automatically uses IMDSv2 by default and falls back to IMDSv1 if needed
cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
```

## Compatibility

### Amazon Linux 2023 Nodes

- **IMDSv2 Enforcement**: AL2023 nodes have `HttpTokens: required` by default
- **Full Support**: The controller works without any manual configuration changes
- **No Manual Intervention**: No need to modify EC2 instance IMDS settings

### Amazon Linux 2 Nodes

- **Backward Compatibility**: Continues to work with both IMDSv1 and IMDSv2
- **Flexible Configuration**: Supports both `HttpTokens: optional` and `HttpTokens: required`
- **No Breaking Changes**: Existing deployments continue to work

## Deployment

### YAML Deployments

The environment variables are automatically included in:

- `deploy/deployment.yaml` (controller)
- `deploy/eni-manager-daemonset.yaml` (ENI manager)

### Helm Charts

The environment variables are automatically included in:

- `charts/aws-multi-eni-controller/templates/controller-deployment.yaml`
- `charts/aws-multi-eni-controller/templates/manager-daemonset.yaml`

No additional configuration is required when deploying via Helm.

## Troubleshooting

### Common Issues

1. **Credential Retrieval Timeout**
   ```
   failed to retrieve credentials: failed to refresh cached credentials, no EC2 IMDS role found, operation error ec2imds: GetMetadata, request canceled, context deadline exceeded
   ```
   
   **Solution**: The IMDSv2 configuration should resolve this. Verify the environment variables are set correctly.

2. **IMDS Access Denied**
   ```
   operation error ec2imds: GetMetadata, https response error StatusCode: 403
   ```
   
   **Solution**: This typically occurs when IMDSv1 is disabled but the application doesn't support IMDSv2. The updated configuration should resolve this.

### Verification

To verify IMDSv2 support is working:

1. **Check Pod Environment Variables**:
   ```bash
   kubectl get pod <controller-pod> -o yaml | grep -A 20 env:
   ```

2. **Check Pod Logs**:
   ```bash
   kubectl logs <controller-pod> | grep -i metadata
   ```

3. **Test AWS API Calls**:
   ```bash
   kubectl logs <controller-pod> | grep -i "AWS config"
   ```

### Testing

Run the IMDSv2 tests to verify configuration:

```bash
# Run IMDSv2 configuration tests
go test ./pkg/aws -run TestIMDSv2 -v

# Run integration tests (requires AWS credentials)
go test ./pkg/aws -tags=integration -v
```

## Security Considerations

### IMDSv2 Benefits

- **Session Token Protection**: Requires a session token for metadata access
- **Request Signing**: Prevents SSRF attacks
- **Hop Limit**: Limits metadata access to the instance itself

### Configuration Security

- **No Credentials in Environment**: Uses IAM roles for service accounts (IRSA)
- **Least Privilege**: Only requests necessary metadata
- **Timeout Protection**: Prevents hanging requests

## References

- [AWS IMDSv2 Documentation](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configuring-instance-metadata-service.html)
- [AWS SDK Go v2 IMDS Configuration](https://docs.aws.amazon.com/sdkref/latest/guide/feature-imds-credentials.html)
- [Amazon Linux 2023 IMDSv2 Enforcement](https://docs.aws.amazon.com/linux/al2023/ug/ec2-imds.html)
