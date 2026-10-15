package activities_test

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/routes/stacks"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/ril/activities"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

type operationRegistrar struct{ *workflow.ActivityRunner }

func (r operationRegistrar) RegisterActivity(name string, activity workflow.ActivityFunc) {
	r.Register(name, activity)
}

// This protects the admission boundary. The production backend has no configured
// stores or dispatcher; the probe never creates a database, approval or success.
func TestGovernedOperationActivitiesDenyBeforeDispatch(t *testing.T) {
	runner := workflow.NewActivityRunner()
	if err := (activities.GovernedOperationActivities{Backend: &stacks.GovernedOperationBackend{}}).Register(operationRegistrar{runner}); err != nil {
		t.Fatal(err)
	}
	const key = "run-admission-boundary:dispatch"
	base := func() map[string]any {
		return map[string]any{
			workflows.InputTenantID: "tenant-model", workflows.InputOwnerSubjectID: "subject-model",
			"operation": "drift-correction", workflows.InputAuthorizationRef: "approval:card-model",
			workflows.InputOperationRef: "action:card-model", workflows.InputCompensationRef: "action:recovery-model",
			workflows.InputTargets: []any{"server-model"},
		}
	}
	cases := []struct {
		name string
		edit func(map[string]any)
		want error
	}{
		{"missing authorization", func(v map[string]any) { delete(v, workflows.InputAuthorizationRef) }, activities.ErrOperationAuthorization},
		{"foreign authorization", func(v map[string]any) { v[workflows.InputAuthorizationRef] = "approval:other-model" }, activities.ErrOperationAuthorization},
		{"raw endpoint", func(v map[string]any) { v[workflows.InputOperationRef] = "https://example.invalid/run" }, activities.ErrOperationReference},
		{"missing compensation", func(v map[string]any) { delete(v, workflows.InputCompensationRef) }, activities.ErrOperationReference},
		{"same action as recovery", func(v map[string]any) { v[workflows.InputCompensationRef] = v[workflows.InputOperationRef] }, activities.ErrOperationReference},
		{"missing target", func(v map[string]any) { delete(v, workflows.InputTargets) }, activities.ErrOperationTargets},
		{"invalid target", func(v map[string]any) { v[workflows.InputTargets] = []any{map[string]any{"endpoint": "localhost"}} }, activities.ErrOperationTargets},
		{"duplicate target", func(v map[string]any) { v[workflows.InputTargets] = []any{"server-model", "server-model"} }, activities.ErrOperationTargets},
		{"missing owner", func(v map[string]any) { delete(v, workflows.InputOwnerSubjectID) }, activities.ErrOperationIdentity},
		{"key override", func(v map[string]any) { v[workflows.InputIdempotencyKey] = "different-key-model" }, activities.ErrOperationIdentity},
	}
	for _, name := range []string{
		workflows.ActQueryMonitoringSignal, workflows.ActDispatchGovernedOperation,
		workflows.ActVerifyGovernedOperation, workflows.ActExecuteGovernedCompensation, workflows.ActRecordGovernedOperationAudit,
	} {
		for _, test := range cases {
			t.Run(name+"/"+test.name, func(t *testing.T) {
				input := base()
				test.edit(input)
				out, err := runner.Run(t.Context(), name, input, key)
				if !errors.Is(err, test.want) || out != nil {
					t.Fatalf("unresolved intent reached the product backend: output=%v error=%v", out, err)
				}
			})
		}
	}
	prepared, err := activities.PrepareOperationInput(workflow.WithIdempotencyKey(t.Context(), key), base())
	if err != nil || prepared.IdempotencyKey != key {
		t.Fatalf("activity identity was rewritten: %v", err)
	}
	if out, err := runner.Run(t.Context(), workflows.ActDispatchGovernedOperation, base(), ""); !errors.Is(err, activities.ErrOperationIdentity) || out != nil {
		t.Fatalf("missing activity identity reached the backend: output=%v error=%v", out, err)
	}
	if out, err := runner.Run(t.Context(), workflows.ActDispatchGovernedOperation, base(), key); !errors.Is(err, orchestrator.ErrStackKitLifecycleUnavailable) || out != nil {
		t.Fatalf("reference shape became execution authority: output=%v error=%v", out, err)
	}
}
