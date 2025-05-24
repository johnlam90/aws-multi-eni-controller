package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// ResourceLock represents a lock on a specific resource
type ResourceLock struct {
	ResourceID   string
	ResourceType string
	LockedAt     time.Time
	LockedBy     string
	Context      context.Context
	Cancel       context.CancelFunc
}

// OperationDependency represents a dependency between operations
type OperationDependency struct {
	OperationID string
	DependsOn   []string
	ResourceIDs []string
	Priority    int
}

// CoordinationManager manages resource locks and operation dependencies
type CoordinationManager struct {
	locks        map[string]*ResourceLock
	dependencies map[string]*OperationDependency
	mutex        sync.RWMutex
	logger       logr.Logger
}

// NewCoordinationManager creates a new coordination manager
func NewCoordinationManager(logger logr.Logger) *CoordinationManager {
	return &CoordinationManager{
		locks:        make(map[string]*ResourceLock),
		dependencies: make(map[string]*OperationDependency),
		logger:       logger.WithName("coordination"),
	}
}

// AcquireLock attempts to acquire a lock on a resource
func (cm *CoordinationManager) AcquireLock(ctx context.Context, resourceID, resourceType, operationID string, timeout time.Duration) (*ResourceLock, error) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	// Check if resource is already locked
	if existingLock, exists := cm.locks[resourceID]; exists {
		// Check if the lock is still valid (context not cancelled)
		select {
		case <-existingLock.Context.Done():
			// Lock is stale, remove it
			delete(cm.locks, resourceID)
			cm.logger.Info("Removed stale lock", "resourceID", resourceID, "previousOwner", existingLock.LockedBy)
		default:
			// Lock is still active
			return nil, fmt.Errorf("resource %s is already locked by %s", resourceID, existingLock.LockedBy)
		}
	}

	// Create new lock with timeout context
	lockCtx, cancel := context.WithTimeout(ctx, timeout)
	lock := &ResourceLock{
		ResourceID:   resourceID,
		ResourceType: resourceType,
		LockedAt:     time.Now(),
		LockedBy:     operationID,
		Context:      lockCtx,
		Cancel:       cancel,
	}

	cm.locks[resourceID] = lock
	cm.logger.Info("Acquired lock", "resourceID", resourceID, "resourceType", resourceType, "operationID", operationID)

	return lock, nil
}

// ReleaseLock releases a lock on a resource
func (cm *CoordinationManager) ReleaseLock(resourceID, operationID string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	lock, exists := cm.locks[resourceID]
	if !exists {
		return fmt.Errorf("no lock found for resource %s", resourceID)
	}

	if lock.LockedBy != operationID {
		return fmt.Errorf("resource %s is locked by %s, not %s", resourceID, lock.LockedBy, operationID)
	}

	lock.Cancel()
	delete(cm.locks, resourceID)
	cm.logger.Info("Released lock", "resourceID", resourceID, "operationID", operationID)

	return nil
}

// RegisterDependency registers an operation dependency
func (cm *CoordinationManager) RegisterDependency(operationID string, dependsOn []string, resourceIDs []string, priority int) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.dependencies[operationID] = &OperationDependency{
		OperationID: operationID,
		DependsOn:   dependsOn,
		ResourceIDs: resourceIDs,
		Priority:    priority,
	}

	cm.logger.Info("Registered dependency", "operationID", operationID, "dependsOn", dependsOn, "priority", priority)
}

// CheckDependencies checks if an operation's dependencies are satisfied
func (cm *CoordinationManager) CheckDependencies(operationID string) (bool, []string) {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	dependency, exists := cm.dependencies[operationID]
	if !exists {
		return true, nil // No dependencies
	}

	var unsatisfied []string
	for _, depID := range dependency.DependsOn {
		if _, stillExists := cm.dependencies[depID]; stillExists {
			unsatisfied = append(unsatisfied, depID)
		}
	}

	return len(unsatisfied) == 0, unsatisfied
}

// CompleteDependency marks an operation as complete
func (cm *CoordinationManager) CompleteDependency(operationID string) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	delete(cm.dependencies, operationID)
	cm.logger.Info("Completed dependency", "operationID", operationID)
}

