// Package autoinstall - Tests for checksum verification.
package autoinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseChecksumFile(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		targetFile string
		wantHash   string
		wantErr    bool
	}{
		{
			name:       "gnu format double space",
			content:    "abc123def456789012345678901234567890123456789012345678901234abcd  test.tar.gz\n",
			targetFile: "test.tar.gz",
			wantHash:   "abc123def456789012345678901234567890123456789012345678901234abcd",
			wantErr:    false,
		},
		{
			name:       "gnu format single space",
			content:    "abc123def456789012345678901234567890123456789012345678901234abcd test.tar.gz\n",
			targetFile: "test.tar.gz",
			wantHash:   "abc123def456789012345678901234567890123456789012345678901234abcd",
			wantErr:    false,
		},
		{
			name:       "binary mode indicator",
			content:    "abc123def456789012345678901234567890123456789012345678901234abcd *test.tar.gz\n",
			targetFile: "test.tar.gz",
			wantHash:   "abc123def456789012345678901234567890123456789012345678901234abcd",
			wantErr:    false,
		},
		{
			name: "multiple files",
			content: `abc123def456789012345678901234567890123456789012345678901234abcd  file1.tar.gz
def456789012345678901234567890123456789012345678901234567890abcd  file2.tar.gz
`,
			targetFile: "file2.tar.gz",
			wantHash:   "def456789012345678901234567890123456789012345678901234567890abcd",
			wantErr:    false,
		},
		{
			name:       "file not found",
			content:    "abc123def456789012345678901234567890123456789012345678901234abcd  other.tar.gz\n",
			targetFile: "test.tar.gz",
			wantHash:   "",
			wantErr:    true,
		},
		{
			name:       "empty content",
			content:    "",
			targetFile: "test.tar.gz",
			wantHash:   "",
			wantErr:    true,
		},
		{
			name:       "comment lines",
			content:    "# This is a comment\nabc123def456789012345678901234567890123456789012345678901234abcd  test.tar.gz\n",
			targetFile: "test.tar.gz",
			wantHash:   "abc123def456789012345678901234567890123456789012345678901234abcd",
			wantErr:    false,
		},
		{
			name:       "path in checksum file",
			content:    "abc123def456789012345678901234567890123456789012345678901234abcd  ./dist/test.tar.gz\n",
			targetFile: "test.tar.gz",
			wantHash:   "abc123def456789012345678901234567890123456789012345678901234abcd",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChecksumFile(tt.content, tt.targetFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseChecksumFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantHash {
				t.Errorf("parseChecksumFile() = %v, want %v", got, tt.wantHash)
			}
		})
	}
}

func TestDownloadChecksumFile(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checksums.txt" {
			w.Write([]byte("abc123def456789012345678901234567890123456789012345678901234  test.tar.gz\n"))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := context.Background()

	// Test successful download
	content, err := downloadChecksumFile(ctx, server.URL+"/checksums.txt")
	if err != nil {
		t.Fatalf("downloadChecksumFile failed: %v", err)
	}

	if content == "" {
		t.Error("expected non-empty content")
	}

	// Test 404
	_, err = downloadChecksumFile(ctx, server.URL+"/notfound")
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestVerifyDownloadedFile(t *testing.T) {
	// Create a temporary file with known content
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.bin")
	testContent := []byte("binary content for verification test")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Compute correct hash
	correctHash := sha256.Sum256(testContent)
	correctHashHex := hex.EncodeToString(correctHash[:])

	// Test with correct hash
	if err := VerifyDownloadedFile(testFile, correctHashHex); err != nil {
		t.Errorf("VerifyDownloadedFile with correct hash failed: %v", err)
	}

	// Test with wrong hash
	if err := VerifyDownloadedFile(testFile, "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Error("expected error for wrong hash")
	}
}

func TestFetchChecksumFromRelease(t *testing.T) {
	// Create mock server for checksum file
	checksumContent := `abc123def456789012345678901234567890123456789012345678901234abcd  tofu_1.6.0_linux_amd64.tar.gz
def456789012345678901234567890123456789012345678901234567890abcd  tofu_1.6.0_darwin_amd64.tar.gz
`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(checksumContent))
	}))
	defer server.Close()

	// Create mock release
	release := &GitHubRelease{
		TagName: "v1.6.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: "tofu_1.6.0_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/tofu.tar.gz"},
			{Name: "SHA256SUMS", BrowserDownloadURL: server.URL + "/checksums"},
		},
	}

	ctx := context.Background()
	hash, err := FetchChecksumFromRelease(ctx, release, "tofu_1.6.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("FetchChecksumFromRelease failed: %v", err)
	}

	expected := "abc123def456789012345678901234567890123456789012345678901234abcd"
	if hash != expected {
		t.Errorf("hash = %s, want %s", hash, expected)
	}
}

func TestFetchChecksumFromRelease_NoChecksumFile(t *testing.T) {
	// Create mock release without checksum file
	release := &GitHubRelease{
		TagName: "v1.6.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{Name: "tofu_1.6.0_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/tofu.tar.gz"},
		},
	}

	ctx := context.Background()
	_, err := FetchChecksumFromRelease(ctx, release, "tofu_1.6.0_linux_amd64.tar.gz")
	if err != ErrNoChecksumFile {
		t.Errorf("expected ErrNoChecksumFile, got %v", err)
	}
}
