package activities

import (
	"context"
	"errors"
	"reflect"
	"testing"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

type launcherGateAuthority struct {
	actions.Authority
	card       *actions.GovernedCard
	beginCalls int
	deny       bool
}

func (a *launcherGateAuthority) Get(context.Context, string, string, string) (*actions.GovernedCard, error) {
	return a.card, nil
}

func (a *launcherGateAuthority) Begin(_ context.Context, input actions.BeginExecution) (actions.BeginResult, error) {
	a.beginCalls++
	if a.deny {
		return actions.BeginResult{}, actions.ErrApprovalRequired
	}
	a.card.Status = "executing"
	a.card.IdempotencyKey = input.IdempotencyKey
	a.card.ExecutionID = input.ExecutionID
	a.card.TraceID = input.TraceID
	return actions.BeginResult{Disposition: actions.BeginAcquired, Card: a.card}, nil
}

type launcherGateEngine struct{ calls int }

var errLauncherReached = errors.New("workflow admission reached")

func (e *launcherGateEngine) StartOrResumeRun(context.Context, *workflow.Run) (string, error) {
	e.calls++
	return "", errLauncherReached
}
func (e *launcherGateEngine) GetRun(context.Context, string) (*workflow.Run, error) {
	return nil, errors.New("unexpected read")
}
func TestCanonicalVerifyReachesDecisionAuthorityWithoutGrantBypass(t *testing.T) {
	input := actions.BeginExecution{TenantID: "tenant", OwnerSubjectID: "owner", CardID: "card", ExecutionID: "exec", TraceID: "trace", IdempotencyKey: "idem"}
	base := actions.GovernedCard{Status: "awaiting_approval", ServerID: "server", Template: actions.ActionTemplate{Primitive: rilaction.PrimitiveBinding{ID: "verify-stackkit-state", OperationClass: "product-verify"}, Target: rilaction.TargetBinding{Scope: rilaction.TargetScopeStack}}}
	for _, tc := range []struct {
		name    string
		mutate  func(*actions.GovernedCard)
		reaches bool
	}{
		{"canonical", func(*actions.GovernedCard) {}, true},
		{"other primitive", func(c *actions.GovernedCard) { c.Template.Primitive.ID = "apply-stackkit-change" }, false},
		{"other class", func(c *actions.GovernedCard) { c.Template.Primitive.OperationClass = "product-apply" }, false},
		{"other scope", func(c *actions.GovernedCard) { c.Template.Target.Scope = rilaction.TargetScopeRuntimeInstance }, false},
		{"awaiting grant", func(c *actions.GovernedCard) { c.Status = "awaiting_grant" }, false},
		{"denied", func(c *actions.GovernedCard) { c.Status = "denied" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := base
			tc.mutate(&card)
			engine := &launcherGateEngine{}
			l := RemediationLauncher{Engine: engine, Authority: &launcherGateAuthority{card: &card}}
			_, err := l.Execute(context.Background(), input)
			if tc.reaches {
				if !errors.Is(err, errLauncherReached) || engine.calls != 1 {
					t.Fatalf("canonical action did not reach Gateway-backed admission: %v", err)
				}
			} else if !errors.Is(err, actions.ErrApprovalRequired) || engine.calls != 0 {
				t.Fatalf("other action bypassed approval: %v", err)
			}
		})
	}
}

func TestDeniedVerifyRetryUsesSameIdentityWithoutTerminalWorkflow(t *testing.T) {
	input := actions.BeginExecution{TenantID: "tenant", OwnerSubjectID: "owner", CardID: "card", ExecutionID: "same-exec", TraceID: "same-trace", IdempotencyKey: "same-idempotency"}
	card := &actions.GovernedCard{Status: "awaiting_approval", ServerID: "server", Template: actions.ActionTemplate{Primitive: rilaction.PrimitiveBinding{ID: "verify-stackkit-state", OperationClass: "product-verify"}, Target: rilaction.TargetBinding{Scope: rilaction.TargetScopeStack}}}
	authority := &launcherGateAuthority{card: card, deny: true}
	engine := &launcherGateEngine{}
	launcher := RemediationLauncher{Authority: authority, Engine: engine}
	if _, err := launcher.Execute(context.Background(), input); !errors.Is(err, actions.ErrApprovalRequired) || engine.calls != 0 || authority.beginCalls != 1 {
		t.Fatalf("unanswered decision created workflow: %v", err)
	}
	authority.deny = false
	if _, err := launcher.Execute(context.Background(), input); !errors.Is(err, errLauncherReached) || engine.calls != 1 || authority.beginCalls != 2 {
		t.Fatalf("same-identity approved retry did not begin workflow: %v", err)
	}
	if card.ExecutionID != input.ExecutionID || card.TraceID != input.TraceID || card.IdempotencyKey != input.IdempotencyKey {
		t.Fatal("execution identity was replaced")
	}
}

func TestAdmittedAndTerminalActionsKeepTheirIdentityFence(t *testing.T) {
	input := actions.BeginExecution{TenantID: "tenant", OwnerSubjectID: "owner", CardID: "card", ExecutionID: "exec", TraceID: "trace", IdempotencyKey: "idem"}
	for _, status := range []string{"executing", "verifying", "completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			card := &actions.GovernedCard{Status: status, ServerID: "server", ExecutionID: "exec", TraceID: "trace", IdempotencyKey: "idem"}
			authority := &launcherGateAuthority{card: card}
			engine := &launcherGateEngine{}
			launcher := RemediationLauncher{Authority: authority, Engine: engine}
			changed := input
			changed.TraceID = "different"
			if _, err := launcher.Execute(context.Background(), changed); !errors.Is(err, actions.ErrCardConflict) || authority.beginCalls != 0 || engine.calls != 0 {
				t.Fatalf("different execution identity was admitted: %v", err)
			}
			got, err := launcher.Execute(context.Background(), input)
			if status == "completed" || status == "failed" {
				if err != nil || got != card || engine.calls != 0 || authority.beginCalls != 0 {
					t.Fatalf("terminal state was revived: %v", err)
				}
			} else if !errors.Is(err, errLauncherReached) || authority.beginCalls != 1 || engine.calls != 1 {
				t.Fatalf("admitted action was readmitted: %v", err)
			}
		})
	}
}

