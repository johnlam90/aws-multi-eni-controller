package mapping

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewInterfaceMappingStore(t *testing.T) {
	// Create temporary directory for testing
	tempDir, err := os.MkdirTemp("", "interface_mapping_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storePath := filepath.Join(tempDir, "mappings.json")
	store, err := NewInterfaceMappingStore(storePath)
	if err != nil {
		t.Fatalf("Failed to create interface mapping store: %v", err)
	}

	if store == nil {
		t.Fatal("Expected non-nil interface mapping store")
	}

	if store.filePath != storePath {
		t.Errorf("Expected file path %s, got %s", storePath, store.filePath)
	}
}

func TestInterfaceMappingStore_AddMapping(t *testing.T) {
	// Create temporary directory for testing
	tempDir, err := os.MkdirTemp("", "interface_mapping_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storePath := filepath.Join(tempDir, "mappings.json")
	store, err := NewInterfaceMappingStore(storePath)
	if err != nil {
		t.Fatalf("Failed to create interface mapping store: %v", err)
	}

	mapping := InterfaceMapping{
		ENIID:       "eni-123456789abcdef0",
		PCIAddress:  "0000:00:06.0",
		IfaceName:   "eth1",
		DeviceIndex: 1,
		DPDKBound:   false,
		DPDKDriver:  "",
		NodeENIName: "test-nodeeni",
	}

	err = store.AddMapping(mapping)
	if err != nil {
		t.Fatalf("Failed to add mapping: %v", err)
	}

	// Verify mapping was added
	retrieved, exists := store.GetMappingByENIID("eni-123456789abcdef0")
	if !exists {
		t.Fatal("Expected mapping to exist")
	}

	if retrieved.ENIID != mapping.ENIID {
		t.Errorf("Expected ENI ID %s, got %s", mapping.ENIID, retrieved.ENIID)
	}
}
