// Package backup provides backup functionality including S3 storage and scheduling.
// This file contains unit tests for scheduler.go
package backup

import (
	"os"
	"testing"
	"time"
)

// =============================================================================
// Sprint 9: NewScheduler Tests (TD-11)
// =============================================================================

func TestNewScheduler_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
	}

	// Note: We can't fully test NewScheduler without a PocketBase app,
	// but we can test the config validation and directory creation
	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}
	if scheduler == nil {
		t.Fatal("NewScheduler returned nil scheduler")
	}
}

func TestNewScheduler_NegativeRetention(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: -5, // Negative
		BackupDir: tmpDir,
	}

	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	if scheduler.config.Retention != 7 {
		t.Errorf("Expected retention to default to 7 for negative value, got %d", scheduler.config.Retention)
	}
}

func TestNewScheduler_CreateBackupDir(t *testing.T) {
	tmpDir := t.TempDir()
	newDir := tmpDir + "/new_backup_dir"

	// Dir doesn't exist yet
	if _, err := os.Stat(newDir); !os.IsNotExist(err) {
		t.Fatal("Test directory should not exist initially")
	}

	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: newDir,
	}

	_, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	// Directory should now exist
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		t.Error("NewScheduler should have created the backup directory")
	}
}

// =============================================================================
// Sprint 9: Scheduler Lifecycle Tests (TD-11)
// =============================================================================

func TestScheduler_IsRunning_Initial(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
	}

	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	// Should not be running initially
	if scheduler.IsRunning() {
		t.Error("Scheduler should not be running initially")
	}
}

func TestScheduler_LastRun_Initial(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
	}

	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	// LastRun should be zero initially
	if !scheduler.LastRun().IsZero() {
		t.Error("LastRun should be zero initially")
	}
}

func TestScheduler_LastError_Initial(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
	}

	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	// LastError should be nil initially
	if scheduler.LastError() != nil {
		t.Error("LastError should be nil initially")
	}
}

func TestScheduler_Status(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
	}

	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	status := scheduler.Status()

	if status.Running {
		t.Error("Expected Running to be false initially")
	}
	if status.Interval != "24h0m0s" {
		t.Errorf("Expected Interval '24h0m0s', got '%s'", status.Interval)
	}
	if status.Retention != 7 {
		t.Errorf("Expected Retention 7, got %d", status.Retention)
	}
	if status.S3Enabled {
		t.Error("Expected S3Enabled to be false (no S3 config)")
	}
	if !status.LastRun.IsZero() {
		t.Error("Expected LastRun to be zero initially")
	}
	if !status.NextRun.IsZero() {
		t.Error("Expected NextRun to be zero when not running")
	}
	if status.LastError != "" {
		t.Errorf("Expected empty LastError, got '%s'", status.LastError)
	}
}

func TestScheduler_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
	}

	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	// Start scheduler
	scheduler.Start()
	time.Sleep(50 * time.Millisecond) // Give goroutine time to start

	if !scheduler.IsRunning() {
		t.Error("Scheduler should be running after Start()")
	}

	// Double start should be safe
	scheduler.Start()
	if !scheduler.IsRunning() {
		t.Error("Scheduler should still be running after double Start()")
	}

	// Stop scheduler
	scheduler.Stop()
	time.Sleep(50 * time.Millisecond) // Give goroutine time to stop

	if scheduler.IsRunning() {
		t.Error("Scheduler should not be running after Stop()")
	}

	// Double stop should be safe
	scheduler.Stop()
	if scheduler.IsRunning() {
		t.Error("Scheduler should still be stopped after double Stop()")
	}
}

// =============================================================================
// Sprint 9: SchedulerConfig Tests (TD-11)
// =============================================================================

func TestSchedulerConfig_AllFields(t *testing.T) {
	cfg := SchedulerConfig{
		Enabled:     true,
		Interval:    12 * time.Hour,
		Retention:   14,
		BackupDir:   "/path/to/backups",
		S3Enabled:   true,
		S3Bucket:    "my-bucket",
		S3Endpoint:  "s3.amazonaws.com",
		S3AccessKey: "AKIAIOSFODNN7EXAMPLE",
		S3SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		S3Region:    "us-east-1",
		S3Prefix:    "backups/techstack",
	}

	if !cfg.Enabled {
		t.Error("Expected Enabled true")
	}
	if cfg.Interval != 12*time.Hour {
		t.Errorf("Expected Interval 12h, got %v", cfg.Interval)
	}
	if cfg.Retention != 14 {
		t.Errorf("Expected Retention 14, got %d", cfg.Retention)
	}
	if !cfg.S3Enabled {
		t.Error("Expected S3Enabled true")
	}
	if cfg.S3Bucket != "my-bucket" {
		t.Errorf("Expected S3Bucket 'my-bucket', got '%s'", cfg.S3Bucket)
	}
}

// =============================================================================
// Sprint 9: SchedulerStatus Tests (TD-11)
// =============================================================================

func TestSchedulerStatus_AllFields(t *testing.T) {
	now := time.Now()
	status := SchedulerStatus{
		Running:   true,
		Interval:  "6h0m0s",
		Retention: 30,
		S3Enabled: true,
		LastRun:   now.Add(-2 * time.Hour),
		NextRun:   now.Add(4 * time.Hour),
		LastError: "test error",
	}

	if !status.Running {
		t.Error("Expected Running true")
	}
	if status.Interval != "6h0m0s" {
		t.Errorf("Expected Interval '6h0m0s', got '%s'", status.Interval)
	}
	if status.Retention != 30 {
		t.Errorf("Expected Retention 30, got %d", status.Retention)
	}
	if !status.S3Enabled {
		t.Error("Expected S3Enabled true")
	}
	if status.LastError != "test error" {
		t.Errorf("Expected LastError 'test error', got '%s'", status.LastError)
	}
}

// =============================================================================
// Sprint 9: S3 Configuration Tests (TD-11)
// =============================================================================

func TestNewScheduler_WithInvalidS3Config(t *testing.T) {
	tmpDir := t.TempDir()

	// S3 enabled but missing required fields
	cfg := SchedulerConfig{
		Enabled:   true,
		Interval:  24 * time.Hour,
		Retention: 7,
		BackupDir: tmpDir,
		S3Enabled: true,
		// Missing: S3Bucket, S3AccessKey, S3SecretKey
	}

	// Should still create scheduler, just without S3
	scheduler, err := NewScheduler(nil, cfg)
	if err != nil {
		t.Fatalf("NewScheduler failed: %v", err)
	}

	// S3 client should be nil due to invalid config
	if scheduler.s3Client != nil {
		t.Error("Expected S3 client to be nil with invalid config")
	}
}
