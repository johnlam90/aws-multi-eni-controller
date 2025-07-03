//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/test"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// setupE2ETest sets up the test environment for E2E testing
func setupE2ETest(t *testing.T) (string, string, kubernetes.Interface, client.Client, string, string) {
	// Skip if no AWS credentials or Kubernetes cluster
	test.SkipIfNoAWSCredentials(t)
	test.SkipIfNoKubernetesCluster(t)

	// Get test subnet ID from environment variable or skip
	subnetID := os.Getenv("TEST_SUBNET_ID")
	if subnetID == "" {
		t.Skip("Skipping test that requires TEST_SUBNET_ID environment variable")
	}

	// Get security group ID from environment variable or skip
	securityGroupID := os.Getenv("TEST_SECURITY_GROUP_ID")
	if securityGroupID == "" {
		t.Skip("Skipping test that requires TEST_SECURITY_GROUP_ID environment variable")
	}

	// Create Kubernetes clients
	clientset, runtimeClient := createK8sClients(t)

	// Find a worker node to test with
	_, nodeName := findWorkerNode(t, clientset)

	// Add a test label to the node
	testLabel := "e2e-test-nodeeni"
	err := addNodeLabelWithRetry(clientset, nodeName, testLabel, "true", 3)
	if err != nil {
		t.Fatalf("Failed to add test label to node: %v", err)
	}

	return subnetID, securityGroupID, clientset, runtimeClient, nodeName, testLabel
}

// createK8sClients creates Kubernetes clients for testing
func createK8sClients(t *testing.T) (kubernetes.Interface, client.Client) {
	// Create Kubernetes client
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		t.Fatalf("Failed to create Kubernetes config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create controller-runtime client
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = networkingv1alpha1.AddToScheme(scheme)

	runtimeClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("Failed to create controller-runtime client: %v", err)
	}

	return clientset, runtimeClient
}

// findWorkerNode finds a worker node in the cluster
func findWorkerNode(t *testing.T, clientset kubernetes.Interface) (*corev1.Node, string) {
	nodes, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list nodes: %v", err)
	}

	if len(nodes.Items) == 0 {
		t.Skip("No nodes found in the cluster")
	}

	// Find a worker node (not a control plane node)
	var testNode *corev1.Node
	for _, node := range nodes.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; !isControlPlane {
			testNode = &node
			break
		}
	}

	if testNode == nil {
		t.Skip("No worker nodes found in the cluster")
	}

	// Get the node name
	nodeName := testNode.Name
	t.Logf("Using node %s for testing", nodeName)

	return testNode, nodeName
}

// createNodeENI creates a NodeENI resource for testing
func createNodeENI(t *testing.T, runtimeClient client.Client, subnetID, securityGroupID, testLabel string) *networkingv1alpha1.NodeENI {
	nodeENI := &networkingv1alpha1.NodeENI{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.aws/v1alpha1",
			Kind:       "NodeENI",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "e2e-test-nodeeni",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			NodeSelector: map[string]string{
				testLabel: "true",
			},
			SubnetID:            subnetID,
			SecurityGroupIDs:    []string{securityGroupID},
			DeviceIndex:         4,
			DeleteOnTermination: true,
			Description:         "E2E test NodeENI",
		},
	}

	// Create the NodeENI
	err := runtimeClient.Create(context.Background(), nodeENI)
	if err != nil {
		t.Fatalf("Failed to create NodeENI: %v", err)
	}

	return nodeENI
}

// waitForNodeENIAttachment waits for the NodeENI to be attached to the node
func waitForNodeENIAttachment(t *testing.T, runtimeClient client.Client, nodeName string) *networkingv1alpha1.NodeENI {
	nodeENI := &networkingv1alpha1.NodeENI{}

	err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		err := runtimeClient.Get(context.Background(), client.ObjectKey{Name: "e2e-test-nodeeni"}, nodeENI)
		if err != nil {
			return false, err
		}

		// Check if the NodeENI has an attachment for our node
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.NodeID == nodeName {
				t.Logf("Found attachment for node %s: ENI %s", nodeName, attachment.ENIID)
				return true, nil
			}
		}

		return false, nil
	})

	if err != nil {
		t.Fatalf("Failed to wait for NodeENI to be reconciled: %v", err)
	}

	return nodeENI
}

