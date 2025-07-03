# IRSA Setup Guide for AWS Multi-ENI Controller

This guide explains how to set up IAM Roles for Service Accounts (IRSA) to enable truly automatic node replacement recovery without manual intervention.

> **Important:** This guide uses placeholder values like `<CLUSTER-NAME>`, `${ACCOUNT_ID}`, and `${OIDC_ISSUER}`. Replace these with your actual values when following the instructions.

## Overview

IRSA (IAM Roles for Service Accounts) is a cloud-native authentication mechanism that allows Kubernetes service accounts to assume AWS IAM roles without requiring IMDS access or manual credential management. This solves the IMDSv2 chicken-and-egg problem for node replacement scenarios.

## Benefits of IRSA

- ✅ **Zero Manual Intervention**: No need to manually configure IMDS hop limits
- ✅ **Cloud-Native Security**: Uses Kubernetes service account tokens
- ✅ **Automatic Node Replacement**: Works seamlessly when nodes are replaced
- ✅ **No IMDS Dependency**: Bypasses IMDS chicken-and-egg problem entirely
- ✅ **Production Ready**: Recommended by AWS for EKS workloads

## Prerequisites

1. EKS cluster with OIDC provider enabled
2. AWS CLI configured with appropriate permissions
3. `eksctl` or equivalent tool for IRSA setup

## Step 1: Enable OIDC Provider (if not already enabled)

```bash
# Check if OIDC provider exists
aws eks describe-cluster --name <CLUSTER-NAME> --query "cluster.identity.oidc.issuer" --output text

# If not enabled, create OIDC provider
eksctl utils associate-iam-oidc-provider --cluster <CLUSTER-NAME> --approve
```

## Step 2: Create IAM Policy

Create an IAM policy with the required permissions for the AWS Multi-ENI Controller:

```bash
cat > aws-multi-eni-controller-policy.json << EOF
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Action": [
                "ec2:CreateNetworkInterface",
                "ec2:DeleteNetworkInterface",
                "ec2:AttachNetworkInterface",
                "ec2:DetachNetworkInterface",
                "ec2:DescribeNetworkInterfaces",
                "ec2:DescribeInstances",
                "ec2:DescribeSubnets",
                "ec2:DescribeSecurityGroups",
                "ec2:DescribeInstanceAttribute",
                "ec2:ModifyInstanceMetadataOptions",
                "ec2:CreateTags",
                "ec2:DescribeTags"
            ],
            "Resource": "*"
        }
    ]
}
EOF

# Create the policy
aws iam create-policy \
    --policy-name AWSMultiENIControllerPolicy \
    --policy-document file://aws-multi-eni-controller-policy.json
```

## Step 3: Create IAM Role with IRSA

```bash
# Get your AWS account ID
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)

# Get your cluster's OIDC issuer URL
OIDC_ISSUER=$(aws eks describe-cluster --name <CLUSTER-NAME> --query "cluster.identity.oidc.issuer" --output text | sed 's|https://||')

# Create trust policy from template
envsubst < trust-policy-template.json > trust-policy.json

# Create the IAM role
aws iam create-role \
    --role-name AWSMultiENIControllerRole \
    --assume-role-policy-document file://trust-policy.json

# Attach the policy to the role
aws iam attach-role-policy \
    --role-name AWSMultiENIControllerRole \
    --policy-arn arn:aws:iam::${ACCOUNT_ID}:policy/AWSMultiENIControllerPolicy
```

## Step 4: Deploy with IRSA Configuration

### Option A: Using Helm (Recommended)

```bash
# Install with IRSA annotation
helm install aws-multi-eni-controller ./charts/aws-multi-eni-controller \
    --namespace eni-controller-system \
    --create-namespace \
    --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"="arn:aws:iam::${ACCOUNT_ID}:role/AWSMultiENIControllerRole"
```

### Option B: Manual Deployment

Update the service account in your deployment:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: eni-controller
  namespace: eni-controller-system
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::${ACCOUNT_ID}:role/AWSMultiENIControllerRole
```

**Note:** Replace `${ACCOUNT_ID}` with your actual AWS account ID.

## Step 5: Verify IRSA Setup

```bash
# Check if service account has the annotation
kubectl get serviceaccount eni-controller -n eni-controller-system -o yaml

# Check controller logs for IRSA authentication
kubectl logs -n eni-controller-system deployment/aws-multi-eni-controller | grep -i irsa
```

## Step 6: Test Node Replacement

```bash
# Terminate both worker nodes to test automatic recovery
kubectl get nodes -o jsonpath='{.items[*].spec.providerID}' | tr ' ' '\n' | sed 's|.*\/||' | xargs -I {} aws ec2 terminate-instances --instance-ids {}

# Monitor automatic recovery (should work without manual intervention)
kubectl get nodeenis -w
```

## Troubleshooting

### Common Issues

1. **OIDC Provider Not Found**
   ```bash
   # Solution: Enable OIDC provider
   eksctl utils associate-iam-oidc-provider --cluster <CLUSTER-NAME> --approve
   ```

2. **Trust Policy Mismatch**
   ```bash
   # Solution: Verify namespace and service account name match
   kubectl get serviceaccount -n eni-controller-system
   ```

3. **Permission Denied**
   ```bash
   # Solution: Check IAM policy has all required permissions
   aws iam get-role-policy --role-name AWSMultiENIControllerRole --policy-name AWSMultiENIControllerPolicy
   ```

### Verification Commands

```bash
# Check IRSA token
kubectl exec -n eni-controller-system deployment/aws-multi-eni-controller -- cat /var/run/secrets/eks.amazonaws.com/serviceaccount/token

# Test AWS API access
kubectl exec -n eni-controller-system deployment/aws-multi-eni-controller -- aws sts get-caller-identity
```

## Security Best Practices

1. **Least Privilege**: Only grant necessary EC2 permissions
2. **Resource Restrictions**: Consider adding resource-based conditions
3. **Regular Rotation**: Rotate OIDC provider certificates regularly
4. **Monitoring**: Monitor IRSA usage with CloudTrail

## Next Steps

After IRSA setup is complete:

1. Test node replacement scenarios
2. Monitor controller logs for authentication success
3. Verify ENI management works without manual intervention
4. Set up monitoring and alerting for controller health

For more information, see the [AWS IRSA Documentation](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html).
