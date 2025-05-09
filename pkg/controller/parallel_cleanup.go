package controller

import (
	"context"
	"sync"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// handleDeletion handles the deletion of a NodeENI resource
func (r *NodeENIReconciler) handleDeletion(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI) (ctrl.Result, error) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	// If our finalizer is present, clean up resources
	if controllerutil.ContainsFinalizer(nodeENI, NodeENIFinalizer) {
		log.Info("Cleaning up resources for NodeENI being deleted", "name", nodeENI.Name)

		// Clean up all ENI attachments in parallel
		cleanupSucceeded := r.cleanupENIAttachmentsInParallel(ctx, nodeENI)

		// Only remove the finalizer if all cleanup operations succeeded
		if !cleanupSucceeded {
			log.Info("Some cleanup operations failed, will retry later", "name", nodeENI.Name)
			// Requeue with a backoff to retry the cleanup
			return ctrl.Result{RequeueAfter: r.Config.DetachmentTimeout}, nil
		}

		// All cleanup operations succeeded, remove the finalizer
		log.Info("All cleanup operations succeeded, removing finalizer", "name", nodeENI.Name)
		controllerutil.RemoveFinalizer(nodeENI, NodeENIFinalizer)
		if err := r.Client.Update(ctx, nodeENI); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Stop reconciliation as the item is being deleted
	return ctrl.Result{}, nil
}
