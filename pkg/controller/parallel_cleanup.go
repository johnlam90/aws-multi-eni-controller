package controller

import (
	"context"
	"sync"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
)

// parallelCleanupENIs is a helper function that cleans up ENI attachments in parallel
// Returns true if all cleanup operations succeeded, false otherwise
func (r *NodeENIReconciler) parallelCleanupENIs(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachments []networkingv1alpha1.ENIAttachment, logPrefix string) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	log.Info(logPrefix+" ENI attachments in parallel", "count", len(attachments))

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

	// Use a worker pool pattern for better resource management
	// Create a channel for work items
	workChan := make(chan networkingv1alpha1.ENIAttachment, len(attachments))

	// Create a channel to collect results
	resultChan := make(chan bool, len(attachments))

	// Create a wait group to wait for all workers to finish
	var wg sync.WaitGroup

	// Start workers
	workerCount := min(maxConcurrent, len(attachments))
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for att := range workChan {
				// Clean up the attachment
				success := r.cleanupENIAttachment(ctx, nodeENI, att)
				resultChan <- success
			}
		}()
	}

	// Send work to workers
	for _, att := range attachments {
		workChan <- att
	}
	close(workChan)

	// Wait for all workers to complete
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
		log.Info("All " + logPrefix + " ENI cleanup operations succeeded")
	} else {
		log.Info("Some " + logPrefix + " ENI cleanup operations failed")
	}

	return allSucceeded
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// cleanupENIAttachmentsInParallel cleans up all ENI attachments in parallel
// Returns true if all cleanup operations succeeded, false otherwise
func (r *NodeENIReconciler) cleanupENIAttachmentsInParallel(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) bool {
	return r.parallelCleanupENIs(ctx, nodeENI, nodeENI.Status.Attachments, "Cleaning up all")
}

// cleanupSpecificENIAttachmentsInParallel cleans up specific ENI attachments in parallel
// Returns true if all cleanup operations succeeded, false otherwise
func (r *NodeENIReconciler) cleanupSpecificENIAttachmentsInParallel(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachments []networkingv1alpha1.ENIAttachment) bool {
	return r.parallelCleanupENIs(ctx, nodeENI, attachments, "Cleaning up specific")
}
