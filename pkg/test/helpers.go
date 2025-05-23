// Package test provides utilities for testing the AWS Multi-ENI Controller.
package test

import (
	"context"
	"os"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SkipIfNoAWSCredentials skips the test if AWS credentials are not available
func SkipIfNoAWSCredentials(t *testing.T) {
	t.Helper()

	// Check for AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		// Also check for AWS_PROFILE as an alternative
		if os.Getenv("AWS_PROFILE") == "" {
			t.Skip("Skipping test that requires AWS credentials - neither AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY nor AWS_PROFILE are set")
		}
	}

	// Check for AWS region
	if os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "" {
		t.Skip("Skipping test that requires AWS region - neither AWS_REGION nor AWS_DEFAULT_REGION are set")
	}
}

// SkipIfNoKubernetesCluster skips the test if a Kubernetes cluster is not available
func SkipIfNoKubernetesCluster(t *testing.T) {
	t.Helper()

	// Check for KUBECONFIG
	if os.Getenv("KUBECONFIG") == "" {
		// Check if we're running in a cluster
		if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); os.IsNotExist(err) {
			t.Skip("Skipping test that requires a Kubernetes cluster - no KUBECONFIG set and not running in-cluster")
		}
	}
}

// CreateTestLogger creates a logger for testing
func CreateTestLogger(t *testing.T) logr.Logger {
	return testr.New(t)
}

// CreateTestEC2Client creates a real EC2 client for integration testing
func CreateTestEC2Client(t *testing.T) awsutil.EC2Interface {
	t.Helper()

	// Skip if no AWS credentials
	SkipIfNoAWSCredentials(t)

	// Determine AWS region
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1" // Default to us-east-1 if no region specified
	}

	// Create logger
	logger := CreateTestLogger(t)

	// Create EC2 client
	client, err := awsutil.CreateEC2Client(context.Background(), region, logger)
	if err != nil {
		t.Fatalf("Failed to create EC2 client: %v", err)
	}

	return client
}

// CreateMockEC2Client creates a mock EC2 client for unit testing
func CreateMockEC2Client() *awsutil.MockEC2Client {
	return awsutil.NewMockEC2Client()
}

// CreateTestNodeENI creates a NodeENI resource for testing
func CreateTestNodeENI(name string, nodeSelector map[string]string, subnetID string, securityGroupIDs []string, deviceIndex int) *networkingv1alpha1.NodeENI {
	return &networkingv1alpha1.NodeENI{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.aws/v1alpha1",
			Kind:       "NodeENI",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector:        nodeSelector,
			SubnetID:            subnetID,
			SecurityGroupIDs:    securityGroupIDs,
			DeviceIndex:         deviceIndex,
			DeleteOnTermination: true,
			Description:         "Test NodeENI for unit testing",
		},
	}
}

// CreateTestNodeENIWithMultipleSubnets creates a NodeENI resource with multiple subnets for testing
func CreateTestNodeENIWithMultipleSubnets(name string, nodeSelector map[string]string, subnetIDs []string, securityGroupIDs []string, deviceIndex int) *networkingv1alpha1.NodeENI {
	return &networkingv1alpha1.NodeENI{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.aws/v1alpha1",
			Kind:       "NodeENI",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector:        nodeSelector,
			SubnetIDs:           subnetIDs,
			SecurityGroupIDs:    securityGroupIDs,
			DeviceIndex:         deviceIndex,
			DeleteOnTermination: true,
			Description:         "Test NodeENI with multiple subnets for unit testing",
		},
	}
}

// CreateTestNodeENIWithSubnetName creates a NodeENI resource with subnet name for testing
func CreateTestNodeENIWithSubnetName(name string, nodeSelector map[string]string, subnetName string, securityGroupIDs []string, deviceIndex int) *networkingv1alpha1.NodeENI {
	return &networkingv1alpha1.NodeENI{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.aws/v1alpha1",
			Kind:       "NodeENI",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector:        nodeSelector,
			SubnetName:          subnetName,
			SecurityGroupIDs:    securityGroupIDs,
			DeviceIndex:         deviceIndex,
			DeleteOnTermination: true,
			Description:         "Test NodeENI with subnet name for unit testing",
		},
	}
}

// CreateTestNodeENIWithDPDK creates a NodeENI resource with DPDK configuration for testing
func CreateTestNodeENIWithDPDK(name string, nodeSelector map[string]string, subnetID string, securityGroupIDs []string, deviceIndex int, enableDPDK bool, dpdkDriver string, dpdkResourceName string, dpdkPCIAddress string) *networkingv1alpha1.NodeENI {
	return &networkingv1alpha1.NodeENI{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.aws/v1alpha1",
			Kind:       "NodeENI",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector:        nodeSelector,
			SubnetID:            subnetID,
			SecurityGroupIDs:    securityGroupIDs,
			DeviceIndex:         deviceIndex,
			DeleteOnTermination: true,
			Description:         "Test NodeENI with DPDK for unit testing",
			EnableDPDK:          enableDPDK,
			DPDKDriver:          dpdkDriver,
			DPDKResourceName:    dpdkResourceName,
			DPDKPCIAddress:      dpdkPCIAddress,
		},
	}
}

// CreateTestNodeENIWithMTU creates a NodeENI resource with MTU configuration for testing
func CreateTestNodeENIWithMTU(name string, nodeSelector map[string]string, subnetID string, securityGroupIDs []string, deviceIndex int, mtu int) *networkingv1alpha1.NodeENI {
	return &networkingv1alpha1.NodeENI{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.aws/v1alpha1",
			Kind:       "NodeENI",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector:        nodeSelector,
			SubnetID:            subnetID,
			SecurityGroupIDs:    securityGroupIDs,
			DeviceIndex:         deviceIndex,
			DeleteOnTermination: true,
			Description:         "Test NodeENI with MTU for unit testing",
			MTU:                 mtu,
		},
	}
}
