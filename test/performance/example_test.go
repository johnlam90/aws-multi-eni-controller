package performance

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/controller"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestBasicPerformance demonstrates basic performance testing capabilities
func TestBasicPerformance(t *testing.T) {
	// Skip if running in short mode
	if testing.Short() {
		t.Skip("Skipping performance test in short mode")
	}

	logger := testr.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create test environment
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// Create mock AWS client
	mockAWS := awsutil.NewMockEC2Client()
	mockAWS.AddInstance("i-test1", "running")
	mockAWS.AddSubnet("subnet-12345", "10.0.1.0/24")

	// Create controller
	controllerConfig := &config.ControllerConfig{
		AWSRegion:                  "us-east-1",
		ReconcilePeriod:            1 * time.Minute,
		DetachmentTimeout:          30 * time.Second,
		MaxConcurrentReconciles:    5,
		MaxConcurrentENICleanup:    3,
		DefaultDeviceIndex:         1,
		DefaultDeleteOnTermination: true,
	}

	metrics := observability.NewMetrics()
	eventRecorder := record.NewFakeRecorder(100)

	reconciler := &controller.NodeENIReconciler{
		Client:   k8sClient,
		Log:      logger,
		Scheme:   scheme,
		Recorder: eventRecorder,
		AWS:      mockAWS,
		Config:   controllerConfig,
		Metrics:  metrics,
	}

	// Create test node
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-1",
			Labels: map[string]string{
				"ng": "multi-eni",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-test1",
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodeRunning,
		},
	}

	err := k8sClient.Create(ctx, node)
	require.NoError(t, err, "Failed to create test node")

	// Performance test: Create multiple NodeENI resources
	nodeCount := 3
	eniPerNode := 3
	totalENIs := nodeCount * eniPerNode

	logger.Info("Starting basic performance test",
		"nodeCount", nodeCount,
		"eniPerNode", eniPerNode,
		"totalENIs", totalENIs)

	startTime := time.Now()
	var successCount int
	var errorCount int

	// Create NodeENI resources
	for i := 0; i < totalENIs; i++ {
		nodeENI := &networkingv1alpha1.NodeENI{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-eni-" + string(rune(i)),
			},
			Spec: networkingv1alpha1.NodeENISpec{
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node.Name,
				},
				SubnetID:         "subnet-12345",
				SecurityGroupIDs: []string{"sg-12345"},
				DeviceIndex:      1,
			},
		}

		err := k8sClient.Create(ctx, nodeENI)
		require.NoError(t, err, "Failed to create NodeENI")

		// Trigger reconciliation
		req := reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(nodeENI),
		}

		_, err = reconciler.Reconcile(ctx, req)
		if err != nil {
			errorCount++
			logger.Error(err, "Reconciliation failed", "nodeENI", nodeENI.Name)
		} else {
			successCount++
		}
	}

	totalDuration := time.Since(startTime)

	// Calculate performance metrics
	avgCreationTime := totalDuration / time.Duration(successCount)
	successRate := float64(successCount) / float64(totalENIs) * 100
	throughput := float64(successCount) / totalDuration.Seconds()

	logger.Info("Basic performance test completed",
		"totalDuration", totalDuration,
		"avgCreationTime", avgCreationTime,
		"successCount", successCount,
		"errorCount", errorCount,
		"successRate", successRate,
		"throughput", throughput)

	// Assertions
	assert.Greater(t, successRate, 90.0, "Success rate should be > 90%")
	assert.Less(t, avgCreationTime, 10*time.Second, "Average creation time should be < 10s")
	assert.Greater(t, throughput, 0.1, "Throughput should be > 0.1 ENI/second")
	assert.Equal(t, 0, errorCount, "Should have no errors in basic test")
}

// TestConcurrentOperations tests concurrent ENI operations
func TestConcurrentOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent operations test in short mode")
	}

	logger := testr.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Create test environment (similar setup as above)
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	mockAWS := awsutil.NewMockEC2Client()
	mockAWS.AddInstance("i-test1", "running")
	mockAWS.AddSubnet("subnet-12345", "10.0.1.0/24")

	controllerConfig := &config.ControllerConfig{
		AWSRegion:                  "us-east-1",
		ReconcilePeriod:            1 * time.Minute,
		DetachmentTimeout:          30 * time.Second,
		MaxConcurrentReconciles:    10, // Increased for concurrency test
		MaxConcurrentENICleanup:    5,
		DefaultDeviceIndex:         1,
		DefaultDeleteOnTermination: true,
	}

	metrics := observability.NewMetrics()
	eventRecorder := record.NewFakeRecorder(100)

	reconciler := &controller.NodeENIReconciler{
		Client:   k8sClient,
		Log:      logger,
		Scheme:   scheme,
		Recorder: eventRecorder,
		AWS:      mockAWS,
		Config:   controllerConfig,
		Metrics:  metrics,
	}

	// Create test node
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-concurrent",
			Labels: map[string]string{
				"ng": "multi-eni",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-test1",
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodeRunning,
		},
	}

	err := k8sClient.Create(ctx, node)
	require.NoError(t, err, "Failed to create test node")

	// Test concurrent operations
	concurrentENIs := 5
	logger.Info("Starting concurrent operations test", "concurrentENIs", concurrentENIs)

	startTime := time.Now()
	results := make(chan error, concurrentENIs)

	// Launch concurrent operations
	for i := 0; i < concurrentENIs; i++ {
		go func(index int) {
			nodeENI := &networkingv1alpha1.NodeENI{
				ObjectMeta: metav1.ObjectMeta{
					Name: "concurrent-eni-" + string(rune(index)),
				},
				Spec: networkingv1alpha1.NodeENISpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": node.Name,
					},
					SubnetID:         "subnet-12345",
					SecurityGroupIDs: []string{"sg-12345"},
					DeviceIndex:      1,
				},
			}

			err := k8sClient.Create(ctx, nodeENI)
			if err != nil {
				results <- err
				return
			}

			req := reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(nodeENI),
			}

			_, err = reconciler.Reconcile(ctx, req)
			results <- err
		}(i)
	}

	// Collect results
	var successCount int
	var errorCount int

	for i := 0; i < concurrentENIs; i++ {
		err := <-results
		if err != nil {
			errorCount++
		} else {
			successCount++
		}
	}

	totalDuration := time.Since(startTime)
	successRate := float64(successCount) / float64(concurrentENIs) * 100

	logger.Info("Concurrent operations test completed",
		"totalDuration", totalDuration,
		"successCount", successCount,
		"errorCount", errorCount,
		"successRate", successRate)

	// Assertions
	assert.Greater(t, successRate, 80.0, "Success rate should be > 80% for concurrent operations")
	assert.Less(t, totalDuration, 30*time.Second, "Concurrent operations should complete within 30s")
}
