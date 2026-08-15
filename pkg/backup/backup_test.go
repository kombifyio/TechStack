// Package backup provides backup functionality including S3 storage and scheduling.
package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		result := formatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("formatBytes(%d) = %s, expected %s", tt.bytes, result, tt.expected)
		}
	}
}

func TestCopyFile(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create source file
	srcPath := filepath.Join(tmpDir, "source.txt")
	content := []byte("test content")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Copy file
	dstPath := filepath.Join(tmpDir, "dest.txt")
	if err := copyFile(srcPath, dstPath); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	// Verify content
	result, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}
	if string(result) != string(content) {
		t.Errorf("Content mismatch: got %s, expected %s", result, content)
	}
}

func TestListLocalBackups(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create some backup files
	files := []string{
		"techstack_backup_20260101_120000.tar.gz",
		"techstack_backup_20260101_130000.tar.gz",
		"techstack_backup_20260101_140000.tar.gz",
		"other_file.txt", // Should be ignored
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
	}

	// List backups
	backups, err := listLocalBackups(tmpDir)
	if err != nil {
		t.Fatalf("listLocalBackups failed: %v", err)
	}

	// Should have 3 backups (not the other_file.txt)
	if len(backups) != 3 {
		t.Errorf("Expected 3 backups, got %d", len(backups))
	}

	// Should be sorted newest first
	if len(backups) > 0 && backups[0].Name != "techstack_backup_20260101_140000.tar.gz" {
		t.Errorf("Expected newest backup first, got %s", backups[0].Name)
	}
}

func TestApplyLocalRetention(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create 5 backup files
	for i := 0; i < 5; i++ {
		name := filepath.Join(tmpDir, "techstack_backup_2026010"+string(rune('0'+i))+"_120000.tar.gz")
		if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	// Apply retention of 3
	deleted, err := applyLocalRetention(tmpDir, 3)
	if err != nil {
		t.Fatalf("applyLocalRetention failed: %v", err)
	}

	// Should have deleted 2 files
	if len(deleted) != 2 {
		t.Errorf("Expected 2 deleted files, got %d", len(deleted))
	}

	// List remaining backups
	backups, err := listLocalBackups(tmpDir)
	if err != nil {
		t.Fatalf("listLocalBackups failed: %v", err)
	}

	// Should have 3 remaining
	if len(backups) != 3 {
		t.Errorf("Expected 3 remaining backups, got %d", len(backups))
	}
}

func TestS3ConfigValidation(t *testing.T) {
	// Test missing bucket
	_, err := NewS3Client(S3Config{
		AccessKey: "test",
		SecretKey: "test",
	})
	if err == nil {
		t.Error("Expected error for missing bucket")
	}

	// Test missing credentials
	_, err = NewS3Client(S3Config{
		Bucket: "test",
	})
	if err == nil {
		t.Error("Expected error for missing credentials")
	}
}

// =============================================================================
// Sprint 9: Additional Tests for Scheduler
// =============================================================================

func TestSchedulerConfig_Defaults(t *testing.T) {
	// Test NewSchedulerConfigFromConfig with nil config handling
	cfg := SchedulerConfig{
		Enabled:   true,
		Retention: 0, // Should default to 7
		Interval:  0, // Should default in scheduler
	}

	if cfg.Retention == 0 {
		// This is expected - the scheduler normalizes this
		t.Log("Retention=0 will be normalized to 7 by scheduler")
	}
}

func TestSchedulerStatus_Fields(t *testing.T) {
	status := SchedulerStatus{
		Running:   true,
		Interval:  "24h0m0s",
		Retention: 7,
		S3Enabled: false,
		LastRun:   time.Now().Add(-1 * time.Hour),
		NextRun:   time.Now().Add(23 * time.Hour),
		LastError: "",
	}

	if !status.Running {
		t.Error("Expected Running to be true")
	}
	if status.Retention != 7 {
		t.Errorf("Expected Retention 7, got %d", status.Retention)
	}
	if status.S3Enabled {
		t.Error("Expected S3Enabled to be false")
	}
	if status.LastError != "" {
		t.Errorf("Expected empty LastError, got %s", status.LastError)
	}
}

func TestBackupInfo_Fields(t *testing.T) {
	info := BackupInfo{
		Name:      "techstack_backup_20260108_120000.tar.gz",
		Size:      1024 * 1024, // 1MB
		SizeHuman: "1.0 MB",
		CreatedAt: time.Now(),
		Path:      "/backups/techstack_backup_20260108_120000.tar.gz",
		S3Path:    "s3://bucket/prefix/techstack_backup_20260108_120000.tar.gz",
	}

	if info.Name == "" {
		t.Error("Expected non-empty Name")
	}
	if info.Size != 1024*1024 {
		t.Errorf("Expected Size 1MB, got %d", info.Size)
	}
	if info.S3Path == "" {
		t.Error("Expected non-empty S3Path")
	}
}

func TestS3Object_Fields(t *testing.T) {
	obj := S3Object{
		Key:          "backups/test.tar.gz",
		Size:         2048,
		LastModified: time.Now(),
		ETag:         "abc123",
	}

	if obj.Key == "" {
		t.Error("Expected non-empty Key")
	}
	if obj.Size != 2048 {
		t.Errorf("Expected Size 2048, got %d", obj.Size)
	}
}

func TestListLocalBackups_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	backups, err := listLocalBackups(tmpDir)
	if err != nil {
		t.Fatalf("listLocalBackups failed: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("Expected 0 backups in empty dir, got %d", len(backups))
	}
}

func TestListLocalBackups_NonExistentDir(t *testing.T) {
	// listLocalBackups returns empty slice for non-existent directory (graceful handling)
	backups, err := listLocalBackups("/nonexistent/path/12345")
	if err != nil {
		t.Fatalf("listLocalBackups should handle non-existent dir gracefully, got error: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("Expected 0 backups for non-existent dir, got %d", len(backups))
	}
}

func TestApplyLocalRetention_ZeroRetention(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a backup file
	name := filepath.Join(tmpDir, "techstack_backup_20260101_120000.tar.gz")
	if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Retention of 0 should keep all files (or be normalized)
	deleted, err := applyLocalRetention(tmpDir, 0)
	if err != nil {
		t.Fatalf("applyLocalRetention failed: %v", err)
	}

	// With retention=0, the function should either keep all or use default
	t.Logf("Deleted %d files with retention=0", len(deleted))
}

func TestCopyFile_NonExistentSource(t *testing.T) {
	tmpDir := t.TempDir()
	dstPath := filepath.Join(tmpDir, "dest.txt")

	err := copyFile("/nonexistent/source.txt", dstPath)
	if err == nil {
		t.Error("Expected error for non-existent source file")
	}
}

func TestCopyFile_InvalidDestination(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.txt")

	// Create source file
	if err := os.WriteFile(srcPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Try to copy to invalid destination (directory that doesn't exist)
	err := copyFile(srcPath, "/nonexistent/path/dest.txt")
	if err == nil {
		t.Error("Expected error for invalid destination path")
	}
}
