// Package hugepages provides comprehensive huge pages management functionality
// for the NodeSystemConfig controller. It handles runtime allocation, validation,
// persistence, and status reporting for huge pages configuration.
package hugepages

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Allocation strategies
	AllocationStrategyRuntime    = "runtime"
	AllocationStrategyPersistent = "persistent"
	AllocationStrategyBestEffort = "best-effort"

	// Allocation statuses
	AllocationStatusSuccess           = "Success"
	AllocationStatusPartialSuccess    = "PartialSuccess"
	AllocationStatusFailed           = "Failed"
	AllocationStatusPending          = "Pending"
	AllocationStatusInsufficientMemory = "InsufficientMemory"

	// Persistence statuses
	PersistenceStatusPersistent = "Persistent"
	PersistenceStatusRuntime    = "Runtime"
	PersistenceStatusUnknown    = "Unknown"

	// Allocation methods
	AllocationMethodRuntime     = "Runtime"
	AllocationMethodPersistent  = "Persistent"
	AllocationMethodPreAllocated = "PreAllocated"

	// Default values
	DefaultAllocationStrategy = AllocationStrategyRuntime
	DefaultMaxRetries        = 3
	DefaultRetryInterval     = "30s"
	DefaultBackoffMultiplier = 2.0
	DefaultPriority         = 50
)

// Manager handles huge pages allocation and management
type Manager struct {
	log logr.Logger
}

// NewManager creates a new huge pages manager
func NewManager(log logr.Logger) *Manager {
	return &Manager{
		log: log.WithName("hugepages-manager"),
	}
}

// AllocateHugePages allocates huge pages according to the configuration
func (m *Manager) AllocateHugePages(ctx context.Context, node corev1.Node, config *networkingv1alpha1.HugePagesConfig) (*networkingv1alpha1.HugePagesStatus, error) {
	log := m.log.WithValues("node", node.Name)
	log.Info("Starting huge pages allocation")

	// Initialize status
	status := &networkingv1alpha1.HugePagesStatus{
		Configured:            false,
		AllocationStrategy:    m.getAllocationStrategy(config),
		AllocationStatus:      AllocationStatusPending,
		LastAllocationAttempt: metav1.Now(),
		RetryCount:           0,
		PersistenceStatus:    PersistenceStatusUnknown,
		Pages:               []networkingv1alpha1.HugePageStatus{},
		Errors:              []string{},
	}

	// Set mount path
	mountPath := config.MountPath
	if mountPath == "" {
		mountPath = "/dev/hugepages"
	}
	status.MountPath = mountPath

	// Pre-allocation checks if enabled
	if m.shouldPerformPreAllocationCheck(config) {
		if err := m.performPreAllocationCheck(ctx, node, config, status); err != nil {
			log.Error(err, "Pre-allocation check failed")
			status.AllocationStatus = AllocationStatusInsufficientMemory
			status.Errors = append(status.Errors, fmt.Sprintf("Pre-allocation check failed: %v", err))
			return status, err
		}
	}

	// Sort pages by priority for allocation
	sortedPages := m.sortPagesByPriority(config.Pages)

	// Allocate each page size
	var totalAllocated, totalAvailable int32
	allocationSuccess := true
	partialSuccess := false

	for _, pageSpec := range sortedPages {
		pageStatus, err := m.allocatePageSize(ctx, node, pageSpec, config, status)
		if err != nil {
			log.Error(err, "Failed to allocate page size", "size", pageSpec.Size)
			allocationSuccess = false
			status.Errors = append(status.Errors, fmt.Sprintf("Failed to allocate %s pages: %v", pageSpec.Size, err))
		} else if pageStatus.Allocated < pageStatus.Requested {
			partialSuccess = true
		}

		status.Pages = append(status.Pages, *pageStatus)
		totalAllocated += pageStatus.Allocated
		totalAvailable += pageStatus.Available
	}

	// Update overall status
	status.TotalAllocated = totalAllocated
	status.TotalAvailable = totalAvailable
	status.Configured = totalAllocated > 0

	if allocationSuccess && !partialSuccess {
		status.AllocationStatus = AllocationStatusSuccess
	} else if totalAllocated > 0 {
		status.AllocationStatus = AllocationStatusPartialSuccess
	} else {
		status.AllocationStatus = AllocationStatusFailed
	}

	// Set persistence status based on allocation strategy
	status.PersistenceStatus = m.getPersistenceStatus(config)

	// Collect memory information
	memoryInfo, err := m.collectMemoryInfo(ctx, node, config.NUMAAware)
	if err != nil {
		log.Error(err, "Failed to collect memory information")
	} else {
		status.MemoryInfo = memoryInfo
	}

	log.Info("Huge pages allocation completed",
		"totalAllocated", totalAllocated,
		"totalAvailable", totalAvailable,
		"allocationStatus", status.AllocationStatus)

	return status, nil
}