type projectionRecordingEngine struct {
	ids    []string
	inputs []map[string]any
}

func (e *projectionRecordingEngine) StartOrResumeRun(_ context.Context, r *workflow.Run) (string, error) {
	e.ids = append(e.ids, r.RunID)
	e.inputs = append(e.inputs, r.Input)
	return "", errLauncherReached
}
func (e *projectionRecordingEngine) GetRun(context.Context, string) (*workflow.Run, error) {
	return nil, errors.New("unexpected read")
}
func TestVerifyProjectionCannotSelectSecondWorkflow(t *testing.T) {
	card := &actions.GovernedCard{Status: "awaiting_approval", ServerID: "server", Template: actions.ActionTemplate{Primitive: rilaction.PrimitiveBinding{ID: "verify-stackkit-state", OperationClass: "product-verify"}, Target: rilaction.TargetBinding{Scope: rilaction.TargetScopeStack}}}
	auth := &launcherGateAuthority{card: card}
	engine := &projectionRecordingEngine{}
	launcher := RemediationLauncher{Authority: auth, Engine: engine}
	input := actions.BeginExecution{TenantID: "tenant", OwnerSubjectID: "owner", CardID: "card", ExecutionID: "exec", TraceID: "trace", IdempotencyKey: "idem", ConnectorProjection: &actions.ConnectorBindingProjection{BindingID: "binding-one"}}
	if _, err := launcher.Execute(context.Background(), input); !errors.Is(err, errLauncherReached) {
		t.Fatal(err)
	}
	changed := input
	changed.ConnectorProjection = &actions.ConnectorBindingProjection{BindingID: "binding-two"}
	if _, err := launcher.Execute(context.Background(), changed); !errors.Is(err, errLauncherReached) {
		t.Fatal(err)
	}
	if len(engine.ids) != 2 || engine.ids[0] != engine.ids[1] {
		t.Fatal("projection changed the workflow identity")
	}
	if reflect.DeepEqual(engine.inputs[0], engine.inputs[1]) {
		t.Fatal("changed projection was lost from immutable workflow input")
	}
}
