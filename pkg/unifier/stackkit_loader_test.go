package unifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	_, err = loader.GetKitInfo("test-kit")
	if err == nil {
		t.Fatal("expected symlink metadata file to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink rejection", err)
	}
}
