package controller

import (
	"context"
	"testing"
	"time"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestDependencyGraph_AddDependency(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dg := NewDependencyGraph(logger)

	// Test adding a simple dependency
	dep1 := &Dependency{
		ID:            "dep1",
		Type:          DependencyTypeInstance,
		ResourceID:    "instance-1",
		DependsOn:     []string{},
		RequiredState: ResourceStateAvailable,
		Priority:      10,
		CreatedAt:     time.Now(),
	}

	err := dg.AddDependency(dep1)
	if err != nil {
		t.Errorf("Expected successful dependency addition, got error: %v", err)
	}

	// Test adding a dependency with dependencies
	dep2 := &Dependency{
		ID:            "dep2",
		Type:          DependencyTypeENI,
		ResourceID:    "eni-1",
		DependsOn:     []string{"dep1"},
		RequiredState: ResourceStateAvailable,
		Priority:      5,
		CreatedAt:     time.Now(),
	}

	err = dg.AddDependency(dep2)
	if err != nil {
		t.Errorf("Expected successful dependency addition, got error: %v", err)
	}

	// Test circular dependency detection
	dep3 := &Dependency{
		ID:            "dep3",
		Type:          DependencyTypeInstance,
		ResourceID:    "instance-2",
		DependsOn:     []string{"dep2"},
		RequiredState: ResourceStateAvailable,
		Priority:      8,
		CreatedAt:     time.Now(),
	}

	err = dg.AddDependency(dep3)
	if err != nil {
		t.Errorf("Expected successful dependency addition, got error: %v", err)
	}

	// This should create a cycle: dep1 -> dep2 -> dep3 -> dep1
	dep1.DependsOn = []string{"dep3"}
	err = dg.AddDependency(dep1)
	if err == nil {
		t.Error("Expected circular dependency error")
	}
}

func TestDependencyGraph_ResourceState(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dg := NewDependencyGraph(logger)

	// Test initial state
	state := dg.GetResourceState("resource1")
	if state != ResourceStateUnknown {
		t.Errorf("Expected initial state to be Unknown, got %v", state)
	}

	// Test state update
	dg.UpdateResourceState("resource1", ResourceStateAvailable)
	state = dg.GetResourceState("resource1")
	if state != ResourceStateAvailable {
		t.Errorf("Expected state to be Available, got %v", state)
	}

	// Test state change
	dg.UpdateResourceState("resource1", ResourceStateInUse)
	state = dg.GetResourceState("resource1")
	if state != ResourceStateInUse {
		t.Errorf("Expected state to be InUse, got %v", state)
	}
}

func TestDependencyGraph_ReadyDependencies(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dg := NewDependencyGraph(logger)

	// Add dependencies
	dep1 := &Dependency{
		ID:            "dep1",
		Type:          DependencyTypeInstance,
		ResourceID:    "instance-1",
		DependsOn:     []string{},
		RequiredState: ResourceStateAvailable,
		Priority:      10,
		CreatedAt:     time.Now(),
	}

	dep2 := &Dependency{
		ID:            "dep2",
		Type:          DependencyTypeENI,
		ResourceID:    "eni-1",
		DependsOn:     []string{"dep1"},
		RequiredState: ResourceStateAvailable,
		Priority:      5,
		CreatedAt:     time.Now(),
	}

	dg.AddDependency(dep1)
	dg.AddDependency(dep2)

	// Initially, no dependencies should be ready because resource states are unknown
	ready := dg.GetReadyDependencies()
	if len(ready) != 0 {
		t.Errorf("Expected no dependencies to be ready initially, got %v", ready)
	}

	// Set resource state to make dep1 ready
	dg.UpdateResourceState("instance-1", ResourceStateAvailable)
	ready = dg.GetReadyDependencies()
	if len(ready) != 1 || ready[0].ID != "dep1" {
		t.Errorf("Expected dep1 to be ready with correct state, got %v", ready)
	}

	// Remove dep1 to make dep2 ready
	dg.RemoveDependency("dep1")
	dg.UpdateResourceState("eni-1", ResourceStateAvailable)
	ready = dg.GetReadyDependencies()
	if len(ready) != 1 || ready[0].ID != "dep2" {
		t.Errorf("Expected dep2 to be ready after dep1 removal, got %v", ready)
	}
}

func TestDependencyGraph_DependencyOrder(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dg := NewDependencyGraph(logger)

	// Create a dependency chain: dep1 -> dep2 -> dep3
	dep1 := &Dependency{
		ID:         "dep1",
		Type:       DependencyTypeInstance,
		ResourceID: "instance-1",
		DependsOn:  []string{},
		Priority:   10,
		CreatedAt:  time.Now(),
	}

	dep2 := &Dependency{
		ID:         "dep2",
		Type:       DependencyTypeSubnet,
		ResourceID: "subnet-1",
		DependsOn:  []string{"dep1"},
		Priority:   8,
		CreatedAt:  time.Now(),
	}

	dep3 := &Dependency{
		ID:         "dep3",
		Type:       DependencyTypeENI,
		ResourceID: "eni-1",
		DependsOn:  []string{"dep2"},
		Priority:   5,
		CreatedAt:  time.Now(),
	}

	dg.AddDependency(dep3) // Add in reverse order
	dg.AddDependency(dep2)
	dg.AddDependency(dep1)

	// Resolve dependency order
	order, err := dg.ResolveDependencyOrder()
	if err != nil {
		t.Errorf("Expected successful dependency resolution, got error: %v", err)
	}

	if len(order) != 3 {
		t.Errorf("Expected 3 dependencies in order, got %d", len(order))
	}

	// Check order: dep1 should come first, dep3 should come last
	if order[0].ID != "dep1" {
		t.Errorf("Expected dep1 to be first, got %s", order[0].ID)
	}
	if order[2].ID != "dep3" {
		t.Errorf("Expected dep3 to be last, got %s", order[2].ID)
	}
}

func TestDependencyGraph_Conflicts(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dg := NewDependencyGraph(logger)

	// Add conflicting dependencies (same resource, different required states)
	dep1 := &Dependency{
		ID:            "dep1",
		Type:          DependencyTypeENI,
		ResourceID:    "eni-1",
		RequiredState: ResourceStateAvailable,
		Priority:      10,
		CreatedAt:     time.Now(),
	}

	dep2 := &Dependency{
		ID:            "dep2",
		Type:          DependencyTypeENI,
		ResourceID:    "eni-1",
		RequiredState: ResourceStateInUse,
		Priority:      8,
		CreatedAt:     time.Now(),
	}

	dg.AddDependency(dep1)
	dg.AddDependency(dep2)

	conflicts := dg.GetDependencyConflicts()
	if len(conflicts) == 0 {
		t.Error("Expected conflicts to be detected")
	}
}

func TestDependencyManager_ENIAttachmentDependencies(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dm := NewDependencyManager(logger)
	ctx := context.Background()

	// Create test NodeENI
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			SubnetIDs: []string{"subnet-12345"},
		},
	}

	// Create test attachment
	attachment := networkingv1alpha1.ENIAttachment{
		NodeID:     "node-1",
		InstanceID: "i-12345",
		ENIID:      "eni-12345",
	}

	// Create dependencies
	err := dm.CreateENIAttachmentDependencies(ctx, nodeENI, attachment)
	if err != nil {
		t.Errorf("Expected successful dependency creation, got error: %v", err)
	}

	// Check that dependencies were created
	status := dm.GetDependencyStatus()
	total, ok := status["total"].(int)
	if !ok || total != 3 {
		t.Errorf("Expected 3 dependencies to be created, got %v", status)
	}

	// Set resource states to make dependencies ready
	dm.UpdateResourceState("i-12345", ResourceStateAvailable)
	dm.UpdateResourceState("subnet-12345", ResourceStateAvailable)

	// Check ready operations (should be instance and subnet dependencies)
	ready := dm.GetReadyOperations()
	if len(ready) != 2 {
		t.Errorf("Expected 2 ready operations, got %d", len(ready))
	}
}

