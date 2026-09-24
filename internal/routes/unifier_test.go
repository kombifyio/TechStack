// Package routes provides tests for Unifier API routes.
package routes

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
)

// Sensitive ownership boundary: placement must never consider another owner
// or tenant's worker even when they share the same canonical store.
func TestUnifierWorkersAreExactTenantAndOwnerScoped(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, worker := range []controlplane.Worker{
		{ID: "mine", TenantID: "tenant-1", OwnerSubjectID: "owner-1", CPUCores: 8},
		{ID: "other-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-2", CPUCores: 32},
		{ID: "other-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1", CPUCores: 64},
	} {
		if _, err := store.UpsertWorkerHeartbeat(t.Context(), worker); err != nil {
			t.Fatal(err)
		}
	}
	api, err := NewUnifierAPI(store)
	if err != nil {
		t.Fatal(err)
	}
	workers, err := api.fetchWorkers(t.Context(), "tenant-1", "owner-1")
	if err != nil || len(workers) != 1 || workers[0].ID != "mine" || workers[0].Capabilities.CPU != 8 {
		t.Fatalf("unexpected placement workers: workers=%v err=%v", workers, err)
	}
}

func TestPipelinePreviewResponseReturnsInvalidResultWithoutHTTPFailureSemantics(t *testing.T) {
	result := &unifier.PipelineResult{
		Success:      false,
		FailedStep:   "stackkit-resolution",
		ErrorMessage: "StackKit resolution failed: kit 'missing-kit' not available",
		Steps: []unifier.PipelineStep{
			{Name: "pre-validation", Status: "success"},
			{Name: "stackkit-resolution", Status: "failed", Error: "missing-kit"},
		},
		ValidationResult: &core.ValidationResult{Valid: true},
		ResolveResult: &unifier.ResolveResult{
			StackKit: "missing-kit",
			Valid:    false,
		},
		Warnings: []string{"StackKit 'missing-kit' may not be available"},
	}

	response := buildPipelinePreviewResponse(result)

	if response["valid"] != false {
		t.Fatalf("valid = %v, want false", response["valid"])
	}
	if response["resolved_stackkit"] != "missing-kit" {
		t.Fatalf("resolved_stackkit = %v, want missing-kit", response["resolved_stackkit"])
	}
	errors, ok := response["errors"].([]map[string]string)
	if !ok || len(errors) == 0 {
		t.Fatalf("expected structured preview errors, got %#v", response["errors"])
	}
	if errors[0]["code"] != "stackkit_resolution" {
		t.Fatalf("error code = %q, want stackkit_resolution", errors[0]["code"])
	}
	if _, ok := response["config"]; ok {
		t.Fatal("invalid preview must not include a unified config")
	}
}
