package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/core"
)

type recordingCurrentPortAuthority struct {
	order    *[]string
	admitErr error
	ref      portinventory.GenerationRef
	state    portinventory.ClaimState
	request  portinventory.CurrentAdmissionRequest
}

func (a *recordingCurrentPortAuthority) EvaluateCurrent(context.Context, portinventory.CurrentAdmissionRequest) (portinventory.CurrentAdmission, error) {
	*a.order = append(*a.order, "evaluate")
	return portinventory.CurrentAdmission{GenerationRef: a.ref, State: a.state}, nil
}

func (a *recordingCurrentPortAuthority) AdmitCurrent(_ context.Context, request portinventory.CurrentAdmissionRequest) (portinventory.CurrentAdmission, error) {
	*a.order = append(*a.order, "admit")
	a.request = request
	if a.admitErr != nil {
		return portinventory.CurrentAdmission{}, a.admitErr
	}
	return portinventory.CurrentAdmission{
		GenerationRef: a.ref,
		State:         a.state,
		Admission:     portinventory.Admission{Claims: make([]portinventory.Claim, len(request.Requirements))},
	}, nil
}

func TestPortGovernedLifecycleKeepsTechstackAndStackKitInstanceIdentitiesSeparate(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})
	plan, err := os.ReadFile(rollout.actionReq.UnifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	plan = []byte(strings.Replace(string(plan), `"stackId":"stack-a"`, `"stackId":"owner-defined-kit-instance"`, 1))
	if err := os.WriteFile(rollout.actionReq.UnifiedPath, plan, 0600); err != nil {
		t.Fatal(err)
	}

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err != nil || !applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v", applied, err)
	}
	if authority.request.StackID != "stack-a" {
		t.Fatalf("port authority StackID = %q, want Techstack record identity", authority.request.StackID)
	}
}

func TestPortGovernedLifecycleContinuesWhenOptionalListenerProjectionIsUnavailable(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})
	plan := `{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":"custom-kit","planHash":"` + testResolvedPlanHash + `","network":{"ownerDefinedListeners":[]}}`
	if err := os.WriteFile(rollout.actionReq.UnifiedPath, []byte(plan), 0600); err != nil {
		t.Fatal(err)
	}

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err != nil || !applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v", applied, err)
	}
	if want := []string{"rollout", "verify"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want StackKits-owned lifecycle %v", order, want)
	}
	details := mapFromInterface(rollout.job.Result["runtime_listener_admission"])
	if details["status"] != "unavailable" || details["stackkit_instance_id"] != "custom-kit" {
		t.Fatalf("optional projection evidence = %#v", details)
	}
}

func (a *recordingCurrentPortAuthority) MarkMutationStarted(context.Context, portinventory.GenerationRef) error {
	*a.order = append(*a.order, "mutation")
	return nil
}

func (a *recordingCurrentPortAuthority) Activate(context.Context, portinventory.GenerationRef) error {
	*a.order = append(*a.order, "activate")
	return nil
}

func (a *recordingCurrentPortAuthority) MarkUncertain(context.Context, portinventory.GenerationRef) error {
	*a.order = append(*a.order, "uncertain")
	return nil
}

func (a *recordingCurrentPortAuthority) AbortBeforeMutation(context.Context, portinventory.GenerationRef) error {
	*a.order = append(*a.order, "abort")
	return nil
}

func (a *recordingCurrentPortAuthority) ReleaseAfterTeardown(context.Context, portinventory.GenerationRef) error {
	*a.order = append(*a.order, "release")
	return nil
}

func TestPortGovernedLifecycleConflictCausesZeroRuntimeMutation(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	authority.admitErr = &portinventory.ConflictError{
		ErrorCode: portinventory.ErrorCodeAllocationConflict, ReasonCode: portinventory.ReasonCodeHostPortReserved,
		Transport: portinventory.TransportTCP, BindAddress: "0.0.0.0", Port: 8443,
		UserGuidance: portinventory.UserGuidance{Body: "Choose another port."},
	}
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err == nil || applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v, want conflict before apply", applied, err)
	}
	if !reflect.DeepEqual(order, []string{"admit"}) {
		t.Fatalf("calls = %v, want admission only", order)
	}
	if details := mapFromInterface(rollout.job.Result["port_admission_error"]); details["error_code"] != portinventory.ErrorCodeAllocationConflict {
		t.Fatalf("stable conflict details = %#v", details)
	}
}

func TestPortGovernedLifecycleMarksMutationThenActivatesAfterVerify(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err != nil || !applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v", applied, err)
	}
	want := []string{"admit", "mutation", "rollout", "verify", "activate"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want %v", order, want)
	}
}

