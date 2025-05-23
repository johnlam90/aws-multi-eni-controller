//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"os"
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
func setupE2ETest(t *testing.T) (string, string, kubernetes.Interface, client.Client, *corev1.Node, string, string) {
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
	testNode, nodeName := findWorkerNode(t, clientset)

	// Add a test label to the node
	testLabel := "e2e-test-nodeeni"
	testNode.Labels[testLabel] = "true"
	_, err := clientset.CoreV1().Nodes().Update(context.Background(), testNode, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update node labels: %v", err)
	}

	return subnetID, securityGroupID, clientset, runtimeClient, testNode, nodeName, testLabel
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
			DeviceIndex:         2,
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
	err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		nodeENI := &networkingv1alpha1.NodeENI{}
		err := runtimeClient.Get(context.Background(), client.ObjectKey{Name: "e2e-test-nodeeni"}, nodeENI)
		if err != nil {
			return false, err
		}

		// Check if the attachment for our node is gone
		for _, att := range nodeENI.Status.Attachments {
			if att.NodeID == nodeName {
				return false, nil
			}
		}

		return true, nil
	})

	if err != nil {
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

	if attachment.DeviceIndex != 2 {
		t.Errorf("Expected device index 2, got %d", attachment.DeviceIndex)
	}

	if attachment.SubnetCIDR == "" {
		t.Error("Expected non-empty subnet CIDR")
	}
}

// TestE2E_NodeENIController tests the NodeENI controller end-to-end
func TestE2E_NodeENIController(t *testing.T) {
	// Set up the test environment
	subnetID, securityGroupID, clientset, runtimeClient, testNode, nodeName, testLabel := setupE2ETest(t)

	// Clean up the test label after the test
	defer func() {
		node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			t.Logf("Failed to get node for cleanup: %v", err)
			return
		}
		delete(node.Labels, testLabel)
		_, err = clientset.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
		if err != nil {
			t.Logf("Failed to remove test label from node: %v", err)
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
	delete(testNode.Labels, testLabel)
	_, err := clientset.CoreV1().Nodes().Update(context.Background(), testNode, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to remove test label from node: %v", err)
	}

	// Wait for the attachment to be removed
	waitForNodeENIDetachment(t, runtimeClient, nodeName)

	t.Log("Successfully verified NodeENI controller end-to-end")
}
