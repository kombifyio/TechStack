// Package autoinstall provides automatic dependency detection and installation.
// It ensures all required tools (OpenTofu, cloudflared, Docker) are available
// without user intervention.
package autoinstall

import (
	"context"
	"fmt"
	"runtime"
)

// Dependency represents a required tool or package.
type Dependency struct {
	Name            string
	Version         string // Minimum required version
	ExecutableName  string // Binary name to check for
	Required        bool   // Critical dependency
	CheckCommand    string // Command to check if installed (e.g., "tofu version")
	InstallStrategy InstallStrategy
}

// InstallStrategy defines how to install a dependency.
type InstallStrategy int

const (
	// StrategyAuto lets the detector choose the best strategy.
	StrategyAuto InstallStrategy = iota
	// StrategyPackageManager uses system package manager (apt, yum, choco, scoop).
	StrategyPackageManager
	// StrategyDownload downloads binary from GitHub releases or official site.
	StrategyDownload
	// StrategyBundled uses pre-bundled binary from offline installer.
	StrategyBundled
	// StrategySkip skips installation (user must install manually).
	StrategySkip
)

// MissingDependency represents a dependency that needs to be installed.
type MissingDependency struct {
	Dependency
	Reason string // Why it's missing or version mismatch
}

// InstallResult tracks the outcome of an installation attempt.
type InstallResult struct {
	Dependency    Dependency
	Success       bool
	Version       string // Installed version
	InstalledPath string
	Error         error
	Strategy      InstallStrategy
	Message       string // Additional info (warnings, notes)
}

// Detector detects missing dependencies and can install them.
type Detector interface {
	// Detect scans for missing or outdated dependencies.
	Detect(ctx context.Context) ([]MissingDependency, error)

	// Install installs a single dependency.
	Install(ctx context.Context, dep Dependency) (*InstallResult, error)

	// InstallAll installs all missing dependencies.
	InstallAll(ctx context.Context, missing []MissingDependency) ([]InstallResult, error)

	// GetStrategy returns the preferred install strategy for current platform.
	GetStrategy(dep Dependency) InstallStrategy
}

// DefaultDependencies returns the list of required dependencies.
func DefaultDependencies() []Dependency {
	deps := []Dependency{
		{
			Name:            "OpenTofu",
			Version:         "1.6.0", // Minimum version
			ExecutableName:  tofuBinary(),
			Required:        true,
			CheckCommand:    tofuBinary() + " version",
			InstallStrategy: StrategyDownload, // Prefer download for cross-platform
		},
		{
			Name:            "cloudflared",
			ExecutableName:  cloudflaredBinary(),
			Required:        false, // Only for tunnel mode
			CheckCommand:    cloudflaredBinary() + " version",
			InstallStrategy: StrategyDownload,
		},
	}

	// Docker is optional on Windows (native mode), required on Linux
	dockerRequired := runtime.GOOS != "windows"
	deps = append(deps, Dependency{
		Name:            "Docker",
		ExecutableName:  "docker",
		Required:        dockerRequired,
		CheckCommand:    "docker version",
		InstallStrategy: StrategyPackageManager, // Complex install, prefer PM
	})

	return deps
}

// tofuBinary returns platform-specific OpenTofu binary name.
func tofuBinary() string {
	if runtime.GOOS == "windows" {
		return "tofu.exe"
	}
	return "tofu"
}

// cloudflaredBinary returns platform-specific cloudflared binary name.
func cloudflaredBinary() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// IsInstalled checks if a dependency is available in PATH.
func IsInstalled(dep Dependency) bool {
	// TODO: Implement in detector_common.go
	return false
}

// GetInstalledVersion returns the installed version of a dependency.
func GetInstalledVersion(dep Dependency) (string, error) {
	// TODO: Implement in detector_common.go
	return "", fmt.Errorf("not implemented")
}
