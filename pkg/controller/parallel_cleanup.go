package controller

import (
	"context"
	"sync"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
)

// cleanupENIAttachmentsInParallel cleans up ENI attachments in parallel
// Returns true if all cleanup operations succeeded, false otherwise
func (r *NodeENIReconciler) cleanupENIAttachmentsInParallel(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	log.Info("Cleaning up ENI attachments in parallel", "count", len(nodeENI.Status.Attachments))

	// If there are no attachments, return success
	if len(nodeENI.Status.Attachments) == 0 {
		return true
	}

	// If there's only one attachment, use the sequential method
	if len(nodeENI.Status.Attachments) == 1 {
		return r.cleanupENIAttachment(ctx, nodeENI, nodeENI.Status.Attachments[0])
	}

	// Determine the maximum number of concurrent cleanup operations
	maxConcurrent := r.Config.MaxConcurrentENICleanup
	if maxConcurrent <= 0 {
		maxConcurrent = 3 // Default to 3 if not configured
	}

	// Create a semaphore to limit concurrency
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	// Create a channel to collect results
	resultChan := make(chan bool, len(nodeENI.Status.Attachments))

	// Process each attachment in parallel
	for _, attachment := range nodeENI.Status.Attachments {
		wg.Add(1)
		go func(att networkingv1alpha1.ENIAttachment) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }() // Release semaphore when done

			// Clean up the attachment
			success := r.cleanupENIAttachment(ctx, nodeENI, att)
			resultChan <- success
		}(attachment)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(resultChan)

	// Check if any cleanup operations failed
	allSucceeded := true
	for success := range resultChan {
		if !success {
			allSucceeded = false
		}
	}

	if allSucceeded {
		log.Info("All ENI cleanup operations succeeded")
	} else {
		log.Info("Some ENI cleanup operations failed")
	}

	return allSucceeded
}

// cleanupSpecificENIAttachmentsInParallel cleans up specific ENI attachments in parallel
// Returns true if all cleanup operations succeeded, false otherwise
func (r *NodeENIReconciler) cleanupSpecificENIAttachmentsInParallel(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachments []networkingv1alpha1.ENIAttachment) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	log.Info("Cleaning up specific ENI attachments in parallel", "count", len(attachments))

	// If there are no attachments, return success
	if len(attachments) == 0 {
		return true
	}

	// If there's only one attachment, use the sequential method
	if len(attachments) == 1 {
		return r.cleanupENIAttachment(ctx, nodeENI, attachments[0])
	}

	// Determine the maximum number of concurrent cleanup operations
	maxConcurrent := r.Config.MaxConcurrentENICleanup
	if maxConcurrent <= 0 {
		maxConcurrent = 3 // Default to 3 if not configured
	}

	// Create a semaphore to limit concurrency
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	// Create a channel to collect results
	resultChan := make(chan bool, len(attachments))

	// Process each attachment in parallel
	for _, attachment := range attachments {
		wg.Add(1)
		go func(att networkingv1alpha1.ENIAttachment) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }() // Release semaphore when done

			// Clean up the attachment
			success := r.cleanupENIAttachment(ctx, nodeENI, att)
			resultChan <- success
		}(attachment)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(resultChan)

	// Check if any cleanup operations failed
	allSucceeded := true
	for success := range resultChan {
		if !success {
			allSucceeded = false
		}
	}

	if allSucceeded {
		log.Info("All specific ENI cleanup operations succeeded")
	} else {
		log.Info("Some specific ENI cleanup operations failed")
	}

	return allSucceeded
}
