package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultProvisionConfigUsesTechStackDataDirForSpecs(t *testing.T) {
	t.Setenv("TECHSTACK_SPEC_BASE_DIR", "")
	t.Setenv("TECHSTACK_DATA_DIR", filepath.Join(t.TempDir(), "data"))

	cfg := DefaultProvisionConfig()

	want := filepath.Join(os.Getenv("TECHSTACK_DATA_DIR"), "stacks")
	if cfg.SpecBaseDir != want {
		t.Fatalf("SpecBaseDir = %q, want %q", cfg.SpecBaseDir, want)
	}
}

func TestDefaultProvisionConfigHonorsExplicitSpecBaseDir(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "specs")
	t.Setenv("TECHSTACK_SPEC_BASE_DIR", explicit)
	t.Setenv("TECHSTACK_DATA_DIR", filepath.Join(t.TempDir(), "data"))

	cfg := DefaultProvisionConfig()

	if cfg.SpecBaseDir != filepath.Clean(explicit) {
		t.Fatalf("SpecBaseDir = %q, want %q", cfg.SpecBaseDir, filepath.Clean(explicit))
	}
}
