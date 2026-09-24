package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/core"
)

type recordingCurrentPortAuthority struct {
	order              *[]string
	admitErr           error
	ref                portinventory.GenerationRef
	state              portinventory.ClaimState
	request            portinventory.CurrentAdmissionRequest
	releasedSnapshots  []portinventory.TeardownSnapshot
	releaseSnapshotErr error
	baseline           *portinventory.Inventory
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

func TestPortGovernedLifecycleRefusesMissingCompilerListenerAuthority(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})
	plan := `{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":"custom-kit","planHash":"` + testResolvedPlanHash + `","network":{"ownerDefinedListeners":[]}}`
	if err := os.WriteFile(rollout.actionReq.UnifiedPath, []byte(plan), 0600); err != nil {
		t.Fatal(err)
	}

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err == nil || applied {
		t.Fatal("missing listener authority permitted host mutation")
	}
	for _, call := range order {
		if call == "rollout" || call == "mutation" {
			t.Fatal("host changed without listener authority")
		}
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

func (a *recordingCurrentPortAuthority) SnapshotForTeardown(context.Context, portinventory.TeardownSnapshotRequest) (portinventory.TeardownSnapshot, error) {
	return portinventory.TeardownSnapshot{}, nil
}

func (a *recordingCurrentPortAuthority) ReleaseTeardownSnapshot(_ context.Context, snapshot portinventory.TeardownSnapshot) error {
	*a.order = append(*a.order, "release_snapshot")
	a.releasedSnapshots = append(a.releasedSnapshots, snapshot)
	return a.releaseSnapshotErr
}

func TestPortGovernedLifecycleConflictCausesZeroRuntimeMutation(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	conflict := &portinventory.ConflictError{
		ErrorCode: portinventory.ErrorCodeAllocationConflict, ReasonCode: portinventory.ReasonCodeHostPortReserved,
		Transport: portinventory.TransportTCP, BindAddress: "0.0.0.0", Port: 8443,
		UserGuidance: portinventory.UserGuidance{Body: "Choose another port."},
	}
	authority.admitErr = conflict
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})

	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err == nil || applied {
		t.Fatalf("runPortGovernedLifecycle() = applied %v err %v, want conflict before apply", applied, err)
	}
	var preservedConflict *portinventory.ConflictError
	if !errors.As(err, &preservedConflict) || preservedConflict != conflict {
		t.Fatalf("port admission conflict cause was lost: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"admit"}) {
		t.Fatalf("calls = %v, want admission only", order)
	}
	if details := mapFromInterface(rollout.job.Result["port_admission_error"]); details["error_code"] != portinventory.ErrorCodeAllocationConflict {
		t.Fatalf("stable conflict details = %#v", details)
	}
}

// Each lifecycle result must move the admitted claim only after its matching
// runtime boundary; replays and evaluate-only requests must skip mutation.
func TestPortGovernedLifecycleTransitionsClaimsAroundRuntimeMutation(t *testing.T) {
	tests := []struct {
		name                                                    string
		activeReplay, verifyFails, rolloutFails, missingRollout bool
		evaluateOnly, wantApplied, wantError                    bool
		wantOrder                                               []string
	}{
		{name: "successful rollout activates", wantApplied: true, wantOrder: []string{"admit", "mutation", "rollout", "verify", "activate"}},
		{name: "active replay only verifies", activeReplay: true, wantApplied: true, wantOrder: []string{"admit", "verify"}},
		{name: "non-verified result stays uncertain", verifyFails: true, wantError: true, wantOrder: []string{"admit", "mutation", "rollout", "verify", "uncertain"}},
		{name: "rollout failure stays uncertain", rolloutFails: true, wantError: true, wantOrder: []string{"admit", "mutation", "rollout", "uncertain"}},
		{name: "missing runner aborts before mutation", missingRollout: true, wantError: true, wantOrder: []string{"admit", "abort"}},
		{name: "evaluate only avoids mutation", evaluateOnly: true, wantOrder: []string{"evaluate"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := []string{}
			authority := newRecordingCurrentPortAuthority(&order)
			if tt.activeReplay {
				authority.state = portinventory.ClaimStateActive
			}
			var rolloutRunner RuntimeActionRunner = &fakeRuntimeRunner{name: "rollout", order: &order}
			if tt.rolloutFails {
				rolloutRunner = &fakeRuntimeRunner{name: "rollout", order: &order, err: errors.New("apply failed")}
			} else if tt.missingRollout {
				rolloutRunner = nil
			}
			verifyResult := map[string]interface{}(nil)
			if tt.verifyFails {
				verifyResult = map[string]interface{}{"status": "failed"}
			}
			rollout := testPortGovernedRollout(t, authority, rolloutRunner, &fakeRuntimeRunner{name: "verify", order: &order, result: verifyResult})
			if tt.evaluateOnly {
				rollout.job.Payload["apply"] = false
			}

			applied, err := rollout.runPortGovernedLifecycle(t.Context())
			if tt.wantError {
				if err == nil {
					t.Fatal("runPortGovernedLifecycle() succeeded, want failure")
				}
			} else if err != nil || applied != tt.wantApplied {
				t.Fatalf("runPortGovernedLifecycle() = applied %v err %v, want applied %v", applied, err, tt.wantApplied)
			}
			if !reflect.DeepEqual(order, tt.wantOrder) {
				t.Fatalf("calls = %v, want %v", order, tt.wantOrder)
			}
		})
	}
}

