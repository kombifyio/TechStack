// Package orchestrator integration tests.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
//
//go:build integration
// +build integration

package orchestrator_test

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

// TestIntegration_OrchestratorJobQueue tests that the orchestrator job queue
// is operational in a real container.
func TestIntegration_OrchestratorJobQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Start real kombifyTechstack container
	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// GET /api/v1/jobs/stats should return queue statistics
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

	// API returns {"available": true, "stats": {...}}
	stats, ok := result["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected 'stats' field to be an object, got %T (full response: %v)", result["stats"], result)
	}

	// Fresh container should have zero jobs in queue
	// Check for expected fields in stats
	expectedFields := []string{"pending", "running", "completed", "failed", "total"}
	for _, field := range expectedFields {
		if _, exists := stats[field]; !exists {
			t.Errorf("expected field '%s' in job stats", field)
		}
	}

	t.Logf("Job stats: %+v", stats)
	t.Log("Orchestrator job queue integration test passed")
}

// TestIntegration_OrchestratorHealth tests orchestrator health via probe endpoints.
func TestIntegration_OrchestratorHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// The orchestrator's health is reflected in the readiness probe
	// When orchestrator is running, the system should be ready
	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/health/ready", nil)
	if err != nil {
		t.Fatalf("readiness probe request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("readiness probe failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// API returns {"data": {"status": "ready", "checks": {...}}}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected 'data' field to be an object, got %T (full response: %v)", result["data"], result)
	}

	// Check that status is "ready"
	status, ok := data["status"].(string)
	if !ok || status != "ready" {
		t.Errorf("expected status 'ready', got '%v'", data["status"])
	}

	t.Log("Orchestrator health integration test passed")
}

// TestIntegration_JobSubmissionWithoutStack tests that job submission fails
// appropriately when no stack exists.
func TestIntegration_JobSubmissionWithoutStack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Try to provision without a stack existing
	// This should fail with an appropriate error
	reqBody := map[string]interface{}{
		"stack_id": "nonexistent-stack-id",
	}
	body, _ := json.Marshal(reqBody)

	// POST /api/v1/stacks/{id}/provision should fail for non-existent stack
	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/stacks/nonexistent-stack-id/provision", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should return 404 (not found) or 401 (unauthorized) or 400 (bad request)
	validStatuses := []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusBadRequest}
	found := false
	for _, valid := range validStatuses {
		if resp.StatusCode == valid {
			found = true
			break
		}
	}

	if !found {
		respBody, _ := io.ReadAll(resp.Body)
		t.Logf("Response body: %s", string(respBody))
		t.Fatalf("expected one of %v, got %d", validStatuses, resp.StatusCode)
	}

	t.Logf("Job submission without stack returned status %d (expected behavior)", resp.StatusCode)
	t.Log("Job submission without stack integration test passed")
}

// TestIntegration_OrchestratorConcurrency tests that the orchestrator handles
// concurrent requests appropriately.
func TestIntegration_OrchestratorConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Make concurrent requests to job stats endpoint
	const numRequests = 10
	results := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/jobs/stats", nil)
			if err != nil {
				results <- err
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				results <- &testError{msg: "unexpected status: " + string(body)}
				return
			}
			results <- nil
		}()
	}

	// Collect results
	var errors []error
	for i := 0; i < numRequests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		t.Errorf("had %d errors in concurrent requests: %v", len(errors), errors)
	}

	t.Log("Orchestrator concurrency integration test passed")
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