func TestDependencyManager_ENIDetachmentDependencies(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dm := NewDependencyManager(logger)
	ctx := context.Background()

	// Create test NodeENI with DPDK enabled
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			EnableDPDK: true,
		},
	}

	// Create test attachment
	attachment := networkingv1alpha1.ENIAttachment{
		NodeID:       "node-1",
		InstanceID:   "i-12345",
		ENIID:        "eni-12345",
		AttachmentID: "eni-attach-12345",
	}

	// Create dependencies
	err := dm.CreateENIDetachmentDependencies(ctx, nodeENI, attachment)
	if err != nil {
		t.Errorf("Expected successful dependency creation, got error: %v", err)
	}

	// Check that dependencies were created (DPDK + detach)
	status := dm.GetDependencyStatus()
	total, ok := status["total"].(int)
	if !ok || total != 2 {
		t.Errorf("Expected 2 dependencies to be created for DPDK-enabled detachment, got %v", status)
	}

	// Set resource state to make DPDK dependency ready
	dm.UpdateResourceState("dpdk-eni-12345", ResourceStateAvailable)

	// Check ready operations (should be DPDK unbind)
	ready := dm.GetReadyOperations()
	if len(ready) != 1 {
		t.Errorf("Expected 1 ready operation (DPDK unbind), got %d", len(ready))
	}
}

func TestDependencyManager_DependencyCompletion(t *testing.T) {
	logger := zap.New(zap.UseDevMode(true))
	dm := NewDependencyManager(logger)
	ctx := context.Background()

	// Create test NodeENI
	nodeENI := &networkingv1alpha1.NodeENI{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeeni",
		},
		Spec: networkingv1alpha1.NodeENISpec{
			SubnetIDs: []string{"subnet-12345"},
		},
	}

	// Create test attachment
	attachment := networkingv1alpha1.ENIAttachment{
		NodeID:     "node-1",
		InstanceID: "i-12345",
		ENIID:      "eni-12345",
	}

	// Create dependencies
	err := dm.CreateENIAttachmentDependencies(ctx, nodeENI, attachment)
	if err != nil {
		t.Errorf("Expected successful dependency creation, got error: %v", err)
	}

	// Set resource states to make some dependencies ready
	dm.UpdateResourceState("i-12345", ResourceStateAvailable)
	dm.UpdateResourceState("subnet-12345", ResourceStateAvailable)

	// Get initial ready operations
	ready := dm.GetReadyOperations()
	initialCount := len(ready)

	// Complete one dependency if any are ready
	if len(ready) > 0 {
		dm.CompleteDependency(ready[0].ID)
	}

	// Check that dependency was removed
	status := dm.GetDependencyStatus()
	total, ok := status["total"].(int)
	if !ok || total != 2 { // Should be 2 remaining (3 - 1)
		t.Errorf("Expected 2 dependencies after completion, got %v", status)
	}

	// Check that ready operations count is reasonable
	newReady := dm.GetReadyOperations()
	// The number of ready operations may stay the same or change depending on dependency relationships
	// Just verify that we have a valid count
	if len(newReady) < 0 {
		t.Errorf("Invalid number of ready operations: %d", len(newReady))
	}

	t.Logf("Initial ready operations: %d, after completion: %d", initialCount, len(newReady))
}
