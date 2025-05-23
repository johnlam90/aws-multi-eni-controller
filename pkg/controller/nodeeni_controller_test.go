package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	testutil "github.com/johnlam90/aws-multi-eni-controller/pkg/test"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// setupTestReconciler creates a mock NodeENI controller with mock clients for testing
func setupTestReconciler(t *testing.T) (*testutil.MockNodeENIController, *testutil.MockClient, *awsutil.MockEC2Client, *testutil.MockEventRecorder) {
	// Create a test logger
	logger := testr.New(t)

	// Create a scheme with the required types
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1alpha1.AddToScheme(scheme)

	// Create a mock Kubernetes client
	mockClient := testutil.NewMockClient(scheme)

	// Create a mock AWS EC2 client
	mockEC2Client := awsutil.NewMockEC2Client()

	// Create a mock event recorder
	mockRecorder := testutil.NewMockEventRecorder()

	// Create the mock controller
	controller := testutil.NewMockNodeENIController(mockClient, mockEC2Client, logger, mockRecorder)

	return controller, mockClient, mockEC2Client, mockRecorder
}

// TestNodeENIReconciler_Reconcile_NoNodes tests reconciliation when no nodes match the selector
func TestNodeENIReconciler_Reconcile_NoNodes(t *testing.T) {
	// Set up the test reconciler
	controller, mockClient, mockEC2Client, _ := setupTestReconciler(t)

	// Create a NodeENI resource
	nodeENI := testutil.CreateTestNodeENI("test-nodeeni", map[string]string{"ng": "multi-eni"}, "subnet-123", []string{"sg-123"}, 1)

	// Add test data to the mock EC2 client
	mockEC2Client.AddSubnet("subnet-123", "10.0.0.0/24")
	mockEC2Client.AddSecurityGroup("sg-123", "sg-123")

	// Create the NodeENI in the mock client
	err := mockClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create NodeENI: %v", err)
	}

	// Create a request to reconcile the NodeENI
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "test-nodeeni",
		},
	}

	// Reconcile the NodeENI
	result, err := controller.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check the result
	if result.RequeueAfter != controller.Config.ReconcilePeriod {
		t.Errorf("Expected requeue after %v, got %v", controller.Config.ReconcilePeriod, result.RequeueAfter)
	}

	// Get the updated NodeENI
	updatedNodeENI := &networkingv1alpha1.NodeENI{}
	err = mockClient.Get(context.Background(), client.ObjectKey{Name: "test-nodeeni"}, updatedNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Check that no attachments were created
	if len(updatedNodeENI.Status.Attachments) != 0 {
		t.Errorf("Expected 0 attachments, got %d", len(updatedNodeENI.Status.Attachments))
	}
}

// TestNodeENIReconciler_Reconcile_WithNodes tests reconciliation when nodes match the selector
func TestNodeENIReconciler_Reconcile_WithNodes(t *testing.T) {
	// Set up the test reconciler
	controller, mockClient, mockEC2Client, _ := setupTestReconciler(t)

	// Create a NodeENI resource
	nodeENI := testutil.CreateTestNodeENI("test-nodeeni", map[string]string{"ng": "multi-eni"}, "subnet-123", []string{"sg-123"}, 1)

	// Add test data to the mock EC2 client
	mockEC2Client.AddSubnet("subnet-123", "10.0.0.0/24")
	mockEC2Client.AddSecurityGroup("sg-123", "sg-123")

	// Create the NodeENI in the mock client
	err := mockClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create NodeENI: %v", err)
	}

	// Create a node that matches the selector
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"ng": "multi-eni",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-123456789abcdef0",
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeInternalIP,
					Address: "10.0.0.1",
				},
			},
		},
	}

	// Create the node in the mock client
	err = mockClient.Create(context.Background(), node)
	if err != nil {
		t.Fatalf("Failed to create Node: %v", err)
	}

	// Create a request to reconcile the NodeENI
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "test-nodeeni",
		},
	}

	// Reconcile the NodeENI
	result, err := controller.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check the result
	if result.RequeueAfter != controller.Config.ReconcilePeriod {
		t.Errorf("Expected requeue after %v, got %v", controller.Config.ReconcilePeriod, result.RequeueAfter)
	}

	// Get the updated NodeENI
	updatedNodeENI := &networkingv1alpha1.NodeENI{}
	err = mockClient.Get(context.Background(), client.ObjectKey{Name: "test-nodeeni"}, updatedNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Check that an ENI was created and attached
	if len(updatedNodeENI.Status.Attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(updatedNodeENI.Status.Attachments))
	}

	// Check the attachment details
	if len(updatedNodeENI.Status.Attachments) > 0 {
		attachment := updatedNodeENI.Status.Attachments[0]
		if attachment.NodeID != "test-node" {
			t.Errorf("Expected node ID test-node, got %s", attachment.NodeID)
		}
		if attachment.InstanceID != "i-123456789abcdef0" {
			t.Errorf("Expected instance ID i-123456789abcdef0, got %s", attachment.InstanceID)
		}
		if attachment.SubnetID != "subnet-123" {
			t.Errorf("Expected subnet ID subnet-123, got %s", attachment.SubnetID)
		}
		if attachment.SubnetCIDR != "10.0.0.0/24" {
			t.Errorf("Expected subnet CIDR 10.0.0.0/24, got %s", attachment.SubnetCIDR)
		}
		if attachment.DeviceIndex != 1 {
			t.Errorf("Expected device index 1, got %d", attachment.DeviceIndex)
		}
	}
}

