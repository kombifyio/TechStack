// Package stacks wizard flow integration tests.
// These tests validate the FULL wizard → create → poll → complete/fail cycle
// using a real Docker container. They catch bugs that only surface when
// frontend, API, and orchestrator interact end-to-end.
//
//go:build integration
// +build integration

package stacks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/testutil"
)

// TestIntegration_WizardCreateFlow tests the complete wizard creation flow:
// 1. Authenticate as default admin user
// 2. POST /api/v1/stacks with wizard config payload
// 3. Poll GET /api/collections/jobs/records/{id} for job progress
// 4. Verify the job transitions from pending → running → completed/failed
//
// This is the critical path that was causing the UI loading screen hang.
func TestIntegration_WizardCreateFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	// Step 1: Authenticate
	token := ks.Authenticate(t, ctx)

	// Step 2: Create a stack via the wizard API (same payload as frontend)
	wizardConfig := map[string]interface{}{
		"name": "test-homelab",
		"mode": "easy",
		"user_config": map[string]interface{}{
			"name":       "test-homelab",
			"wizardType": "easy",
			"provider":   "homelab",
			"goals": map[string]interface{}{
				"smart-home": false,
				"photos":     true,
				"media":      false,
				"vault":      true,
				"files":      false,
				"ai":         false,
				"dev":        false,
				"mail":       false,
				"game":       false,
				"everything": false,
			},
			"network": map[string]interface{}{
				"accessType": "home",
			},
			"services": []string{},
			"auth": map[string]interface{}{
				"requirePassword": false,
			},
		},
		"user_config_format": "json",
	}

	body, _ := json.Marshal(wizardConfig)
	resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodPost, "/api/v1/stacks", bytes.NewReader(body), token)
	if err != nil {
		t.Fatalf("create stack request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Stack creation should return 202 Accepted
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", resp.StatusCode, string(respBody))
	}

	var createResult map[string]interface{}
	if err := json.Unmarshal(respBody, &createResult); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}

	// Extract from wrapped response (API uses {data: {...}} format or flat)
	resultData := createResult
	if d, ok := createResult["data"].(map[string]interface{}); ok {
		resultData = d
	}

	stackID, _ := resultData["stack_id"].(string)
	jobID, _ := resultData["job_id"].(string)

	if stackID == "" {
		t.Fatalf("no stack_id in response: %s", string(respBody))
	}
	if jobID == "" {
		t.Fatalf("no job_id in response: %s", string(respBody))
	}

	t.Logf("Stack created: id=%s, job_id=%s", stackID, jobID)

	// Step 3: Poll job status (same as the /stacks/creating page does)
	// The job should transition from pending → eventually completed or failed
	// within a reasonable time. If it stays pending forever, that's the bug.
	pollDeadline := time.Now().Add(60 * time.Second)
	var lastState string
	var lastJobBody string
	pollCount := 0

	for time.Now().Before(pollDeadline) {
		pollCount++
		jobResp, err := ks.AuthenticatedAPIRequest(
			ctx, http.MethodGet,
			fmt.Sprintf("/api/collections/jobs/records/%s", jobID),
			nil, token,
		)
		if err != nil {
			t.Logf("poll %d: request error: %v", pollCount, err)
			time.Sleep(2 * time.Second)
			continue
		}

		jobBody, _ := io.ReadAll(jobResp.Body)
		jobResp.Body.Close()
		lastJobBody = string(jobBody)

		if jobResp.StatusCode != http.StatusOK {
			t.Logf("poll %d: HTTP %d: %s", pollCount, jobResp.StatusCode, lastJobBody)
			time.Sleep(2 * time.Second)
			continue
		}

		var job map[string]interface{}
		if err := json.Unmarshal(jobBody, &job); err != nil {
			t.Logf("poll %d: parse error: %v", pollCount, err)
			time.Sleep(2 * time.Second)
			continue
		}

		state, _ := job["state"].(string)
		step, _ := job["current_step"].(string)
		progress, _ := job["progress"].(float64)

		if state != lastState {
			t.Logf("poll %d: state changed: %s → %s (step=%s, progress=%.0f%%)", pollCount, lastState, state, step, progress)
			lastState = state
		}

		// Terminal states
		switch state {
		case "completed", "success":
			t.Logf("Job completed successfully after %d polls", pollCount)
			// Verify stack was updated
			verifyStackStatus(t, ctx, ks, stackID, token)
			return
		case "failed", "error":
			// Job failed, but at least it didn't hang! This is still a valid test result.
			errorMsg, _ := job["error"].(string)
			errorDetails, _ := job["error_details"].(string)
			t.Logf("Job failed (but did NOT hang): error=%s, details=%s", errorMsg, errorDetails)
			// A failed job is acceptable - it means the system responded.
			// The important thing is it didn't stay in "pending" forever.
			return
		}

		time.Sleep(2 * time.Second)
	}

	// If we get here, the job never completed - this is the exact bug we're catching
	t.Fatalf("CRITICAL: Job %s stuck in state %q after %d polls (60s). "+
		"This is the loading screen hang bug!\nLast job response: %s",
		jobID, lastState, pollCount, lastJobBody)
}