// GetResourceConflicts checks for potential resource conflicts
func (cm *CoordinationManager) GetResourceConflicts(resourceIDs []string) []string {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	var conflicts []string
	for _, resourceID := range resourceIDs {
		if lock, exists := cm.locks[resourceID]; exists {
			// Check if lock is still valid
			select {
			case <-lock.Context.Done():
				// Lock is stale, will be cleaned up
				continue
			default:
				conflicts = append(conflicts, fmt.Sprintf("%s (locked by %s)", resourceID, lock.LockedBy))
			}
		}
	}

	return conflicts
}

// CleanupStaleLocks removes locks that have expired or been cancelled
func (cm *CoordinationManager) CleanupStaleLocks() {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	var staleKeys []string
	for resourceID, lock := range cm.locks {
		select {
		case <-lock.Context.Done():
			staleKeys = append(staleKeys, resourceID)
		default:
			// Check if lock is too old (safety mechanism)
			if time.Since(lock.LockedAt) > 30*time.Minute {
				lock.Cancel()
				staleKeys = append(staleKeys, resourceID)
				cm.logger.Info("Force-released stale lock", "resourceID", resourceID, "age", time.Since(lock.LockedAt))
			}
		}
	}

	for _, key := range staleKeys {
		delete(cm.locks, key)
	}

	if len(staleKeys) > 0 {
		cm.logger.Info("Cleaned up stale locks", "count", len(staleKeys))
	}
}

// CoordinatedOperation represents an operation that requires coordination
type CoordinatedOperation struct {
	ID          string
	Type        string
	ResourceIDs []string
	Priority    int
	DependsOn   []string
	Timeout     time.Duration
	Execute     func(ctx context.Context) error
}

// ExecuteCoordinated executes an operation with proper coordination
func (cm *CoordinationManager) ExecuteCoordinated(ctx context.Context, op *CoordinatedOperation) error {
	// Clean up stale locks first
	cm.CleanupStaleLocks()

	// Check for resource conflicts
	conflicts := cm.GetResourceConflicts(op.ResourceIDs)
	if len(conflicts) > 0 {
		return fmt.Errorf("resource conflicts detected for operation %s: %v", op.ID, conflicts)
	}

	// Check dependencies
	satisfied, unsatisfied := cm.CheckDependencies(op.ID)
	if !satisfied {
		return fmt.Errorf("dependencies not satisfied for operation %s: %v", op.ID, unsatisfied)
	}

	// Acquire locks for all resources
	var locks []*ResourceLock
	var lockErrors []error

	for _, resourceID := range op.ResourceIDs {
		lock, err := cm.AcquireLock(ctx, resourceID, op.Type, op.ID, op.Timeout)
		if err != nil {
			lockErrors = append(lockErrors, err)
			break
		}
		locks = append(locks, lock)
	}

	// If we couldn't acquire all locks, release the ones we got
	if len(lockErrors) > 0 {
		for _, lock := range locks {
			cm.ReleaseLock(lock.ResourceID, op.ID)
		}
		return fmt.Errorf("failed to acquire all locks for operation %s: %v", op.ID, lockErrors)
	}

	// Execute the operation
	defer func() {
		// Release all locks
		for _, lock := range locks {
			cm.ReleaseLock(lock.ResourceID, op.ID)
		}
		// Mark operation as complete
		cm.CompleteDependency(op.ID)
	}()

	cm.logger.Info("Executing coordinated operation", "operationID", op.ID, "type", op.Type, "resourceCount", len(op.ResourceIDs))
	return op.Execute(ctx)
}

// GetLockStatus returns the current status of all locks
func (cm *CoordinationManager) GetLockStatus() map[string]ResourceLock {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	status := make(map[string]ResourceLock)
	for id, lock := range cm.locks {
		status[id] = *lock
	}
	return status
}

// GetDependencyStatus returns the current status of all dependencies
func (cm *CoordinationManager) GetDependencyStatus() map[string]OperationDependency {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()

	status := make(map[string]OperationDependency)
	for id, dep := range cm.dependencies {
		status[id] = *dep
	}
	return status
}
