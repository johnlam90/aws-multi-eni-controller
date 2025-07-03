package controller

import (
	"context"
	"sync"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
)

// handleSingleAttachment handles the case of a single attachment
func (r *NodeENIReconciler) handleSingleAttachment(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachments []networkingv1alpha1.ENIAttachment) bool {
	// If there are no attachments, return success
	if len(attachments) == 0 {
		return true
	}

	// If there's only one attachment, use the sequential method
	if len(attachments) == 1 {
		return r.cleanupENIAttachment(ctx, nodeENI, attachments[0])
	}

	return false
}

// calculateWorkerCount determines the optimal number of workers
func (r *NodeENIReconciler) calculateWorkerCount(attachmentCount int) int {
	// Determine the maximum number of concurrent cleanup operations
	maxConcurrent := r.Config.MaxConcurrentENICleanup
	if maxConcurrent <= 0 {
		maxConcurrent = 3 // Default to 3 if not configured
	}

	// Dynamic worker scaling based on workload
	workerCount := min(maxConcurrent, max(1, attachmentCount/2))
	if workerCount > attachmentCount {
		workerCount = attachmentCount
	}

	return workerCount
}

// startWorkers starts the worker goroutines for parallel processing
func (r *NodeENIReconciler) startWorkers(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	workChan <-chan networkingv1alpha1.ENIAttachment,
	resultChan chan<- bool,
	workerCount int,
	wg *sync.WaitGroup,
) {
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.workerFunc(ctx, nodeENI, workChan, resultChan)
		}()
	}
}

// workerFunc is the function executed by each worker goroutine
func (r *NodeENIReconciler) workerFunc(
	ctx context.Context,
	nodeENI *networkingv1alpha1.NodeENI,
	workChan <-chan networkingv1alpha1.ENIAttachment,
	resultChan chan<- bool,
) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name, "worker", "cleanup")

	for {
		select {
		case att, ok := <-workChan:
			if !ok {
				// Channel closed, exit worker
				return
			}

			// Determine the appropriate coordination strategy
			var success bool
			if r.shouldUseNodeLevelCoordination(nodeENI, att) {
				log.V(1).Info("Using node-level coordination for ENI cleanup",
					"eniID", att.ENIID,
					"nodeID", att.NodeID,
					"reason", "DPDK or SR-IOV enabled")
				success = r.cleanupENIAttachmentWithNodeCoordination(ctx, nodeENI, att)
			} else {
				log.V(1).Info("Using granular coordination for ENI cleanup",
					"eniID", att.ENIID,
					"nodeID", att.NodeID,
					"reason", "standard ENI")
				success = r.cleanupENIAttachmentCoordinated(ctx, nodeENI, att)
			}

			select {
			case resultChan <- success:
			case <-ctx.Done():
				// Context cancelled, exit worker
				return
			}
		case <-ctx.Done():
			// Context cancelled, exit worker
			return
		}
	}
}

// sendWorkToWorkers distributes work to the worker goroutines
func (r *NodeENIReconciler) sendWorkToWorkers(
	ctx context.Context,
	attachments []networkingv1alpha1.ENIAttachment,
	workChan chan<- networkingv1alpha1.ENIAttachment,
	wg *sync.WaitGroup,
) bool {
	// Send work to workers
	for _, att := range attachments {
		select {
		case workChan <- att:
		case <-ctx.Done():
			// Context cancelled, stop sending work
			close(workChan)
			wg.Wait()
			return false
		}
	}
	close(workChan)
	return true
}

// collectResults collects and processes the results from worker goroutines
func (r *NodeENIReconciler) collectResults(
	resultChan <-chan bool,
	nodeENI *networkingv1alpha1.NodeENI,
	logPrefix string,
) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

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

// parallelCleanupENIs is a helper function that cleans up ENI attachments in parallel
// Returns true if all cleanup operations succeeded, false otherwise
func (r *NodeENIReconciler) parallelCleanupENIs(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachments []networkingv1alpha1.ENIAttachment, logPrefix string) bool {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)
	log.Info(logPrefix+" ENI attachments in parallel", "count", len(attachments))

	// Analyze coordination requirements
	r.analyzeCoordinationRequirements(nodeENI, attachments)

	// Handle the case of no attachments or a single attachment
	singleResult := r.handleSingleAttachment(ctx, nodeENI, attachments)
	if len(attachments) <= 1 {
		return singleResult
	}

	// Calculate the optimal number of workers
	workerCount := r.calculateWorkerCount(len(attachments))

	// Create channels for work distribution and result collection
	workChan := make(chan networkingv1alpha1.ENIAttachment, len(attachments))
	resultChan := make(chan bool, len(attachments))
	var wg sync.WaitGroup

	// Start the worker goroutines
	r.startWorkers(ctx, nodeENI, workChan, resultChan, workerCount, &wg)

	// Send work to the workers
	if !r.sendWorkToWorkers(ctx, attachments, workChan, &wg) {
		return false
	}

	// Wait for all workers to complete
	wg.Wait()
	close(resultChan)

	// Collect and process the results
	return r.collectResults(resultChan, nodeENI, logPrefix)
}

// analyzeCoordinationRequirements analyzes and logs the coordination strategy for each attachment
func (r *NodeENIReconciler) analyzeCoordinationRequirements(nodeENI *networkingv1alpha1.NodeENI, attachments []networkingv1alpha1.ENIAttachment) {
	log := r.Log.WithValues("nodeeni", nodeENI.Name)

	granularCount := 0
	nodeLevelCount := 0
	nodeGroups := make(map[string][]string) // nodeID -> list of ENI IDs

	for _, att := range attachments {
		if r.shouldUseNodeLevelCoordination(nodeENI, att) {
			nodeLevelCount++
			log.V(1).Info("ENI requires node-level coordination",
				"eniID", att.ENIID,
				"nodeID", att.NodeID,
				"enableDPDK", nodeENI.Spec.EnableDPDK,
				"dpdkPCIAddress", nodeENI.Spec.DPDKPCIAddress,
				"dpdkResourceName", nodeENI.Spec.DPDKResourceName)
		} else {
			granularCount++
			log.V(1).Info("ENI uses granular coordination",
				"eniID", att.ENIID,
				"nodeID", att.NodeID)
		}

		// Group by node for analysis
		nodeGroups[att.NodeID] = append(nodeGroups[att.NodeID], att.ENIID)
	}

	log.Info("Coordination strategy analysis",
		"totalAttachments", len(attachments),
		"granularCoordination", granularCount,
		"nodeLevelCoordination", nodeLevelCount,
		"affectedNodes", len(nodeGroups))

	// Log node-level grouping for multi-subnet scenarios
	for nodeID, eniIDs := range nodeGroups {
		if len(eniIDs) > 1 {
			log.Info("Multi-ENI node detected",
				"nodeID", nodeID,
				"eniCount", len(eniIDs),
				"eniIDs", eniIDs)
		}
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
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
