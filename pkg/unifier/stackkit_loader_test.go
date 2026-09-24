package unifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackKitLoader_LoadKitFromDirMergesKitStdlibImports(t *testing.T) {
	tmpDir := t.TempDir()

	foundationDir := filepath.Join(tmpDir, "foundation")
	if err := os.MkdirAll(foundationDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(foundationDir, "foundation.cue"),
		[]byte("package foundation\n\n#RuntimeDefaults: {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	kitDir := filepath.Join(tmpDir, "test-kit")
	if err := os.MkdirAll(kitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	kitCue := `package test_kit

import (
	// StackKit sources may alias standard-library imports.
	l "list"
	"struct"
)

#TestStack: {
	nodes:       [...string] & l.MinItems(1)
	matchLabels: {[string]: string} & struct.MinFields(1)
}
`
	if err := os.WriteFile(filepath.Join(kitDir, "stackfile.cue"), []byte(kitCue), 0o600); err != nil {
		t.Fatal(err)
	}

	loader, err := NewStackKitLoaderWithDir(tmpDir)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
	}
	if !loader.foundationLoaded {
		t.Fatalf("foundation not loaded: %v", loader.lastError)
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
	foundationDir := filepath.Join(root, "foundation")
	if err := os.MkdirAll(foundationDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.cue")
	if err := os.WriteFile(outside, []byte("package foundation\n\n#Outside: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(foundationDir, "foundation.cue")); err != nil {
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

func TestStackKitLoaderRejectsSymlinkFoundationDir(t *testing.T) {
	root := t.TempDir()
	outsideFoundation := filepath.Join(root, "outside-foundation")
	if err := os.MkdirAll(outsideFoundation, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideFoundation, "foundation.cue"), []byte("package foundation\n\n#Outside: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFoundation, filepath.Join(root, "foundation")); err != nil {
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
	foundationDir := filepath.Join(root, "foundation")
	if err := os.MkdirAll(foundationDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foundationDir, "foundation.cue"), []byte("package foundation\n\n#RuntimeDefaults: {}\n"), 0o600); err != nil {
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

`
	if writeErr := os.WriteFile(filepath.Join(testKitDir, "stackfile.cue"), []byte(testCue), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(testKitDir, "stackkit.yaml"), []byte("metadata:\n  name: test-kit\n  version: 0.1.0\n  description: A test StackKit\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	loader, err := NewStackKitLoaderWithDir(tmpDir)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir failed: %v", err)
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