// TestNodeENIReconciler_Reconcile_WithMultipleSubnets tests reconciliation with multiple subnets
func TestNodeENIReconciler_Reconcile_WithMultipleSubnets(t *testing.T) {
	// Set up the test reconciler
	controller, mockClient, mockEC2Client, _ := setupTestReconciler(t)

	// Create a NodeENI resource with multiple subnets
	nodeENI := testutil.CreateTestNodeENIWithMultipleSubnets(
		"test-nodeeni-multi-subnet",
		map[string]string{"ng": "multi-eni"},
		[]string{"subnet-123", "subnet-456"},
		[]string{"sg-123"},
		1,
	)

	// Add test data to the mock EC2 client
	mockEC2Client.AddSubnet("subnet-123", "10.0.0.0/24")
	mockEC2Client.AddSubnet("subnet-456", "10.0.1.0/24")
	mockEC2Client.AddSecurityGroup("sg-123", "sg-123")

	// Create the NodeENI in the mock client
	err := mockClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create NodeENI: %v", err)
	}

	// Create a node that matches the selector
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"ng": "multi-eni",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-123456789abcdef0",
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeInternalIP,
					Address: "10.0.0.1",
				},
			},
		},
	}

	// Create the node in the mock client
	err = mockClient.Create(context.Background(), node)
	if err != nil {
		t.Fatalf("Failed to create Node: %v", err)
	}

	// Create a request to reconcile the NodeENI
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "test-nodeeni-multi-subnet",
		},
	}

	// Reconcile the NodeENI
	result, err := controller.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check the result
	if result.RequeueAfter != controller.Config.ReconcilePeriod {
		t.Errorf("Expected requeue after %v, got %v", controller.Config.ReconcilePeriod, result.RequeueAfter)
	}

	// Get the updated NodeENI
	updatedNodeENI := &networkingv1alpha1.NodeENI{}
	err = mockClient.Get(context.Background(), client.ObjectKey{Name: "test-nodeeni-multi-subnet"}, updatedNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Check that ENIs were created and attached for both subnets
	if len(updatedNodeENI.Status.Attachments) != 2 {
		t.Errorf("Expected 2 attachments, got %d", len(updatedNodeENI.Status.Attachments))
	}

	// Check that we have one attachment for each subnet
	subnetIDs := make(map[string]bool)
	for _, attachment := range updatedNodeENI.Status.Attachments {
		subnetIDs[attachment.SubnetID] = true
	}

	if !subnetIDs["subnet-123"] {
		t.Error("Expected attachment for subnet-123")
	}

	if !subnetIDs["subnet-456"] {
		t.Error("Expected attachment for subnet-456")
	}
}

// verifyDPDKSpec verifies the DPDK specification in a NodeENI resource
func verifyDPDKSpec(t *testing.T, nodeENI *networkingv1alpha1.NodeENI) {
	// Check DPDK-specific fields in the spec
	if !nodeENI.Spec.EnableDPDK {
		t.Error("Expected EnableDPDK to be true")
	}
	if nodeENI.Spec.DPDKDriver != "vfio-pci" {
		t.Errorf("Expected DPDKDriver vfio-pci, got %s", nodeENI.Spec.DPDKDriver)
	}
	if nodeENI.Spec.DPDKResourceName != "intel.com/sriov_dpdk" {
		t.Errorf("Expected DPDKResourceName intel.com/sriov_dpdk, got %s", nodeENI.Spec.DPDKResourceName)
	}
	if nodeENI.Spec.DPDKPCIAddress != "0000:00:06.0" {
		t.Errorf("Expected DPDKPCIAddress 0000:00:06.0, got %s", nodeENI.Spec.DPDKPCIAddress)
	}
}

