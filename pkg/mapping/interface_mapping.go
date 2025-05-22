// Package mapping provides utilities for mapping between ENI IDs, interface names, and PCI addresses
package mapping

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// InterfaceMapping represents the mapping between ENI IDs, interface names, and PCI addresses
type InterfaceMapping struct {
	ENIID       string `json:"eniID"`
	IfaceName   string `json:"ifaceName"`
	PCIAddress  string `json:"pciAddress"`
	DeviceIndex int    `json:"deviceIndex"`
	DPDKBound   bool   `json:"dpdkBound"`
	DPDKDriver  string `json:"dpdkDriver"`
	NodeENIName string `json:"nodeENIName"`
}

// InterfaceMappingStore is a persistent store for interface mappings
type InterfaceMappingStore struct {
	mappings map[string]InterfaceMapping // Key is ENI ID
	pciMap   map[string]string           // Maps PCI address to ENI ID
	ifaceMap map[string]string           // Maps interface name to ENI ID
	filePath string
	mutex    sync.RWMutex
}

// NewInterfaceMappingStore creates a new interface mapping store
func NewInterfaceMappingStore(filePath string) (*InterfaceMappingStore, error) {
	store := &InterfaceMappingStore{
		mappings: make(map[string]InterfaceMapping),
		pciMap:   make(map[string]string),
		ifaceMap: make(map[string]string),
		filePath: filePath,
	}

	// Create the directory if it doesn't exist
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for interface mapping store: %v", err)
	}

	// Load existing mappings if the file exists
	if _, err := os.Stat(filePath); err == nil {
		if err := store.load(); err != nil {
			log.Printf("Warning: Failed to load interface mappings from %s: %v", filePath, err)
			// Continue with an empty store
		}
	}

	return store, nil
}

// AddMapping adds a new mapping to the store
func (s *InterfaceMappingStore) AddMapping(mapping InterfaceMapping) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Add to the main map
	s.mappings[mapping.ENIID] = mapping

	// Update the lookup maps
	if mapping.PCIAddress != "" {
		s.pciMap[mapping.PCIAddress] = mapping.ENIID
	}
	if mapping.IfaceName != "" {
		s.ifaceMap[mapping.IfaceName] = mapping.ENIID
	}

	// Save to disk
	return s.save()
}

// GetMappingByENIID gets a mapping by ENI ID
func (s *InterfaceMappingStore) GetMappingByENIID(eniID string) (InterfaceMapping, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	mapping, exists := s.mappings[eniID]
	return mapping, exists
}

// GetMappingByPCIAddress gets a mapping by PCI address
func (s *InterfaceMappingStore) GetMappingByPCIAddress(pciAddress string) (InterfaceMapping, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	eniID, exists := s.pciMap[pciAddress]
	if !exists {
		return InterfaceMapping{}, false
	}

	mapping, exists := s.mappings[eniID]
	return mapping, exists
}

// GetMappingByInterfaceName gets a mapping by interface name
func (s *InterfaceMappingStore) GetMappingByInterfaceName(ifaceName string) (InterfaceMapping, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	eniID, exists := s.ifaceMap[ifaceName]
	if !exists {
		return InterfaceMapping{}, false
	}

	mapping, exists := s.mappings[eniID]
	return mapping, exists
}

// UpdateMapping updates an existing mapping
func (s *InterfaceMappingStore) UpdateMapping(mapping InterfaceMapping) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Check if the mapping exists
	_, exists := s.mappings[mapping.ENIID]
	if !exists {
		return fmt.Errorf("mapping for ENI ID %s does not exist", mapping.ENIID)
	}

	// Update the mapping
	s.mappings[mapping.ENIID] = mapping

	// Update the lookup maps
	if mapping.PCIAddress != "" {
		s.pciMap[mapping.PCIAddress] = mapping.ENIID
	}
	if mapping.IfaceName != "" {
		s.ifaceMap[mapping.IfaceName] = mapping.ENIID
	}

	// Save to disk
	return s.save()
}

// RemoveMapping removes a mapping from the store
func (s *InterfaceMappingStore) RemoveMapping(eniID string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Check if the mapping exists
	mapping, exists := s.mappings[eniID]
	if !exists {
		return fmt.Errorf("mapping for ENI ID %s does not exist", eniID)
	}

	// Remove from the lookup maps
	if mapping.PCIAddress != "" {
		delete(s.pciMap, mapping.PCIAddress)
	}
	if mapping.IfaceName != "" {
		delete(s.ifaceMap, mapping.IfaceName)
	}

	// Remove from the main map
	delete(s.mappings, eniID)

	// Save to disk
	return s.save()
}

// GetAllMappings returns all mappings in the store
func (s *InterfaceMappingStore) GetAllMappings() []InterfaceMapping {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	mappings := make([]InterfaceMapping, 0, len(s.mappings))
	for _, mapping := range s.mappings {
		mappings = append(mappings, mapping)
	}

	return mappings
}

// save saves the mappings to disk
func (s *InterfaceMappingStore) save() error {
	// Convert the mappings to a slice for JSON serialization
	mappings := make([]InterfaceMapping, 0, len(s.mappings))
	for _, mapping := range s.mappings {
		mappings = append(mappings, mapping)
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal interface mappings to JSON: %v", err)
	}

	// Write to file
	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write interface mappings to file: %v", err)
	}

	return nil
}

// load loads the mappings from disk
func (s *InterfaceMappingStore) load() error {
	// Read the file
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to read interface mappings from file: %v", err)
	}

	// Unmarshal from JSON
	var mappings []InterfaceMapping
	if err := json.Unmarshal(data, &mappings); err != nil {
		return fmt.Errorf("failed to unmarshal interface mappings from JSON: %v", err)
	}

	// Clear existing mappings
	s.mappings = make(map[string]InterfaceMapping)
	s.pciMap = make(map[string]string)
	s.ifaceMap = make(map[string]string)

	// Add the mappings
	for _, mapping := range mappings {
		s.mappings[mapping.ENIID] = mapping
		if mapping.PCIAddress != "" {
			s.pciMap[mapping.PCIAddress] = mapping.ENIID
		}
		if mapping.IfaceName != "" {
			s.ifaceMap[mapping.IfaceName] = mapping.ENIID
		}
	}

	return nil
}
