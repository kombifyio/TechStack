package autoinstall

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/logger"
)

func TestNewDetector(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewDetector(log)

	if det == nil {
		t.Fatal("NewDetector returned nil")
	}
}

func TestDetect(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewDetector(log)

	ctx := context.Background()
	missing, err := det.Detect(ctx)

	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// At least OpenTofu should be missing in test environment
	t.Logf("Missing dependencies: %d", len(missing))
	for _, m := range missing {
		t.Logf("  - %s: %s", m.Name, m.Reason)
	}
}

func TestGetStrategy(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewDetector(log).(*detector)

	tests := []struct {
		name     string
		dep      Dependency
		expected InstallStrategy
	}{
		{
			name:     "explicit strategy",
			dep:      Dependency{Name: "Test", InstallStrategy: StrategyPackageManager},
			expected: StrategyPackageManager,
		},
		{
			name:     "default strategy",
			dep:      Dependency{Name: "Test"},
			expected: StrategyDownload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := det.GetStrategy(tt.dep)
			if strategy != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, strategy)
			}
		})
	}
}

func TestIsInstalled(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewDetector(log).(*detector)

	tests := []struct {
		name     string
		dep      Dependency
		expected bool
	}{
		{
			name:     "existing binary (go)",
			dep:      Dependency{Name: "Go", ExecutableName: "go"},
			expected: true,
		},
		{
			name:     "non-existing binary",
			dep:      Dependency{Name: "Fake", ExecutableName: "fakebinary12345"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := det.isInstalled(tt.dep)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetVersion(t *testing.T) {
	log := logger.New("debug", "json")
	det := NewDetector(log).(*detector)

	dep := Dependency{
		Name:           "Go",
		ExecutableName: "go",
		CheckCommand:   "go version",
	}

	version, err := det.getVersion(dep)
	if err != nil {
		t.Logf("getVersion failed (expected in CI): %v", err)
		return
	}

	t.Logf("Go version: %s", version)
}

func TestDefaultDependencies(t *testing.T) {
	deps := DefaultDependencies()

	if len(deps) == 0 {
		t.Fatal("DefaultDependencies returned empty list")
	}

	t.Logf("Default dependencies: %d", len(deps))
	for _, dep := range deps {
		t.Logf("  - %s (%s)", dep.Name, dep.ExecutableName)

		if dep.Name == "" {
			t.Error("dependency has empty name")
		}
		if dep.ExecutableName == "" {
			t.Error("dependency has empty executable name")
		}
	}
}

func TestStrategyName(t *testing.T) {
	tests := []struct {
		strategy InstallStrategy
		expected string
	}{
		{StrategyAuto, "auto"},
		{StrategyPackageManager, "package_manager"},
		{StrategyDownload, "download"},
		{StrategyBundled, "bundled"},
		{StrategySkip, "skip"},
		{InstallStrategy(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := strategyName(tt.strategy)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
