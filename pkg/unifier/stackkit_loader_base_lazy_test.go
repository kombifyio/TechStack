package unifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelang.org/go/cue"
)

// newBaseKitFixture writes a minimal StackKits checkout: a base directory and
// one kit that references it, which is the shape loadKitFromDir expects.
func newBaseKitFixture(t *testing.T, baseCUE string) string {
	t.Helper()
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "schema.cue"), []byte(baseCUE), 0o600); err != nil {
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

const validBaseCUE = "#Service: {\n\tname: string\n\tenabled: bool | *true\n}\n"

// Evaluating the base standalone produced a second copy of the whole base
// schema graph that nothing reads, and a cue.Context retains everything
// compiled in it, so that copy lived as long as the loader. On the real
// checkout it cost ~190MB and pushed the generate peak from 398MB to 591MB,
// which is what OOM-killed rollouts on a 512Mi instance.
func TestLoaderDoesNotEvaluateTheBaseEagerly(t *testing.T) {
	root := newBaseKitFixture(t, validBaseCUE)

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir: %v", err)
	}
	if !loader.baseLoaded {
		t.Fatalf("base should be loaded, lastError=%v", loader.lastError)
	}
	if loader.baseSource == "" {
		t.Fatal("base source must still be recorded for kit concatenation")
	}
	if loader.baseKit.Exists() {
		t.Fatal("base was evaluated eagerly; that is the allocation this change removes")
	}
}

// The value is still available to anyone who asks, just paid for on demand.
func TestGetBaseKitEvaluatesLazilyAndCaches(t *testing.T) {
	root := newBaseKitFixture(t, validBaseCUE)
	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir: %v", err)
	}

	first := loader.GetBaseKit()
	if !first.Exists() {
		t.Fatal("GetBaseKit returned an empty value")
	}
	if !loader.baseKit.Exists() {
		t.Fatal("GetBaseKit must cache the evaluated base")
	}
	if second := loader.GetBaseKit(); !second.Exists() {
		t.Fatal("second GetBaseKit returned an empty value")
	}
}

// Dropping the eager evaluation must not drop the eager error. A broken base
// should fail when the loader is built, not inside the first rollout.
func TestLoaderStillRejectsAMalformedBaseAtLoadTime(t *testing.T) {
	root := newBaseKitFixture(t, "#Service: {\n\tname: string\n") // unbalanced brace

	loader, err := NewStackKitLoaderWithDir(root)
	if err != nil {
		t.Fatalf("NewStackKitLoaderWithDir returned a hard error: %v", err)
	}
	if loader.baseLoaded {
		t.Fatal("a malformed base must not report as loaded")
	}
	if loader.lastError == nil {
		t.Fatal("a malformed base must record why it failed")
	}
	if !strings.Contains(strings.ToLower(loader.lastError.Error()), "parse") {
		t.Fatalf("lastError should name the parse failure, got %v", loader.lastError)
	}
}

// The kit path is what actually consumes the base source, so it must still
// resolve base definitions.
func TestLoadKitStillCombinesTheBaseSource(t *testing.T) {
	root := newBaseKitFixture(t, validBaseCUE)
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
		t.Fatal("combined kit lost the base definitions")
	}
}
