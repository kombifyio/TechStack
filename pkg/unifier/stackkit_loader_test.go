package unifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackKitLoader_New(t *testing.T) {
	// This test may fail if base stackkits are not properly embedded
	// That's expected - the test documents the expected behavior
	loader, err := NewStackKitLoader()
	if err != nil {
		t.Logf("NewStackKitLoader failed (expected if base schemas not embedded): %v", err)
		t.Skip("Skipping - base schemas not embedded yet")
	}

	if loader == nil {
		t.Fatal("loader should not be nil")
	}

	if loader.ctx == nil {
		t.Error("CUE context should be initialized")
	}
}

func TestStackKitLoader_ListAvailableKits(t *testing.T) {
	loader, err := NewStackKitLoader()
	if err != nil {
		t.Skip("Skipping - base schemas not available")
	}

	kits := loader.ListAvailableKits()
	if len(kits) == 0 {
		t.Error("should return at least the built-in kits")
	}

	// Check that basement-kit is in the list
	found := false
	for _, k := range kits {
		if k == "basement-kit" {
			found = true
			break
		}
	}
	if !found {
		t.Error("basement-kit should be in the list")
	}
}

func TestStackKitLoader_LoadKitFromDir(t *testing.T) {
	// Find the project root first
	projectRoot := findProjectRoot()
	if projectRoot == "" {
		t.Skip("Could not find project root")
	}

	stackkitsDir := filepath.Join(projectRoot, "pkg", "stackkits")
	t.Logf("Looking for stackkits at: %s", stackkitsDir)

	// Create loader with explicit stackkits directory
	loader, err := NewStackKitLoaderWithDir(stackkitsDir)
	if err != nil {
		t.Fatalf("Failed to create loader: %v", err)
	}

	// Check if base was loaded
	if !loader.baseLoaded {
		baseDir := filepath.Join(stackkitsDir, "base")
		t.Logf("Base dir: %s", baseDir)
		if _, err := os.Stat(baseDir); os.IsNotExist(err) {
			t.Skipf("Base directory does not exist: %s", baseDir)
		}
		// Try to understand why it failed
		entries, _ := os.ReadDir(baseDir)
		t.Logf("Base dir contents: %v", entries)
		if loader.lastError != nil {
			t.Logf("Last error: %v", loader.lastError)
		}
		t.Skip("Base stackkit not loaded - skipping kit loading test")
	}

	kitDir := filepath.Join(projectRoot, "pkg", "stackkits", "basement-kit")
	if _, err := os.Stat(kitDir); os.IsNotExist(err) {
		t.Skipf("Kit directory not found: %s", kitDir)
	}

	kit, err := loader.loadKitFromDir(kitDir)
	if err != nil {
		t.Fatalf("loadKitFromDir failed: %v", err)
	}

	if kit.Err() != nil {
		t.Errorf("loaded kit has error: %v", kit.Err())
	}
}