func TestPortGovernedLifecycleActiveReplaySkipsMutationAndApplyButVerifies(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	authority.state = portinventory.ClaimStateActive
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err != nil || !applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v", applied, err)
	}
	if want := []string{"admit", "verify"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want active replay %v", order, want)
	}
}

func TestPortGovernedLifecycleLocalNonVerifiedResultRemainsUncertain(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority,
		&fakeRuntimeRunner{name: "rollout", order: &order},
		&fakeRuntimeRunner{name: "verify", order: &order, result: map[string]interface{}{"status": "failed"}},
	)

	if _, err := rollout.runPortGovernedLifecycle(t.Context()); err == nil {
		t.Fatal("runPortGovernedLifecycle() succeeded with a non-verified local result")
	}
	if want := []string{"admit", "mutation", "rollout", "verify", "uncertain"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want %v", order, want)
	}
}

func TestPortGovernedLifecyclePreservesUncertainClaimsAfterMutationFailure(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", order: &order, err: errors.New("apply failed")}
	rollout := testPortGovernedRollout(t, authority, rolloutRunner, &fakeRuntimeRunner{name: "verify", order: &order})

	if _, err := rollout.runPortGovernedLifecycle(t.Context()); err == nil {
		t.Fatal("runPortGovernedLifecycle() succeeded, want rollout failure")
	}
	want := []string{"admit", "mutation", "rollout", "uncertain"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want %v", order, want)
	}
}

func TestPortGovernedLifecycleAbortsPendingClaimsBeforeAnyMutation(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority, nil, &fakeRuntimeRunner{name: "verify", order: &order})

	if _, err := rollout.runPortGovernedLifecycle(t.Context()); err == nil {
		t.Fatal("runPortGovernedLifecycle() succeeded without rollout runner")
	}
	if want := []string{"admit", "abort"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want %v", order, want)
	}
}

func TestPortGovernedLifecycleApplyFalseEvaluatesWithoutPersistedOrRuntimeMutation(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})
	rollout.job.Payload["apply"] = false

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err != nil || applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v", applied, err)
	}
	if want := []string{"evaluate"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("calls = %v, want %v", order, want)
	}
}

func newRecordingCurrentPortAuthority(order *[]string) *recordingCurrentPortAuthority {
	return &recordingCurrentPortAuthority{order: order, state: portinventory.ClaimStatePending, ref: portinventory.GenerationRef{
		ServerRef: portinventory.ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 7},
		StackID:   "stack-a", ResolvedPlanHash: testResolvedPlanHash,
	}}
}

func testPortGovernedRollout(t *testing.T, authority portinventory.CurrentAuthority, rolloutRunner, verifier RuntimeActionRunner) *deployRollout {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "resolved-plan.json")
	plan := resolvedPlanWithListeners(`[
		{"id":"https","moduleRef":"module","unitRef":"unit","instanceRef":"instance","nodeRef":"node-a","componentRef":"api","transport":"tcp","bindAddress":"0.0.0.0","port":8443,"targetPort":443,"sharing":"exclusive","sourceRouteRefs":["route-a"],"exposure":"public"}
	]`)
	if err := os.WriteFile(planPath, []byte(plan), 0600); err != nil {
		t.Fatal(err)
	}
	job := &Job{ID: "job-a", TargetID: "stack-a", Payload: map[string]interface{}{"apply": true}, Result: map[string]interface{}{}}
	queue := NewQueue(0, nil)
	queue.jobs[job.ID] = job
	return &deployRollout{
		cfg: &ProvisionConfig{PortInventory: authority, RuntimeActions: RuntimeActions{
			RolloutRunner: rolloutRunner, RolloutVerifier: verifier,
		}},
		job: job, q: queue, unifiedSpec: &core.UnifiedSpec{StackKit: "basement-kit"}, targetKind: "local",
		generatedPlanHash: testResolvedPlanHash,
		actionReq: RuntimeActionRequest{
			StackID: "stack-a", UnifiedPath: planPath,
			PlatformNodes:       []PlatformNode{{Name: "node-a"}},
			TechStackEnrollment: &TechStackEnrollment{TenantID: "tenant-a", OwnerID: "owner-a", ServerID: "server-a"},
		},
		e2eProof: map[string]any{"phases_completed": []string{}}, runtimeProof: map[string]interface{}{}, stackKitOutputs: map[string]interface{}{},
		runtimeMetrics: map[string]string{}, finalRuntimePhase: RuntimePhaseDeployed,
	}
}
