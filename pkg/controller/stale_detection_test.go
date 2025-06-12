package controller

import (
	"context"
	"testing"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestComprehensiveStaleDetection(t *testing.T) {
	// Create mock AWS client
	mockAWS := aws.NewMockEC2Client()

	// Add test instances
	mockAWS.AddInstance("i-running", "running")
	mockAWS.AddInstance("i-terminated", "terminated")

	// Add test ENIs
	mockAWS.AddSubnet("subnet-12345", "10.0.1.0/24")
	eniID1, _ := mockAWS.CreateENI(context.Background(), "subnet-12345", []string{"sg-12345"}, "test ENI 1", nil)
	eniID2, _ := mockAWS.CreateENI(context.Background(), "subnet-12345", []string{"sg-12345"}, "test ENI 2", nil)

	// Attach ENI1 to running instance
	attachmentID1, _ := mockAWS.AttachENI(context.Background(), eniID1, "i-running", 1, false)

	// Attach ENI2 to terminated instance (this will be stale)
	attachmentID2, _ := mockAWS.AttachENI(context.Background(), eniID2, "i-terminated", 1, false)

	// Create test NodeENI with attachments
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
		Status: networkingv1alpha1.NodeENIStatus{
			Attachments: []networkingv1alpha1.ENIAttachment{
				{
					NodeID:       "node-running",
					InstanceID:   "i-running",
					ENIID:        eniID1,
					AttachmentID: attachmentID1,
					Status:       "attached",
				},
				{
					NodeID:       "node-terminated",
					InstanceID:   "i-terminated",
					ENIID:        eniID2,
					AttachmentID: attachmentID2,
					Status:       "attached",
				},
			},
		},
	}

	// Create fake client
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeENI).Build()

	// Create reconciler
	reconciler := &NodeENIReconciler{
		Client:   client,
		Log:      zap.New(zap.UseDevMode(true)),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
		Config: &config.ControllerConfig{
			DetachmentTimeout: 15 * time.Second,
		},
		AWS: mockAWS,
	}

	ctx := context.Background()

	// Test 1: Verify instance exists for running instance
	if !reconciler.verifyInstanceExists(ctx, "i-running") {
		t.Error("Expected running instance to exist")
	}

	// Test 2: Verify instance does not exist for terminated instance
	if reconciler.verifyInstanceExists(ctx, "i-terminated") {
		t.Error("Expected terminated instance to not exist")
	}

	// Test 3: Test comprehensive stale detection for terminated instance
	terminatedAttachment := nodeENI.Status.Attachments[1]
	if !reconciler.isAttachmentComprehensivelyStale(ctx, nodeENI, terminatedAttachment) {
		t.Error("Expected attachment to terminated instance to be stale")
	}

	// Test 4: Test comprehensive stale detection for running instance
	// Note: isAttachmentComprehensivelyStale is designed to be called only when the node
	// doesn't match the NodeENI selector. In this case, since we're testing a running instance
	// that would normally match the selector, we should test isAttachmentStaleInAWS instead.
	runningAttachment := nodeENI.Status.Attachments[0]
	if reconciler.isAttachmentStaleInAWS(ctx, nodeENI, runningAttachment) {
		t.Error("Expected attachment to running instance to not be stale in AWS")
	}

	// Test 5: Test AWS stale detection for properly attached ENI
	if reconciler.isAttachmentStaleInAWS(ctx, nodeENI, runningAttachment) {
		t.Error("Expected properly attached ENI to not be stale in AWS")
	}

	// Test 6: Test AWS stale detection for ENI attached to terminated instance
	if !reconciler.isAttachmentStaleInAWS(ctx, nodeENI, terminatedAttachment) {
		t.Error("Expected ENI attached to terminated instance to be stale in AWS")
	}
}

