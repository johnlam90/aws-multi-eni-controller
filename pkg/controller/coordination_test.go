package controller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCoordinationManager_AcquireLock(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)
	ctx := context.Background()

	// Test successful lock acquisition
	lock, err := cm.AcquireLock(ctx, "resource1", "eni", "operation1", 5*time.Second)
	if err != nil {
		t.Errorf("Expected successful lock acquisition, got error: %v", err)
	}
	if lock == nil {
		t.Error("Expected lock to be returned")
		return
	}
	if lock.ResourceID != "resource1" {
		t.Errorf("Expected resource ID 'resource1', got '%s'", lock.ResourceID)
	}

	// Test lock conflict
	_, err = cm.AcquireLock(ctx, "resource1", "eni", "operation2", 5*time.Second)
	if err == nil {
		t.Error("Expected lock conflict error")
	}

	// Test lock acquisition after release
	err = cm.ReleaseLock("resource1", "operation1")
	if err != nil {
		t.Errorf("Expected successful lock release, got error: %v", err)
	}

	lock2, err := cm.AcquireLock(ctx, "resource1", "eni", "operation2", 5*time.Second)
	if err != nil {
		t.Errorf("Expected successful lock acquisition after release, got error: %v", err)
	}
	if lock2 == nil {
		t.Error("Expected lock to be returned after release")
	}
}

func TestCoordinationManager_ReleaseLock(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)
	ctx := context.Background()

	// Test releasing non-existent lock
	err := cm.ReleaseLock("nonexistent", "operation1")
	if err == nil {
		t.Error("Expected error when releasing non-existent lock")
	}

	// Test releasing lock with wrong operation ID
	_, err = cm.AcquireLock(ctx, "resource1", "eni", "operation1", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to acquire lock: %v", err)
	}

	err = cm.ReleaseLock("resource1", "operation2")
	if err == nil {
		t.Error("Expected error when releasing lock with wrong operation ID")
	}

	// Test successful release
	err = cm.ReleaseLock("resource1", "operation1")
	if err != nil {
		t.Errorf("Expected successful lock release, got error: %v", err)
	}
}

func TestCoordinationManager_Dependencies(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)

	// Register dependencies
	cm.RegisterDependency("op1", []string{}, []string{"resource1"}, 1)
	cm.RegisterDependency("op2", []string{"op1"}, []string{"resource2"}, 2)
	cm.RegisterDependency("op3", []string{"op1", "op2"}, []string{"resource3"}, 3)

	// Test dependency checking
	satisfied, unsatisfied := cm.CheckDependencies("op1")
	if !satisfied {
		t.Errorf("Expected op1 to have satisfied dependencies, got unsatisfied: %v", unsatisfied)
	}

	satisfied, unsatisfied = cm.CheckDependencies("op2")
	if satisfied {
		t.Error("Expected op2 to have unsatisfied dependencies")
	}
	if len(unsatisfied) != 1 || unsatisfied[0] != "op1" {
		t.Errorf("Expected op2 to depend on op1, got: %v", unsatisfied)
	}

	satisfied, unsatisfied = cm.CheckDependencies("op3")
	if satisfied {
		t.Error("Expected op3 to have unsatisfied dependencies")
	}
	if len(unsatisfied) != 2 {
		t.Errorf("Expected op3 to have 2 unsatisfied dependencies, got: %v", unsatisfied)
	}

	// Complete op1 and check again
	cm.CompleteDependency("op1")
	satisfied, unsatisfied = cm.CheckDependencies("op2")
	if !satisfied {
		t.Errorf("Expected op2 to have satisfied dependencies after op1 completion, got unsatisfied: %v", unsatisfied)
	}

	satisfied, unsatisfied = cm.CheckDependencies("op3")
	if satisfied {
		t.Error("Expected op3 to still have unsatisfied dependencies")
	}
	if len(unsatisfied) != 1 || unsatisfied[0] != "op2" {
		t.Errorf("Expected op3 to depend only on op2 now, got: %v", unsatisfied)
	}
}

func TestCoordinationManager_ResourceConflicts(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)
	ctx := context.Background()

	// Acquire locks on some resources
	_, err := cm.AcquireLock(ctx, "resource1", "eni", "operation1", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to acquire lock: %v", err)
	}

	_, err = cm.AcquireLock(ctx, "resource2", "instance", "operation2", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to acquire lock: %v", err)
	}

	// Test conflict detection
	conflicts := cm.GetResourceConflicts([]string{"resource1", "resource3"})
	if len(conflicts) != 1 {
		t.Errorf("Expected 1 conflict, got %d: %v", len(conflicts), conflicts)
	}

	conflicts = cm.GetResourceConflicts([]string{"resource3", "resource4"})
	if len(conflicts) != 0 {
		t.Errorf("Expected no conflicts, got %d: %v", len(conflicts), conflicts)
	}

	conflicts = cm.GetResourceConflicts([]string{"resource1", "resource2"})
	if len(conflicts) != 2 {
		t.Errorf("Expected 2 conflicts, got %d: %v", len(conflicts), conflicts)
	}
}

