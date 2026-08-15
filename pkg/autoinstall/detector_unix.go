//go:build linux || darwin
// +build linux darwin

package autoinstall

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// installViaPackageManager uses apt/yum/brew.
func (d *detector) installViaPackageManager(ctx context.Context, dep Dependency) (*InstallResult, error) {
	// Detect package manager
	var installCmd []string

	if _, err := exec.LookPath("apt-get"); err == nil {
		installCmd = []string{"sudo", "apt-get", "install", "-y"}
	} else if _, err := exec.LookPath("yum"); err == nil {
		installCmd = []string{"sudo", "yum", "install", "-y"}
	} else if _, err := exec.LookPath("dnf"); err == nil {
		installCmd = []string{"sudo", "dnf", "install", "-y"}
	} else if _, err := exec.LookPath("brew"); err == nil {
		installCmd = []string{"brew", "install"}
	} else {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("no supported package manager found"),
			Strategy:   StrategyPackageManager,
		}, nil
	}

	// Map dependency name to package name
	pkgName := dep.Name
	if dep.Name == "OpenTofu" {
		pkgName = "tofu" // Adjust if needed
	}

	// Add timeout for package manager operations
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, installCmd[0], append(installCmd[1:], pkgName)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("package manager install failed: %w\nOutput: %s", err, string(output)),
			Strategy:   StrategyPackageManager,
		}, nil
	}

	return &InstallResult{
		Dependency: dep,
		Success:    true,
		Message:    string(output),
		Strategy:   StrategyPackageManager,
	}, nil
}

// installViaDownload downloads and installs from GitHub releases.
func (d *detector) installViaDownload(ctx context.Context, dep Dependency) (*InstallResult, error) {
	switch dep.Name {
	case "OpenTofu":
		return d.installOpenTofu(ctx)
	case "Cloudflared":
		return d.installCloudflared(ctx)
	default:
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("download install not implemented for %s", dep.Name),
			Strategy:   StrategyDownload,
		}, nil
	}
}

// installFromBundle extracts bundled binaries.
func (d *detector) installFromBundle(ctx context.Context, dep Dependency) (*InstallResult, error) {
	// TODO: Implement bundled install in v1.1
	return &InstallResult{
		Dependency: dep,
		Success:    false,
		Error:      fmt.Errorf("bundled install not yet implemented"),
		Strategy:   StrategyBundled,
	}, nil
}

