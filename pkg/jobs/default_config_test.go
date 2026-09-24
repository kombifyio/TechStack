package jobs

import (
	"path/filepath"
	"testing"
)

func TestDefaultProvisionConfigResolvesSpecBaseDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	t.Setenv("TECHSTACK_DATA_DIR", dataDir)
	t.Setenv("TECHSTACK_SPEC_BASE_DIR", "")
	if got, want := DefaultProvisionConfig().SpecBaseDir, filepath.Join(dataDir, "stacks"); got != want {
		t.Fatalf("fallback SpecBaseDir = %q, want %q", got, want)
	}

	explicit := filepath.Join(t.TempDir(), "specs")
	t.Setenv("TECHSTACK_SPEC_BASE_DIR", explicit)
	if got, want := DefaultProvisionConfig().SpecBaseDir, filepath.Clean(explicit); got != want {
		t.Fatalf("explicit SpecBaseDir = %q, want %q", got, want)
	}
}
