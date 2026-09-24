package routes

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

type workflowAuditReaderFake struct {
	run    *workflow.Run
	steps  []*workflow.Step
	events []workflow.AuditEvent
}

func (f workflowAuditReaderFake) GetRun(runID string) (*workflow.Run, error) {
	if f.run == nil || f.run.RunID != runID {
		return nil, workflow.ErrNotFound
	}
	return f.run, nil
}

func (f workflowAuditReaderFake) ListSteps(string) ([]*workflow.Step, error) {
	return f.steps, nil
}

func (f workflowAuditReaderFake) ListAudit(tenantID, _ string, _ int) ([]workflow.AuditEvent, error) {
	if tenantID != "tenant-1" {
		return nil, errors.New("unexpected tenant")
	}
	return f.events, nil
}

func TestWorkflowAuditIsOwnerScopedAndSanitized(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	reader := workflowAuditReaderFake{
		run: &workflow.Run{
			RunID: "run-1", Type: workflow.TypeActionCardRemediation, Status: workflow.RunCompleted,
			OwnerID: "owner-1", ServerID: "server-1", CardID: "card-1",
			Input:   map[string]any{"tenant_id": "tenant-1", "secret": "never-return"},
			Context: map[string]any{"connector_token": "never-return"}, Created: now, Updated: now,
		},
		steps: []*workflow.Step{{
			RunID: "run-1", StepIndex: 0, Name: "verify", Status: workflow.StepCompleted,
			Input: map[string]any{"secret": "never-return"}, Output: map[string]any{"token": "never-return"},
			Error: "never-return", IdempotencyKey: "never-return",
		}},
		events: []workflow.AuditEvent{{ID: 1, Action: "ril.workflow.run.created", ResourceType: "ril_workflow_run", ResourceID: "run-1", Details: map[string]any{"status": "pending"}, CreatedAt: now}},
	}
	h := workflowAuditHandler{reader: reader}

	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/v1/ril/workflow-runs/run-1", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("runId", "run-1")
	if err := h.get(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(recorder.Body.String(), "never-return") {
		t.Fatalf("owner audit response = %d %q %s", recorder.Code, recorder.Header().Get("Cache-Control"), recorder.Body.String())
	}

	event, recorder = registryRouteStoreTestEvent(http.MethodGet, "/v1/ril/workflow-runs/run-1", "owner-1", "tenant-2", nil)
	event.Request.SetPathValue("runId", "run-1")
	if err := h.get(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "tenant-1") {
		t.Fatalf("cross-tenant audit response = %d %s", recorder.Code, recorder.Body.String())
	}
}
