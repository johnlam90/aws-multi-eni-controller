package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
)

// DependencyType represents different types of dependencies
type DependencyType int

const (
	// DependencyTypeInstance - dependency on instance state
	DependencyTypeInstance DependencyType = iota
	// DependencyTypeSubnet - dependency on subnet availability
	DependencyTypeSubnet
	// DependencyTypeENI - dependency on ENI state
	DependencyTypeENI
	// DependencyTypeNode - dependency on node state
	DependencyTypeNode
	// DependencyTypeSecurityGroup - dependency on security group
	DependencyTypeSecurityGroup
)

// ResourceState represents the state of a resource
type ResourceState int

const (
	// ResourceStateUnknown - state is unknown
	ResourceStateUnknown ResourceState = iota
	// ResourceStateAvailable - resource is available
	ResourceStateAvailable
	// ResourceStateInUse - resource is in use
	ResourceStateInUse
	// ResourceStateUnavailable - resource is unavailable
	ResourceStateUnavailable
	// ResourceStateDeleting - resource is being deleted
	ResourceStateDeleting
)

// Dependency represents a dependency between resources
type Dependency struct {
	ID            string
	Type          DependencyType
	ResourceID    string
	DependsOn     []string
	RequiredState ResourceState
	Priority      int
	CreatedAt     time.Time
	Metadata      map[string]interface{}
}

// DependencyGraph represents a graph of dependencies
type DependencyGraph struct {
	dependencies   map[string]*Dependency
	resourceStates map[string]ResourceState
	mutex          sync.RWMutex
	logger         logr.Logger
}

// NewDependencyGraph creates a new dependency graph
func NewDependencyGraph(logger logr.Logger) *DependencyGraph {
	return &DependencyGraph{
		dependencies:   make(map[string]*Dependency),
		resourceStates: make(map[string]ResourceState),
		logger:         logger.WithName("dependency-graph"),
	}
}

// AddDependency adds a dependency to the graph
func (dg *DependencyGraph) AddDependency(dep *Dependency) error {
	dg.mutex.Lock()
	defer dg.mutex.Unlock()

	// Check for circular dependencies
	if dg.wouldCreateCycle(dep) {
		return fmt.Errorf("adding dependency %s would create a circular dependency", dep.ID)
	}

	dg.dependencies[dep.ID] = dep
	dg.logger.Info("Added dependency", "id", dep.ID, "type", dep.Type, "resourceID", dep.ResourceID)
	return nil
}

// RemoveDependency removes a dependency from the graph
func (dg *DependencyGraph) RemoveDependency(id string) {
	dg.mutex.Lock()
	defer dg.mutex.Unlock()

	delete(dg.dependencies, id)
	dg.logger.Info("Removed dependency", "id", id)
}

// UpdateResourceState updates the state of a resource
func (dg *DependencyGraph) UpdateResourceState(resourceID string, state ResourceState) {
	dg.mutex.Lock()
	defer dg.mutex.Unlock()

	oldState := dg.resourceStates[resourceID]
	dg.resourceStates[resourceID] = state

	dg.logger.Info("Updated resource state",
		"resourceID", resourceID,
		"oldState", oldState,
		"newState", state)
}

// GetResourceState gets the current state of a resource
func (dg *DependencyGraph) GetResourceState(resourceID string) ResourceState {
	dg.mutex.RLock()
	defer dg.mutex.RUnlock()

	if state, exists := dg.resourceStates[resourceID]; exists {
		return state
	}
	return ResourceStateUnknown
}

// GetReadyDependencies returns dependencies that are ready to be processed
func (dg *DependencyGraph) GetReadyDependencies() []*Dependency {
	dg.mutex.RLock()
	defer dg.mutex.RUnlock()

	var ready []*Dependency
	for _, dep := range dg.dependencies {
		if dg.isDependencyReady(dep) {
			ready = append(ready, dep)
		}
	}

	// Sort by priority (higher priority first)
	sort.Slice(ready, func(i, j int) bool {
		return ready[i].Priority > ready[j].Priority
	})

	return ready
}

// GetBlockedDependencies returns dependencies that are blocked
func (dg *DependencyGraph) GetBlockedDependencies() []*Dependency {
	dg.mutex.RLock()
	defer dg.mutex.RUnlock()

	var blocked []*Dependency
	for _, dep := range dg.dependencies {
		if !dg.isDependencyReady(dep) {
			blocked = append(blocked, dep)
		}
	}

	return blocked
}

