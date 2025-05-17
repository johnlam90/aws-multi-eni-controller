package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
)

// updateAttachmentDPDKStatus updates the DPDK status of an ENI attachment
// This function communicates with the NodeENI controller to update the attachment status
func updateAttachmentDPDKStatus(attachment networkingv1alpha1.ENIAttachment, nodeENIName string, dpdkBound bool) {
	// Get the Kubernetes client
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Error getting Kubernetes config: %v", err)
		return
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		log.Printf("Error creating Kubernetes client: %v", err)
		return
	}

	// Use retry with backoff to handle transient errors
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the NodeENI resource
		nodeENIRaw, err := clientset.CoreV1().RESTClient().
			Get().
			AbsPath(fmt.Sprintf("/apis/networking.k8s.aws/v1alpha1/nodeenis/%s", nodeENIName)).
			Do(context.Background()).
			Raw()

		if err != nil {
			return fmt.Errorf("error getting NodeENI resource: %v", err)
		}

		// Parse the NodeENI resource
		var nodeENI networkingv1alpha1.NodeENI
		if err := json.Unmarshal(nodeENIRaw, &nodeENI); err != nil {
			return fmt.Errorf("error parsing NodeENI resource: %v", err)
		}

		// Find the attachment in the NodeENI status
		updated := false
		for i, att := range nodeENI.Status.Attachments {
			if att.ENIID == attachment.ENIID && att.NodeID == attachment.NodeID {
				// Update the DPDK status
				nodeENI.Status.Attachments[i].DPDKBound = dpdkBound
				updated = true
				break
			}
		}

		if !updated {
			log.Printf("Attachment not found in NodeENI status, cannot update DPDK status")
			return nil
		}

		// Update the NodeENI resource
		nodeENIJSON, err := json.Marshal(nodeENI)
		if err != nil {
			return fmt.Errorf("error marshaling NodeENI resource: %v", err)
		}

		_, err = clientset.CoreV1().RESTClient().
			Put().
			AbsPath(fmt.Sprintf("/apis/networking.k8s.aws/v1alpha1/nodeenis/%s/status", nodeENIName)).
			Body(nodeENIJSON).
			Do(context.Background()).
			Raw()

		if err != nil {
			return fmt.Errorf("error updating NodeENI resource: %v", err)
		}

		log.Printf("Successfully updated DPDK status for attachment %s to %v", attachment.ENIID, dpdkBound)
		return nil
	})

	if err != nil {
		log.Printf("Failed to update DPDK status after retries: %v", err)
	}
}
