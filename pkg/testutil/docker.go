// Package testutil provides Docker-based integration test utilities.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// RequireDocker skips the test if Docker is not available.
// This is the MANDATORY check for all integration tests.
func RequireDocker(t *testing.T) {
	t.Helper()

	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("SKIP_DOCKER_TESTS=1 set, skipping Docker-based test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("The configured Docker-compatible engine is unavailable.\n"+
			"Error: %v\nOutput: %s\n"+
			"Start the local engine or fix DOCKER_HOST and try again.", err, string(output))
	}

	t.Logf("Docker available: version %s", string(output))
}

// GetProjectRoot returns the absolute path to the kombifyTechstack project root.
func GetProjectRoot() (string, error) {
	// Try to find go.mod by walking up
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot get working directory: %w", err)
	}

	for {
		if _, err := os.Stat(wd + "/go.mod"); err == nil {
			return wd, nil
		}

		// Move to parent directory
		parent := wd
		for i := len(wd) - 1; i >= 0; i-- {
			if wd[i] == '/' || wd[i] == '\\' {
				parent = wd[:i]
				break
			}
		}

		if parent == wd {
			return "", fmt.Errorf("could not find project root (go.mod not found)")
		}
		wd = parent
	}
}
