package unifier

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineStatusReportsDirectoryPresence(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	dir := t.TempDir()
	engine.SetDataDir(dir)
	if _, err := engine.Status(""); err == nil {
		t.Fatal("Status(\"\") error = nil")
	}
	status, err := engine.Status("missing")
	if err != nil || status.State != "not_found" {
		t.Fatalf("Status(missing) = %#v, %v", status, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "present"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, err = engine.Status("present")
	if err != nil || status.State != "present" {
		t.Fatalf("Status(present) = %#v, %v", status, err)
	}
}
