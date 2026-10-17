package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveStateDirKeepsLegacyAndNeverCreatesIt(t *testing.T) {
	legacyRoot := t.TempDir()
	legacy := filepath.Join(legacyRoot, "pb_data")
	if err := os.MkdirAll(filepath.Join(legacy, "agent-grpc-tls"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got := ResolveStateDir(legacyRoot); got != legacy {
		t.Fatalf("existing legacy data must keep being used, got %s", got)
	}

	freshRoot := t.TempDir()
	t.Setenv("TECHSTACK_DATA_DIR", freshRoot)
	t.Setenv("TECHSTACK_STATE_DIR", "")
	t.Setenv("TECHSTACK_PB_DATA_DIR", "")
	got := StateDirFromEnv()
	if got != filepath.Join(freshRoot, "state") {
		t.Fatalf("fresh install must use the neutral directory, got %s", got)
	}
	if _, err := os.Stat(filepath.Join(freshRoot, "pb_data")); !os.IsNotExist(err) {
		t.Fatalf("legacy directory must not be created, stat err = %v", err)
	}

	t.Setenv("TECHSTACK_PB_DATA_DIR", "/custom/alias")
	if got := StateDirFromEnv(); got != "/custom/alias" {
		t.Fatalf("deprecated alias must still be honored, got %s", got)
	}
}
