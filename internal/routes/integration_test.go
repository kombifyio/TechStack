// Package routes integration tests.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
//
//go:build integration
// +build integration

package routes_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/testutil"
)

// TestIntegration_HealthEndpoint tests the health endpoint with a real container.
func TestIntegration_HealthEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Start real kombifyTechstack container
	ks := testutil.StartkombifyTechstack(t, ctx)

	// Test main health endpoint
	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Check expected fields
	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object in response: %v", result)
	}

	if status, _ := data["status"].(string); status != "healthy" {
		t.Errorf("expected status=healthy, got %s", status)
	}

	if service, _ := data["service"].(string); service != "kombify-techstack" {
		t.Errorf("expected service=kombify-techstack, got %s", service)
	}

	t.Logf("Health check passed: version=%v", data["version"])
}

// TestIntegration_LivenessProbe tests the Kubernetes liveness probe.
func TestIntegration_LivenessProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/health/live", nil)
	if err != nil {
		t.Fatalf("liveness request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("liveness probe failed: %d: %s", resp.StatusCode, string(body))
	}

	t.Log("Liveness probe passed")
}

// TestIntegration_ReadinessProbe tests the Kubernetes readiness probe.
func TestIntegration_ReadinessProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/health/ready", nil)
	if err != nil {
		t.Fatalf("readiness request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("readiness probe failed: %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	data, ok := result["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object in response")
	}

	checks, ok := data["checks"].(map[string]any)
	if !ok {
		t.Fatalf("expected checks object in response")
	}

	if dbCheck, _ := checks["database"].(bool); !dbCheck {
		t.Errorf("database check failed")
	}

	t.Logf("Readiness probe passed: checks=%v", checks)
}

// TestIntegration_StartupProbe tests the Kubernetes startup probe.
func TestIntegration_StartupProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/health/startup", nil)
	if err != nil {
		t.Fatalf("startup request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("startup probe failed: %d: %s", resp.StatusCode, string(body))
	}

	t.Log("Startup probe passed")
}