// ResolveDependencyOrder returns dependencies in the order they should be processed
func (dg *DependencyGraph) ResolveDependencyOrder() ([]*Dependency, error) {
	dg.mutex.RLock()
	defer dg.mutex.RUnlock()

	// Topological sort
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var result []*Dependency

	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("circular dependency detected involving %s", id)
		}
		if visited[id] {
			return nil
		}

		visiting[id] = true
		dep := dg.dependencies[id]
		if dep != nil {
			for _, depID := range dep.DependsOn {
				if err := visit(depID); err != nil {
					return err
				}
			}
			result = append(result, dep)
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}

	for id := range dg.dependencies {
		if err := visit(id); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// GetDependencyConflicts returns conflicts between dependencies
func (dg *DependencyGraph) GetDependencyConflicts() []string {
	dg.mutex.RLock()
	defer dg.mutex.RUnlock()

	var conflicts []string
	resourceUsage := make(map[string][]*Dependency)

	// Group dependencies by resource
	for _, dep := range dg.dependencies {
		resourceUsage[dep.ResourceID] = append(resourceUsage[dep.ResourceID], dep)
	}

	// Check for conflicts
	for resourceID, deps := range resourceUsage {
		if len(deps) > 1 {
			// Check if multiple dependencies require conflicting states
			stateRequirements := make(map[ResourceState]int)
			for _, dep := range deps {
				stateRequirements[dep.RequiredState]++
			}

			if len(stateRequirements) > 1 {
				conflicts = append(conflicts, fmt.Sprintf("Resource %s has conflicting state requirements", resourceID))
			}
		}
	}

	return conflicts
}

// isDependencyReady checks if a dependency is ready to be processed
func (dg *DependencyGraph) isDependencyReady(dep *Dependency) bool {
	// Check if all dependencies are satisfied
	for _, depID := range dep.DependsOn {
		if _, exists := dg.dependencies[depID]; exists {
			return false // Dependency still exists, not satisfied
		}
	}

	// Check if resource is in required state
	currentState := dg.resourceStates[dep.ResourceID]
	if dep.RequiredState != ResourceStateUnknown && currentState != dep.RequiredState {
		return false
	}

	return true
}

// wouldCreateCycle checks if adding a dependency would create a cycle
func (dg *DependencyGraph) wouldCreateCycle(newDep *Dependency) bool {
	// Simple cycle detection - check if any of the dependencies eventually depend on this one
	visited := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(id string) bool {
		if id == newDep.ID {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true

		if dep, exists := dg.dependencies[id]; exists {
			for _, depID := range dep.DependsOn {
				if hasCycle(depID) {
					return true
				}
			}
		}
		return false
	}

	for _, depID := range newDep.DependsOn {
		if hasCycle(depID) {
			return true
		}
	}

	return false
}

// DependencyManager manages resource dependencies for ENI operations
type DependencyManager struct {
	graph  *DependencyGraph
	logger logr.Logger
	mutex  sync.RWMutex
}

// NewDependencyManager creates a new dependency manager
func NewDependencyManager(logger logr.Logger) *DependencyManager {
	return &DependencyManager{
		graph:  NewDependencyGraph(logger),
		logger: logger.WithName("dependency-manager"),
	}
}

// CreateENIAttachmentDependencies creates dependencies for ENI attachment
func (dm *DependencyManager) CreateENIAttachmentDependencies(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	// Instance dependency
	instanceDep := &Dependency{
		ID:            fmt.Sprintf("instance-%s-%s", attachment.InstanceID, attachment.ENIID),
		Type:          DependencyTypeInstance,
		ResourceID:    attachment.InstanceID,
		DependsOn:     []string{},
		RequiredState: ResourceStateAvailable,
		Priority:      10,
		CreatedAt:     time.Now(),
		Metadata: map[string]interface{}{
			"nodeENI":   nodeENI.Name,
			"eniID":     attachment.ENIID,
			"operation": "attach",
		},
	}

	// Subnet dependency
	subnetDep := &Dependency{
		ID:            fmt.Sprintf("subnet-%s-%s", nodeENI.Spec.SubnetIDs[0], attachment.ENIID),
		Type:          DependencyTypeSubnet,
		ResourceID:    nodeENI.Spec.SubnetIDs[0],
		DependsOn:     []string{},
		RequiredState: ResourceStateAvailable,
		Priority:      8,
		CreatedAt:     time.Now(),
		Metadata: map[string]interface{}{
			"nodeENI":   nodeENI.Name,
			"eniID":     attachment.ENIID,
			"operation": "attach",
		},
	}

	// ENI dependency (depends on instance and subnet)
	eniDep := &Dependency{
		ID:            fmt.Sprintf("eni-%s", attachment.ENIID),
		Type:          DependencyTypeENI,
		ResourceID:    attachment.ENIID,
		DependsOn:     []string{instanceDep.ID, subnetDep.ID},
		RequiredState: ResourceStateAvailable,
		Priority:      5,
		CreatedAt:     time.Now(),
		Metadata: map[string]interface{}{
			"nodeENI":    nodeENI.Name,
			"instanceID": attachment.InstanceID,
			"operation":  "attach",
		},
	}

	// Add dependencies to graph
	if err := dm.graph.AddDependency(instanceDep); err != nil {
		return fmt.Errorf("failed to add instance dependency: %w", err)
	}
	if err := dm.graph.AddDependency(subnetDep); err != nil {
		return fmt.Errorf("failed to add subnet dependency: %w", err)
	}
	if err := dm.graph.AddDependency(eniDep); err != nil {
		return fmt.Errorf("failed to add ENI dependency: %w", err)
	}

	dm.logger.Info("Created ENI attachment dependencies",
		"nodeENI", nodeENI.Name,
		"eniID", attachment.ENIID,
		"instanceID", attachment.InstanceID)

	return nil
}

// CreateENIDetachmentDependencies creates dependencies for ENI detachment
func (dm *DependencyManager) CreateENIDetachmentDependencies(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, attachment networkingv1alpha1.ENIAttachment) error {
	dm.mutex.Lock()
	defer dm.mutex.Unlock()

	// DPDK unbind dependency (if applicable)
	var dependsOn []string
	if nodeENI.Spec.EnableDPDK {
		dpdkDep := &Dependency{
			ID:            fmt.Sprintf("dpdk-unbind-%s", attachment.ENIID),
			Type:          DependencyTypeENI,
			ResourceID:    fmt.Sprintf("dpdk-%s", attachment.ENIID),
			DependsOn:     []string{},
			RequiredState: ResourceStateAvailable,
			Priority:      15,
			CreatedAt:     time.Now(),
			Metadata: map[string]interface{}{
				"nodeENI":   nodeENI.Name,
				"eniID":     attachment.ENIID,
				"operation": "dpdk-unbind",
			},
		}

		if err := dm.graph.AddDependency(dpdkDep); err != nil {
			return fmt.Errorf("failed to add DPDK dependency: %w", err)
		}
		dependsOn = append(dependsOn, dpdkDep.ID)
	}

	// ENI detachment dependency
	detachDep := &Dependency{
		ID:            fmt.Sprintf("detach-%s", attachment.ENIID),
		Type:          DependencyTypeENI,
		ResourceID:    attachment.ENIID,
		DependsOn:     dependsOn,
		RequiredState: ResourceStateInUse,
		Priority:      10,
		CreatedAt:     time.Now(),
		Metadata: map[string]interface{}{
			"nodeENI":      nodeENI.Name,
			"instanceID":   attachment.InstanceID,
			"attachmentID": attachment.AttachmentID,
			"operation":    "detach",
		},
	}

	if err := dm.graph.AddDependency(detachDep); err != nil {
		return fmt.Errorf("failed to add detachment dependency: %w", err)
	}

	dm.logger.Info("Created ENI detachment dependencies",
		"nodeENI", nodeENI.Name,
		"eniID", attachment.ENIID,
		"dpdkEnabled", nodeENI.Spec.EnableDPDK)

	return nil
}

// UpdateResourceState updates the state of a resource
func (dm *DependencyManager) UpdateResourceState(resourceID string, state ResourceState) {
	dm.graph.UpdateResourceState(resourceID, state)
}

// GetReadyOperations returns operations that are ready to be executed
func (dm *DependencyManager) GetReadyOperations() []*Dependency {
	return dm.graph.GetReadyDependencies()
}

// CompleteDependency marks a dependency as completed
func (dm *DependencyManager) CompleteDependency(dependencyID string) {
	dm.graph.RemoveDependency(dependencyID)
}

// GetDependencyStatus returns the current status of all dependencies
func (dm *DependencyManager) GetDependencyStatus() map[string]interface{} {
	dm.mutex.RLock()
	defer dm.mutex.RUnlock()

	ready := dm.graph.GetReadyDependencies()
	blocked := dm.graph.GetBlockedDependencies()
	conflicts := dm.graph.GetDependencyConflicts()

	return map[string]interface{}{
		"ready":     len(ready),
		"blocked":   len(blocked),
		"conflicts": conflicts,
		"total":     len(dm.graph.dependencies),
	}
}
