// Package backup provides backup functionality including S3 storage and scheduling.
package backup

import (
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// copyFile copies a file from src to dst.
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

// createTarGz creates a gzipped archive containing the specified files.
func createTarGz(outPath string, files ...string) error {
	parentDir := filepath.Dir(outPath)
	if parentDir != "" && parentDir != "." {
		if _, err := os.Stat(parentDir); err != nil {
			return err
		}
	}

	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gzWriter := gzip.NewWriter(outFile)
	defer gzWriter.Close()

	zipWriter := zip.NewWriter(gzWriter)
	defer zipWriter.Close()

	for _, file := range files {
		if err := addToZip(zipWriter, file); err != nil {
			return err
		}
	}

	return nil
}

// addToZip adds a file to a zip archive.
func addToZip(zw *zip.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.Base(filePath)
	header.Method = zip.Deflate

	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	return err
}

// formatBytes formats a byte count as a human-readable string.
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

// LocalBackupInfo represents metadata about a local backup file.
type LocalBackupInfo struct {
	Name      string
	Size      int64
	SizeHuman string
	Path      string
	ModTime   os.FileInfo
}

// listLocalBackups returns all local backups sorted by modification time (newest first).
func listLocalBackups(backupDir string) ([]LocalBackupInfo, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LocalBackupInfo{}, nil
		}
		return nil, err
	}

	var backups []LocalBackupInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "techstack_backup_") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		backups = append(backups, LocalBackupInfo{
			Name:      entry.Name(),
			Size:      info.Size(),
			SizeHuman: formatBytes(info.Size()),
			Path:      filepath.Join(backupDir, entry.Name()),
		})
	}

	// Sort by name (which includes timestamp, so newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name > backups[j].Name
	})

	return backups, nil
}

// applyLocalRetention removes old backups exceeding the retention count.
func applyLocalRetention(backupDir string, retention int) ([]string, error) {
	backups, err := listLocalBackups(backupDir)
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