// TestNodeENIReconciler_Reconcile_WithDPDK tests reconciliation with DPDK enabled
func TestNodeENIReconciler_Reconcile_WithDPDK(t *testing.T) {
	// Set up the test environment
	controller, mockClient, _, req := setupDPDKTest(t, "test-nodeeni-dpdk")

	// Reconcile the NodeENI
	result, err := controller.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check the result
	if result.RequeueAfter != controller.Config.ReconcilePeriod {
		t.Errorf("Expected requeue after %v, got %v", controller.Config.ReconcilePeriod, result.RequeueAfter)
	}

	// Get the updated NodeENI
	updatedNodeENI := &networkingv1alpha1.NodeENI{}
	err = mockClient.Get(context.Background(), client.ObjectKey{Name: "test-nodeeni-dpdk"}, updatedNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Verify the initial attachment
	verifyInitialDPDKAttachment(t, mockClient, "test-nodeeni-dpdk")

	// Verify the DPDK specification
	verifyDPDKSpec(t, updatedNodeENI)
}

// setupDPDKTest sets up a test environment for DPDK testing
func setupDPDKTest(t *testing.T, nodeENIName string) (*testutil.MockNodeENIController, *testutil.MockClient, *networkingv1alpha1.NodeENI, reconcile.Request) {
	// Set up the test reconciler
	controller, mockClient, mockEC2Client, _ := setupTestReconciler(t)

	// Create a NodeENI resource with DPDK enabled
	nodeENI := testutil.CreateTestNodeENIWithDPDK(
		nodeENIName,
		map[string]string{"ng": "dpdk-node"},
		"subnet-123",
		[]string{"sg-123"},
		1,
		true,
		"vfio-pci",
		"intel.com/sriov_dpdk",
		"0000:00:06.0",
	)

	// Add test data to the mock EC2 client
	mockEC2Client.AddSubnet("subnet-123", "10.0.0.0/24")
	mockEC2Client.AddSecurityGroup("sg-123", "sg-123")

	// Create the NodeENI in the mock client
	err := mockClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create NodeENI: %v", err)
	}

	// Create a node that matches the selector
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-dpdk-node",
			Labels: map[string]string{
				"ng": "dpdk-node",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-dpdknode123456",
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{
					Type:    corev1.NodeInternalIP,
					Address: "10.0.0.10",
				},
			},
		},
	}

	// Create the node in the mock client
	err = mockClient.Create(context.Background(), node)
	if err != nil {
		t.Fatalf("Failed to create Node: %v", err)
	}

	// Create a request to reconcile the NodeENI
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: nodeENIName,
		},
	}

	return controller, mockClient, nodeENI, req
}

