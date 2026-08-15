package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

// fakeActivities records activity invocations and returns configured outputs /
// errors, so workflow step functions can be unit-tested without the engine or a
// real activity registry.
type fakeActivities struct {
	calls   []activityCall
	outputs map[string]map[string]any
	errs    map[string]error
}

type activityCall struct {
	name  string
	idem  string
	input map[string]any
}

func (f *fakeActivities) Run(_ context.Context, name string, input map[string]any, idem string) (map[string]any, error) {
	f.calls = append(f.calls, activityCall{name: name, idem: idem, input: input})
	if f.errs != nil {
		if err := f.errs[name]; err != nil {
			return nil, err
		}
	}
	if f.outputs != nil {
		return f.outputs[name], nil
	}
	return nil, nil
}

func (f *fakeActivities) names() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.name)
	}
	return out
}

func (f *fakeActivities) callsTo(name string) []activityCall {
	var out []activityCall
	for _, c := range f.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func newRC(fake *fakeActivities) *workflow.RunContext {
	return &workflow.RunContext{
		Run: &workflow.Run{
			RunID: "run-1",
			Type:  workflow.TypeServiceMigration,
			Input: map[string]any{
				InputSourceServiceID: "svc-source-1",
				InputTargetServerID:  "node-target-1",
				InputStackID:         "stack-1",
				InputServiceName:     "coolify",
			},
			Context: map[string]any{},
		},
		Activities: fake,
	}
}

func TestServiceMigrationWorkflow_DefinitionShape(t *testing.T) {
	w := NewServiceMigrationWorkflow()
	if w.Type() != workflow.TypeServiceMigration {
		t.Fatalf("Type() = %q, want service_migration", w.Type())
	}
	steps := w.Steps()
	wantNames := []string{"claim", "provision-target", "verify-target", "confirm", "cutover", "drain-source", "archive-source"}
	if len(steps) != len(wantNames) {
		t.Fatalf("step count = %d, want %d", len(steps), len(wantNames))
	}
	for i, name := range wantNames {
		if steps[i].Name != name {
			t.Errorf("step[%d] = %q, want %q", i, steps[i].Name, name)
		}
		if steps[i].Run == nil {
			t.Errorf("step %q has nil Run", name)
		}
	}
	// Steps with externally-visible side effects must be compensatable.
	compensatable := map[string]bool{"claim": true, "provision-target": true, "cutover": true}
	for _, s := range steps {
		if compensatable[s.Name] && s.Compensate == nil {
			t.Errorf("step %q must have a Compensate", s.Name)
		}
	}
}

func TestServiceMigrationClaim_MarksSourceAndCreatesTarget(t *testing.T) {
	fake := &fakeActivities{outputs: map[string]map[string]any{
		ActCreateTargetService: {"target_service_id": "svc-target-1"},
	}}
	rc := newRC(fake)

	res, err := NewServiceMigrationWorkflow().claim(context.Background(), rc)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got := mapString(res.Output, "target_service_id"); got != "svc-target-1" {
		t.Fatalf("claim output target = %q, want svc-target-1", got)
	}
	if got := contextString(rc, ctxTargetServiceID); got != "svc-target-1" {
		t.Fatalf("context target_service_id = %q, want svc-target-1", got)
	}
	mark := fake.callsTo(ActMarkServiceStatus)
	if len(mark) != 1 || mark[0].input["status"] != "migrating" || mark[0].input["service_id"] != "svc-source-1" {
		t.Fatalf("expected one mark migrating for source, got %+v", mark)
	}
	if len(fake.callsTo(ActCreateTargetService)) != 1 {
		t.Fatalf("expected one create_target_service call, got %+v", fake.names())
	}
}

func TestServiceMigrationClaim_RequiresInputs(t *testing.T) {
	fake := &fakeActivities{}
	rc := newRC(fake)
	rc.Run.Input = map[string]any{} // strip required inputs
	if _, err := NewServiceMigrationWorkflow().claim(context.Background(), rc); err == nil {
		t.Fatal("expected error when source/target inputs are missing")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("no activities should run when inputs are invalid, got %+v", fake.names())
	}
}

func TestServiceMigrationVerifyTarget_HealthyVsUnhealthy(t *testing.T) {
	healthy := &fakeActivities{outputs: map[string]map[string]any{
		ActVerifyServiceHealthy: {"healthy": true},
	}}
	rc := newRC(healthy)
	setContext(rc, ctxTargetServiceID, "svc-target-1")
	if _, err := NewServiceMigrationWorkflow().verifyTarget(context.Background(), rc); err != nil {
		t.Fatalf("verifyTarget healthy: unexpected error %v", err)
	}

	unhealthy := &fakeActivities{outputs: map[string]map[string]any{
		ActVerifyServiceHealthy: {"healthy": false},
	}}
	rc2 := newRC(unhealthy)
	setContext(rc2, ctxTargetServiceID, "svc-target-1")
	if _, err := NewServiceMigrationWorkflow().verifyTarget(context.Background(), rc2); err == nil {
		t.Fatal("verifyTarget should error when the target is not healthy")
	}
}

func TestServiceMigrationConfirm_SuspendResumeRejectTimeout(t *testing.T) {
	w := NewServiceMigrationWorkflow()

	// First entry (no signal) suspends with the run-scoped confirm key.
	rc := newRC(&fakeActivities{})
	res, err := w.confirm(context.Background(), rc)
	if err != nil {
		t.Fatalf("confirm suspend: %v", err)
	}
	if res.Suspend == nil || res.Suspend.SignalKey != MigrationConfirmSignalKey("run-1") {
		t.Fatalf("confirm should suspend on %q, got %+v", MigrationConfirmSignalKey("run-1"), res.Suspend)
	}

	// Resumed with confirmation proceeds.
	rc.Signal = &workflow.Signal{Key: MigrationConfirmSignalKey("run-1"), Payload: map[string]any{"confirmed": true}}
	if _, err := w.confirm(context.Background(), rc); err != nil {
		t.Fatalf("confirm resume confirmed: %v", err)
	}

	// Explicit rejection aborts.
	rc.Signal = &workflow.Signal{Key: MigrationConfirmSignalKey("run-1"), Payload: map[string]any{"confirmed": false}}
	if _, err := w.confirm(context.Background(), rc); err == nil {
		t.Fatal("confirm should error on operator rejection")
	}

	// Timeout aborts.
	rc.Signal = &workflow.Signal{Key: MigrationConfirmSignalKey("run-1"), TimedOut: true}
	if _, err := w.confirm(context.Background(), rc); err == nil {
		t.Fatal("confirm should error on confirmation timeout")
	}
}

func TestServiceMigrationCompensateClaim_DeletesTargetAndRestoresSource(t *testing.T) {
	fake := &fakeActivities{}
	rc := newRC(fake)
	setContext(rc, ctxTargetServiceID, "svc-target-1")

	if _, err := NewServiceMigrationWorkflow().compensateClaim(context.Background(), rc); err != nil {
		t.Fatalf("compensateClaim: %v", err)
	}
	if len(fake.callsTo(ActDeleteService)) != 1 {
		t.Fatalf("expected target delete, got %+v", fake.names())
	}
	restore := fake.callsTo(ActMarkServiceStatus)
	if len(restore) != 1 || restore[0].input["status"] != "running" || restore[0].input["service_id"] != "svc-source-1" {
		t.Fatalf("expected source restored to running, got %+v", restore)
	}
}

func TestServiceMigrationCompensateCutover_RevertsTraffic(t *testing.T) {
	fake := &fakeActivities{}
	rc := newRC(fake)
	setContext(rc, ctxTargetServiceID, "svc-target-1")

	if _, err := NewServiceMigrationWorkflow().compensateCutover(context.Background(), rc); err != nil {
		t.Fatalf("compensateCutover: %v", err)
	}
	switches := fake.callsTo(ActSwitchServiceTraffic)
	if len(switches) != 1 {
		t.Fatalf("expected one traffic revert, got %+v", fake.names())
	}
	// Revert flows target -> source (the inverse of cutover).
	if switches[0].input["from_service_id"] != "svc-target-1" || switches[0].input["to_service_id"] != "svc-source-1" {
		t.Fatalf("revert direction = %+v, want target->source", switches[0].input)
	}
}

// TestServiceMigration_HappyPathDrivesAllSteps emulates the engine: it runs each
// step in order, delivering the confirm signal on suspend, and asserts the full
// activity sequence ends by archiving the source.
func TestServiceMigration_HappyPathDrivesAllSteps(t *testing.T) {
	fake := &fakeActivities{outputs: map[string]map[string]any{
		ActCreateTargetService:  {"target_service_id": "svc-target-1"},
		ActVerifyServiceHealthy: {"healthy": true},
	}}
	w := NewServiceMigrationWorkflow()
	rc := newRC(fake)
	ctx := context.Background()

	for _, sd := range w.Steps() {
		res, err := sd.Run(ctx, rc)
		if err != nil {
			t.Fatalf("step %q: %v", sd.Name, err)
		}
		if res.Suspend != nil {
			// Deliver an operator confirmation and re-run the suspended step.
			rc.Signal = &workflow.Signal{Key: res.Suspend.SignalKey, Payload: map[string]any{"confirmed": true}}
			if _, err := sd.Run(ctx, rc); err != nil {
				t.Fatalf("step %q resume: %v", sd.Name, err)
			}
			rc.Signal = nil
		}
	}

	got := fake.names()
	want := []string{
		ActMarkServiceStatus,    // claim: migrating
		ActCreateTargetService,  // claim: create target
		ActDeployServiceToNode,  // provision-target
		ActVerifyServiceHealthy, // verify-target
		ActSwitchServiceTraffic, // cutover: switch
		ActMarkServiceStatus,    // cutover: mark target running
		ActDrainServiceOnNode,   // drain-source
		ActMarkServiceStatus,    // archive-source: archived
	}
	if len(got) != len(want) {
		t.Fatalf("activity sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("activity[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}

	marks := fake.callsTo(ActMarkServiceStatus)
	if marks[len(marks)-1].input["status"] != "archived" {
		t.Fatalf("final mark status = %v, want archived", marks[len(marks)-1].input["status"])
	}
	// Idempotency keys for same-named activities must be distinct per action.
	if marks[0].idem == marks[len(marks)-1].idem {
		t.Fatalf("idempotency keys must differ per action, got %q for both", marks[0].idem)
	}
}

func TestServiceMigrationClaim_PropagatesActivityError(t *testing.T) {
	fake := &fakeActivities{errs: map[string]error{
		ActCreateTargetService: errors.New("boom"),
	}}
	rc := newRC(fake)
	if _, err := NewServiceMigrationWorkflow().claim(context.Background(), rc); err == nil {
		t.Fatal("claim should propagate create_target_service error")
	}
}
