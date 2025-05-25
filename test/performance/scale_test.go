//go:build performance
// +build performance

package performance

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	awsutil "github.com/johnlam90/aws-multi-eni-controller/pkg/aws"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/controller"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/observability"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// PerformanceTestConfig holds configuration for performance tests
type PerformanceTestConfig struct {
	NodeCount               int
	ENIPerNode              int
	MaxConcurrentReconciles int
	MaxConcurrentCleanup    int
	TestTimeout             time.Duration
	UseRealAWS              bool
	AWSRegion               string
	SubnetID                string
	SecurityGroupIDs        []string
}

// DefaultPerformanceConfig returns default configuration for performance tests
func DefaultPerformanceConfig() *PerformanceTestConfig {
	return &PerformanceTestConfig{
		NodeCount:               10,
		ENIPerNode:              10,
		MaxConcurrentReconciles: 15,
		MaxConcurrentCleanup:    8,
		TestTimeout:             30 * time.Minute,
		UseRealAWS:              false,
		AWSRegion:               "us-east-1",
		SubnetID:                "subnet-12345",
		SecurityGroupIDs:        []string{"sg-12345"},
	}
}

// PerformanceTestSuite manages performance test execution
type PerformanceTestSuite struct {
	config     *PerformanceTestConfig
	client     client.Client
	aws        awsutil.EC2Interface
	reconciler *controller.NodeENIReconciler
	metrics    *observability.Metrics
	logger     logr.Logger
	scheme     *runtime.Scheme
}

// NewPerformanceTestSuite creates a new performance test suite
func NewPerformanceTestSuite(testConfig *PerformanceTestConfig) *PerformanceTestSuite {
	logger := testr.New(&testing.T{})

	// Create Kubernetes client
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// Create AWS client (mock or real)
	var awsClient awsutil.EC2Interface
	if testConfig.UseRealAWS {
		// Use real AWS client for integration testing
		awsClient = test.CreateTestEC2Client(&testing.T{})
	} else {
		// Use mock AWS client for unit testing
		mockClient := awsutil.NewMockEC2Client()
		// Pre-populate with test data
		for i := 0; i < testConfig.NodeCount; i++ {
			instanceID := fmt.Sprintf("i-test%d", i)
			mockClient.AddInstance(instanceID, "running")
		}
		mockClient.AddSubnet(testConfig.SubnetID, "10.0.1.0/24")
		awsClient = mockClient
	}

	// Create metrics
	metrics := observability.NewMetrics()

	// Create controller configuration
	controllerConfig := &config.ControllerConfig{
		AWSRegion:                  testConfig.AWSRegion,
		ReconcilePeriod:            1 * time.Minute,
		DetachmentTimeout:          30 * time.Second,
		MaxConcurrentReconciles:    testConfig.MaxConcurrentReconciles,
		MaxConcurrentENICleanup:    testConfig.MaxConcurrentCleanup,
		DefaultDeviceIndex:         1,
		DefaultDeleteOnTermination: true,
	}

	// Create event recorder
	eventRecorder := record.NewFakeRecorder(1000)

	// Create reconciler
	reconciler := &controller.NodeENIReconciler{
		Client:   k8sClient,
		Log:      logger,
		Scheme:   scheme,
		Recorder: eventRecorder,
		AWS:      awsClient,
		Config:   controllerConfig,
		Metrics:  metrics,
	}

	return &PerformanceTestSuite{
		config:     testConfig,
		client:     k8sClient,
		aws:        awsClient,
		reconciler: reconciler,
		metrics:    metrics,
		logger:     logger,
		scheme:     scheme,
	}
}

// TestScaleUp tests gradual scale-up performance
func TestScaleUp(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping scale-up test in short mode")
	}

	config := DefaultPerformanceConfig()
	// Reduce scale for faster testing
	config.NodeCount = 2
	config.ENIPerNode = 2
	config.TestTimeout = 5 * time.Minute

	suite := NewPerformanceTestSuite(config)
	suite.testScaleUp(t)
}

