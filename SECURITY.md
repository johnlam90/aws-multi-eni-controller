# Security Policy

## Reporting a Vulnerability

The AWS Multi-ENI Controller team takes security vulnerabilities seriously. We appreciate your efforts to responsibly disclose your findings and will make every effort to address them quickly and thoroughly.

### Reporting Process

1. **DO NOT** create a public GitHub issue for security vulnerabilities
2. Email your findings to [johnlam@johnlam.io](mailto:johnlam@johnlam.io)
3. Include the following information in your report:
   - Description of the vulnerability
   - Steps to reproduce or proof-of-concept code
   - Potential impact of the vulnerability
   - Any suggested mitigations (if available)
   - Your contact information for follow-up questions

### What to Expect

- We will acknowledge receipt of your report within 3 business days
- We will provide an initial assessment of the report within 14 days
- We will work with you to understand and address the issue
- We will keep you informed of our progress throughout the remediation process
- We will credit you for your discovery when we publish the fix (unless you prefer to remain anonymous)

## Security Update Process

Security updates will be released as part of our regular release cycle or as emergency patches depending on severity:

- **Critical vulnerabilities**: Emergency patch as soon as a fix is available
- **High severity vulnerabilities**: Within 30 days of confirmation
- **Medium severity vulnerabilities**: Within 60 days of confirmation
- **Low severity vulnerabilities**: Addressed in the next regular release

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.3.x   | :white_check_mark: |
| 1.2.x   | :white_check_mark: |
| < 1.2.0 | :x:                |

We generally support the current and previous minor release with security updates.

## Security Design and Implementation

The AWS Multi-ENI Controller is designed and maintained by developers with expertise in secure software development practices. Our security approach includes:

### Secure Design Principles

- Principle of least privilege for AWS IAM roles and Kubernetes RBAC
- Defense in depth for network interface management
- Secure defaults for all configuration options
- Input validation for all external data

### Common Vulnerability Mitigation

Our developers are knowledgeable about common vulnerabilities in cloud-native applications and implement mitigations for:

- Command injection in shell commands
- Path traversal in file operations
- Insecure direct object references
- Improper error handling that could leak sensitive information
- Race conditions in concurrent operations

### Security Best Practices

- Regular dependency updates to address known vulnerabilities
- Code reviews with security considerations
- Static code analysis to identify potential security issues
- Secure coding practices for all new development

## Security-Related Configuration

### AWS IAM Permissions

The AWS Multi-ENI Controller requires specific IAM permissions to function properly. We recommend following the principle of least privilege and only granting the following permissions:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "ec2:CreateNetworkInterface",
        "ec2:DeleteNetworkInterface",
        "ec2:DescribeNetworkInterfaces",
        "ec2:AttachNetworkInterface",
        "ec2:DetachNetworkInterface",
        "ec2:DescribeSubnets",
        "ec2:DescribeSecurityGroups"
      ],
      "Resource": "*"
    }
  ]
}
```

### Kubernetes RBAC

The controller requires specific RBAC permissions which are included in the deployment manifests. These permissions are scoped to only what is necessary for the controller to function.

## Third-Party Dependencies

We regularly monitor and update our dependencies to address security vulnerabilities. Our CI/CD pipeline includes dependency scanning to identify known vulnerabilities.