// waitForNodeENIDetachment waits for the NodeENI to be detached from the node
func waitForNodeENIDetachment(t *testing.T, runtimeClient client.Client, nodeName string) {
	t.Logf("Starting to wait for NodeENI detachment for node %s", nodeName)

	err := wait.PollImmediate(5*time.Second, 5*time.Minute, func() (bool, error) {
		nodeENI := &networkingv1alpha1.NodeENI{}
		err := runtimeClient.Get(context.Background(), client.ObjectKey{Name: "e2e-test-nodeeni"}, nodeENI)
		if err != nil {
			t.Logf("Failed to get NodeENI: %v", err)
			return false, err
		}

		t.Logf("NodeENI status: %d total attachments", len(nodeENI.Status.Attachments))

		// Log all current attachments for debugging
		for i, att := range nodeENI.Status.Attachments {
			t.Logf("Attachment %d: NodeID=%s, ENIID=%s, Status=%s", i, att.NodeID, att.ENIID, att.Status)
		}

		// Check if the attachment for our node is gone
		for _, att := range nodeENI.Status.Attachments {
			if att.NodeID == nodeName {
				t.Logf("Still waiting for attachment to be removed: ENI %s on node %s (Status: %s)", att.ENIID, att.NodeID, att.Status)
				return false, nil
			}
		}

		t.Logf("Attachment successfully removed from NodeENI status")
		return true, nil
	})

	if err != nil {
		// Get final status for debugging
		nodeENI := &networkingv1alpha1.NodeENI{}
		if getErr := runtimeClient.Get(context.Background(), client.ObjectKey{Name: "e2e-test-nodeeni"}, nodeENI); getErr == nil {
			t.Logf("Final NodeENI status before failure: %d attachments", len(nodeENI.Status.Attachments))
			for i, att := range nodeENI.Status.Attachments {
				t.Logf("Final attachment %d: NodeID=%s, ENIID=%s, Status=%s", i, att.NodeID, att.ENIID, att.Status)
			}
		}
		t.Fatalf("Failed to wait for NodeENI attachment to be removed: %v", err)
	}
}

// verifyAttachmentDetails verifies the details of the NodeENI attachment
func verifyAttachmentDetails(t *testing.T, nodeENI *networkingv1alpha1.NodeENI, nodeName, subnetID string) {
	var attachment *networkingv1alpha1.ENIAttachment
	for i := range nodeENI.Status.Attachments {
		if nodeENI.Status.Attachments[i].NodeID == nodeName {
			attachment = &nodeENI.Status.Attachments[i]
			break
		}
	}

	if attachment == nil {
		t.Fatal("Attachment not found after successful wait")
	}

	if attachment.SubnetID != subnetID {
		t.Errorf("Expected subnet ID %s, got %s", subnetID, attachment.SubnetID)
	}

	if attachment.DeviceIndex != 4 {
		t.Errorf("Expected device index 4, got %d", attachment.DeviceIndex)
	}

	if attachment.SubnetCIDR == "" {
		t.Error("Expected non-empty subnet CIDR")
	}
}