func newRecordingCurrentPortAuthority(order *[]string) *recordingCurrentPortAuthority {
	return &recordingCurrentPortAuthority{order: order, state: portinventory.ClaimStatePending, ref: portinventory.GenerationRef{
		ServerRef: portinventory.ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 7},
		StackID:   "stack-a", ResolvedPlanHash: testResolvedPlanHash,
	}}
}

func testPortGovernedRollout(t *testing.T, authority portinventory.LifecycleAuthority, rolloutRunner, verifier RuntimeActionRunner) *deployRollout {
	t.Helper()
	planPath := filepath.Join(t.TempDir(), "resolved-plan.json")
	plan := resolvedPlanWithListeners(`[
		{"id":"https","moduleRef":"module","unitRef":"unit","instanceRef":"instance","nodeRef":"cloud-main","componentRef":"api","transport":"tcp","bindAddress":"0.0.0.0","port":8443,"targetPort":443,"sharing":"exclusive","sourceRouteRefs":["route-a"],"exposure":"public"}
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
		job: job, q: queue, unifiedSpec: &core.UnifiedSpec{StackKit: "cloud-kit"}, targetKind: "managed",
		generatedPlanHash: testResolvedPlanHash,
		actionReq: RuntimeActionRequest{
			StackID: "stack-a", StackKit: "cloud-kit", UnifiedPath: planPath,
			TechStackEnrollment: &TechStackEnrollment{TenantID: "tenant-a", OwnerID: "owner-a", ServerID: "server-a"},
		},
		e2eProof: map[string]any{"phases_completed": []string{}}, runtimeProof: map[string]interface{}{}, stackKitOutputs: map[string]interface{}{},
		runtimeMetrics: map[string]string{}, finalRuntimePhase: RuntimePhaseDeployed,
	}
}

func (a *recordingCurrentPortAuthority) ReadCurrent(_ context.Context, request portinventory.InventoryRequest, now time.Time) (portinventory.Inventory, error) {
	if a.baseline != nil {
		return *a.baseline, nil
	}
	expires := now.Add(time.Minute)
	return portinventory.Inventory{ServerID: request.ServerID, ServerGeneration: a.ref.ServerGeneration, ObservedAt: &now, ExpiresAt: &expires, ListenersComplete: true}, nil
}

// Preservation boundary: stale evidence must stop before reserving or mutating.
func TestPortGovernedLifecycleRefusesStaleHostBaselineBeforeSideEffects(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	expired := time.Now().Add(-time.Minute)
	authority.baseline = &portinventory.Inventory{ObservedAt: &expired, ExpiresAt: &expired, ListenersComplete: true}
	rollout := testPortGovernedRollout(t, authority, &fakeRuntimeRunner{name: "rollout", order: &order}, &fakeRuntimeRunner{name: "verify", order: &order})
	applied, err := rollout.runPortGovernedLifecycle(t.Context())
	if err == nil || applied {
		t.Fatal("stale host baseline permitted rollout")
	}
	for _, call := range order {
		if call == "admit" || call == "mutation" || call == "rollout" {
			t.Fatal("stale baseline caused a side effect")
		}
	}
	if details := mapFromInterface(rollout.job.Result["port_admission_error"]); details["reason_code"] != "host_baseline_unverified" {
		t.Fatal("actionable baseline denial was lost")
	}
}
