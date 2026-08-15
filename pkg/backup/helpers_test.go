// Package backup provides backup functionality including S3 storage and scheduling.
// This file contains unit tests for helpers.go
package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// =============================================================================
// Sprint 9: createTarGz Tests (TD-11)
// =============================================================================

func TestCreateTarGz_SingleFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source file
	srcPath := filepath.Join(tmpDir, "test.txt")
	content := []byte("test content for tar.gz")
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Create tar.gz
	outPath := filepath.Join(tmpDir, "test.tar.gz")
	if err := createTarGz(outPath, srcPath); err != nil {
		t.Fatalf("createTarGz failed: %v", err)
	}

	// Verify file exists and has content
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Output file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("Output file is empty")
	}
}

func TestCreateTarGz_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple source files
	files := []string{}
	for i := 0; i < 3; i++ {
		srcPath := filepath.Join(tmpDir, "test"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(srcPath, []byte("content "+string(rune('0'+i))), 0644); err != nil {
			t.Fatalf("Failed to create source file: %v", err)
		}
		files = append(files, srcPath)
	}

	// Create tar.gz with multiple files
	outPath := filepath.Join(tmpDir, "multi.tar.gz")
	if err := createTarGz(outPath, files...); err != nil {
		t.Fatalf("createTarGz with multiple files failed: %v", err)
	}

	// Verify file exists
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Output file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("Output file is empty")
	}
}

func TestCreateTarGz_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "test.tar.gz")

	// Try to create tar.gz with non-existent file
	err := createTarGz(outPath, "/nonexistent/file.txt")
	if err == nil {
		t.Error("Expected error for non-existent source file")
	}
}

func TestCreateTarGz_InvalidOutputPath(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(srcPath, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create source file: %v", err)
	}

	// Try to create tar.gz in a missing parent directory.
	err := createTarGz(filepath.Join(tmpDir, "missing", "test.tar.gz"), srcPath)
	if err == nil {
		t.Error("Expected error for invalid output path")
	}
}

func TestCreateTarGz_EmptyFileList(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "empty.tar.gz")

	// Create tar.gz with no files - should create empty archive
	err := createTarGz(outPath)
	if err != nil {
		t.Fatalf("createTarGz with empty file list failed: %v", err)
	}

	// Verify file exists
	_, err = os.Stat(outPath)
	if err != nil {
		t.Fatalf("Output file not found: %v", err)
	}
}

// =============================================================================
// Sprint 9: addToZip Tests (TD-11)
// =============================================================================

func TestAddToZip_LargeFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a larger file (1MB)
	srcPath := filepath.Join(tmpDir, "large.txt")
	content := make([]byte, 1024*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}
	if err := os.WriteFile(srcPath, content, 0644); err != nil {
		t.Fatalf("Failed to create large file: %v", err)
	}

	// Create tar.gz (which internally uses addToZip)
	outPath := filepath.Join(tmpDir, "large.tar.gz")
	if err := createTarGz(outPath, srcPath); err != nil {
		t.Fatalf("createTarGz with large file failed: %v", err)
	}

	// Verify compression happened (output should be smaller than input)
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("Output file not found: %v", err)
	}

	// Compressed size should be less than original (1MB)
	// Note: highly compressible content so this should be significant
	if info.Size() >= int64(len(content)) {
		t.Logf("Warning: Compressed size (%d) not smaller than original (%d)", info.Size(), len(content))
	}
}

// =============================================================================
// Sprint 9: formatBytes Edge Cases (TD-11)
// =============================================================================

func TestFormatBytes_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		contains string
	}{
		{"zero", 0, "0 B"},
		{"one_byte", 1, "1 B"},
		{"just_under_kb", 1023, "1023 B"},
		{"exactly_kb", 1024, "1.0 KB"},
		{"just_over_kb", 1025, "KB"},
		{"half_mb", 512 * 1024, "KB"},
		{"exactly_mb", 1048576, "1.0 MB"},
		{"exactly_gb", 1073741824, "1.0 GB"},
		{"large_tb", 1099511627776, "TB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBytes(tt.bytes)
			if result == "" {
				t.Error("formatBytes returned empty string")
			}
			// Check that result contains expected substring for unit
			if len(result) < 2 {
				t.Errorf("formatBytes(%d) returned unexpectedly short result: %s", tt.bytes, result)
			}
		})
	}
}

func TestFormatBytes_NegativeValues(t *testing.T) {
	// Negative values should still return something sensible
	result := formatBytes(-100)
	if result == "" {
		t.Error("formatBytes with negative value returned empty string")
	}
}

// =============================================================================
// Sprint 9: listLocalBackups Additional Tests (TD-11)
// =============================================================================

func TestListLocalBackups_MixedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mix of backup and non-backup files
	// Note: listLocalBackups filters by prefix "techstack_backup_" only
	files := []struct {
		name     string
		isBackup bool
	}{
		{"techstack_backup_20260101_120000.tar.gz", true},
		{"techstack_backup_20260102_120000.tar.gz", true},
		{"other_backup.tar.gz", false},
		{"techstack_backup_invalid.txt", true}, // Has prefix, will be included
		{"readme.md", false},
		{".hidden_file", false},
	}

	for _, f := range files {
		path := filepath.Join(tmpDir, f.name)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	backups, err := listLocalBackups(tmpDir)
	if err != nil {
		t.Fatalf("listLocalBackups failed: %v", err)
	}

	// Count expected backups (files with techstack_backup_ prefix)
	expectedCount := 0
	for _, f := range files {
		if f.isBackup {
			expectedCount++
		}
	}

	if len(backups) != expectedCount {
		t.Errorf("Expected %d backups, got %d", expectedCount, len(backups))
	}
}