func TestCoordinationManager_ExecuteCoordinated(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)
	ctx := context.Background()

	// Test successful coordinated execution
	executed := false
	operation := &CoordinatedOperation{
		ID:          "test-op",
		Type:        "test",
		ResourceIDs: []string{"resource1"},
		Priority:    1,
		DependsOn:   []string{},
		Timeout:     5 * time.Second,
		Execute: func(ctx context.Context) error {
			executed = true
			return nil
		},
	}

	err := cm.ExecuteCoordinated(ctx, operation)
	if err != nil {
		t.Errorf("Expected successful execution, got error: %v", err)
	}
	if !executed {
		t.Error("Expected operation to be executed")
	}

	// Test execution with resource conflict
	_, err = cm.AcquireLock(ctx, "resource2", "test", "other-op", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to acquire lock: %v", err)
	}

	operation2 := &CoordinatedOperation{
		ID:          "test-op-2",
		Type:        "test",
		ResourceIDs: []string{"resource2"},
		Priority:    1,
		DependsOn:   []string{},
		Timeout:     5 * time.Second,
		Execute: func(ctx context.Context) error {
			return nil
		},
	}

	err = cm.ExecuteCoordinated(ctx, operation2)
	if err == nil {
		t.Error("Expected resource conflict error")
	}
}

func TestCoordinationManager_CleanupStaleLocks(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)

	// Create a context that will be cancelled
	ctx, cancel := context.WithCancel(context.Background())

	// Acquire a lock
	lock, err := cm.AcquireLock(ctx, "resource1", "eni", "operation1", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to acquire lock: %v", err)
	}

	// Cancel the context to make the lock stale
	cancel()

	// Wait a bit for the context cancellation to propagate
	time.Sleep(10 * time.Millisecond)

	// Cleanup should remove the stale lock
	cm.CleanupStaleLocks()

	// Try to acquire the same resource - should succeed now
	newCtx := context.Background()
	_, err = cm.AcquireLock(newCtx, "resource1", "eni", "operation2", 5*time.Second)
	if err != nil {
		t.Errorf("Expected to acquire lock after cleanup, got error: %v", err)
	}

	// Verify the old lock is gone
	if lock != nil {
		select {
		case <-lock.Context.Done():
			// This is expected
		default:
			t.Error("Expected old lock context to be cancelled")
		}
	}
}

func TestCoordinationManager_ConcurrentOperations(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)
	ctx := context.Background()

	// Test concurrent lock acquisition
	var wg sync.WaitGroup
	var successCount int32
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			resourceID := fmt.Sprintf("resource-%d", id%3) // 3 different resources
			operationID := fmt.Sprintf("operation-%d", id)

			lock, err := cm.AcquireLock(ctx, resourceID, "test", operationID, 1*time.Second)
			if err == nil && lock != nil {
				// Hold the lock briefly
				time.Sleep(10 * time.Millisecond)

				// Release the lock
				err = cm.ReleaseLock(resourceID, operationID)
				if err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()

	// All operations should eventually succeed since they release locks
	// But due to timing, not all might succeed in this test
	if successCount == 0 {
		t.Error("Expected at least some operations to succeed")
	}

	t.Logf("Successful operations: %d out of 10", successCount)
}

func TestCoordinationManager_Status(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	cm := NewCoordinationManager(logger)
	ctx := context.Background()

	// Acquire some locks and register dependencies
	_, err := cm.AcquireLock(ctx, "resource1", "eni", "operation1", 5*time.Second)
	if err != nil {
		t.Errorf("Failed to acquire lock: %v", err)
	}

	cm.RegisterDependency("op1", []string{}, []string{"resource1"}, 1)
	cm.RegisterDependency("op2", []string{"op1"}, []string{"resource2"}, 2)

	// Check lock status
	lockStatus := cm.GetLockStatus()
	if len(lockStatus) != 1 {
		t.Errorf("Expected 1 lock in status, got %d", len(lockStatus))
	}

	if lock, exists := lockStatus["resource1"]; !exists {
		t.Error("Expected resource1 to be in lock status")
	} else {
		if lock.LockedBy != "operation1" {
			t.Errorf("Expected lock to be held by operation1, got %s", lock.LockedBy)
		}
	}

	// Check dependency status
	depStatus := cm.GetDependencyStatus()
	if len(depStatus) != 2 {
		t.Errorf("Expected 2 dependencies in status, got %d", len(depStatus))
	}

	if dep, exists := depStatus["op2"]; !exists {
		t.Error("Expected op2 to be in dependency status")
	} else {
		if len(dep.DependsOn) != 1 || dep.DependsOn[0] != "op1" {
			t.Errorf("Expected op2 to depend on op1, got %v", dep.DependsOn)
		}
	}
}