// testScaleUp is the actual implementation
func (pts *PerformanceTestSuite) testScaleUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), pts.config.TestTimeout)
	defer cancel()

	startTime := time.Now()
	pts.logger.Info("Starting scale-up performance test",
		"nodeCount", pts.config.NodeCount,
		"eniPerNode", pts.config.ENIPerNode,
		"totalENIs", pts.config.NodeCount*pts.config.ENIPerNode)

	// Create nodes first
	nodes := pts.createTestNodes(t, ctx)

	// Track performance metrics
	var totalCreationTime time.Duration
	var successCount int
	var errorCount int

	// Create NodeENI resources gradually
	for nodeIdx, node := range nodes {
		for eniIdx := 0; eniIdx < pts.config.ENIPerNode; eniIdx++ {
			eniStartTime := time.Now()

			nodeENI := pts.createNodeENI(fmt.Sprintf("test-eni-%d-%d", nodeIdx, eniIdx), node.Name)

			err := pts.client.Create(ctx, nodeENI)
			require.NoError(t, err, "Failed to create NodeENI")

			// Trigger reconciliation
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeENI.Name,
				},
			}
			_, err = pts.reconciler.Reconcile(ctx, req)
			if err != nil {
				errorCount++
				pts.logger.Error(err, "Reconciliation failed", "nodeENI", nodeENI.Name)
				continue
			}

			eniDuration := time.Since(eniStartTime)
			totalCreationTime += eniDuration
			successCount++

			pts.logger.Info("ENI created successfully",
				"nodeENI", nodeENI.Name,
				"duration", eniDuration,
				"node", node.Name)
		}
	}

	totalDuration := time.Since(startTime)

	// Calculate performance metrics
	avgCreationTime := totalCreationTime / time.Duration(successCount)
	successRate := float64(successCount) / float64(pts.config.NodeCount*pts.config.ENIPerNode) * 100

	// Log results
	pts.logger.Info("Scale-up test completed",
		"totalDuration", totalDuration,
		"avgCreationTime", avgCreationTime,
		"successCount", successCount,
		"errorCount", errorCount,
		"successRate", successRate)

	// Assertions
	assert.Greater(t, successRate, 95.0, "Success rate should be > 95%")
	assert.Less(t, avgCreationTime, 30*time.Second, "Average creation time should be < 30s")
	assert.Less(t, totalDuration, pts.config.TestTimeout, "Total test time should be within timeout")
}

// TestBurstCreation tests burst creation performance
func TestBurstCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping burst creation test in short mode")
	}

	config := DefaultPerformanceConfig()
	// Reduce scale for faster testing
	config.NodeCount = 2
	config.ENIPerNode = 3
	config.TestTimeout = 5 * time.Minute

	suite := NewPerformanceTestSuite(config)
	suite.testBurstCreation(t)
}

// testBurstCreation is the actual implementation
func (pts *PerformanceTestSuite) testBurstCreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), pts.config.TestTimeout)
	defer cancel()

	startTime := time.Now()
	pts.logger.Info("Starting burst creation performance test")

	// Create nodes
	nodes := pts.createTestNodes(t, ctx)

	// Create all NodeENI resources simultaneously
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successCount int
	var errorCount int

	totalENIs := pts.config.NodeCount * pts.config.ENIPerNode

	for nodeIdx, node := range nodes {
		for eniIdx := 0; eniIdx < pts.config.ENIPerNode; eniIdx++ {
			wg.Add(1)
			go func(nodeIdx, eniIdx int, nodeName string) {
				defer wg.Done()

				nodeENI := pts.createNodeENI(fmt.Sprintf("burst-eni-%d-%d", nodeIdx, eniIdx), nodeName)

				err := pts.client.Create(ctx, nodeENI)
				if err != nil {
					mu.Lock()
					errorCount++
					mu.Unlock()
					pts.logger.Error(err, "Failed to create NodeENI", "nodeENI", nodeENI.Name)
					return
				}

				// Trigger reconciliation
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: nodeENI.Name,
					},
				}
				_, err = pts.reconciler.Reconcile(ctx, req)

				mu.Lock()
				if err != nil {
					errorCount++
				} else {
					successCount++
				}
				mu.Unlock()
			}(nodeIdx, eniIdx, node.Name)
		}
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	// Calculate metrics
	successRate := float64(successCount) / float64(totalENIs) * 100
	throughput := float64(successCount) / totalDuration.Seconds()

	pts.logger.Info("Burst creation test completed",
		"totalDuration", totalDuration,
		"successCount", successCount,
		"errorCount", errorCount,
		"successRate", successRate,
		"throughput", throughput)

	// Assertions
	assert.Greater(t, successRate, 90.0, "Success rate should be > 90% for burst creation")
	assert.Greater(t, throughput, 1.0, "Throughput should be > 1 ENI/second")
}