// allocatePageSize allocates a specific page size
func (m *Manager) allocatePageSize(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, config *networkingv1alpha1.HugePagesConfig, overallStatus *networkingv1alpha1.HugePagesStatus) (*networkingv1alpha1.HugePageStatus, error) {
	log := m.log.WithValues("node", node.Name, "pageSize", pageSpec.Size, "count", pageSpec.Count)
	log.Info("Allocating huge pages for specific size")

	// Initialize page status
	pageStatus := &networkingv1alpha1.HugePageStatus{
		Size:                     pageSpec.Size,
		Requested:               pageSpec.Count,
		Allocated:               0,
		Available:               0,
		NUMANode:                pageSpec.NUMANode,
		AllocationStatus:        AllocationStatusPending,
		AllocationMethod:        m.getAllocationMethod(config),
		LastAllocationAttempt:   metav1.Now(),
		AllocationErrors:        []string{},
		Priority:                m.getPriority(&pageSpec),
		PartialAllocationAccepted: m.getAllowPartialAllocation(&pageSpec),
	}

	// Calculate memory requirement
	pageStatus.MemoryRequirement = m.calculateMemoryRequirement(pageSpec.Size, pageSpec.Count)

	// Perform allocation with retry logic
	retryPolicy := m.getRetryPolicy(config)
	allocated, available, err := m.performAllocationWithRetry(ctx, node, pageSpec, config, retryPolicy, pageStatus)

	if err != nil {
		pageStatus.AllocationStatus = AllocationStatusFailed
		pageStatus.AllocationErrors = append(pageStatus.AllocationErrors, err.Error())
		return pageStatus, err
	}

	pageStatus.Allocated = allocated
	pageStatus.Available = available

	// Determine allocation status
	if allocated == pageSpec.Count {
		pageStatus.AllocationStatus = AllocationStatusSuccess
	} else if allocated > 0 && *m.getAllowPartialAllocation(&pageSpec) {
		minCount := m.getMinCount(&pageSpec)
		if allocated >= minCount {
			pageStatus.AllocationStatus = AllocationStatusPartialSuccess
		} else {
			pageStatus.AllocationStatus = AllocationStatusFailed
			return pageStatus, fmt.Errorf("allocated pages (%d) below minimum required (%d)", allocated, minCount)
		}
	} else {
		pageStatus.AllocationStatus = AllocationStatusFailed
		return pageStatus, fmt.Errorf("failed to allocate required huge pages")
	}

	log.Info("Page size allocation completed",
		"allocated", allocated,
		"available", available,
		"status", pageStatus.AllocationStatus)

	return pageStatus, nil
}

// performAllocationWithRetry performs allocation with retry logic
func (m *Manager) performAllocationWithRetry(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, config *networkingv1alpha1.HugePagesConfig, retryPolicy *networkingv1alpha1.HugePageRetryPolicy, pageStatus *networkingv1alpha1.HugePageStatus) (int32, int32, error) {
	log := m.log.WithValues("node", node.Name, "pageSize", pageSpec.Size)

	maxRetries := m.getMaxRetries(retryPolicy)
	retryInterval := m.getRetryInterval(retryPolicy)
	backoffMultiplier := m.getBackoffMultiplier(retryPolicy)

	var lastErr error
	currentInterval := retryInterval

	for attempt := int32(0); attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			log.Info("Retrying huge pages allocation", "attempt", attempt, "interval", currentInterval)
			select {
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			case <-time.After(currentInterval):
				// Continue with retry
			}
			currentInterval = time.Duration(float64(currentInterval) * backoffMultiplier)
		}

		allocated, available, err := m.performActualAllocation(ctx, node, pageSpec, config)
		if err == nil {
			return allocated, available, nil
		}

		lastErr = err
		pageStatus.AllocationErrors = append(pageStatus.AllocationErrors, fmt.Sprintf("Attempt %d: %v", attempt+1, err))
		log.Error(err, "Allocation attempt failed", "attempt", attempt+1)
	}

	return 0, 0, fmt.Errorf("allocation failed after %d attempts: %v", maxRetries+1, lastErr)
}

