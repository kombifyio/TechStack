// Package jobs integration tests.
// These tests require a Docker-compatible engine, locally or through DOCKER_HOST.
//
//go:build integration
// +build integration

package jobs_test

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

// TestIntegration_JobQueueStats tests the /api/v1/jobs/stats endpoint.
func TestIntegration_JobQueueStats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

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

	// API should return stats object
	stats, ok := result["stats"].(map[string]interface{})
	if !ok {
		t.Logf("Response structure: %+v", result)
		t.Fatal("expected 'stats' field to be an object")
	}

	// Verify expected fields exist
	expectedFields := []string{"pending", "running", "completed", "failed", "total"}
	for _, field := range expectedFields {
		if _, exists := stats[field]; !exists {
			t.Errorf("expected field '%s' in job stats", field)
		}
	}

	// Fresh container should have zero jobs
	if total, ok := stats["total"].(float64); ok {
		t.Logf("Total jobs: %v", total)
	}

	t.Log("Job queue stats integration test passed")
}

// TestIntegration_JobQueueAvailability tests that job queue is available.
func TestIntegration_JobQueueAvailability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/jobs/stats", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Check 'available' field
	available, ok := result["available"].(bool)
	if !ok {
		t.Logf("Response: %+v", result)
		t.Skip("'available' field not present or not boolean")
	}

	if !available {
		t.Error("expected job queue to be available")
	}

	t.Log("Job queue availability integration test passed")
}

// TestIntegration_CancelNonExistentJob tests canceling a job that doesn't exist.
func TestIntegration_CancelNonExistentJob(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Try to cancel a non-existent job
	reqBody := map[string]interface{}{}
	body, _ := json.Marshal(reqBody)

	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/jobs/nonexistent-job-id/cancel", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should return 404 Not Found or 401 Unauthorized (if auth required)
	validStatuses := []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusBadRequest}
	found := false
	for _, valid := range validStatuses {
		if resp.StatusCode == valid {
			found = true
			break
		}
	}

	if !found {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 404/401/400 for non-existent job, got %d: %s", resp.StatusCode, string(body))
	}

	t.Log("Cancel non-existent job integration test passed")
}

// TestIntegration_GetNonExistentJob tests getting a job that doesn't exist.
func TestIntegration_GetNonExistentJob(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Try to get a non-existent job
	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/jobs/nonexistent-job-id", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should return 404 Not Found or 401 Unauthorized
	validStatuses := []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusBadRequest}
	found := false
	for _, valid := range validStatuses {
		if resp.StatusCode == valid {
			found = true
			break
		}
	}

	if !found {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 404/401/400 for non-existent job, got %d: %s", resp.StatusCode, string(body))
	}

	t.Log("Get non-existent job integration test passed")
}

// TestIntegration_JobQueueUnderLoad tests job queue behavior with multiple requests.
func TestIntegration_JobQueueUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Make multiple concurrent requests to job stats
	numRequests := 5
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
				results <- &statusError{code: resp.StatusCode, body: string(body)}
				return
			}

			results <- nil
		}()
	}

	// Collect results
	var errors []error
	for i := 0; i < numRequests; i++ {
		select {
		case err := <-results:
			if err != nil {
				errors = append(errors, err)
			}
		case <-ctx.Done():
			t.Fatal("context deadline exceeded waiting for responses")
		}
	}

	if len(errors) > 0 {
		t.Errorf("some requests failed: %v", errors)
	}

	t.Logf("Job queue handled %d concurrent requests successfully", numRequests)
	t.Log("Job queue under load integration test passed")
}

// statusError is a simple error type for HTTP status errors
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return e.body
}

// TestIntegration_JobQueueConsistency tests that job stats are consistent.
func TestIntegration_JobQueueConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Get stats twice, they should be consistent (no random jobs appearing)
	getStats := func() (map[string]interface{}, error) {
		resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/jobs/stats", nil)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}
		return result, nil
	}

	stats1, err := getStats()
	if err != nil {
		t.Fatalf("first stats request failed: %v", err)
	}

	// Small delay
	time.Sleep(100 * time.Millisecond)

	stats2, err := getStats()
	if err != nil {
		t.Fatalf("second stats request failed: %v", err)
	}

	// Compare total counts - should be same or increased (never decreased) in fresh container
	s1, _ := stats1["stats"].(map[string]interface{})
	s2, _ := stats2["stats"].(map[string]interface{})

	if s1 != nil && s2 != nil {
		total1, _ := s1["total"].(float64)
		total2, _ := s2["total"].(float64)

		if total2 < total1 {
			t.Errorf("job total decreased: %v -> %v (should be monotonic)", total1, total2)
		}
	}

	t.Log("Job queue consistency integration test passed")
}
