package unifier

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestNewSpecPersister(t *testing.T) {
	t.Run("creates directory structure", func(t *testing.T) {
		tmpDir := t.TempDir()
		stackID := "test-stack-123"

		// Use custom path for testing
		persister, err := NewSpecPersisterWithPath(filepath.Join(tmpDir, stackID))
		if err != nil {
			t.Fatalf("NewSpecPersisterWithPath failed: %v", err)
		}

		if persister.BaseDir == "" {
			t.Error("BaseDir should not be empty")
		}

		// Check directory exists
		if _, err := os.Stat(persister.BaseDir); os.IsNotExist(err) {
			t.Error("BaseDir should exist")
		}
	})

	t.Run("empty stack ID returns error", func(t *testing.T) {
		_, err := NewSpecPersister("")
		if err == nil {
			t.Error("expected error for empty stack ID")
		}
	})

	t.Run("unsafe stack IDs return error", func(t *testing.T) {
		for _, stackID := range []string{"../escape", `..\escape`, "/tmp/escape", `C:\tmp\escape`, "bad/id", "bad id"} {
			if _, err := NewSpecPersister(stackID); err == nil {
				t.Fatalf("expected error for unsafe stack ID %q", stackID)
			}
		}
	})
}

func TestSpecPersister_SaveRequirementsSpec(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	spec := &core.RequirementsSpec{
		StackKit:       "basement-kit",
		DetectedAddons: []string{"monitoring"},
		IntentName:     "my-homelab",
		RequiredWorkers: core.WorkerRequirements{
			MinRAM: 4096,
			MinCPU: 2,
		},
	}

	path, err := persister.SaveRequirementsSpec(spec, "")
	if err != nil {
		t.Fatalf("SaveRequirementsSpec failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("requirements file should exist at %s", path)
	}

	// Verify metadata was set
	loaded, err := persister.LoadRequirementsSpec()
	if err != nil {
		t.Fatalf("LoadRequirementsSpec failed: %v", err)
	}

	if loaded.APIVersion != "techstack/v1" {
		t.Errorf("APIVersion = %q, want %q", loaded.APIVersion, "techstack/v1")
	}
	if loaded.Kind != "RequirementsSpec" {
		t.Errorf("Kind = %q, want %q", loaded.Kind, "RequirementsSpec")
	}
	if loaded.StackKit != "basement-kit" {
		t.Errorf("StackKit = %q, want %q", loaded.StackKit, "basement-kit")
	}
}

func TestSpecPersister_SaveUnifiedSpec(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	spec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name:    "my-homelab",
			Version: "1.0",
			Kit:     "basement-kit",
		},
		StackKit: "basement-kit",
	}

	path, err := persister.SaveUnifiedSpec(spec, "")
	if err != nil {
		t.Fatalf("SaveUnifiedSpec failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("unified file should exist at %s", path)
	}

	// Verify can load
	loaded, err := persister.LoadUnifiedSpec()
	if err != nil {
		t.Fatalf("LoadUnifiedSpec failed: %v", err)
	}

	if loaded.Name != "my-homelab" {
		t.Errorf("Name = %q, want %q", loaded.Name, "my-homelab")
	}
	if loaded.APIVersion != "techstack/v1" {
		t.Errorf("APIVersion = %q, want %q", loaded.APIVersion, "techstack/v1")
	}
}

func TestSpecPersister_HashChain(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	// Save requirements first
	reqSpec := &core.RequirementsSpec{
		StackKit:   "basement-kit",
		IntentName: "my-homelab",
	}
	reqPath, err := persister.SaveRequirementsSpec(reqSpec, "")
	if err != nil {
		t.Fatalf("SaveRequirementsSpec failed: %v", err)
	}

	// Save unified with reference to requirements
	unifiedSpec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "my-homelab",
		},
	}
	_, err = persister.SaveUnifiedSpec(unifiedSpec, reqPath)
	if err != nil {
		t.Fatalf("SaveUnifiedSpec failed: %v", err)
	}

	// Verify hash chain
	valid, err := persister.VerifyHashChain()
	if err != nil {
		t.Fatalf("VerifyHashChain failed: %v", err)
	}
	if !valid {
		t.Error("hash chain should be valid")
	}
}

func TestSpecPersister_ExistsChecks(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	// Initially nothing exists
	if persister.RequirementsSpecExists() {
		t.Error("requirements should not exist initially")
	}
	if persister.UnifiedSpecExists() {
		t.Error("unified should not exist initially")
	}

	// Save requirements
	persister.SaveRequirementsSpec(&core.RequirementsSpec{StackKit: "test"}, "")
	if !persister.RequirementsSpecExists() {
		t.Error("requirements should exist after save")
	}

	// Save unified
	persister.SaveUnifiedSpec(&core.UnifiedSpec{}, "")
	if !persister.UnifiedSpecExists() {
		t.Error("unified should exist after save")
	}
}

