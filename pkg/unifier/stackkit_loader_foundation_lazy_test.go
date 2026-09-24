package unifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelang.org/go/cue"
)

// newFoundationFixture writes a minimal StackKits checkout: a foundation
// directory and one kit that references it.
func newFoundationFixture(t *testing.T, foundationCUE string) string {
	t.Helper()
	root := t.TempDir()
	foundationDir := filepath.Join(root, "foundation")
	if err := os.MkdirAll(foundationDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foundationDir, "schema.cue"), []byte(foundationCUE), 0o600); err != nil {
		t.Fatal(err)
	}
	kitDir := filepath.Join(root, "cloud-kit")
	if err := os.MkdirAll(kitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	kitCUE := "#Kit: {\n\tname: string\n}\n\nkit: #Kit & {name: \"cloud-kit\"}\n"
	if err := os.WriteFile(filepath.Join(kitDir, "kit.cue"), []byte(kitCUE), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

const validFoundationCUE = "#Service: {\n\tname: string\n\tenabled: bool | *true\n}\n"

func TestLoaderDoesNotEvaluateTheFoundationEagerly(t *testing.T) {
	root := newFoundationFixture(t, validFoundationCUE)

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir: %v", err)
	}
	if !loader.foundationLoaded {
		t.Fatalf("foundation should be loaded, lastError=%v", loader.lastError)
	}
	if loader.foundationSource == "" {
		t.Fatal("foundation source must still be recorded for kit concatenation")
	}
	if loader.foundation.Exists() {
		t.Fatal("foundation was evaluated eagerly; that is the allocation this change removes")
	}
}

func TestGetFoundationEvaluatesLazilyAndCaches(t *testing.T) {
	root := newFoundationFixture(t, validFoundationCUE)
	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir: %v", err)
	}

	first := loader.GetFoundation()
	if !first.Exists() {
		t.Fatal("GetFoundation returned an empty value")
	}
	if !loader.foundation.Exists() {
		t.Fatal("GetFoundation must cache the evaluated foundation")
	}
	if second := loader.GetFoundation(); !second.Exists() {
		t.Fatal("second GetFoundation returned an empty value")
	}
}

func TestLoaderStillRejectsAMalformedFoundationAtLoadTime(t *testing.T) {
	root := newFoundationFixture(t, "#Service: {\n\tname: string\n") // unbalanced brace

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir returned a hard error: %v", err)
	}
	if loader.foundationLoaded {
		t.Fatal("a malformed foundation must not report as loaded")
	}
	if loader.lastError == nil {
		t.Fatal("a malformed foundation must record why it failed")
	}
	if !strings.Contains(strings.ToLower(loader.lastError.Error()), "parse") {
		t.Fatalf("lastError should name the parse failure, got %v", loader.lastError)
	}
}

func TestLoadKitStillCombinesTheFoundationSource(t *testing.T) {
	root := newFoundationFixture(t, validFoundationCUE)
	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir: %v", err)
	}
	kit, kitErr := loader.LoadKit("cloud-kit")
	if kitErr != nil {
		t.Fatalf("LoadKit: %v", kitErr)
	}
	if !kit.Exists() {
		t.Fatal("LoadKit returned an empty value")
	}
	if !kit.LookupPath(cue.ParsePath("#Service")).Exists() {
		t.Fatal("combined kit lost the foundation definitions")
	}
}