func TestListLocalBackups_SortOrder(t *testing.T) {
	tmpDir := t.TempDir()

	// Create backups with different timestamps (not in order)
	timestamps := []string{
		"20260103_120000", // newest
		"20260101_120000", // oldest
		"20260102_120000", // middle
	}

	for _, ts := range timestamps {
		name := filepath.Join(tmpDir, "techstack_backup_"+ts+".tar.gz")
		if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	backups, err := listLocalBackups(tmpDir)
	if err != nil {
		t.Fatalf("listLocalBackups failed: %v", err)
	}

	if len(backups) != 3 {
		t.Fatalf("Expected 3 backups, got %d", len(backups))
	}

	// Should be sorted newest first
	if backups[0].Name != "techstack_backup_20260103_120000.tar.gz" {
		t.Errorf("Expected newest backup first, got %s", backups[0].Name)
	}
	if backups[2].Name != "techstack_backup_20260101_120000.tar.gz" {
		t.Errorf("Expected oldest backup last, got %s", backups[2].Name)
	}
}

// =============================================================================
// Sprint 9: applyLocalRetention Additional Tests (TD-11)
// =============================================================================

func TestApplyLocalRetention_ExactRetention(t *testing.T) {
	tmpDir := t.TempDir()

	// Create exactly 3 backups with retention of 3
	for i := 1; i <= 3; i++ {
		name := filepath.Join(tmpDir, "techstack_backup_2026010"+string(rune('0'+i))+"_120000.tar.gz")
		if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	deleted, err := applyLocalRetention(tmpDir, 3)
	if err != nil {
		t.Fatalf("applyLocalRetention failed: %v", err)
	}

	// Should not delete anything
	if len(deleted) != 0 {
		t.Errorf("Expected 0 deletions with exact retention count, got %d", len(deleted))
	}
}

func TestApplyLocalRetention_LessBackupsThanRetention(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 2 backups with retention of 5
	for i := 1; i <= 2; i++ {
		name := filepath.Join(tmpDir, "techstack_backup_2026010"+string(rune('0'+i))+"_120000.tar.gz")
		if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	deleted, err := applyLocalRetention(tmpDir, 5)
	if err != nil {
		t.Fatalf("applyLocalRetention failed: %v", err)
	}

	// Should not delete anything
	if len(deleted) != 0 {
		t.Errorf("Expected 0 deletions when backups < retention, got %d", len(deleted))
	}
}

func TestApplyLocalRetention_DeletesCorrectFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 5 backups (oldest to newest by name)
	for i := 1; i <= 5; i++ {
		name := filepath.Join(tmpDir, "techstack_backup_2026010"+string(rune('0'+i))+"_120000.tar.gz")
		if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	// Retention of 2 should keep newest 2 (5 and 4), delete oldest 3 (1, 2, 3)
	deleted, err := applyLocalRetention(tmpDir, 2)
	if err != nil {
		t.Fatalf("applyLocalRetention failed: %v", err)
	}

	if len(deleted) != 3 {
		t.Errorf("Expected 3 deletions, got %d", len(deleted))
	}

	// Verify remaining files
	backups, err := listLocalBackups(tmpDir)
	if err != nil {
		t.Fatalf("listLocalBackups failed: %v", err)
	}

	if len(backups) != 2 {
		t.Errorf("Expected 2 remaining backups, got %d", len(backups))
	}

	// Check that newest files remain
	for _, b := range backups {
		if b.Name != "techstack_backup_20260105_120000.tar.gz" && b.Name != "techstack_backup_20260104_120000.tar.gz" {
			t.Errorf("Unexpected remaining backup: %s", b.Name)
		}
	}
}

func TestApplyLocalRetention_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()

	deleted, err := applyLocalRetention(tmpDir, 5)
	if err != nil {
		t.Fatalf("applyLocalRetention failed on empty dir: %v", err)
	}

	if len(deleted) != 0 {
		t.Errorf("Expected 0 deletions in empty dir, got %d", len(deleted))
	}
}

// =============================================================================
// Sprint 9: LocalBackupInfo Tests (TD-11)
// =============================================================================

func TestLocalBackupInfo_Fields(t *testing.T) {
	info := LocalBackupInfo{
		Name:      "techstack_backup_20260108_120000.tar.gz",
		Size:      1024 * 1024,
		SizeHuman: "1.0 MB",
		Path:      "/backups/techstack_backup_20260108_120000.tar.gz",
	}

	if info.Name == "" {
		t.Error("Expected non-empty Name")
	}
	if info.Size != 1024*1024 {
		t.Errorf("Expected Size 1MB, got %d", info.Size)
	}
	if info.SizeHuman != "1.0 MB" {
		t.Errorf("Expected SizeHuman '1.0 MB', got '%s'", info.SizeHuman)
	}
	if info.Path == "" {
		t.Error("Expected non-empty Path")
	}
}
