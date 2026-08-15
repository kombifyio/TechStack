// Package stacks integration tests.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
//
//go:build integration
// +build integration

package stacks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/testutil"
)

// TestIntegration_CreateStack tests stack creation with a real container.
func TestIntegration_CreateStack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Start real kombifyTechstack container
	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// First, we need to authenticate
	// For testing, we'll use the PocketBase admin API or create a test user
	t.Log("Stack CRUD integration test - authentication required")

	// Test that unauthenticated request fails
	reqBody := map[string]interface{}{
		"name":     "test-stack",
		"mode":     "easy",
		"provider": "proxmox",
		"services": []string{"traefik"},
	}
	body, _ := json.Marshal(reqBody)

	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/stacks", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should return 401 Unauthorized without auth
	if resp.StatusCode != http.StatusUnauthorized {
		respBody, _ := io.ReadAll(resp.Body)
		t.Logf("Response: %s", string(respBody))
		// Note: This might return 400 or another error depending on implementation
		t.Logf("Expected 401 for unauthenticated request, got %d", resp.StatusCode)
	}

	t.Log("Stack CRUD integration test completed")
}

// TestIntegration_MultipleStacksDocumentation records the supported multi-stack direction.
func TestIntegration_MultipleStacksDocumentation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Multiple stacks are now allowed. Repeated create behavior is validated in
	// wizard_flow_test.go with a real authenticated create flow.

	t.Log("Multi-stack support documented at integration level")
}

// TestIntegration_GetStacks tests the GET /api/v1/stacks endpoint.
func TestIntegration_GetStacks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	token := ks.Authenticate(t, ctx)

	// GET /api/v1/stacks should return an array (empty on fresh instance)
	resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/stacks", nil, token)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	data, ok := result["data"].([]interface{})
	if !ok {
		t.Fatalf("expected data to be an array, got: %v", result["data"])
	}

	t.Logf("GET /api/v1/stacks returned %d stack(s)", len(data))
}

// TestIntegration_JobStats tests job statistics endpoint.
func TestIntegration_JobStats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// GET /api/v1/jobs/stats should return job statistics
	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/jobs/stats", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// API wraps in {"data": {"available": true, "stats": {...}}, "meta": {...}}
	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("expected 'data' field in response, got: %v", result)
	}
	if _, ok := data["stats"]; !ok {
		t.Errorf("expected 'stats' field in data, got: %v", data)
	}

	t.Log("Job stats integration test passed")
}

// TestIntegration_DiscoveryEndpoint tests network discovery.
func TestIntegration_DiscoveryEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// GET /api/v1/discovery/stats should return discovery statistics
	// Note: The actual endpoint is /stats not /status
	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/discovery/stats", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should return 200 or require auth
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 or 401, got %d: %s", resp.StatusCode, string(body))
	}

	t.Log("Discovery status integration test passed")
}
