package performance

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/controller"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/test"
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

// TestRealAWSPerformance tests performance with real AWS APIs
func TestRealAWSPerformance(t *testing.T) {
	// Skip if no AWS credentials or if running in short mode
	if testing.Short() {
		t.Skip("Skipping real AWS performance test in short mode")
	}

	// Check for AWS credentials
	if os.Getenv("AWS_REGION") == "" {
		t.Skip("Skipping real AWS test - AWS_REGION not set")
	}

	// Check for required test environment variables
	subnetID := os.Getenv("TEST_SUBNET_ID")
	securityGroupID := os.Getenv("TEST_SECURITY_GROUP_ID")

	if subnetID == "" || securityGroupID == "" {
		t.Skip("Skipping real AWS test - TEST_SUBNET_ID or TEST_SECURITY_GROUP_ID not set")
	}

	logger := testr.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	logger.Info("Starting real AWS performance test",
		"subnetID", subnetID,
		"securityGroupID", securityGroupID)

	// Create test environment
	scheme := runtime.NewScheme()
	_ = networkingv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	// Create real AWS client
	awsClient := test.CreateTestEC2Client(t)

	// Create controller configuration
	controllerConfig := &config.ControllerConfig{
		AWSRegion:                  os.Getenv("AWS_REGION"),
		ReconcilePeriod:            1 * time.Minute,
		DetachmentTimeout:          30 * time.Second,
		MaxConcurrentReconciles:    5,
		MaxConcurrentENICleanup:    3,
		DefaultDeviceIndex:         1,
		DefaultDeleteOnTermination: true,
	}

	// Create event recorder
	eventRecorder := record.NewFakeRecorder(100)

	// Create reconciler
	reconciler := &controller.NodeENIReconciler{
		Client:   k8sClient,
		Log:      logger,
		Scheme:   scheme,
		Recorder: eventRecorder,
		AWS:      awsClient,
		Config:   controllerConfig,
	}

	// Create test node
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node-real-aws",
			Labels: map[string]string{
				"ng": "multi-eni",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-test-real",
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodeRunning,
		},
	}

	err := k8sClient.Create(ctx, node)
	require.NoError(t, err, "Failed to create test node")

	// Performance test: Create a small number of NodeENI resources with real AWS
	testENICount := 2 // Keep small to avoid costs and rate limits
	logger.Info("Starting real AWS performance test", "eniCount", testENICount)

	startTime := time.Now()
	var successCount int
	var errorCount int
	var creationTimes []time.Duration

	// Create NodeENI resources
	for i := 0; i < testENICount; i++ {
		eniStartTime := time.Now()

		nodeENI := &networkingv1alpha1.NodeENI{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-real-eni-" + string(rune(i)),
			},
			Spec: networkingv1alpha1.NodeENISpec{
				NodeSelector: map[string]string{
					"kubernetes.io/hostname": node.Name,
				},
				SubnetID:         subnetID,
				SecurityGroupIDs: []string{securityGroupID},
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
		creationTimes = append(creationTimes, eniDuration)

		if err != nil {
			errorCount++
			logger.Error(err, "Reconciliation failed", "nodeENI", nodeENI.Name)
		} else {
			successCount++
			logger.Info("ENI created successfully with real AWS",
				"nodeENI", nodeENI.Name,
				"duration", eniDuration)
		}
	}

	totalDuration := time.Since(startTime)

	// Calculate performance metrics
	var totalCreationTime time.Duration
	for _, duration := range creationTimes {
		totalCreationTime += duration
	}

	avgCreationTime := totalCreationTime / time.Duration(len(creationTimes))
	successRate := float64(successCount) / float64(testENICount) * 100
	throughput := float64(successCount) / totalDuration.Seconds()

	logger.Info("Real AWS performance test completed",
		"totalDuration", totalDuration,
		"avgCreationTime", avgCreationTime,
		"successCount", successCount,
		"errorCount", errorCount,
		"successRate", successRate,
		"throughput", throughput)

	// Assertions for real AWS performance
	assert.Greater(t, successRate, 80.0, "Success rate should be > 80% for real AWS")
	assert.Less(t, avgCreationTime, 60*time.Second, "Average creation time should be < 60s for real AWS")
	assert.Greater(t, throughput, 0.01, "Throughput should be > 0.01 ENI/second for real AWS")

	// Cleanup: Delete the NodeENI resources
	logger.Info("Cleaning up test resources")
	for i := 0; i < testENICount; i++ {
		nodeENI := &networkingv1alpha1.NodeENI{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-real-eni-" + string(rune(i)),
			},
		}

		err := k8sClient.Delete(ctx, nodeENI)
		if err != nil {
			logger.Error(err, "Failed to delete NodeENI", "name", nodeENI.Name)
		}
	}

	logger.Info("Real AWS performance test cleanup completed")
}

// TestAWSAPILatency tests AWS API call latency
func TestAWSAPILatency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping AWS API latency test in short mode")
	}

	// Check for AWS credentials
	if os.Getenv("AWS_REGION") == "" {
		t.Skip("Skipping AWS API latency test - AWS_REGION not set")
	}

	logger := testr.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create real AWS client
	awsClient := test.CreateTestEC2Client(t)

	// Test AWS API call latency
	testCalls := 5
	var latencies []time.Duration

	logger.Info("Starting AWS API latency test", "testCalls", testCalls)

	for i := 0; i < testCalls; i++ {
		startTime := time.Now()

		// Make a simple AWS API call (get subnet CIDR by ID)
		// Use a known subnet ID or create a dummy one for testing
		testSubnetID := os.Getenv("TEST_SUBNET_ID")
		if testSubnetID == "" {
			testSubnetID = "subnet-12345" // Dummy subnet ID for latency testing
		}
		_, err := awsClient.GetSubnetCIDRByID(ctx, testSubnetID)

		latency := time.Since(startTime)
		latencies = append(latencies, latency)

		if err != nil {
			logger.Error(err, "AWS API call failed", "call", i)
		} else {
			logger.Info("AWS API call completed", "call", i, "latency", latency)
		}
	}

	// Calculate latency statistics
	var totalLatency time.Duration
	var maxLatency time.Duration
	var minLatency time.Duration = time.Hour // Initialize to a large value

	for _, latency := range latencies {
		totalLatency += latency
		if latency > maxLatency {
			maxLatency = latency
		}
		if latency < minLatency {
			minLatency = latency
		}
	}

	avgLatency := totalLatency / time.Duration(len(latencies))

	logger.Info("AWS API latency test completed",
		"avgLatency", avgLatency,
		"minLatency", minLatency,
		"maxLatency", maxLatency,
		"totalCalls", len(latencies))

	// Assertions for AWS API latency
	assert.Less(t, avgLatency, 5*time.Second, "Average AWS API latency should be < 5s")
	assert.Less(t, maxLatency, 10*time.Second, "Max AWS API latency should be < 10s")
}