// TestE2E_NodeENIController tests the NodeENI controller end-to-end
func TestE2E_NodeENIController(t *testing.T) {
	// Set up the test environment
	subnetID, securityGroupID, clientset, runtimeClient, nodeName, testLabel := setupE2ETest(t)

	// Clean up the test label after the test
	defer func() {
		err := removeNodeLabelWithRetry(clientset, nodeName, testLabel, 3)
		if err != nil {
			t.Logf("Failed to remove test label from node after retries: %v", err)
		}
	}()

	// Create a NodeENI resource
	nodeENI := createNodeENI(t, runtimeClient, subnetID, securityGroupID, testLabel)

	// Clean up the NodeENI after the test
	defer func() {
		err := runtimeClient.Delete(context.Background(), nodeENI)
		if err != nil {
			t.Logf("Failed to delete NodeENI: %v", err)
		}
	}()

	// Wait for the NodeENI to be reconciled
	nodeENI = waitForNodeENIAttachment(t, runtimeClient, nodeName)

	// Verify the attachment details
	verifyAttachmentDetails(t, nodeENI, nodeName, subnetID)

	// Test cleanup by removing the node label
	err := removeNodeLabelWithRetry(clientset, nodeName, testLabel, 3)
	if err != nil {
		t.Fatalf("Failed to remove test label from node: %v", err)
	}

	// Give the controller a moment to detect the node label change
	t.Log("Waiting for controller to detect node label removal...")
	time.Sleep(10 * time.Second)

	// Trigger reconciliation by updating the NodeENI resource (add an annotation)
	err = triggerNodeENIReconciliation(runtimeClient, nodeENI.Name)
	if err != nil {
		t.Logf("Failed to trigger NodeENI reconciliation: %v", err)
	}

	// Wait for the attachment to be removed
	waitForNodeENIDetachment(t, runtimeClient, nodeName)

	t.Log("Successfully verified NodeENI controller end-to-end")
}

// removeNodeLabelWithRetry removes a label from a node with retry logic to handle resource version conflicts
func removeNodeLabelWithRetry(clientset kubernetes.Interface, nodeName, labelKey string, maxRetries int) error {
	for i := 0; i < maxRetries; i++ {
		// Get the latest version of the node
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %v", nodeName, err)
		}

		// Check if the label exists
		if _, exists := node.Labels[labelKey]; !exists {
			// Label doesn't exist, nothing to do
			return nil
		}

		// Remove the label
		delete(node.Labels, labelKey)

		// Try to update the node
		_, err = clientset.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
		if err == nil {
			// Success
			return nil
		}

		// Check if it's a conflict error
		if strings.Contains(err.Error(), "the object has been modified") {
			// Retry after a short delay
			time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
			continue
		}

		// Non-conflict error, return immediately
		return fmt.Errorf("failed to update node %s: %v", nodeName, err)
	}

	return fmt.Errorf("failed to remove label %s from node %s after %d retries", labelKey, nodeName, maxRetries)
}

// addNodeLabelWithRetry adds a label to a node with retry logic to handle resource version conflicts
func addNodeLabelWithRetry(clientset kubernetes.Interface, nodeName, labelKey, labelValue string, maxRetries int) error {
	for i := 0; i < maxRetries; i++ {
		// Get the latest version of the node
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %v", nodeName, err)
		}

		// Check if the label already exists with the correct value
		if existingValue, exists := node.Labels[labelKey]; exists && existingValue == labelValue {
			// Label already exists with correct value, nothing to do
			return nil
		}

		// Add/update the label
		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		node.Labels[labelKey] = labelValue

		// Try to update the node
		_, err = clientset.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
		if err == nil {
			// Success
			return nil
		}

		// Check if it's a conflict error
		if strings.Contains(err.Error(), "the object has been modified") {
			// Retry after a short delay
			time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
			continue
		}

		// Non-conflict error, return immediately
		return fmt.Errorf("failed to update node %s: %v", nodeName, err)
	}

	return fmt.Errorf("failed to add label %s to node %s after %d retries", labelKey, nodeName, maxRetries)
}

// triggerNodeENIReconciliation triggers a reconciliation of the NodeENI resource by updating it
func triggerNodeENIReconciliation(runtimeClient client.Client, nodeENIName string) error {
	nodeENI := &networkingv1alpha1.NodeENI{}
	err := runtimeClient.Get(context.Background(), client.ObjectKey{Name: nodeENIName}, nodeENI)
	if err != nil {
		return fmt.Errorf("failed to get NodeENI %s: %v", nodeENIName, err)
	}

	// Add or update an annotation to trigger reconciliation
	if nodeENI.Annotations == nil {
		nodeENI.Annotations = make(map[string]string)
	}
	nodeENI.Annotations["test.e2e/reconcile-trigger"] = time.Now().Format(time.RFC3339)

	err = runtimeClient.Update(context.Background(), nodeENI)
	if err != nil {
		return fmt.Errorf("failed to update NodeENI %s: %v", nodeENIName, err)
	}

	return nil
}
