package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMaterializeStackKitInventoryPublishesCanonicalProtectedDocument(t *testing.T) {
	workDir := t.TempDir()
	raw := []byte(" { \"nodes\": {}, \"schemaVersion\": \"stackkit.inventory/v1\" } \n")
	if err := materializeStackKitInventory(workDir, raw); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, ".stackkit", "inventory.json")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "{\"nodes\":{},\"schemaVersion\":\"stackkit.inventory/v1\"}\n" {
		t.Fatalf("stored Inventory = %q", stored)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("Inventory permissions = %o, want no group/other access", info.Mode().Perm())
	}
	replacement := []byte(`{"schemaVersion":"stackkit.inventory/v1","nodes":{"main":{"arch":"amd64"}}}`)
	if err := materializeStackKitInventory(workDir, replacement); err != nil {
		t.Fatalf("replace Inventory: %v", err)
	}
	stored, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "{\"nodes\":{\"main\":{\"arch\":\"amd64\"}},\"schemaVersion\":\"stackkit.inventory/v1\"}\n" {
		t.Fatalf("replaced Inventory = %q", stored)
	}
}

func TestMaterializeStackKitInventoryRejectsInvalidContractBeforeWriting(t *testing.T) {
	for name, raw := range map[string][]byte{
		"invalid-json":  []byte("{"),
		"wrong-version": []byte(`{"schemaVersion":"stackkit.inventory/v2"}`),
		"trailing":      []byte(`{"schemaVersion":"stackkit.inventory/v1"} {}`),
		"oversized":     []byte(`{"schemaVersion":"stackkit.inventory/v1","padding":"` + strings.Repeat("x", maxStackKitInventoryBytes) + `"}`),
	} {
		t.Run(name, func(t *testing.T) {
			workDir := t.TempDir()
			if err := materializeStackKitInventory(workDir, raw); err == nil {
				t.Fatal("invalid Inventory was accepted")
			}
			if _, err := os.Stat(filepath.Join(workDir, ".stackkit", "inventory.json")); !os.IsNotExist(err) {
				t.Fatalf("invalid Inventory created a persisted document: %v", err)
			}
		})
	}
}