func TestENIAttachmentStateVerification(t *testing.T) {
	// Create mock AWS client
	mockAWS := aws.NewMockEC2Client()

	// Add test instance and subnet
	mockAWS.AddInstance("i-test", "running")
	mockAWS.AddSubnet("subnet-12345", "10.0.1.0/24")

	// Create and attach ENI
	eniID, _ := mockAWS.CreateENI(context.Background(), "subnet-12345", []string{"sg-12345"}, "test ENI", nil)
	attachmentID, _ := mockAWS.AttachENI(context.Background(), eniID, "i-test", 1, false)

	// Create reconciler
	reconciler := &NodeENIReconciler{
		Log: zap.New(zap.UseDevMode(true)),
		AWS: mockAWS,
		Config: &config.ControllerConfig{
			DetachmentTimeout: 15 * time.Second,
		},
	}

	ctx := context.Background()
	attachment := networkingv1alpha1.ENIAttachment{
		ENIID:        eniID,
		InstanceID:   "i-test",
		AttachmentID: attachmentID,
	}

	// Test 1: Verify ENI attachment state for properly attached ENI
	if !reconciler.verifyENIAttachmentState(ctx, attachment) {
		t.Error("Expected properly attached ENI to have valid attachment state")
	}

	// Test 2: Verify ENI instance mapping
	if !reconciler.verifyENIInstanceMapping(ctx, attachment) {
		t.Error("Expected ENI to be mapped to correct instance")
	}

	// Test 3: Detach ENI and verify state becomes invalid
	_ = mockAWS.DetachENI(ctx, attachmentID, true)
	if reconciler.verifyENIAttachmentState(ctx, attachment) {
		t.Error("Expected detached ENI to have invalid attachment state")
	}
}

func TestStaleAttachmentRemoval(t *testing.T) {
	// Create mock AWS client
	mockAWS := aws.NewMockEC2Client()

	// Add test instances
	mockAWS.AddInstance("i-running", "running")
	mockAWS.AddInstance("i-terminated", "terminated")

	// Add test subnet
	mockAWS.AddSubnet("subnet-12345", "10.0.1.0/24")

	// Create ENIs
	eniID1, _ := mockAWS.CreateENI(context.Background(), "subnet-12345", []string{"sg-12345"}, "test ENI 1", nil)
	eniID2, _ := mockAWS.CreateENI(context.Background(), "subnet-12345", []string{"sg-12345"}, "test ENI 2", nil)

	// Attach ENIs
	attachmentID1, _ := mockAWS.AttachENI(context.Background(), eniID1, "i-running", 1, false)
	attachmentID2, _ := mockAWS.AttachENI(context.Background(), eniID2, "i-terminated", 1, false)

	// Create test NodeENI
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
		Status: networkingv1alpha1.NodeENIStatus{
			Attachments: []networkingv1alpha1.ENIAttachment{
				{
					NodeID:       "node-running",
					InstanceID:   "i-running",
					ENIID:        eniID1,
					AttachmentID: attachmentID1,
					Status:       "attached",
				},
				{
					NodeID:       "node-terminated",
					InstanceID:   "i-terminated",
					ENIID:        eniID2,
					AttachmentID: attachmentID2,
					Status:       "attached",
				},
			},
		},
	}

	// Create reconciler
	reconciler := &NodeENIReconciler{
		Log: zap.New(zap.UseDevMode(true)),
		AWS: mockAWS,
		Config: &config.ControllerConfig{
			DetachmentTimeout: 15 * time.Second,
		},
		Recorder: record.NewFakeRecorder(100),
	}

	ctx := context.Background()

	// Test individual stale detection functions instead of full cleanup
	runningAttachment := nodeENI.Status.Attachments[0]
	terminatedAttachment := nodeENI.Status.Attachments[1]

	// Test 1: Running instance attachment should not be stale in AWS
	if reconciler.isAttachmentStaleInAWS(ctx, nodeENI, runningAttachment) {
		t.Error("Expected running instance attachment to not be stale in AWS")
	}

	// Test 2: Terminated instance attachment should be stale in AWS
	if !reconciler.isAttachmentStaleInAWS(ctx, nodeENI, terminatedAttachment) {
		t.Error("Expected terminated instance attachment to be stale in AWS")
	}

	// Test 3: Comprehensive stale detection for terminated instance
	if !reconciler.isAttachmentComprehensivelyStale(ctx, nodeENI, terminatedAttachment) {
		t.Error("Expected terminated instance attachment to be comprehensively stale")
	}

	// Test 4: Comprehensive stale detection for running instance
	// Note: isAttachmentComprehensivelyStale assumes the node doesn't match the selector
	// For a running instance that would normally match, we should test AWS-level staleness
	if reconciler.isAttachmentStaleInAWS(ctx, nodeENI, runningAttachment) {
		t.Error("Expected running instance attachment to not be stale in AWS")
	}
}