// performActualAllocation performs the actual huge pages allocation
func (m *Manager) performActualAllocation(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, config *networkingv1alpha1.HugePagesConfig) (int32, int32, error) {
	log := m.log.WithValues("node", node.Name, "pageSize", pageSpec.Size, "count", pageSpec.Count)
	log.Info("Performing actual huge pages allocation")

	// TODO: Implement actual huge pages allocation via system manager DaemonSet
	// This would involve:
	// 1. Finding the system manager pod on the node
	// 2. Executing commands to allocate huge pages based on strategy:
	//    - Runtime: echo <count> > /proc/sys/vm/nr_hugepages_<size>
	//    - Persistent: Update /etc/sysctl.conf or grub configuration
	//    - Best-effort: Try runtime first, fall back to persistent if needed
	// 3. Verifying the allocation was successful
	// 4. Updating node resources to advertise huge pages
	// 5. Setting up mount points if needed

	// For now, simulate allocation based on strategy
	strategy := m.getAllocationStrategy(config)
	
	switch strategy {
	case AllocationStrategyRuntime:
		return m.performRuntimeAllocation(ctx, node, pageSpec, config)
	case AllocationStrategyPersistent:
		return m.performPersistentAllocation(ctx, node, pageSpec, config)
	case AllocationStrategyBestEffort:
		return m.performBestEffortAllocation(ctx, node, pageSpec, config)
	default:
		return m.performRuntimeAllocation(ctx, node, pageSpec, config)
	}
}

// getAllocationStrategy gets the allocation strategy from config
func (m *Manager) getAllocationStrategy(config *networkingv1alpha1.HugePagesConfig) string {
	if config.AllocationStrategy != "" {
		return config.AllocationStrategy
	}
	return DefaultAllocationStrategy
}

// shouldPerformPreAllocationCheck determines if pre-allocation check should be performed
func (m *Manager) shouldPerformPreAllocationCheck(config *networkingv1alpha1.HugePagesConfig) bool {
	if config.PreAllocationCheck != nil {
		return *config.PreAllocationCheck
	}
	return true // Default to true for safety
}

// performPreAllocationCheck performs pre-allocation memory checks
func (m *Manager) performPreAllocationCheck(ctx context.Context, node corev1.Node, config *networkingv1alpha1.HugePagesConfig, status *networkingv1alpha1.HugePagesStatus) error {
	m.log.Info("Performing pre-allocation check", "node", node.Name)
	// TODO: Implement actual pre-allocation check
	return nil
}

// sortPagesByPriority sorts pages by priority for allocation
func (m *Manager) sortPagesByPriority(pages []networkingv1alpha1.HugePageSpec) []networkingv1alpha1.HugePageSpec {
	// TODO: Implement priority-based sorting
	return pages
}

// getPersistenceStatus determines persistence status based on config
func (m *Manager) getPersistenceStatus(config *networkingv1alpha1.HugePagesConfig) string {
	strategy := m.getAllocationStrategy(config)
	switch strategy {
	case AllocationStrategyPersistent:
		return PersistenceStatusPersistent
	case AllocationStrategyRuntime:
		return PersistenceStatusRuntime
	default:
		return PersistenceStatusUnknown
	}
}

// collectMemoryInfo collects memory information from the node
func (m *Manager) collectMemoryInfo(ctx context.Context, node corev1.Node, numaAware bool) (*networkingv1alpha1.HugePageMemoryInfo, error) {
	// TODO: Implement memory info collection
	return &networkingv1alpha1.HugePageMemoryInfo{}, nil
}

// getAllocationMethod gets the allocation method based on config
func (m *Manager) getAllocationMethod(config *networkingv1alpha1.HugePagesConfig) string {
	strategy := m.getAllocationStrategy(config)
	switch strategy {
	case AllocationStrategyPersistent:
		return AllocationMethodPersistent
	case AllocationStrategyRuntime:
		return AllocationMethodRuntime
	default:
		return AllocationMethodRuntime
	}
}

// getPriority gets priority for a page spec
func (m *Manager) getPriority(pageSpec *networkingv1alpha1.HugePageSpec) *int32 {
	if pageSpec.Priority != nil {
		return pageSpec.Priority
	}
	defaultPriority := int32(DefaultPriority)
	return &defaultPriority
}

