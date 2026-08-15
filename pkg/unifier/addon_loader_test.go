package unifier

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestAddonSchemaLoader_NewLoader(t *testing.T) {
	loader := NewAddonSchemaLoader()
	if loader == nil {
		t.Fatal("Expected non-nil loader")
	}
	if loader.ctx == nil {
		t.Error("Expected CUE context to be initialized")
	}
	if loader.schemas == nil {
		t.Error("Expected schemas map to be initialized")
	}
}

func TestAddonSchemaLoader_LoadFromPath(t *testing.T) {
	// Set up debug logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

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

	t.Logf("Test dir: %s", tmpDir)

	// Load from path
	err = loader.LoadFromPath(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromPath failed: %v", err)
	}

	// Verify test addon was loaded
	names := loader.ListNames()
	t.Logf("Loaded addon names: %v", names)

	if len(names) != 1 {
		t.Errorf("Expected 1 addon, got %d", len(names))
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

func TestAddonSchemaLoader_MergeAddons(t *testing.T) {
	loader := NewAddonSchemaLoader()
	tmpDir := t.TempDir()

	// Write two compatible addons
	addon1 := `
package addon
addon1: {
    metadata: {
        name: "addon1"
        displayName: "Addon 1"
        description: "First addon"
    }
    spec: {
        field1: "value1"
    }
}
`
	addon2 := `
package addon
addon2: {
    metadata: {
        name: "addon2"
        displayName: "Addon 2"
        description: "Second addon"
    }
    spec: {
        field2: "value2"
    }
}
`
	os.WriteFile(filepath.Join(tmpDir, "addon1.cue"), []byte(addon1), 0644)
	os.WriteFile(filepath.Join(tmpDir, "addon2.cue"), []byte(addon2), 0644)

	err := loader.LoadFromPath(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromPath failed: %v", err)
	}

	merged, err := loader.MergeAddons([]string{"addon1", "addon2"})
	if err != nil {
		t.Fatalf("MergeAddons failed: %v", err)
	}

	if !merged.Exists() {
		t.Error("Expected merged value to exist")
	}
}

func TestAddonSchemaLoader_Get_NotFound(t *testing.T) {
	loader := NewAddonSchemaLoader()

	_, ok := loader.Get("nonexistent")
	if ok {
		t.Error("Expected false for nonexistent addon")
	}
}

func TestAddonSchemaLoader_Count(t *testing.T) {
	loader := NewAddonSchemaLoader()

	if loader.Count() != 0 {
		t.Errorf("Expected 0 addons initially, got %d", loader.Count())
	}

	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "test.cue"), []byte(`package addon
test: {
	name: "test"
}
`), 0644)

	loader.LoadFromPath(tmpDir)

	if loader.Count() != 1 {
		t.Errorf("Expected 1 addon after loading, got %d", loader.Count())
	}
}

func TestAddonSchemaLoader_GetAll(t *testing.T) {
	loader := NewAddonSchemaLoader()
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "a.cue"), []byte(`package addon
a: {
	name: "a"
}
`), 0644)
	os.WriteFile(filepath.Join(tmpDir, "b.cue"), []byte(`package addon
b: {
	name: "b"
}
`), 0644)

	loader.LoadFromPath(tmpDir)

	all := loader.GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 addons, got %d", len(all))
	}
}
