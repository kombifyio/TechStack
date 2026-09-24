package activities

import (
	"context"
	"errors"
	"testing"
	"time"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

type stubActionAuthority struct {
	card *actions.GovernedCard
}

func (s stubActionAuthority) Create(context.Context, actions.CreateGovernedCard) (*actions.GovernedCard, error) {
	panic("unused")
}
func (s stubActionAuthority) Get(context.Context, string, string, string) (*actions.GovernedCard, error) {
	if s.card == nil {
		return nil, actions.ErrCardNotFound
	}
	clone := *s.card
	return &clone, nil
}
func (s stubActionAuthority) List(context.Context, string, string) ([]actions.GovernedCard, error) {
	panic("unused")
}
func (s stubActionAuthority) Approve(context.Context, string, string, string, string, time.Time) (*actions.GovernedCard, error) {
	panic("unused")
}
func (s stubActionAuthority) Deny(context.Context, string, string, string, string, time.Time) (*actions.GovernedCard, error) {
	panic("unused")
}
func (s stubActionAuthority) Begin(context.Context, actions.BeginExecution) (actions.BeginResult, error) {
	panic("unused")
}
func (s stubActionAuthority) Complete(context.Context, string, string, rilaction.Evidence, string, time.Time) (*actions.GovernedCard, error) {
	panic("unused")
}
func (s stubActionAuthority) Fail(context.Context, string, string, string, string, time.Time) (*actions.GovernedCard, error) {
	panic("unused")
}

type recordingRemediationEngine struct {
	started int
}

func (e *recordingRemediationEngine) StartOrResumeRun(context.Context, *workflow.Run) (string, error) {
	e.started++
	return "run-1", nil
}

func (e *recordingRemediationEngine) GetRun(string) (*workflow.Run, error) {
	return &workflow.Run{RunID: "run-1", Status: workflow.RunCompleted}, nil
}

func TestExecuteRefusesMissingGrantBeforeStartingWorkflow(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	engine := &recordingRemediationEngine{}
	launcher := &RemediationLauncher{
		Engine: engine,
		Authority: stubActionAuthority{card: &actions.GovernedCard{
			ID: "card-1", ServerID: "server-1", Status: string(actions.StatusApproved),
		}},
	}
	_, err := launcher.Execute(context.Background(), actions.BeginExecution{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "card-1",
		ExecutionID: "exec-1", TraceID: "trace-1", IdempotencyKey: "idem-1", Now: now,
	})
	if !errors.Is(err, actions.ErrGrantRequired) {
		t.Fatalf("execute = %v, want ErrGrantRequired", err)
	}
	if engine.started != 0 {
		t.Fatalf("workflow starts = %d, want 0 before a live grant exists", engine.started)
	}
}

func TestExecuteStartsWorkflowWhenGrantIsLive(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	engine := &recordingRemediationEngine{}
	launcher := &RemediationLauncher{
		Engine: engine,
		Authority: stubActionAuthority{card: &actions.GovernedCard{
			ID: "card-1", ServerID: "server-1", Status: string(actions.StatusApproved),
			Template: actions.ActionTemplate{Grant: &rilaction.GrantBinding{
				BindingRef: "grant:1", BindingHash: "sha256:abc", Audience: "stackkits",
				Scopes:     []string{"stackkit-verify"},
				GrantedAt:  now.Add(-time.Minute).Format(time.RFC3339Nano),
				ValidUntil: now.Add(10 * time.Minute).Format(time.RFC3339Nano),
			}},
		}},
	}
	if _, err := launcher.Execute(context.Background(), actions.BeginExecution{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", CardID: "card-1",
		ExecutionID: "exec-1", TraceID: "trace-1", IdempotencyKey: "idem-1", Now: now,
	}); err != nil {
		t.Fatalf("live grant execute: %v", err)
	}
	if engine.started != 1 {
		t.Fatalf("workflow starts = %d, want 1 for a live grant", engine.started)
	}
}