// TestCleanupPerformance tests cleanup performance during scale-down
func TestCleanupPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping cleanup performance test in short mode")
	}

	config := DefaultPerformanceConfig()
	// Reduce scale for faster testing
	config.NodeCount = 2
	config.ENIPerNode = 2
	config.TestTimeout = 5 * time.Minute

	suite := NewPerformanceTestSuite(config)
	suite.testCleanupPerformance(t)
}

// testCleanupPerformance is the actual implementation
func (pts *PerformanceTestSuite) testCleanupPerformance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), pts.config.TestTimeout)
	defer cancel()

	// First create resources to clean up
	nodes := pts.createTestNodes(t, ctx)
	var nodeENIs []*networkingv1alpha1.NodeENI

	// Create NodeENI resources
	for nodeIdx, node := range nodes {
		for eniIdx := 0; eniIdx < pts.config.ENIPerNode; eniIdx++ {
			nodeENI := pts.createNodeENI(fmt.Sprintf("cleanup-eni-%d-%d", nodeIdx, eniIdx), node.Name)
			err := pts.client.Create(ctx, nodeENI)
			require.NoError(t, err, "Failed to create NodeENI for cleanup test")
			nodeENIs = append(nodeENIs, nodeENI)
		}
	}

	// Now test cleanup performance
	startTime := time.Now()
	pts.logger.Info("Starting cleanup performance test", "nodeENICount", len(nodeENIs))

	var wg sync.WaitGroup
	var mu sync.Mutex
	var successCount int
	var errorCount int

	for _, nodeENI := range nodeENIs {
		wg.Add(1)
		go func(eni *networkingv1alpha1.NodeENI) {
			defer wg.Done()

			err := pts.client.Delete(ctx, eni)

			mu.Lock()
			if err != nil {
				errorCount++
			} else {
				successCount++
			}
			mu.Unlock()
		}(nodeENI)
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	// Calculate metrics
	successRate := float64(successCount) / float64(len(nodeENIs)) * 100
	cleanupThroughput := float64(successCount) / totalDuration.Seconds()

	pts.logger.Info("Cleanup performance test completed",
		"totalDuration", totalDuration,
		"successCount", successCount,
		"errorCount", errorCount,
		"successRate", successRate,
		"cleanupThroughput", cleanupThroughput)

	// Assertions
	assert.Greater(t, successRate, 95.0, "Cleanup success rate should be > 95%")
	assert.Greater(t, cleanupThroughput, 2.0, "Cleanup throughput should be > 2 ENI/second")
}

// createTestNodes creates test nodes for performance testing
func (pts *PerformanceTestSuite) createTestNodes(t *testing.T, ctx context.Context) []*corev1.Node {
	var nodes []*corev1.Node

	for i := 0; i < pts.config.NodeCount; i++ {
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("test-node-%d", i),
				Labels: map[string]string{
					"ng": "multi-eni",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: fmt.Sprintf("aws:///us-east-1a/i-test%d", i),
			},
			Status: corev1.NodeStatus{
				Phase: corev1.NodeRunning,
			},
		}

		err := pts.client.Create(ctx, node)
		require.NoError(t, err, "Failed to create test node")

		nodes = append(nodes, node)
	}

	return nodes
}

// createNodeENI creates a NodeENI resource for testing
func (pts *PerformanceTestSuite) createNodeENI(name, nodeName string) *networkingv1alpha1.NodeENI {
	return &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			SubnetID:         pts.config.SubnetID,
			SecurityGroupIDs: pts.config.SecurityGroupIDs,
			DeviceIndex:      1,
		},
	}
}
