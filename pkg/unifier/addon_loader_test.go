package unifier

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddonSchemaLoader_LoadFromPath(t *testing.T) {
	loader := NewAddonSchemaLoader()

	// Create temp directory with test CUE files
	tmpDir := t.TempDir()

	// Write base schema - simplified
	baseContent := `package addon
`
	err := os.WriteFile(filepath.Join(tmpDir, "addon.cue"), []byte(baseContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write base schema: %v", err)
	}

	// Write a test addon - simplified valid CUE
	addonContent := `package addon

testAddon: {
	metadata: {
		name: "test-addon"
		displayName: "Test Addon"
		description: "A test addon for unit tests"
	}
}
`
	err = os.WriteFile(filepath.Join(tmpDir, "test-addon.cue"), []byte(addonContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test addon: %v", err)
	}

	// Load from path
	err = loader.LoadFromPath(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromPath failed: %v", err)
	}

	// Get the loaded addon
	val, ok := loader.Get("test-addon")
	if !ok {
		t.Error("Expected test-addon to be loaded")
	} else if !val.Exists() {
		t.Error("Expected valid CUE value")
	}
}

func TestAddonSchemaLoader_GetLoadedAddon(t *testing.T) {
	loader := NewAddonSchemaLoader()

	// Create temp directory with test CUE files
	tmpDir := t.TempDir()

	// Write addon with full metadata
	addonContent := `
package addon

myAddon: {
    metadata: {
        name: "my-addon"
        displayName: "My Addon"
        description: "Description of my addon"
        version: "2.0.0"
        priority: 75
        tags: ["test", "unit"]
    }
    spec: {}
}
`
	err := os.WriteFile(filepath.Join(tmpDir, "my-addon.cue"), []byte(addonContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write addon: %v", err)
	}

	err = loader.LoadFromPath(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromPath failed: %v", err)
	}

	addon, err := loader.GetLoadedAddon("my-addon")
	if err != nil {
		t.Fatalf("GetLoadedAddon failed: %v", err)
	}

	if addon.Name != "my-addon" {
		t.Errorf("Expected name 'my-addon', got '%s'", addon.Name)
	}
	if addon.DisplayName != "My Addon" {
		t.Errorf("Expected displayName 'My Addon', got '%s'", addon.DisplayName)
	}
	if addon.Description != "Description of my addon" {
		t.Errorf("Expected description mismatch")
	}
	if addon.Version != "2.0.0" {
		t.Errorf("Expected version '2.0.0', got '%s'", addon.Version)
	}
	if addon.Priority != 75 {
		t.Errorf("Expected priority 75, got %d", addon.Priority)
	}
	if len(addon.Tags) != 2 {
		t.Errorf("Expected 2 tags, got %d", len(addon.Tags))
	}
}

func TestAddonSchemaLoader_Get_NotFound(t *testing.T) {
	loader := NewAddonSchemaLoader()

	_, ok := loader.Get("nonexistent")
	if ok {
		t.Error("Expected false for nonexistent addon")
	}
}
