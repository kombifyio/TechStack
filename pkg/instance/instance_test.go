package instance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolverLoadCreatesIdentity(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir)
	id, err := r.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if id.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if id.CreatedAt.IsZero() {
		t.Fatal("expected non-zero CreatedAt")
	}
	if !fileExists(r.Path()) {
		t.Fatalf("instance.json not written at %s", r.Path())
	}
}

func TestResolverLoadIsStable(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir)
	first, err := r.Load()
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	second, err := r.Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("instance id changed across loads: %s vs %s", first.ID, second.ID)
	}
	if !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("created_at changed across loads")
	}

	// New resolver against the same dir must observe the same identity.
	third, err := NewResolver(dir).Load()
	if err != nil {
		t.Fatalf("third Load: %v", err)
	}
	if third.ID != first.ID {
		t.Fatalf("instance id changed across resolver instances: %s vs %s", first.ID, third.ID)
	}
}

func TestResolverLoadRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := NewResolver(dir).Load(); err == nil {
		t.Fatal("expected error for corrupt instance.json")
	}
}

func TestResolverLoadRejectsEmptyID(t *testing.T) {
	dir := t.TempDir()
	payload, _ := json.Marshal(Identity{})
	if err := os.WriteFile(filepath.Join(dir, fileName), payload, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := NewResolver(dir).Load()
	if err == nil || !strings.Contains(err.Error(), "empty id") {
		t.Fatalf("expected empty id error, got %v", err)
	}
}

func TestResolverLoadRequiresDataDir(t *testing.T) {
	if _, err := NewResolver("").Load(); err == nil {
		t.Fatal("expected error for empty data dir")
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
