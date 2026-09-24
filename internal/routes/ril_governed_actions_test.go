package routes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
)

type stubGovernedActionAuthority struct {
	card *actions.GovernedCard
}

func (s stubGovernedActionAuthority) Create(context.Context, actions.CreateGovernedCard) (*actions.GovernedCard, error) {
	return nil, actions.ErrCardNotFound
}

func (s stubGovernedActionAuthority) Get(context.Context, string, string, string) (*actions.GovernedCard, error) {
	return s.card, nil
}

func (s stubGovernedActionAuthority) List(context.Context, string, string) ([]actions.GovernedCard, error) {
	return nil, nil
}

func (s stubGovernedActionAuthority) Approve(context.Context, string, string, string, string, time.Time) (*actions.GovernedCard, error) {
	return nil, actions.ErrCardNotFound
}

func (s stubGovernedActionAuthority) Deny(context.Context, string, string, string, string, time.Time) (*actions.GovernedCard, error) {
	return nil, actions.ErrCardNotFound
}

func (s stubGovernedActionAuthority) Begin(context.Context, actions.BeginExecution) (actions.BeginResult, error) {
	return actions.BeginResult{}, actions.ErrCardNotFound
}

func (s stubGovernedActionAuthority) Complete(context.Context, string, string, rilaction.Evidence, string, time.Time) (*actions.GovernedCard, error) {
	return nil, actions.ErrCardNotFound
}

func (s stubGovernedActionAuthority) Fail(context.Context, string, string, string, string, time.Time) (*actions.GovernedCard, error) {
	return nil, actions.ErrCardNotFound
}

type recordingGovernedActionWorkflow struct {
	called bool
}

func (w *recordingGovernedActionWorkflow) Execute(context.Context, actions.BeginExecution) (*actions.GovernedCard, error) {
	w.called = true
	return &actions.GovernedCard{ID: "card-1", ServerID: "server-1", Status: "approved"}, nil
}

func TestGovernedActionExecuteRejectsMissingOperateEntitlementBeforeWorkflow(t *testing.T) {
	workflow := &recordingGovernedActionWorkflow{}
	handler := governedActionHandler{config: GovernedActionRouteConfig{
		Authority: stubGovernedActionAuthority{card: &actions.GovernedCard{ID: "card-1", ServerID: "server-1", Status: "approved"}},
		Workflow:  workflow,
		Policy:    denyInventoryPolicy{},
		Now:       func() time.Time { return time.Now().UTC() },
	}}
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/v1/ril/action-cards/card-1/execute", "owner-1", "tenant-1", map[string]any{
		"execution_id": "exec-1", "trace_id": "trace-1", "idempotency_key": "idem-1",
	})
	event.Request.SetPathValue("cardId", "card-1")
	if err := handler.execute(event); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if recorder.Code != http.StatusForbidden || workflow.called {
		t.Fatalf("status=%d workflow_called=%v body=%s, want fail-closed 403 before workflow", recorder.Code, workflow.called, recorder.Body.String())
	}
}