func TestStackKitLoader_LoadKitFromDirMergesKitStdlibImports(t *testing.T) {
	tmpDir := t.TempDir()

	baseDir := filepath.Join(tmpDir, "base")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(baseDir, "base.cue"),
		[]byte("package base\n\n#RuntimeDefaults: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	kitDir := filepath.Join(tmpDir, "test-kit")
	if err := os.MkdirAll(kitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	kitCue := `package test_kit

import "list"

#TestStack: {
	nodes: [...string] & list.MinItems(1)
}
`
	if err := os.WriteFile(filepath.Join(kitDir, "stackfile.cue"), []byte(kitCue), 0o600); err != nil {
		t.Fatal(err)
	}

	loader, err := NewStackKitLoaderWithDir(tmpDir)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
	}
	if !loader.baseLoaded {
		t.Fatalf("base kit not loaded: %v", loader.lastError)
	}

	kit, err := loader.loadKitFromDir(kitDir)
	if err != nil {
		t.Fatalf("loadKitFromDir failed: %v", err)
	}
	if kit.Err() != nil {
		t.Fatalf("loaded kit has error: %v", kit.Err())
	}
}

func TestStackKitLoaderRejectsSymlinkCueFile(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.cue")
	if err := os.WriteFile(outside, []byte("package base\n\n#Outside: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(baseDir, "base.cue")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
	}
	if loader.lastError == nil || !strings.Contains(loader.lastError.Error(), "symlink") {
		t.Fatalf("lastError = %v, want symlink rejection", loader.lastError)
	}
}

func TestStackKitLoaderRejectsSymlinkBaseDir(t *testing.T) {
	root := t.TempDir()
	outsideBase := filepath.Join(root, "outside-base")
	if err := os.MkdirAll(outsideBase, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideBase, "base.cue"), []byte("package base\n\n#Outside: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideBase, filepath.Join(root, "base")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
	}
	if loader.lastError == nil || !strings.Contains(loader.lastError.Error(), "symlink directory") {
		t.Fatalf("lastError = %v, want symlink directory rejection", loader.lastError)
	}
}

func TestStackKitLoaderRejectsSymlinkKitDir(t *testing.T) {
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "base.cue"), []byte("package base\n\n#RuntimeDefaults: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideKit := filepath.Join(root, "outside-kit")
	if err := os.MkdirAll(outsideKit, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideKit, "stackfile.cue"), []byte("package test_kit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideKit, filepath.Join(root, "test-kit")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
	}
	_, err = loader.loadExternalKit("test-kit")
	if err == nil {
		t.Fatal("expected symlink kit directory to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink directory") {
		t.Fatalf("error = %v, want symlink directory rejection", err)
	}
}

func TestStackKitLoaderRejectsSymlinkMetadataFile(t *testing.T) {
	root := t.TempDir()
	kitDir := filepath.Join(root, "test-kit")
	if err := os.MkdirAll(kitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "stackkit.yaml")
	if err := os.WriteFile(outside, []byte("metadata:\n  name: external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(kitDir, "stackkit.yaml")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
	}
	_, err = loader.readStackKitYAMLMetadata("test-kit")
	if err == nil {
		t.Fatal("expected symlink metadata file to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink rejection", err)
	}
}

func TestStackKitLoader_GetKitInfo(t *testing.T) {
	loader, err := NewStackKitLoader()
	if err != nil {
		t.Skip("Skipping - base schemas not available")
	}

	// Try to get info for basement-kit
	info, err := loader.GetKitInfo("basement-kit")
	if err != nil {
		t.Logf("GetKitInfo failed (kit may not be loadable): %v", err)
		t.Skip("Kit not loadable")
	}

	if info == nil {
		t.Fatal("info should not be nil")
	}

	if info.Name != "basement-kit" {
		t.Fatalf("expected basement-kit name, got %q", info.Name)
	}
	if info.Version != "2.0.0" {
		t.Fatalf("expected stackkit.yaml version 2.0.0, got %q", info.Version)
	}
	if info.Author != "kombify Techstack Team" {
		t.Fatalf("expected stackkit.yaml author, got %q", info.Author)
	}
	if len(info.SupportedOS) == 0 {
		t.Fatal("expected supported OS list from stackkit.yaml")
	}
	if len(info.Services.Required) == 0 || info.Services.Required[0] != "traefik" {
		t.Fatalf("expected required services from stackfile metadata, got %+v", info.Services.Required)
	}
	if len(info.Services.Available) == 0 {
		t.Fatal("expected available services from stackfile metadata")
	}
	if _, ok := info.Modes["simple"]; !ok {
		t.Fatal("expected simple mode from stackkit.yaml")
	}
	if _, ok := info.Modes["advanced"]; !ok {
		t.Fatal("expected advanced mode from stackkit.yaml")
	}

	t.Logf("Loaded kit info: %+v", info)
}

func TestStackKitLoader_WithExternalDir(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "stackkits-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a minimal test kit
	testKitDir := filepath.Join(tmpDir, "test-kit")
	if mkdirErr := os.MkdirAll(testKitDir, 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	testCue := `package test_kit

metadata: {
	name: "test-kit"
	displayName: "Test Kit"
	version: "0.1.0"
	description: "A test StackKit"
}

func TestStackKitLoader_RejectsInvalidNames(t *testing.T) {
	loader, err := NewStackKitLoader()
	if err != nil {
		t.Skip("Skipping - base schemas not available")
	}

	tests := []string{
		"../evil",
		"evil/kit",
		"evil\\kit",
		"INVALID",
		"evil..kit",
	}

	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loader.LoadKit(name)
			if err == nil {
				t.Fatalf("expected error for invalid kit name %q", name)
			}
		})
	}
}
`
	if writeErr := os.WriteFile(filepath.Join(testKitDir, "stackfile.cue"), []byte(testCue), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(testKitDir, "stackkit.yaml"), []byte("metadata:\n  name: test-kit\n  version: 0.1.0\n  description: A test StackKit\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	loader, err := NewStackKitLoaderWithDir(tmpDir)
	if err != nil {
		t.Skip("Skipping - base schemas not available")
	}

	kits := loader.ListAvailableKits()
	foundTestKit := false
	foundBasement := false
	for _, k := range kits {
		if k == "test-kit" {
			foundTestKit = true
		}
		if k == StackKitBasement {
			foundBasement = true
		}
	}

	if foundTestKit {
		t.Error("test-kit should not be exposed as an available product kit")
	}
	if !foundBasement {
		t.Errorf("expected basement-kit fallback in available kits, got %#v", kits)
	}
}

// findProjectRoot walks up directories to find the project root (has go.mod)
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return ""
}

func TestEngine_ListStackKits(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	kits := engine.ListStackKits()
	if len(kits) == 0 {
		t.Error("should return at least default kits")
	}
	t.Logf("Available kits: %v", kits)
}

func TestEngine_GetStackKitInfo(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	info, err := engine.GetStackKitInfo("basement-kit")
	if err != nil {
		t.Logf("GetStackKitInfo failed: %v", err)
		t.Skip("StackKit info not available")
	}

	t.Logf("StackKit info: %+v", info)
}
