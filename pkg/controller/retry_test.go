package controller

import (
	"context"
	"fmt"
	"testing"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	testutil "github.com/johnlam90/aws-multi-eni-controller/pkg/test"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// TestUpdateNodeENIWithRetry tests the retry logic for resource version conflicts
func TestUpdateNodeENIWithRetry(t *testing.T) {
	// Create scheme and add types
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1alpha1.AddToScheme(scheme)

	// Create a mock client that simulates resource version conflicts
	mockClient := testutil.NewMockClient(scheme)

	// Create a test NodeENI
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-nodeeni",
			ResourceVersion: "1",
			Finalizers:      []string{"test-finalizer"},
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				"test": "true",
			},
			SubnetIDs: []string{"subnet-123"},
		},
	}

	// Add the NodeENI to the mock client
	err := mockClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create test NodeENI: %v", err)
	}

	// Create a reconciler with the mock client
	reconciler := &NodeENIReconciler{
		Client: mockClient,
		Log:    zap.New(zap.UseDevMode(true)),
		Config: config.DefaultControllerConfig(),
	}

	t.Run("successful update without conflicts", func(t *testing.T) {
		// Make a change to the NodeENI
		nodeENI.Annotations = map[string]string{
			"test": "annotation",
		}

		// Update should succeed
		result, err := reconciler.updateNodeENIWithRetry(context.Background(), nodeENI)
		if err != nil {
			t.Errorf("Expected successful update, got error: %v", err)
		}
		if result.Requeue {
			t.Errorf("Expected no requeue, got requeue")
		}
	})

	t.Run("successful update with simulated conflicts", func(t *testing.T) {
		// Create a mock client that simulates conflicts for the first few attempts
		conflictClient := &ConflictSimulatingClient{
			MockClient:    mockClient,
			ConflictCount: 2, // Simulate 2 conflicts before success
		}

		reconcilerWithConflicts := &NodeENIReconciler{
			Client: conflictClient,
			Log:    zap.New(zap.UseDevMode(true)),
			Config: config.DefaultControllerConfig(),
		}

		// Make a change to the NodeENI
		nodeENI.Annotations = map[string]string{
			"test": "annotation-with-conflicts",
		}

		// Update should eventually succeed after retries
		result, err := reconcilerWithConflicts.updateNodeENIWithRetry(context.Background(), nodeENI)
		if err != nil {
			t.Errorf("Expected successful update after retries, got error: %v", err)
		}
		if result.Requeue {
			t.Errorf("Expected no requeue, got requeue")
		}

		// Verify that conflicts were encountered and resolved
		if conflictClient.ConflictCount != 0 {
			t.Errorf("Expected all conflicts to be resolved, but %d conflicts remain", conflictClient.ConflictCount)
		}
	})
}

// TestUpdateNodeENIStatusWithRetry tests the retry logic for status update conflicts
func TestUpdateNodeENIStatusWithRetry(t *testing.T) {
	// Create scheme and add types
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1alpha1.AddToScheme(scheme)

	// Create a mock client
	mockClient := testutil.NewMockClient(scheme)

	// Create a test NodeENI
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-nodeeni-status",
			ResourceVersion: "1",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				"test": "true",
			},
			SubnetIDs: []string{"subnet-123"},
		},
		Status: networkingv1alpha1.NodeENIStatus{
			Attachments: []networkingv1alpha1.ENIAttachment{
				{
					ENIID:      "eni-123",
					NodeID:     "node-1",
					InstanceID: "i-123",
				},
			},
		},
	}

	// Add the NodeENI to the mock client
	err := mockClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create test NodeENI: %v", err)
	}

	// Create a reconciler with the mock client
	reconciler := &NodeENIReconciler{
		Client: mockClient,
		Log:    zap.New(zap.UseDevMode(true)),
		Config: config.DefaultControllerConfig(),
	}

	t.Run("successful status update without conflicts", func(t *testing.T) {
		// Make a change to the NodeENI status
		nodeENI.Status.Attachments[0].DeviceIndex = 1

		// Status update should succeed
		err := reconciler.updateNodeENIStatusWithRetry(context.Background(), nodeENI)
		if err != nil {
			t.Errorf("Expected successful status update, got error: %v", err)
		}
	})

	t.Run("successful status update with simulated conflicts", func(t *testing.T) {
		// Create a mock client that simulates conflicts for status updates
		conflictClient := &ConflictSimulatingClient{
			MockClient:    mockClient,
			ConflictCount: 3, // Simulate 3 conflicts before success
		}

		reconcilerWithConflicts := &NodeENIReconciler{
			Client: conflictClient,
			Log:    zap.New(zap.UseDevMode(true)),
			Config: config.DefaultControllerConfig(),
		}

		// Make a change to the NodeENI status
		nodeENI.Status.Attachments[0].DeviceIndex = 2

		// Status update should eventually succeed after retries
		err := reconcilerWithConflicts.updateNodeENIStatusWithRetry(context.Background(), nodeENI)
		if err != nil {
			t.Errorf("Expected successful status update after retries, got error: %v", err)
		}

		// Verify that conflicts were encountered and resolved
		if conflictClient.ConflictCount != 0 {
			t.Errorf("Expected all conflicts to be resolved, but %d conflicts remain", conflictClient.ConflictCount)
		}
	})
}

// ConflictSimulatingClient wraps a mock client to simulate resource version conflicts
type ConflictSimulatingClient struct {
	*testutil.MockClient
	ConflictCount int
}

// Update simulates resource version conflicts for the first few attempts
func (c *ConflictSimulatingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if c.ConflictCount > 0 {
		c.ConflictCount--
		// Return a conflict error
		return errors.NewConflict(
			schema.GroupResource{Group: "networking.k8s.aws", Resource: "nodeenis"},
			obj.GetName(),
			fmt.Errorf("simulated resource version conflict"),
		)
	}
	// After conflicts are exhausted, call the real Update method
	return c.MockClient.Update(ctx, obj, opts...)
}

// Status returns a status writer that also simulates conflicts
func (c *ConflictSimulatingClient) Status() client.StatusWriter {
	return &ConflictSimulatingStatusWriter{
		StatusWriter:  c.MockClient.Status(),
		ConflictCount: &c.ConflictCount,
	}
}

// ConflictSimulatingStatusWriter wraps a status writer to simulate conflicts
type ConflictSimulatingStatusWriter struct {
	client.StatusWriter
	ConflictCount *int
}

// Update simulates resource version conflicts for status updates
func (s *ConflictSimulatingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if *s.ConflictCount > 0 {
		*s.ConflictCount--
		// Return a conflict error
		return errors.NewConflict(
			schema.GroupResource{Group: "networking.k8s.aws", Resource: "nodeenis"},
			obj.GetName(),
			fmt.Errorf("simulated status resource version conflict"),
		)
	}
	// After conflicts are exhausted, call the real Update method
	return s.StatusWriter.Update(ctx, obj, opts...)
}
