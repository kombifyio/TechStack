//go:build windows
// +build windows

package autoinstall

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// isElevated checks if the current process has administrator privileges.
func isElevated() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	return token.IsElevated()
}

// installViaPackageManager - Scoop/Chocolatey (future: v1.1).
func (d *detector) installViaPackageManager(ctx context.Context, dep Dependency) (*InstallResult, error) {
	// TODO: Implement Scoop/Chocolatey in v1.1
	return &InstallResult{
		Dependency: dep,
		Success:    false,
		Error:      fmt.Errorf("package manager install not yet implemented on Windows"),
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

// installFromBundle extracts bundled binaries (v1.1).
func (d *detector) installFromBundle(ctx context.Context, dep Dependency) (*InstallResult, error) {
	// TODO: Implement bundled install in v1.1
	return &InstallResult{
		Dependency: dep,
		Success:    false,
		Error:      fmt.Errorf("bundled install not yet implemented"),
		Strategy:   StrategyBundled,
	}, nil
}

// installOpenTofu downloads and installs OpenTofu for Windows.
func (d *detector) installOpenTofu(ctx context.Context) (*InstallResult, error) {
	const (
		releaseURL = "https://api.github.com/repos/opentofu/opentofu/releases/latest"
		binName    = "tofu.exe"
	)

	dep := Dependency{Name: "OpenTofu", ExecutableName: "tofu"}

	// Get latest release
	downloadURL, version, err := d.getLatestRelease(ctx, releaseURL, "windows_amd64.zip")
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to get latest release: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	d.log.Info("Downloading OpenTofu", "version", version, "url", downloadURL)

	// Download to temp
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

	installDir, installRoot, err := openWindowsInstallRoot()
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to create install dir: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}
	defer installRoot.Close()

	if err := d.extractZip(tmpFile, installRoot, binName); err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("extraction failed: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	// Add to PATH
	if err := d.addToPath(installDir); err != nil {
		return &InstallResult{
			Dependency:    dep,
			Success:       true,
			Version:       version,
			InstalledPath: filepath.Join(installDir, binName),
			Strategy:      StrategyDownload,
			Message:       fmt.Sprintf("Installed but failed to add to PATH: %v. Please add manually.", err),
		}, nil
	}

	return &InstallResult{
		Dependency:    dep,
		Success:       true,
		Version:       version,
		InstalledPath: filepath.Join(installDir, binName),
		Strategy:      StrategyDownload,
	}, nil
}

// installCloudflared downloads and installs cloudflared for Windows.
func (d *detector) installCloudflared(ctx context.Context) (*InstallResult, error) {
	const (
		releaseURL = "https://api.github.com/repos/cloudflare/cloudflared/releases/latest"
		binName    = "cloudflared.exe"
	)

	dep := Dependency{Name: "Cloudflared", ExecutableName: "cloudflared"}

	downloadURL, version, err := d.getLatestRelease(ctx, releaseURL, "windows-amd64.exe")
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to get latest release: %w", err),
			Strategy:   StrategyDownload,
		}, nil
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

	installDir, installRoot, err := openWindowsInstallRoot()
	if err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to create install dir: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}
	defer installRoot.Close()

	// Copy exe
	destPath := filepath.Join(installDir, binName)
	if err := d.copyFile(tmpFile, installRoot, binName); err != nil {
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("failed to copy binary: %w", err),
			Strategy:   StrategyDownload,
		}, nil
	}

	if err := d.addToPath(installDir); err != nil {
		return &InstallResult{
			Dependency:    dep,
			Success:       true,
			Version:       version,
			InstalledPath: destPath,
			Strategy:      StrategyDownload,
			Message:       fmt.Sprintf("Installed but failed to add to PATH: %v", err),
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

// extractZip extracts a specific file from a ZIP archive.
func (d *detector) extractZip(zipPath string, destRoot *os.Root, targetFile string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if filepath.Base(f.Name) == targetFile {
			return d.extractZipFile(f, destRoot, targetFile)
		}
	}

	return fmt.Errorf("file %s not found in archive", targetFile)
}

func (d *detector) extractZipFile(f *zip.File, destRoot *os.Root, destName string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := destRoot.OpenFile(destName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}

// copyFile copies a file into a confined installation root.
func (d *detector) copyFile(src string, destRoot *os.Root, destName string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := destRoot.OpenFile(destName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func openWindowsInstallRoot() (string, *os.Root, error) {
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles"))
	if programFiles == "" || !filepath.IsAbs(programFiles) {
		return "", nil, fmt.Errorf("ProgramFiles must be an absolute path")
	}
	installDir := filepath.Join(programFiles, "kombifyTechstack", "bin")
	// #nosec G703 -- ProgramFiles is the Windows process environment boundary and is required to be absolute.
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", nil, err
	}
	installRoot, err := os.OpenRoot(installDir)
	if err != nil {
		return "", nil, err
	}
	return installDir, installRoot, nil
}

// addToPath adds a directory to the system PATH.
func (d *detector) addToPath(dir string) error {
	// Check for admin privileges before modifying system PATH
	if !isElevated() {
		return fmt.Errorf("modifying system PATH requires administrator privileges. Please run kombifyTechstack as administrator or add %s to PATH manually", dir)
	}

	// Get current PATH from registry
	currentPath, err := getSystemPath()
	if err != nil {
		return fmt.Errorf("failed to get system PATH: %w", err)
	}

	// Check if already in PATH
	if strings.Contains(strings.ToLower(currentPath), strings.ToLower(dir)) {
		return nil
	}

	// Add to PATH
	newPath := currentPath + ";" + dir
	return setSystemPath(newPath)
}

// getSystemPath reads the system PATH from registry.
func getSystemPath() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`,
		registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()

	path, _, err := k.GetStringValue("Path")
	return path, err
}

// setSystemPath writes the system PATH to registry.
func setSystemPath(newPath string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Environment`,
		registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	return k.SetStringValue("Path", newPath)
}
