package performance

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestScaleDemo demonstrates the scale testing capabilities
func TestScaleDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping scale demo test in short mode")
	}

	logger := testr.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Configuration for scale demo
	nodeCount := 5
	eniPerNode := 4
	totalENIs := nodeCount * eniPerNode
	maxConcurrentReconciles := 10

	logger.Info("Starting AWS Multi-ENI Controller Scale Demo",
		"nodeCount", nodeCount,
		"eniPerNode", eniPerNode,
		"totalENIs", totalENIs,
		"maxConcurrentReconciles", maxConcurrentReconciles)

	// Create test environment
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// Create mock AWS client with pre-populated data
	mockAWS := awsutil.NewMockEC2Client()
	for i := 0; i < nodeCount; i++ {
		instanceID := fmt.Sprintf("i-demo%d", i)
		mockAWS.AddInstance(instanceID, "running")
	}
	mockAWS.AddSubnet("subnet-demo", "10.0.1.0/24")

	// Create controller configuration optimized for scale
	controllerConfig := &config.ControllerConfig{
		AWSRegion:                  "us-east-1",
		ReconcilePeriod:            1 * time.Minute,
		DetachmentTimeout:          30 * time.Second,
		MaxConcurrentReconciles:    maxConcurrentReconciles,
		MaxConcurrentENICleanup:    5,
		DefaultDeviceIndex:         1,
		DefaultDeleteOnTermination: true,
	}

	// Create event recorder
	eventRecorder := record.NewFakeRecorder(1000)

	// Create reconciler (without metrics to avoid conflicts)
	reconciler := &controller.NodeENIReconciler{
		Client:   k8sClient,
		Log:      logger,
		Scheme:   scheme,
		Recorder: eventRecorder,
		AWS:      mockAWS,
		Config:   controllerConfig,
	}

	// Create test nodes
	var nodes []*corev1.Node
	for i := 0; i < nodeCount; i++ {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("demo-node-%d", i),
				Labels: map[string]string{
					"ng": "multi-eni",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: fmt.Sprintf("aws:///us-east-1a/i-demo%d", i),
			},
			Status: corev1.NodeStatus{
				Phase: corev1.NodeRunning,
			},
		}

		err := k8sClient.Create(ctx, node)
		require.NoError(t, err, "Failed to create test node")
		nodes = append(nodes, node)
	}

	logger.Info("Created test nodes", "count", len(nodes))

	// Phase 1: Gradual Scale-Up Test
	logger.Info("=== Phase 1: Gradual Scale-Up Test ===")
	gradualStartTime := time.Now()
	var gradualSuccessCount int
	var gradualErrorCount int

	for nodeIdx, node := range nodes {
		for eniIdx := 0; eniIdx < eniPerNode; eniIdx++ {
			eniStartTime := time.Now()

			nodeENI := &networkingv1alpha1.NodeENI{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("gradual-eni-%d-%d", nodeIdx, eniIdx),
				},
				Spec: networkingv1alpha1.NodeENISpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": node.Name,
					},
					SubnetID:         "subnet-demo",
					SecurityGroupIDs: []string{"sg-demo"},
					DeviceIndex:      1,
				},
			}

			err := k8sClient.Create(ctx, nodeENI)
			require.NoError(t, err, "Failed to create NodeENI")

			// Trigger reconciliation
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeENI.Name,
				},
			}

			_, err = reconciler.Reconcile(ctx, req)
			eniDuration := time.Since(eniStartTime)

			if err != nil {
				gradualErrorCount++
				logger.Error(err, "Gradual reconciliation failed", "nodeENI", nodeENI.Name)
			} else {
				gradualSuccessCount++
				logger.Info("Gradual ENI created",
					"nodeENI", nodeENI.Name,
					"duration", eniDuration,
					"node", node.Name)
			}
		}
	}

	gradualTotalDuration := time.Since(gradualStartTime)
	gradualSuccessRate := float64(gradualSuccessCount) / float64(totalENIs) * 100
	gradualAvgTime := gradualTotalDuration / time.Duration(gradualSuccessCount)

	logger.Info("Phase 1 Results - Gradual Scale-Up",
		"totalDuration", gradualTotalDuration,
		"avgCreationTime", gradualAvgTime,
		"successCount", gradualSuccessCount,
		"errorCount", gradualErrorCount,
		"successRate", gradualSuccessRate)

	// Phase 2: Burst Creation Test
	logger.Info("=== Phase 2: Burst Creation Test ===")
	burstStartTime := time.Now()
	var burstWg sync.WaitGroup
	var burstMu sync.Mutex
	var burstSuccessCount int
	var burstErrorCount int

	for nodeIdx, node := range nodes {
		for eniIdx := 0; eniIdx < eniPerNode; eniIdx++ {
			burstWg.Add(1)
			go func(nodeIdx, eniIdx int, nodeName string) {
				defer burstWg.Done()

				nodeENI := &networkingv1alpha1.NodeENI{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("burst-eni-%d-%d", nodeIdx, eniIdx),
					},
					Spec: networkingv1alpha1.NodeENISpec{
						NodeSelector: map[string]string{
							"kubernetes.io/hostname": nodeName,
						},
						SubnetID:         "subnet-demo",
						SecurityGroupIDs: []string{"sg-demo"},
						DeviceIndex:      1,
					},
				}

				err := k8sClient.Create(ctx, nodeENI)
				if err != nil {
					burstMu.Lock()
					burstErrorCount++
					burstMu.Unlock()
					logger.Error(err, "Failed to create burst NodeENI", "nodeENI", nodeENI.Name)
					return
				}

				// Trigger reconciliation
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: nodeENI.Name,
					},
				}

				_, err = reconciler.Reconcile(ctx, req)

				burstMu.Lock()
				if err != nil {
					burstErrorCount++
				} else {
					burstSuccessCount++
				}
				burstMu.Unlock()
			}(nodeIdx, eniIdx, node.Name)
		}
	}

	burstWg.Wait()
	burstTotalDuration := time.Since(burstStartTime)
	burstSuccessRate := float64(burstSuccessCount) / float64(totalENIs) * 100
	burstThroughput := float64(burstSuccessCount) / burstTotalDuration.Seconds()

	logger.Info("Phase 2 Results - Burst Creation",
		"totalDuration", burstTotalDuration,
		"successCount", burstSuccessCount,
		"errorCount", burstErrorCount,
		"successRate", burstSuccessRate,
		"throughput", burstThroughput)

	// Phase 3: Cleanup Performance Test
	logger.Info("=== Phase 3: Cleanup Performance Test ===")
	cleanupStartTime := time.Now()
	var cleanupWg sync.WaitGroup
	var cleanupMu sync.Mutex
	var cleanupSuccessCount int
	var cleanupErrorCount int

	// Delete all burst ENIs
	for nodeIdx := 0; nodeIdx < nodeCount; nodeIdx++ {
		for eniIdx := 0; eniIdx < eniPerNode; eniIdx++ {
			cleanupWg.Add(1)
			go func(nodeIdx, eniIdx int) {
				defer cleanupWg.Done()

				nodeENI := &networkingv1alpha1.NodeENI{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("burst-eni-%d-%d", nodeIdx, eniIdx),
					},
				}

				err := k8sClient.Delete(ctx, nodeENI)

				cleanupMu.Lock()
				if err != nil {
					cleanupErrorCount++
				} else {
					cleanupSuccessCount++
				}
				cleanupMu.Unlock()
			}(nodeIdx, eniIdx)
		}
	}

	cleanupWg.Wait()
	cleanupTotalDuration := time.Since(cleanupStartTime)
	cleanupSuccessRate := float64(cleanupSuccessCount) / float64(totalENIs) * 100
	cleanupThroughput := float64(cleanupSuccessCount) / cleanupTotalDuration.Seconds()

	logger.Info("Phase 3 Results - Cleanup Performance",
		"totalDuration", cleanupTotalDuration,
		"successCount", cleanupSuccessCount,
		"errorCount", cleanupErrorCount,
		"successRate", cleanupSuccessRate,
		"throughput", cleanupThroughput)

	// Final Summary
	logger.Info("=== Scale Demo Summary ===",
		"totalNodes", nodeCount,
		"eniPerNode", eniPerNode,
		"totalENIs", totalENIs,
		"gradualSuccessRate", gradualSuccessRate,
		"burstSuccessRate", burstSuccessRate,
		"cleanupSuccessRate", cleanupSuccessRate)

	// Assertions
	assert.Greater(t, gradualSuccessRate, 95.0, "Gradual scale-up success rate should be > 95%")
	assert.Greater(t, burstSuccessRate, 90.0, "Burst creation success rate should be > 90%")
	assert.Greater(t, cleanupSuccessRate, 95.0, "Cleanup success rate should be > 95%")
	assert.Greater(t, burstThroughput, 10.0, "Burst throughput should be > 10 ops/sec")
	assert.Less(t, gradualAvgTime, 1*time.Second, "Average gradual creation time should be < 1s")

	logger.Info("Scale demo completed successfully! 🎉")
}
