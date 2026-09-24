package routes

import (
	"errors"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

const workflowAuditLimit = 500

type WorkflowAuditReader interface {
	GetRun(string) (*workflow.Run, error)
	ListSteps(string) ([]*workflow.Step, error)
	ListAudit(string, string, int) ([]workflow.AuditEvent, error)
}

type workflowAuditHandler struct{ reader WorkflowAuditReader }

// RegisterWorkflowAuditRoutes exposes the sanitized durable run/step history
// to the authenticated owner. Inputs, outputs, context, idempotency keys, raw
// errors, and connector material never cross this boundary.
func RegisterWorkflowAuditRoutes(r *httpx.Router, reader WorkflowAuditReader) {
	if reader == nil {
		panic("RegisterWorkflowAuditRoutes: reader required")
	}
	h := workflowAuditHandler{reader: reader}
	r.GET("/v1/ril/workflow-runs/{runId}", h.get)
}

func (h workflowAuditHandler) get(e *httpx.Event) error {
	if len(e.Request.URL.Query()) != 0 {
		return httpx.BadRequest(e, "Workflow audit query parameters are not supported", map[string]any{inventoryReasonCodeField: "invalid_request"})
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	runID := strings.TrimSpace(e.Request.PathValue("runId"))
	run, err := h.reader.GetRun(runID)
	if err != nil {
		return writeWorkflowAuditError(e, err)
	}
	if run.OwnerID != scope.ownerID || workflowRunTenant(run) != scope.tenantID {
		return httpx.NotFound(e, "Workflow run not found")
	}
	steps, err := h.reader.ListSteps(runID)
	if err != nil {
		return writeWorkflowAuditError(e, err)
	}
	events, err := h.reader.ListAudit(scope.tenantID, runID, workflowAuditLimit)
	if err != nil {
		return writeWorkflowAuditError(e, err)
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, workflowAuditResponse{
		Run:    projectWorkflowRun(run),
		Steps:  projectWorkflowSteps(steps),
		Events: events,
	})
}

type workflowRunAudit struct {
	RunID       string             `json:"run_id"`
	Type        workflow.RunType   `json:"type"`
	Status      workflow.RunStatus `json:"status"`
	CurrentStep int                `json:"current_step"`
	ServerID    string             `json:"server_id,omitempty"`
	CardID      string             `json:"card_id,omitempty"`
	StartedAt   *time.Time         `json:"started_at,omitempty"`
	FinishedAt  *time.Time         `json:"finished_at,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type workflowStepAudit struct {
	StepIndex  int                 `json:"step_index"`
	Name       string              `json:"name"`
	Status     workflow.StepStatus `json:"status"`
	Attempt    int                 `json:"attempt"`
	StartedAt  *time.Time          `json:"started_at,omitempty"`
	FinishedAt *time.Time          `json:"finished_at,omitempty"`
}

type workflowAuditResponse struct {
	Run    workflowRunAudit      `json:"run"`
	Steps  []workflowStepAudit   `json:"steps"`
	Events []workflow.AuditEvent `json:"events"`
}

func workflowRunTenant(run *workflow.Run) string {
	if run == nil || run.Input == nil {
		return ""
	}
	tenantID, _ := run.Input[workflows.InputTenantID].(string)
	return strings.TrimSpace(tenantID)
}

func projectWorkflowRun(run *workflow.Run) workflowRunAudit {
	return workflowRunAudit{
		RunID: run.RunID, Type: run.Type, Status: run.Status, CurrentStep: run.CurrentStep,
		ServerID: run.ServerID, CardID: run.CardID, StartedAt: run.StartedAt,
		FinishedAt: run.FinishedAt, CreatedAt: run.Created, UpdatedAt: run.Updated,
	}
}

func projectWorkflowSteps(steps []*workflow.Step) []workflowStepAudit {
	result := make([]workflowStepAudit, 0, len(steps))
	for _, step := range steps {
		if step == nil {
			continue
		}
		result = append(result, workflowStepAudit{
			StepIndex: step.StepIndex, Name: step.Name, Status: step.Status,
			Attempt: step.Attempt, StartedAt: step.StartedAt, FinishedAt: step.FinishedAt,
		})
	}
	return result
}

func writeWorkflowAuditError(e *httpx.Event, err error) error {
	if errors.Is(err, workflow.ErrNotFound) {
		return httpx.NotFound(e, "Workflow run not found")
	}
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Workflow audit unavailable", map[string]any{inventoryReasonCodeField: "workflow_audit_unavailable"})
}
