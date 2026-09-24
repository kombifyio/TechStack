// Package autoinstall - Checksum verification for downloaded files.
package autoinstall

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrChecksumMismatch indicates the downloaded file's checksum doesn't match expected.
var ErrChecksumMismatch = fmt.Errorf("checksum verification failed")

// ErrNoChecksumFile indicates no checksum file was found for the release.
var ErrNoChecksumFile = fmt.Errorf("no checksum file found")

// computeFileSHA256 computes the SHA256 hash of a file.
func computeFileSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// FetchChecksumFromRelease attempts to download and parse a checksum file from GitHub release.
// It looks for common checksum file patterns: SHASUMS256.txt, SHA256SUMS, *.sha256sum
func FetchChecksumFromRelease(ctx context.Context, release *GitHubRelease, targetAssetName string) (string, error) {
	checksumPatterns := []string{
		"SHA256SUMS",
		"SHASUMS256.txt",
		"checksums.txt",
		".sha256",
		"_checksums",
	}

	var checksumURL string
	for _, pattern := range checksumPatterns {
		for _, asset := range release.Assets {
			if strings.Contains(strings.ToLower(asset.Name), strings.ToLower(pattern)) {
				checksumURL = asset.BrowserDownloadURL
				break
			}
		}
		if checksumURL != "" {
			break
		}
	}

	if checksumURL == "" {
		return "", ErrNoChecksumFile
	}

	// Download checksum file
	checksumContent, err := downloadChecksumFile(ctx, checksumURL)
	if err != nil {
		return "", fmt.Errorf("failed to download checksum file: %w", err)
	}

	// Parse checksum for our target file
	hash, err := parseChecksumFile(checksumContent, targetAssetName)
	if err != nil {
		return "", fmt.Errorf("failed to parse checksum file: %w", err)
	}

	return hash, nil
}

// downloadChecksumFile downloads the checksum file content.
func downloadChecksumFile(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit to 1MB to prevent DoS
	content, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	return string(content), nil
}

// parseChecksumFile extracts the SHA256 hash for a specific file from a checksum file.
// Supports common formats:
// - <hash>  <filename>  (GNU coreutils sha256sum format)
// - <hash> *<filename>  (binary mode indicator)
// - <hash> <filename>   (single space)
func parseChecksumFile(content, targetFile string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))

	// Normalize target filename (just the base name)
	targetBase := strings.ToLower(targetFile)
	if idx := strings.LastIndex(targetBase, "/"); idx >= 0 {
		targetBase = targetBase[idx+1:]
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Try different formats
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		hash := parts[0]
		filename := parts[len(parts)-1]

		// Remove leading * (binary mode indicator)
		filename = strings.TrimPrefix(filename, "*")

		// Normalize filename for comparison
		filenameBase := strings.ToLower(filename)
		if idx := strings.LastIndex(filenameBase, "/"); idx >= 0 {
			filenameBase = filenameBase[idx+1:]
		}

		// Check if this is our file
		if filenameBase == targetBase || strings.Contains(filenameBase, targetBase) || strings.Contains(targetBase, filenameBase) {
			// Validate hash looks like SHA256 (64 hex chars)
			if len(hash) == 64 {
				if _, err := hex.DecodeString(hash); err == nil {
					return hash, nil
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan error: %w", err)
	}

	return "", fmt.Errorf("checksum for %s not found in checksum file", targetFile)
}

// VerifyDownloadedFile verifies a downloaded file against an expected SHA256 hash.
func VerifyDownloadedFile(filePath, expectedHash string) error {
	if expectedHash == "" {
		return nil
	}

	hash, err := computeFileSHA256(filePath)
	if err != nil {
		return fmt.Errorf("failed to compute checksum: %w", err)
	}
	if !strings.EqualFold(hash, expectedHash) {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expectedHash, hash)
	}
	return nil
}