func TestComputeDataHash(t *testing.T) {
	data := []byte("test data for hashing")
	hash := ComputeDataHash(data)

	if hash == "" {
		t.Error("hash should not be empty")
	}
	if len(hash) < 10 {
		t.Errorf("hash seems too short: %s", hash)
	}
	if hash[:7] != "sha256:" {
		t.Errorf("hash should start with 'sha256:', got %s", hash)
	}

	// Same data should produce same hash
	hash2 := ComputeDataHash(data)
	if hash != hash2 {
		t.Error("same data should produce same hash")
	}

	// Different data should produce different hash
	hash3 := ComputeDataHash([]byte("different data"))
	if hash == hash3 {
		t.Error("different data should produce different hash")
	}
}

func TestSpecPersister_Paths(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	reqPath := persister.GetRequirementsSpecPath()
	if filepath.Base(reqPath) != RequirementsSpecFilename {
		t.Errorf("wrong requirements filename: %s", filepath.Base(reqPath))
	}

	unifiedPath := persister.GetUnifiedSpecPath()
	if filepath.Base(unifiedPath) != UnifiedSpecFilename {
		t.Errorf("wrong unified filename: %s", filepath.Base(unifiedPath))
	}

	tofuDir := persister.GetTofuDir()
	if filepath.Base(tofuDir) != "tofu" {
		t.Errorf("wrong tofu dir: %s", tofuDir)
	}
}

func TestSpecPersister_NilSpec(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	_, err := persister.SaveRequirementsSpec(nil, "")
	if err == nil {
		t.Error("SaveRequirementsSpec(nil) should return error")
	}

	_, err = persister.SaveUnifiedSpec(nil, "")
	if err == nil {
		t.Error("SaveUnifiedSpec(nil) should return error")
	}
}

func TestSpecPersister_MetadataTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	before := time.Now().Add(-time.Second)

	spec := &core.RequirementsSpec{StackKit: "test"}
	persister.SaveRequirementsSpec(spec, "")

	after := time.Now().Add(time.Second)

	loaded, _ := persister.LoadRequirementsSpec()

	if loaded.Metadata.GeneratedAt.Before(before) {
		t.Error("GeneratedAt should be after test start")
	}
	if loaded.Metadata.GeneratedAt.After(after) {
		t.Error("GeneratedAt should be before test end")
	}
}

func TestSpecPersister_SaveIntentBytes_PreservesBytes(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	original := []byte("# user header\nname: my-homelab\nkit: basement-kit\n\n# trailing comment\n")

	path, hash, err := persister.SaveIntentBytes(original)
	if err != nil {
		t.Fatalf("SaveIntentBytes failed: %v", err)
	}

	if filepath.Base(path) != IntentSpecFilename {
		t.Fatalf("unexpected intent filename: %s", filepath.Base(path))
	}

	if !persister.IntentExists() {
		t.Fatal("IntentExists() should be true after SaveIntentBytes")
	}

	loaded, err := persister.LoadIntentBytes()
	if err != nil {
		t.Fatalf("LoadIntentBytes failed: %v", err)
	}

	if string(loaded) != string(original) {
		t.Fatalf("intent bytes not preserved")
	}

	if hash != ComputeDataHash(original) {
		t.Fatalf("returned hash mismatch: got %s", hash)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat intent file: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0600 {
		t.Fatalf("intent file mode = %o, want 0600", got)
	}

	h2, err := ComputeFileHash(persister.GetIntentPath())
	if err != nil {
		t.Fatalf("ComputeFileHash failed: %v", err)
	}
	if h2 != hash {
		t.Fatalf("file hash mismatch: got %s want %s", h2, hash)
	}
}

func TestSpecPersister_SaveIntentBytes_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	_, _, err := persister.SaveIntentBytes(nil)
	if err == nil {
		t.Fatal("expected error for empty intent data")
	}
}

func TestSpecPersister_SaveIntentFile_CopiesBytes(t *testing.T) {
	tmpDir := t.TempDir()
	persister, _ := NewSpecPersisterWithPath(tmpDir)

	src := filepath.Join(tmpDir, "src-kombination.yaml")
	original := []byte("name: imported\nkit: modern-homelab\n")
	if err := os.WriteFile(src, original, 0644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	_, hash, err := persister.SaveIntentFile(src)
	if err != nil {
		t.Fatalf("SaveIntentFile failed: %v", err)
	}

	loaded, err := persister.LoadIntentBytes()
	if err != nil {
		t.Fatalf("LoadIntentBytes failed: %v", err)
	}
	if string(loaded) != string(original) {
		t.Fatalf("intent bytes not preserved")
	}
	if hash != ComputeDataHash(original) {
		t.Fatalf("hash mismatch: got %s", hash)
	}
}