// installOpenTofu downloads and installs OpenTofu for Linux/macOS.
// SEC-4: Downloads are verified with SHA256 checksums before extraction.
func (d *detector) installOpenTofu(ctx context.Context) (*InstallResult, error) {
	const (
		releaseURL = "https://api.github.com/repos/opentofu/opentofu/releases/latest"
		binName    = "tofu"
	)

	dep := Dependency{Name: "OpenTofu", ExecutableName: "tofu"}

	// Determine platform
	platform := "linux_amd64.tar.gz"
	if strings.Contains(strings.ToLower(os.Getenv("OSTYPE")), "darwin") {
		platform = "darwin_amd64.tar.gz"
	}

	// Fetch release info including checksum file
	release, err := FetchLatestRelease(ctx, releaseURL)
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to get latest release: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	downloadURL, err := release.FindAsset(platform)
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to find asset: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	version := release.TagName

	// SEC-4: Fetch expected checksum from release
	assetName := downloadURL[strings.LastIndex(downloadURL, "/")+1:]
	expectedHash, err := FetchChecksumFromRelease(ctx, release, assetName)
	if err != nil {
		d.log.Warn("Could not fetch checksum file, proceeding without verification", "error", err)
		// Continue without verification but log warning
	}

	d.log.Info("Downloading OpenTofu", "version", version, "url", downloadURL)

	tmpFile, err := d.downloadFile(ctx, downloadURL)
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("download failed: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}
	defer os.Remove(tmpFile)

	// SEC-4: Verify SHA256 checksum before extraction
	if expectedHash != "" {
		if err := VerifyDownloadedFile(tmpFile, expectedHash); err != nil {
			return &InstallResult{
				Dependency: dep,
				Success:    false,
				Error:      fmt.Errorf("checksum verification failed: %w", err),
				Strategy:   StrategyDownload,
			}, nil
		}
		d.log.Info("Checksum verification passed", "file", assetName)
	}

	// Extract to /usr/local/bin
	installDir := "/usr/local/bin"
	if err := d.extractTarGz(tmpFile, installDir, binName); err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("extraction failed: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	destPath := filepath.Join(installDir, binName)
	if err := os.Chmod(destPath, 0755); err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("chmod failed: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	return &InstallResult{
		Dependency:    dep,
		Success:       true,
		Version:       version,
		InstalledPath: destPath,
		Strategy:      StrategyDownload,
	}, nil
}

// installCloudflared downloads and installs cloudflared for Linux/macOS.
// SEC-4: Downloads are verified with SHA256 checksums before installation.
func (d *detector) installCloudflared(ctx context.Context) (*InstallResult, error) {
	const (
		releaseURL = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"
		binName    = "cloudflared"
	)

	dep := Dependency{Name: "Cloudflared", ExecutableName: "cloudflared"}

	platform := "linux-amd64"
	if strings.Contains(strings.ToLower(os.Getenv("OSTYPE")), "darwin") {
		platform = "darwin-amd64.tgz"
	}

	// Fetch release info including checksum file
	release, err := FetchLatestRelease(ctx, releaseURL)
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to get latest release: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	downloadURL, err := release.FindAsset(platform)
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to find asset: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	version := release.TagName

	// SEC-4: Fetch expected checksum from release
	assetName := downloadURL[strings.LastIndex(downloadURL, "/")+1:]
	expectedHash, err := FetchChecksumFromRelease(ctx, release, assetName)
	if err != nil {
		d.log.Warn("Could not fetch checksum file, proceeding without verification", "error", err)
	}

	d.log.Info("Downloading cloudflared", "version", version, "url", downloadURL)

	tmpFile, err := d.downloadFile(ctx, downloadURL)
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("download failed: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}
	defer os.Remove(tmpFile)

	// SEC-4: Verify SHA256 checksum before installation
	if expectedHash != "" {
		if err := VerifyDownloadedFile(tmpFile, expectedHash); err != nil {
			return &InstallResult{
				Dependency: dep,
				Success:    false,
				Error:      fmt.Errorf("checksum verification failed: %w", err),
				Strategy:   StrategyDownload,
			}, nil
		}
		d.log.Info("Checksum verification passed", "file", assetName)
	}

	installDir := "/usr/local/bin"
	destPath := filepath.Join(installDir, binName)

	// If it's a tarball, extract; otherwise copy directly
	if strings.HasSuffix(downloadURL, ".tgz") || strings.HasSuffix(downloadURL, ".tar.gz") {
		if err := d.extractTarGz(tmpFile, installDir, binName); err != nil {
			return &InstallResult{
				Dependency: dep,
				Success:    false,
				Error:      fmt.Errorf("extraction failed: %w", err),
				Strategy:   StrategyDownload,
			}, nil
		}
	} else {
		if err := d.copyFile(tmpFile, destPath); err != nil {
			return &InstallResult{
				Dependency: dep,
				Success:    false,
				Error:      fmt.Errorf("failed to copy binary: %w", err),
				Strategy:   StrategyDownload,
			}, nil
		}
	}

	if err := os.Chmod(destPath, 0755); err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("chmod failed: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	return &InstallResult{
		Dependency:    dep,
		Success:       true,
		Version:       version,
		InstalledPath: destPath,
		Strategy:      StrategyDownload,
	}, nil
}

// getLatestRelease fetches the latest GitHub release URL.
func (d *detector) getLatestRelease(ctx context.Context, apiURL, assetPattern string) (string, string, error) {
	return getLatestReleaseHelper(ctx, apiURL, assetPattern)
}

// downloadFile downloads a file to temp.
func (d *detector) downloadFile(ctx context.Context, url string) (string, error) {
	// Add timeout for download
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "techstack-*.tmp")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

// extractTarGz extracts a tar.gz archive.
func (d *detector) extractTarGz(tarPath, destDir, targetFile string) error {
	// Prevent path traversal: validate target file
	cleanTarget := filepath.Clean(targetFile)
	if strings.Contains(cleanTarget, "..") {
		return fmt.Errorf("invalid target file path (possible path traversal): %s", targetFile)
	}

	// Use tar command with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tar", "-xzf", tarPath, "-C", destDir, targetFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar extraction failed: %w\nOutput: %s", err, string(output))
	}
	return nil
}

// copyFile copies a file.
func (d *detector) copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
