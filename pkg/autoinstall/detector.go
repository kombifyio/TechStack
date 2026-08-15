// Package autoinstall - Common detection logic.
package autoinstall

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kombifyio/techstack/pkg/logger"
)

// detector implements the Detector interface.
type detector struct {
	log *logger.Logger
}

// NewDetector creates a new dependency detector.
func NewDetector(log *logger.Logger) Detector {
	return &detector{
		log: log,
	}
}

// Detect scans for missing dependencies.
func (d *detector) Detect(ctx context.Context) ([]MissingDependency, error) {
	deps := DefaultDependencies()
	var missing []MissingDependency

	for _, dep := range deps {
		if !d.isInstalled(dep) {
			missing = append(missing, MissingDependency{
				Dependency: dep,
				Reason:     fmt.Sprintf("%s not found in PATH", dep.Name),
			})
			continue
		}

		// Check version if installed
		version, err := d.getVersion(dep)
		if err != nil {
			d.log.Warn("Failed to get version", "dependency", dep.Name, "error", err)
			continue
		}

		// TODO: Version comparison
		d.log.Info("Dependency available", "name", dep.Name, "version", version)
	}

	return missing, nil
}

// Install installs a single dependency.
func (d *detector) Install(ctx context.Context, dep Dependency) (*InstallResult, error) {
	strategy := d.GetStrategy(dep)

	d.log.Info("Installing dependency",
		"name", dep.Name,
		"strategy", strategyName(strategy))

	switch strategy {
	case StrategyPackageManager:
		return d.installViaPackageManager(ctx, dep)
	case StrategyDownload:
		return d.installViaDownload(ctx, dep)
	case StrategyBundled:
		return d.installFromBundle(ctx, dep)
	case StrategySkip:
		return &InstallResult{
			Dependency: dep,
			Success:    false,
			Error:      fmt.Errorf("installation skipped - manual install required"),
			Strategy:   strategy,
		}, nil
	default:
		return nil, fmt.Errorf("unknown install strategy: %v", strategy)
	}
}

// InstallAll installs all missing dependencies.
func (d *detector) InstallAll(ctx context.Context, missing []MissingDependency) ([]InstallResult, error) {
	results := make([]InstallResult, 0, len(missing))

	for _, miss := range missing {
		result, err := d.Install(ctx, miss.Dependency)
		if err != nil {
			d.log.Error("Failed to install dependency",
				"name", miss.Name,
				"error", err)
			results = append(results, InstallResult{
				Dependency: miss.Dependency,
				Success:    false,
				Error:      err,
			})
			continue
		}
		results = append(results, *result)
	}

	return results, nil
}

// GetStrategy returns the preferred install strategy.
func (d *detector) GetStrategy(dep Dependency) InstallStrategy {
	// Use explicit strategy if set
	if dep.InstallStrategy != StrategyAuto {
		return dep.InstallStrategy
	}

	// Default: Download for most tools
	return StrategyDownload
}

// isInstalled checks if a dependency is in PATH.
func (d *detector) isInstalled(dep Dependency) bool {
	_, err := exec.LookPath(dep.ExecutableName)
	return err == nil
}

// getVersion gets the installed version.
func (d *detector) getVersion(dep Dependency) (string, error) {
	if dep.CheckCommand == "" {
		return "unknown", nil
	}

	parts := strings.Fields(dep.CheckCommand)
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid check command")
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}

	// Parse version from output (simple heuristic)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), "version") {
			return strings.TrimSpace(line), nil
		}
	}

	return string(output), nil
}

func strategyName(s InstallStrategy) string {
	switch s {
	case StrategyAuto:
		return "auto"
	case StrategyPackageManager:
		return "package_manager"
	case StrategyDownload:
		return "download"
	case StrategyBundled:
		return "bundled"
	case StrategySkip:
		return "skip"
	default:
		return "unknown"
	}
}
