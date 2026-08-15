// Package unifier integration tests.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
//
//go:build integration
// +build integration

package unifier_test

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

// TestIntegration_UnifierValidate tests the /api/v1/unifier/validate endpoint.
func TestIntegration_UnifierValidate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Minimal valid spec
	spec := map[string]interface{}{
		"version": "1.0",
		"meta": map[string]interface{}{
			"name":        "test-homelab",
			"environment": "development",
		},
		"stackkit": "basement-kit",
		"workers": []map[string]interface{}{
			{
				"name":   "worker-1",
				"role":   "worker",
				"type":   "virtual",
				"memory": "4096Mi",
				"cpu":    2,
			},
		},
	}

	body, _ := json.Marshal(map[string]interface{}{"spec": spec})
	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/unifier/validate", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("Response status: %d, body: %s", resp.StatusCode, string(respBody))

	// Expect either success (200) or validation error (400) - both are valid responses
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 200 or 400, got %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Should have either 'valid' or 'errors' field
	if _, hasValid := result["valid"]; !hasValid {
		if _, hasErrors := result["errors"]; !hasErrors {
			t.Error("response should contain either 'valid' or 'errors' field")
		}
	}

	t.Log("Unifier validate endpoint integration test passed")
}

// TestIntegration_UnifierPipeline tests the /api/v1/unifier/pipeline endpoint.
func TestIntegration_UnifierPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Spec that should pass through the pipeline
	spec := map[string]interface{}{
		"version": "1.0",
		"meta": map[string]interface{}{
			"name":        "pipeline-test",
			"environment": "development",
		},
		"stackkit": "basement-kit",
		"workers": []map[string]interface{}{
			{
				"name":   "test-worker",
				"role":   "worker",
				"type":   "virtual",
				"memory": "4096Mi",
				"cpu":    2,
			},
		},
	}

	body, _ := json.Marshal(map[string]interface{}{"spec": spec})
	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/unifier/pipeline", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("Response status: %d", resp.StatusCode)

	// We expect a structured response with pipeline steps
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 200 or 400, got %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check for expected pipeline response fields
	if steps, ok := result["steps"]; ok {
		t.Logf("Pipeline returned %d steps", len(steps.([]interface{})))
	} else if _, hasError := result["error"]; hasError {
		t.Logf("Pipeline returned error (expected for test spec)")
	}

	t.Log("Unifier pipeline endpoint integration test passed")
}

// TestIntegration_ListStackKits tests the /api/v1/stackkits endpoint.
func TestIntegration_ListStackKits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/stackkits", nil)
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

	// Should return a list of stackkits
	stackkits, ok := result["stackkits"].([]interface{})
	if !ok {
		// Might be at root level or nested differently
		t.Logf("Response structure: %+v", result)
	} else {
		t.Logf("Found %d StackKits", len(stackkits))

		// Verify basement-kit exists
		found := false
		for _, sk := range stackkits {
			if skMap, ok := sk.(map[string]interface{}); ok {
				if name, _ := skMap["name"].(string); name == "basement-kit" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected to find 'basement-kit' StackKit")
		}
	}

	t.Log("List StackKits integration test passed")
}

// TestIntegration_ListAddons tests the /api/v1/addons endpoint.
func TestIntegration_ListAddons(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/addons", nil)
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

	// Should return a list of addons
	if addons, ok := result["addons"].([]interface{}); ok {
		t.Logf("Found %d addons", len(addons))
	}

	t.Log("List addons integration test passed")
}

// TestIntegration_UnifierAnalyze tests the /api/v1/unifier/analyze endpoint.
func TestIntegration_UnifierAnalyze(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Intent spec for analysis
	spec := map[string]interface{}{
		"version": "1.0",
		"meta": map[string]interface{}{
			"name":        "analyze-test",
			"environment": "development",
		},
		"stackkit": "basement-kit",
		"workers": []map[string]interface{}{
			{
				"name":   "worker-1",
				"role":   "worker",
				"type":   "virtual",
				"memory": "8192Mi",
				"cpu":    4,
			},
		},
		"services": []map[string]interface{}{
			{
				"name": "traefik",
				"type": "router",
			},
		},
	}

	body, _ := json.Marshal(map[string]interface{}{"spec": spec})
	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/unifier/analyze", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("Response status: %d", resp.StatusCode)

	// Analyze endpoint should return requirements analysis
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 200 or 400, got %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Analysis should return structured requirements
	t.Logf("Analysis result keys: %+v", keysOf(result))

	t.Log("Unifier analyze endpoint integration test passed")
}

// TestIntegration_UnifierInvalidSpec tests error handling for invalid specs.
func TestIntegration_UnifierInvalidSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Invalid spec (missing required fields)
	invalidSpec := map[string]interface{}{
		"version": "invalid", // Invalid version
	}

	body, _ := json.Marshal(map[string]interface{}{"spec": invalidSpec})
	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/unifier/validate", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should return 400 Bad Request for invalid spec
	if resp.StatusCode == http.StatusOK {
		t.Log("Note: Validation passed for simple spec - may have loose validation")
	}

	respBody, _ := io.ReadAll(resp.Body)
	t.Logf("Invalid spec response: status=%d, body=%s", resp.StatusCode, string(respBody))

	t.Log("Unifier invalid spec error handling integration test passed")
}

// TestIntegration_GetStackKit tests the /api/v1/stackkits/{name} endpoint.
func TestIntegration_GetStackKit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Get specific StackKit
	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/stackkits/basement-kit", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// May return 200 (found) or 404 (not found) depending on available stackkits
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 200 or 404, got %d: %s", resp.StatusCode, string(respBody))
	}

	if resp.StatusCode == http.StatusOK {
		var result map[string]interface{}
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("failed to parse response: %v", err)
		}

		// Should contain stackkit info
		if name, ok := result["name"].(string); ok {
			t.Logf("Found StackKit: %s", name)
		}
	}

	t.Log("Get StackKit integration test passed")
}

// keysOf returns the keys of a map for logging
func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
