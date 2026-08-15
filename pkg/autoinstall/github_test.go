package autoinstall

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLatestRelease(t *testing.T) {
	// Mock GitHub API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github.v3+json" {
			t.Error("missing GitHub API accept header")
		}

		response := GitHubRelease{
			TagName: "v1.2.3",
			Assets: []struct {
				Name               string `json:"name"`
				BrowserDownloadURL string `json:"browser_download_url"`
			}{
				{
					Name:               "app-linux-amd64.tar.gz",
					BrowserDownloadURL: "https://example.com/app-linux-amd64.tar.gz",
				},
				{
					Name:               "app-windows-amd64.zip",
					BrowserDownloadURL: "https://example.com/app-windows-amd64.zip",
				},
			},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	ctx := context.Background()
	release, err := FetchLatestRelease(ctx, server.URL)

	if err != nil {
		t.Fatalf("FetchLatestRelease failed: %v", err)
	}

	if release.TagName != "v1.2.3" {
		t.Errorf("expected version v1.2.3, got %s", release.TagName)
	}

	if len(release.Assets) != 2 {
		t.Errorf("expected 2 assets, got %d", len(release.Assets))
	}
}

func TestFindAsset(t *testing.T) {
	release := &GitHubRelease{
		TagName: "v1.0.0",
		Assets: []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		}{
			{
				Name:               "app-linux-amd64.tar.gz",
				BrowserDownloadURL: "https://example.com/linux.tar.gz",
			},
			{
				Name:               "app-windows-amd64.zip",
				BrowserDownloadURL: "https://example.com/windows.zip",
			},
		},
	}

	tests := []struct {
		name        string
		pattern     string
		expected    string
		shouldError bool
	}{
		{
			name:     "find linux asset",
			pattern:  "linux-amd64",
			expected: "https://example.com/linux.tar.gz",
		},
		{
			name:     "find windows asset",
			pattern:  "windows-amd64",
			expected: "https://example.com/windows.zip",
		},
		{
			name:        "no match",
			pattern:     "darwin",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, err := release.FindAsset(tt.pattern)

			if tt.shouldError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("FindAsset failed: %v", err)
			}

			if url != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, url)
			}
		})
	}
}

func TestGetLatestReleaseIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// Real GitHub API call
	ctx := context.Background()
	release, err := FetchLatestRelease(ctx, "https://api.github.com/repos/cloudflare/cloudflared/releases/latest")

	if err != nil {
		t.Logf("GitHub API call failed (expected in CI): %v", err)
		return
	}

	t.Logf("Latest cloudflared release: %s", release.TagName)
	t.Logf("Assets: %d", len(release.Assets))

	if len(release.Assets) == 0 {
		t.Error("no assets found")
	}
}