// verifyInitialDPDKAttachment verifies the initial attachment of a DPDK-enabled ENI
func verifyInitialDPDKAttachment(t *testing.T, mockClient *testutil.MockClient, nodeENIName string) *networkingv1alpha1.NodeENI {
	// Get the updated NodeENI
	updatedNodeENI := &networkingv1alpha1.NodeENI{}
	err := mockClient.Get(context.Background(), client.ObjectKey{Name: nodeENIName}, updatedNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Check that an ENI was created and attached
	if len(updatedNodeENI.Status.Attachments) != 1 {
		t.Errorf("Expected 1 attachment, got %d", len(updatedNodeENI.Status.Attachments))
	}

	// Check the attachment details
	if len(updatedNodeENI.Status.Attachments) > 0 {
		attachment := updatedNodeENI.Status.Attachments[0]
		if attachment.NodeID != "test-dpdk-node" {
			t.Errorf("Expected node ID test-dpdk-node, got %s", attachment.NodeID)
		}
		if attachment.InstanceID != "i-dpdknode123456" {
			t.Errorf("Expected instance ID i-dpdknode123456, got %s", attachment.InstanceID)
		}
		if attachment.SubnetID != "subnet-123" {
			t.Errorf("Expected subnet ID subnet-123, got %s", attachment.SubnetID)
		}
		if attachment.SubnetCIDR != "10.0.0.0/24" {
			t.Errorf("Expected subnet CIDR 10.0.0.0/24, got %s", attachment.SubnetCIDR)
		}
		if attachment.DeviceIndex != 1 {
			t.Errorf("Expected device index 1, got %d", attachment.DeviceIndex)
		}

		// Check DPDK-specific fields in the attachment status
		// Initially, the DPDK fields might not be set yet as the ENI Manager DaemonSet
		// is responsible for binding the ENI to DPDK and updating the status
		if attachment.DPDKDriver != "vfio-pci" {
			t.Logf("DPDKDriver not set yet, expected vfio-pci, got %s", attachment.DPDKDriver)
		}
		if attachment.DPDKPCIAddress != "0000:00:06.0" {
			t.Logf("DPDKPCIAddress not set yet, expected 0000:00:06.0, got %s", attachment.DPDKPCIAddress)
		}
	}

	return updatedNodeENI
}

// simulateDPDKBinding simulates a DPDK binding update by the ENI Manager DaemonSet
func simulateDPDKBinding(t *testing.T, mockClient *testutil.MockClient, nodeENI *networkingv1alpha1.NodeENI) {
	if len(nodeENI.Status.Attachments) > 0 {
		// Update DPDK fields
		nodeENI.Status.Attachments[0].DPDKBound = true
		nodeENI.Status.Attachments[0].DPDKDriver = "vfio-pci"
		nodeENI.Status.Attachments[0].DPDKPCIAddress = "0000:00:06.0"
		nodeENI.Status.Attachments[0].DPDKResourceName = "intel.com/sriov_dpdk"
		nodeENI.Status.Attachments[0].LastUpdated = metav1.Now()

		// Update the NodeENI status
		err := mockClient.Status().Update(context.Background(), nodeENI)
		if err != nil {
			t.Fatalf("Failed to update NodeENI status: %v", err)
		}
	}
}

// verifyFinalDPDKStatus verifies the final DPDK status after reconciliation
func verifyFinalDPDKStatus(t *testing.T, mockClient *testutil.MockClient, nodeENIName string) {
	// Get the updated NodeENI again
	finalNodeENI := &networkingv1alpha1.NodeENI{}
	err := mockClient.Get(context.Background(), client.ObjectKey{Name: nodeENIName}, finalNodeENI)
	if err != nil {
		t.Fatalf("Failed to get updated NodeENI: %v", err)
	}

	// Check that the DPDK status is still present and correct
	if len(finalNodeENI.Status.Attachments) > 0 {
		attachment := finalNodeENI.Status.Attachments[0]

		// Check DPDK fields
		if !attachment.DPDKBound {
			t.Error("Expected DPDKBound to be true after reconciliation")
		}
		if attachment.DPDKDriver != "vfio-pci" {
			t.Errorf("Expected DPDKDriver vfio-pci, got %s", attachment.DPDKDriver)
		}
		if attachment.DPDKPCIAddress != "0000:00:06.0" {
			t.Errorf("Expected DPDKPCIAddress 0000:00:06.0, got %s", attachment.DPDKPCIAddress)
		}
		if attachment.DPDKResourceName != "intel.com/sriov_dpdk" {
			t.Errorf("Expected DPDKResourceName intel.com/sriov_dpdk, got %s", attachment.DPDKResourceName)
		}
	}
}

// TestNodeENIReconciler_Reconcile_WithDPDKStatusUpdate tests reconciliation with DPDK enabled and status updates
func TestNodeENIReconciler_Reconcile_WithDPDKStatusUpdate(t *testing.T) {
	// Set up the test environment
	controller, mockClient, _, req := setupDPDKTest(t, "test-nodeeni-dpdk-status")

	// Reconcile the NodeENI
	result, err := controller.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Check the result
	if result.RequeueAfter != controller.Config.ReconcilePeriod {
		t.Errorf("Expected requeue after %v, got %v", controller.Config.ReconcilePeriod, result.RequeueAfter)
	}

	// Verify the initial attachment
	updatedNodeENI := verifyInitialDPDKAttachment(t, mockClient, "test-nodeeni-dpdk-status")

	// Simulate a DPDK binding update
	simulateDPDKBinding(t, mockClient, updatedNodeENI)

	// Reconcile the NodeENI again to process the DPDK status update
	result, err = controller.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify the final DPDK status
	verifyFinalDPDKStatus(t, mockClient, "test-nodeeni-dpdk-status")
}
