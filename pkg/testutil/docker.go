// Package testutil provides Docker-based integration test utilities.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
package testutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	} else {
		cmd = exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("The configured Docker-compatible engine is unavailable.\n"+
			"Error: %v\nOutput: %s\n"+
			"Start the local engine or fix DOCKER_HOST and try again.", err, string(output))
	}

	t.Logf("Docker available: version %s", string(output))
}

// DockerComposeUp starts the docker-compose stack for integration testing.
// Returns a cleanup function that should be deferred.
func DockerComposeUp(t *testing.T, composeFile string) func() {
	t.Helper()
	RequireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Build and start containers
	cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "up", "-d", "--build", "--wait")
	cmd.Dir = projectRoot()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker-compose up failed: %v\nOutput: %s", err, string(output))
	}

	t.Logf("Docker Compose started: %s", composeFile)

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "docker", "compose", "-f", composeFile, "down", "-v", "--remove-orphans")
		cmd.Dir = projectRoot()
		_ = cmd.Run() // Best effort cleanup
	}
}

// WaitForService waits until the given URL returns a successful HTTP response.
func WaitForService(t *testing.T, url string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, "curl", "-sf", url)
		err := cmd.Run()
		cancel()

		if err == nil {
			t.Logf("Service ready: %s", url)
			return
		}

		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("Service not ready after %s: %s", timeout, url)
}

// projectRoot returns the root directory of the kombifyTechstack project.
func projectRoot() string {
	// Walk up from current working directory to find go.mod
	wd, _ := os.Getwd()
	for {
		if _, err := os.Stat(wd + "/go.mod"); err == nil {
			return wd
		}
		parent := wd[:len(wd)-len("/"+wd[len(wd)-1:])]
		if parent == wd {
			break
		}
		wd = parent
	}

	// Fallback: assume we're in the project root
	wd, _ = os.Getwd()
	return wd
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
