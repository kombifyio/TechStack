package autoinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// GitHubRelease represents a GitHub release.
type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// FetchLatestRelease fetches the latest GitHub release.
func FetchLatestRelease(ctx context.Context, apiURL string) (*GitHubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github.v3+json")

	// Add GitHub Token if present to avoid rate limits
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to decode release: %w", err)
	}

	return &release, nil
}

// FindAsset finds an asset matching the pattern.
func (r *GitHubRelease) FindAsset(pattern string) (string, error) {
	for _, asset := range r.Assets {
		if strings.Contains(asset.Name, pattern) {
			return asset.BrowserDownloadURL, nil
		}
	}

	return "", fmt.Errorf("no asset found matching pattern: %s", pattern)
}

// getLatestReleaseHelper is a helper used by platform-specific implementations.
func getLatestReleaseHelper(ctx context.Context, apiURL, assetPattern string) (string, string, error) {
	release, err := FetchLatestRelease(ctx, apiURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch release: %w", err)
	}

	downloadURL, err := release.FindAsset(assetPattern)
	if err != nil {
		return "", "", err
	}

	return downloadURL, release.TagName, nil
}
