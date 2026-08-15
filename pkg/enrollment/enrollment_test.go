package enrollment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadCreatesDefaultStandalone(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir, "instance-aaa")

	got, err := r.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.InstanceID != "instance-aaa" {
		t.Errorf("InstanceID = %q, want instance-aaa", got.InstanceID)
	}
	if got.Mode != ModeStandalone {
		t.Errorf("Mode = %q, want standalone", got.Mode)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
	if got.LinkedAt != nil {
		t.Errorf("LinkedAt = %v, want nil", got.LinkedAt)
	}
}

func TestLoadIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir, "instance-aaa")

	first, err := r.Load()
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	second, err := r.Load()
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if !first.UpdatedAt.Equal(second.UpdatedAt) {
		t.Errorf("UpdatedAt drifted: %v vs %v", first.UpdatedAt, second.UpdatedAt)
	}
}

func TestLoadAcrossResolversReturnsSameRecord(t *testing.T) {
	dir := t.TempDir()
	a, err := NewResolver(dir, "instance-aaa").Load()
	if err != nil {
		t.Fatalf("a.Load: %v", err)
	}
	b, err := NewResolver(dir, "instance-aaa").Load()
	if err != nil {
		t.Fatalf("b.Load: %v", err)
	}
	if a.InstanceID != b.InstanceID || !a.UpdatedAt.Equal(b.UpdatedAt) {
		t.Errorf("records differ: %#v vs %#v", a, b)
	}
}

func TestLoadRejectsInstanceMismatch(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewResolver(dir, "instance-aaa").Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	_, err := NewResolver(dir, "instance-bbb").Load()
	if err == nil {
		t.Fatal("expected error on instance mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "belongs to instance") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "enrollment.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	_, err := NewResolver(dir, "instance-aaa").Load()
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestLoadRejectsInvalidMode(t *testing.T) {
	dir := t.TempDir()
	bad := map[string]any{
		"instance_id": "instance-aaa",
		"mode":        "bogus",
		"updated_at":  time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(filepath.Join(dir, "enrollment.json"), raw, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := NewResolver(dir, "instance-aaa").Load()
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("expected invalid mode error, got %v", err)
	}
}

func TestLoadEmptyDataDir(t *testing.T) {
	if _, err := NewResolver("", "instance-aaa").Load(); err == nil {
		t.Fatal("expected error for empty data dir")
	}
}

func TestLoadEmptyInstanceID(t *testing.T) {
	if _, err := NewResolver(t.TempDir(), "").Load(); err == nil {
		t.Fatal("expected error for empty instance id")
	}
}

func TestSaveLinksToCloud(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir, "instance-aaa")
	if _, err := r.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	saved, err := r.Save(Enrollment{
		Mode:          ModeCloudLinked,
		CloudTenantID: "tenant-42",
		LinkedAt:      &now,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.InstanceID != "instance-aaa" {
		t.Errorf("InstanceID = %q, want instance-aaa", saved.InstanceID)
	}
	if saved.Mode != ModeCloudLinked || saved.CloudTenantID != "tenant-42" {
		t.Errorf("unexpected saved record: %#v", saved)
	}
	if saved.UpdatedAt.IsZero() {
		t.Error("UpdatedAt not set on Save")
	}

	// Round-trip: a fresh resolver sees the persisted state.
	again, err := NewResolver(dir, "instance-aaa").Load()
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	if again.Mode != ModeCloudLinked || again.CloudTenantID != "tenant-42" {
		t.Errorf("round-trip mismatch: %#v", again)
	}
}

func TestSaveRejectsCloudLinkedWithoutTenant(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir, "instance-aaa")
	if _, err := r.Load(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := r.Save(Enrollment{Mode: ModeCloudLinked}); err == nil {
		t.Fatal("expected error: cloud_linked without tenant")
	}
}

func TestSaveRejectsInvalidMode(t *testing.T) {
	dir := t.TempDir()
	r := NewResolver(dir, "instance-aaa")
	if _, err := r.Load(); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := r.Save(Enrollment{Mode: Mode("nope")}); err == nil {
		t.Fatal("expected error on invalid mode")
	}
}

func TestModeValid(t *testing.T) {
	if !ModeStandalone.Valid() {
		t.Error("ModeStandalone should be valid")
	}
	if !ModeCloudLinked.Valid() {
		t.Error("ModeCloudLinked should be valid")
	}
	if Mode("").Valid() {
		t.Error("empty mode should not be valid")
	}
}