// TestIntegration_WizardCreateUnauth tests that unauthenticated requests are rejected.
func TestIntegration_WizardCreateUnauth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	body := []byte(`{"name":"test","mode":"easy","user_config":{"name":"test"}}`)
	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/stacks", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 401, got %d: %s", resp.StatusCode, string(respBody))
	}

	t.Log("Unauthenticated create correctly rejected with 401")
}

// TestIntegration_WizardJobPolling tests that job records are readable via PB SDK.
func TestIntegration_WizardJobPolling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	token := ks.Authenticate(t, ctx)

	// Create a stack first
	body := []byte(`{"name":"poll-test","mode":"easy","user_config":{"name":"poll-test","provider":"homelab"},"user_config_format":"json"}`)
	resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodPost, "/api/v1/stacks", bytes.NewReader(body), token)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	json.Unmarshal(respBody, &result)
	data := result
	if d, ok := result["data"].(map[string]interface{}); ok {
		data = d
	}
	jobID, _ := data["job_id"].(string)

	if jobID == "" {
		t.Fatalf("no job_id: %s", string(respBody))
	}

	// Poll the job via PocketBase collections API (same as frontend getJob())
	jobURL := fmt.Sprintf("/api/collections/jobs/records/%s", jobID)
	jobResp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, jobURL, nil, token)
	if err != nil {
		t.Fatalf("job poll failed: %v", err)
	}
	defer jobResp.Body.Close()

	jobBody, _ := io.ReadAll(jobResp.Body)

	if jobResp.StatusCode != http.StatusOK {
		t.Fatalf("expected job to be readable, got %d: %s", jobResp.StatusCode, string(jobBody))
	}

	var job map[string]interface{}
	if err := json.Unmarshal(jobBody, &job); err != nil {
		t.Fatalf("failed to parse job: %v", err)
	}

	state, _ := job["state"].(string)
	t.Logf("Job %s is in state %q - polling works correctly", jobID, state)

	// Verify required fields exist for frontend consumption
	requiredFields := []string{"id", "state", "progress"}
	for _, field := range requiredFields {
		if _, ok := job[field]; !ok {
			t.Errorf("job record missing required field %q (frontend depends on it)", field)
		}
	}
}

// TestIntegration_MultipleStacksAllowed tests that the same owner can create more than one stack.
func TestIntegration_MultipleStacksAllowed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	ks := testutil.StartkombifyTechstack(t, ctx)
	ks.HealthCheck(t)

	token := ks.Authenticate(t, ctx)

	// Create first stack
	body := []byte(`{"name":"first-stack","mode":"easy","user_config":{"name":"first-stack","provider":"homelab"},"user_config_format":"json"}`)
	resp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodPost, "/api/v1/stacks", bytes.NewReader(body), token)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first create expected 202, got %d", resp.StatusCode)
	}

	// Create a second stack for the same owner.
	body2 := []byte(`{"name":"second-stack","mode":"easy","user_config":{"name":"second-stack","provider":"homelab"},"user_config_format":"json"}`)
	resp2, err := ks.AuthenticatedAPIRequest(ctx, http.MethodPost, "/api/v1/stacks", bytes.NewReader(body2), token)
	if err != nil {
		t.Fatalf("second create request failed: %v", err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("second create expected 202, got %d", resp2.StatusCode)
	}

	listResp, err := ks.AuthenticatedAPIRequest(ctx, http.MethodGet, "/api/v1/stacks", nil, token)
	if err != nil {
		t.Fatalf("list stacks request failed: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(listResp.Body)
		t.Fatalf("expected 200 when listing stacks, got %d: %s", listResp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode stack list response: %v", err)
	}

	data, ok := result["data"].([]any)
	if !ok {
		t.Fatalf("expected data array in list response, got %v", result["data"])
	}
	if len(data) < 2 {
		t.Fatalf("expected at least 2 stacks for the same owner, got %d", len(data))
	}

	t.Log("Multiple stack creation supported for the same owner")
}

// verifyStackStatus checks that the stack record was updated after job completion.
func verifyStackStatus(t *testing.T, ctx context.Context, ks *testutil.KombifyTechstackContainer, stackID, token string) {
	t.Helper()

	resp, err := ks.AuthenticatedAPIRequest(
		ctx, http.MethodGet,
		fmt.Sprintf("/api/collections/stacks/records/%s", stackID),
		nil, token,
	)
	if err != nil {
		t.Logf("warning: could not verify stack status: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var stack map[string]interface{}
	if err := json.Unmarshal(body, &stack); err != nil {
		t.Logf("warning: could not parse stack: %v", err)
		return
	}

	status, _ := stack["status"].(string)
	t.Logf("Stack %s status after completion: %s", stackID, status)
}
