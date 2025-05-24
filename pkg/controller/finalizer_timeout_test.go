package controller

import (
	"context"
	"testing"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestFinalizerTimeout(t *testing.T) {
	// Create a test NodeENI with finalizer
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-nodeeni",
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
			Finalizers:        []string{NodeENIFinalizer},
			Annotations: map[string]string{
				CleanupStartTimeAnnotation: time.Now().Add(-35 * time.Minute).Format(time.RFC3339),
			},
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{"test": "true"},
		},
		Status: networkingv1alpha1.NodeENIStatus{
			Attachments: []networkingv1alpha1.ENIAttachment{},
		},
	}

	// Create fake client
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeENI).Build()

	// Create test config with short timeout
	cfg := &config.ControllerConfig{
		MaxCleanupDuration: 30 * time.Minute,
		DetachmentTimeout:  15 * time.Second,
	}

	// Create reconciler
	reconciler := &NodeENIReconciler{
		Client:   client,
		Log:      zap.New(zap.UseDevMode(true)),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
		Config:   cfg,
	}

	// Test timeout detection
	if !reconciler.isCleanupTimedOut(nodeENI) {
		t.Error("Expected cleanup to be timed out")
	}

	// Test with recent start time
	nodeENI.Annotations[CleanupStartTimeAnnotation] = time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	if reconciler.isCleanupTimedOut(nodeENI) {
		t.Error("Expected cleanup to not be timed out")
	}

	// Test with no annotation
	delete(nodeENI.Annotations, CleanupStartTimeAnnotation)
	if reconciler.isCleanupTimedOut(nodeENI) {
		t.Error("Expected cleanup to not be timed out when no annotation exists")
	}
}

func TestSetCleanupStartTime(t *testing.T) {
	// Create a test NodeENI without cleanup annotation
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
	}

	// Create fake client
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeENI).Build()

	// Create reconciler
	reconciler := &NodeENIReconciler{
		Client: client,
		Log:    zap.New(zap.UseDevMode(true)),
		Scheme: scheme,
	}

	ctx := context.Background()

	// Test setting cleanup start time
	err := reconciler.setCleanupStartTime(ctx, nodeENI)
	if err != nil {
		t.Fatalf("Failed to set cleanup start time: %v", err)
	}

	// Verify annotation was set
	if nodeENI.Annotations == nil {
		t.Error("Expected annotations to be created")
	}

	startTimeStr, exists := nodeENI.Annotations[CleanupStartTimeAnnotation]
	if !exists {
		t.Error("Expected cleanup start time annotation to be set")
	}

	// Verify the time can be parsed
	_, err = time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		t.Errorf("Failed to parse cleanup start time: %v", err)
	}

	// Test that setting again doesn't change the time
	originalTime := startTimeStr
	time.Sleep(10 * time.Millisecond) // Small delay to ensure time would be different
	err = reconciler.setCleanupStartTime(ctx, nodeENI)
	if err != nil {
		t.Fatalf("Failed to set cleanup start time second time: %v", err)
	}

	if nodeENI.Annotations[CleanupStartTimeAnnotation] != originalTime {
		t.Error("Expected cleanup start time to remain unchanged on second call")
	}
}
