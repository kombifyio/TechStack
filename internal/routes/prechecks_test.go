package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

func TestPreCheckRequestChecks_DefaultsAndCustomChecks(t *testing.T) {
	defaults := preCheckRequestChecks(PreCheckRequest{})
	if len(defaults) != len(getDefaultPreChecks()) {
		t.Fatalf("expected default prechecks, got %d", len(defaults))
	}

	custom := []kscore.PreCheckDefinition{{
		Type:        "custom_check",
		Description: "custom",
		Blocking:    true,
	}}
	checks := preCheckRequestChecks(PreCheckRequest{Checks: custom})
	if len(checks) != 1 || checks[0].Type != "custom_check" {
		t.Fatalf("expected custom checks to be preserved, got %+v", checks)
	}
}

func TestPreCheckResultResponseFromRecord_ReadsRecordFields(t *testing.T) {
	record := newPreCheckResultTestRecord()
	record.Id = "precheck-1"
	record.Set("worker_id", "worker-1")
	record.Set("stack_id", "stack-1")
	record.Set("check_type", "docker_version")
	record.Set("description", "Docker version")
	record.Set("blocking", true)
	record.Set("status", "failed")
	record.Set("message", "too old")
	record.Set("details", map[string]any{"version": "19.03"})
	record.Set("error", "minimum version is 20.10.0")
	record.Set("duration_ms", 42)
	record.Set("request_id", "request-1")

	response := preCheckResultResponseFromRecord(record)
	if response.ID != "precheck-1" || response.WorkerID != "worker-1" || response.StackID != "stack-1" {
		t.Fatalf("unexpected identity fields: %+v", response)
	}
	if response.CheckType != "docker_version" || response.Status != "failed" || !response.Blocking {
		t.Fatalf("unexpected check fields: %+v", response)
	}
	if response.Details["version"] != "19.03" || response.DurationMS != 42 || response.RequestID != "request-1" {
		t.Fatalf("unexpected detail fields: %+v", response)
	}
}

func TestAllBlockingPreChecksPassed(t *testing.T) {
	passed := newPreCheckResultTestRecord()
	passed.Set("blocking", true)
	passed.Set("status", "passed")
	warning := newPreCheckResultTestRecord()
	warning.Set("blocking", false)
	warning.Set("status", "failed")

	if !allBlockingPreChecksPassed([]*core.Record{passed, warning}) {
		t.Fatal("expected nonblocking failure not to block")
	}

	failed := newPreCheckResultTestRecord()
	failed.Set("blocking", true)
	failed.Set("status", "failed")
	if allBlockingPreChecksPassed([]*core.Record{passed, failed}) {
		t.Fatal("expected blocking failure to block")
	}
}

func TestRequireOwnedWorkerID_MissingPathStopsAfterBadRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workers//prechecks", nil)
	rec := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: rec}

	workerID, ok, err := preCheckRouteHandlers{}.requireOwnedWorkerID(event, "owner-1")
	if err != nil {
		t.Fatalf("requireOwnedWorkerID write error: %v", err)
	}
	if ok || workerID != "" {
		t.Fatalf("expected ok=false and empty worker ID, got ok=%v workerID=%q", ok, workerID)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func newPreCheckResultTestRecord() *core.Record {
	collection := core.NewBaseCollection("precheck_results")
	collection.Fields.Add(
		&core.TextField{Name: "worker_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "check_type"},
		&core.TextField{Name: "description"},
		&core.BoolField{Name: "blocking"},
		&core.SelectField{Name: "status", Values: []string{"pending", "passed", "failed", "skipped", "warning"}},
		&core.TextField{Name: "message"},
		&core.JSONField{Name: "details"},
		&core.TextField{Name: "error"},
		&core.DateField{Name: "executed_at"},
		&core.NumberField{Name: "duration_ms"},
		&core.TextField{Name: "request_id"},
	)
	return core.NewRecord(collection)
}
