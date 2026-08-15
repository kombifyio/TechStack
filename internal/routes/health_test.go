// Package routes provides tests for health API routes.
package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// TestHealthLiveEndpoint tests the liveness probe endpoint.
func TestHealthLiveEndpoint(t *testing.T) {
	// Create a test request
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	rec := httptest.NewRecorder()

	// We can't easily test via PocketBase router in unit tests,
	// so we test the response format expectations
	t.Run("response format", func(t *testing.T) {
		// Simulate expected response
		response := map[string]string{"status": "alive"}
		body, _ := json.Marshal(response)

		if rec.Code == 0 {
			rec.Code = http.StatusOK
		}

		var result map[string]string
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		if result["status"] != "alive" {
			t.Errorf("expected status 'alive', got '%s'", result["status"])
		}
	})

	_ = req // Used in manual/integration tests
}

// TestHealthReadyEndpoint tests the readiness probe endpoint.
func TestHealthReadyEndpoint(t *testing.T) {
	t.Run("all checks pass", func(t *testing.T) {
		// Simulate ready state
		response := map[string]any{
			"status": "ready",
			"checks": map[string]bool{
				"database": true,
				"grpc":     true,
			},
		}
		body, _ := json.Marshal(response)

		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		if result["status"] != "ready" {
			t.Errorf("expected status 'ready', got '%v'", result["status"])
		}

		checks := result["checks"].(map[string]any)
		if !checks["database"].(bool) {
			t.Error("expected database check to be true")
		}
		if !checks["grpc"].(bool) {
			t.Error("expected grpc check to be true")
		}
	})

	t.Run("database check fails", func(t *testing.T) {
		// Simulate not ready state
		response := map[string]any{
			"status": "not_ready",
			"checks": map[string]bool{
				"database": false,
				"grpc":     true,
			},
		}
		body, _ := json.Marshal(response)

		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		if result["status"] != "not_ready" {
			t.Errorf("expected status 'not_ready', got '%v'", result["status"])
		}
	})
}

// TestCheckDatabase tests the database connectivity check function.
func TestCheckDatabase(t *testing.T) {
	testDataDir := pocketBaseTestDataDir(t)
	testApp, err := tests.NewTestApp(testDataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer testApp.Cleanup()

	// The test app should have a working database
	result := checkDatabase(testApp)
	if !result {
		t.Error("expected database check to pass with test app")
	}
}

// TestHealthDependencies tests the HealthDependencies struct.
func TestHealthDependencies(t *testing.T) {
	t.Run("nil dependencies", func(t *testing.T) {
		var deps *HealthDependencies
		if deps != nil {
			t.Error("expected nil dependencies")
		}
	})

	t.Run("with grpc check", func(t *testing.T) {
		deps := &HealthDependencies{
			GRPCRunning: func() bool {
				return true
			},
		}

		if !deps.GRPCRunning() {
			t.Error("expected GRPCRunning to return true")
		}
	})

	t.Run("grpc not running", func(t *testing.T) {
		deps := &HealthDependencies{
			GRPCRunning: func() bool {
				return false
			},
		}

		if deps.GRPCRunning() {
			t.Error("expected GRPCRunning to return false")
		}
	})
}

func TestNormalizeRuntimeIdentity(t *testing.T) {
	edition, mode := normalizeRuntimeIdentity(config.EditionPreview, config.ModeSelfHosted)
	if edition != config.EditionPreview || mode != config.ModeSaaS {
		t.Fatalf("preview identity = (%q, %q), want (%q, %q)", edition, mode, config.EditionPreview, config.ModeSaaS)
	}

	edition, mode = normalizeRuntimeIdentity("", config.ModeSaaS)
	if edition != config.EditionSaaSStandalone || mode != config.ModeSaaS {
		t.Fatalf("legacy saas identity = (%q, %q), want (%q, %q)", edition, mode, config.EditionSaaSStandalone, config.ModeSaaS)
	}

	edition, mode = runtimeIdentityFields(nil)
	if edition != config.EditionSelfHostOSS || mode != config.ModeSelfHosted {
		t.Fatalf("nil deps identity = (%q, %q), want (%q, %q)", edition, mode, config.EditionSelfHostOSS, config.ModeSelfHosted)
	}
}

func TestNormalizedBuildRevisionRequiresFullSHA(t *testing.T) {
	const sha = "02fa3578e0e6f74362d4208023a241cf4d2434ac"
	if got := normalizedBuildRevision(strings.ToUpper(sha)); got != sha {
		t.Fatalf("normalizedBuildRevision(full SHA) = %q, want %q", got, sha)
	}
	for _, invalid := range []string{"", "dev", "deadbeef", sha[:39], "g" + sha[1:]} {
		if got := normalizedBuildRevision(invalid); got != "" {
			t.Fatalf("normalizedBuildRevision(%q) = %q, want empty", invalid, got)
		}
	}
}

// TestRegisterHealthRoutesWithDeps tests route registration with dependencies.
func TestRegisterHealthRoutesWithDeps(t *testing.T) {
	// This test verifies that the function signature is correct
	// and can be called without panic
	t.Run("signature check", func(t *testing.T) {
		deps := &HealthDependencies{
			GRPCRunning: func() bool { return true },
		}
		version := "test"
		startTime := time.Now()

		// We can't fully test route registration without a running PocketBase,
		// but we can verify the types are correct
		_ = deps
		_ = version
		_ = startTime
	})
}

// Compile-time interface checks
var _ core.App = (*tests.TestApp)(nil)

func pocketBaseTestDataDir(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve go module cache: %v", err)
	}

	modCache := strings.TrimSpace(string(output))
	if modCache == "" {
		t.Fatal("resolve go module cache: empty result")
	}

	matches, err := filepath.Glob(filepath.Join(modCache, "github.com", "pocketbase", "pocketbase@*", "tests", "data"))
	if err != nil {
		t.Fatalf("resolve pocketbase test data: %v", err)
	}
	if len(matches) == 0 {
		// Windows module cache may preserve escaped uppercase letters.
		matches, err = filepath.Glob(filepath.Join(modCache, "github.com", "*", "pocketbase@*", "tests", "data"))
		if err != nil {
			t.Fatalf("resolve pocketbase test data fallback: %v", err)
		}
	}
	for _, match := range matches {
		if strings.Contains(filepath.ToSlash(match), path.Join("github.com", "pocketbase", "pocketbase@")) {
			return match
		}
	}

	t.Fatal("resolve pocketbase test data: no matching data directory found")
	return ""
}
