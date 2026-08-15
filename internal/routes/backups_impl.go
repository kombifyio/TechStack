// Package routes provides custom HTTP routes for kombifyTechstack API.
package routes

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// createBackup creates a new backup of the PocketBase database.
func createBackup(app core.App, config BackupConfig) (*BackupInfo, error) {
	timestamp := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("techstack_backup_%s.tar.gz", timestamp)
	backupPath := filepath.Join(config.BackupDir, backupName)

	// Find the database file
	dataDir := config.DataDir
	if dataDir == "" {
		dataDir = app.DataDir()
	}

	dbFile := filepath.Join(dataDir, "data.db")
	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		// Try other common PocketBase DB filenames
		possible := []string{"pb_data/data.db", "pb_data/app.db", "pb_data/db.sqlite", "data.db"}
		for _, name := range possible {
			alt := filepath.Join(dataDir, name)
			if _, err := os.Stat(alt); err == nil {
				dbFile = alt
				break
			}
		}
	}

	if _, err := os.Stat(dbFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("database file not found in %s", dataDir)
	}

	// Create a temporary copy using SQLite backup API if available
	tempBackup := filepath.Join(config.BackupDir, fmt.Sprintf("temp_%s.db", timestamp))
	defer os.Remove(tempBackup)

	// Try sqlite3 online backup first (safe for WAL mode)
	if _, err := exec.LookPath("sqlite3"); err == nil {
		cmd := exec.Command("sqlite3", dbFile, fmt.Sprintf(".backup '%s'", tempBackup))
		if err := cmd.Run(); err != nil {
			// Fallback to file copy
			if err := copyFile(dbFile, tempBackup); err != nil {
				return nil, fmt.Errorf("failed to copy database: %w", err)
			}
		}
	} else {
		// Fallback to file copy
		if err := copyFile(dbFile, tempBackup); err != nil {
			return nil, fmt.Errorf("failed to copy database: %w", err)
		}
	}

	// Create gzipped tar archive
	if err := createTarGz(backupPath, tempBackup); err != nil {
		return nil, fmt.Errorf("failed to create archive: %w", err)
	}

	// Get file info
	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, err
	}

	return &BackupInfo{
		Name:      backupName,
		Size:      info.Size(),
		SizeHuman: formatBytes(info.Size()),
		CreatedAt: info.ModTime(),
		Path:      backupPath,
	}, nil
}

// listBackups returns all backups sorted by creation time (newest first).
func listBackups(backupDir string) ([]BackupInfo, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupInfo{}, nil
		}
		return nil, err
	}

	var backups []BackupInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "techstack_backup_") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		backups = append(backups, BackupInfo{
			Name:      entry.Name(),
			Size:      info.Size(),
			SizeHuman: formatBytes(info.Size()),
			CreatedAt: info.ModTime(),
			Path:      filepath.Join(backupDir, entry.Name()),
		})
	}

	// Sort by creation time (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	return backups, nil
}

// applyRetention removes old backups exceeding the retention count.
func applyRetention(backupDir string, retention int) ([]string, error) {
	backups, err := listBackups(backupDir)
	if err != nil {
		return nil, err
	}

	var deleted []string
	if len(backups) > retention {
		for _, backup := range backups[retention:] {
			if err := os.Remove(backup.Path); err == nil {
				deleted = append(deleted, backup.Name)
			}
		}
	}

	return deleted, nil
}

// Helper functions

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

func createTarGz(outPath string, files ...string) error {
	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gzWriter := gzip.NewWriter(outFile)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	for _, file := range files {
		if err := addToTar(tarWriter, file); err != nil {
			return err
		}
	}

	return nil
}

func addToTar(tw *tar.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.Base(filePath)

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.Copy(tw, file)
	return err
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// BackupManifest is written to track the latest backup.
type BackupManifest struct {
	Timestamp  string `json:"timestamp"`
	File       string `json:"file"`
	Size       string `json:"size"`
	S3Uploaded bool   `json:"s3_uploaded"`
	S3Path     string `json:"s3_path,omitempty"`
	Retention  int    `json:"retention"`
}