// getAllowPartialAllocation gets partial allocation setting
func (m *Manager) getAllowPartialAllocation(pageSpec *networkingv1alpha1.HugePageSpec) *bool {
	if pageSpec.AllowPartialAllocation != nil {
		return pageSpec.AllowPartialAllocation
	}
	defaultValue := true
	return &defaultValue
}

// calculateMemoryRequirement calculates memory requirement for pages
func (m *Manager) calculateMemoryRequirement(pageSize string, count int32) int64 {
	// Parse page size and calculate total memory
	pageSizeBytes := m.parsePageSize(pageSize)
	return int64(pageSizeBytes) * int64(count)
}

// parsePageSize parses page size string to bytes
func (m *Manager) parsePageSize(pageSize string) int64 {
	switch pageSize {
	case "2Mi", "2MB":
		return 2 * 1024 * 1024
	case "1Gi", "1GB":
		return 1024 * 1024 * 1024
	case "4Ki":
		return 4 * 1024
	case "64Ki":
		return 64 * 1024
	case "2Gi":
		return 2 * 1024 * 1024 * 1024
	default:
		return 2 * 1024 * 1024 // Default to 2MB
	}
}

// getRetryPolicy gets retry policy from config
func (m *Manager) getRetryPolicy(config *networkingv1alpha1.HugePagesConfig) *networkingv1alpha1.HugePageRetryPolicy {
	if config.RetryPolicy != nil {
		return config.RetryPolicy
	}
	return &networkingv1alpha1.HugePageRetryPolicy{}
}

// getMaxRetries gets max retries from retry policy
func (m *Manager) getMaxRetries(retryPolicy *networkingv1alpha1.HugePageRetryPolicy) int32 {
	if retryPolicy.MaxRetries != nil {
		return *retryPolicy.MaxRetries
	}
	return DefaultMaxRetries
}

// getRetryInterval gets retry interval from retry policy
func (m *Manager) getRetryInterval(retryPolicy *networkingv1alpha1.HugePageRetryPolicy) time.Duration {
	if retryPolicy.RetryInterval != "" {
		if duration, err := time.ParseDuration(retryPolicy.RetryInterval); err == nil {
			return duration
		}
	}
	defaultDuration, _ := time.ParseDuration(DefaultRetryInterval)
	return defaultDuration
}

// getBackoffMultiplier gets backoff multiplier from retry policy
func (m *Manager) getBackoffMultiplier(retryPolicy *networkingv1alpha1.HugePageRetryPolicy) float64 {
	if retryPolicy.BackoffMultiplier != nil {
		return *retryPolicy.BackoffMultiplier
	}
	return DefaultBackoffMultiplier
}

// getMinCount gets minimum count for a page spec
func (m *Manager) getMinCount(pageSpec *networkingv1alpha1.HugePageSpec) int32 {
	if pageSpec.MinCount != nil {
		return *pageSpec.MinCount
	}
	return 0
}

// performRuntimeAllocation performs runtime huge pages allocation
func (m *Manager) performRuntimeAllocation(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, config *networkingv1alpha1.HugePagesConfig) (int32, int32, error) {
	m.log.Info("Performing runtime allocation", "node", node.Name, "pageSize", pageSpec.Size)
	// TODO: Implement actual runtime allocation
	// For now, simulate successful allocation
	return pageSpec.Count, pageSpec.Count, nil
}

// performPersistentAllocation performs persistent huge pages allocation
func (m *Manager) performPersistentAllocation(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, config *networkingv1alpha1.HugePagesConfig) (int32, int32, error) {
	m.log.Info("Performing persistent allocation", "node", node.Name, "pageSize", pageSpec.Size)
	// TODO: Implement actual persistent allocation
	// For now, simulate successful allocation
	return pageSpec.Count, pageSpec.Count, nil
}

// performBestEffortAllocation performs best-effort huge pages allocation
func (m *Manager) performBestEffortAllocation(ctx context.Context, node corev1.Node, pageSpec networkingv1alpha1.HugePageSpec, config *networkingv1alpha1.HugePagesConfig) (int32, int32, error) {
	m.log.Info("Performing best-effort allocation", "node", node.Name, "pageSize", pageSpec.Size)
	// Try runtime first
	allocated, available, err := m.performRuntimeAllocation(ctx, node, pageSpec, config)
	if err == nil {
		return allocated, available, nil
	}
	// Fall back to persistent if runtime fails
	return m.performPersistentAllocation(ctx, node, pageSpec, config)
}
