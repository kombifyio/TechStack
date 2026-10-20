package layoutadvice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/layout"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/specv2"
)

type fakeStacks struct{ stack *controlplane.Stack }

func (f fakeStacks) GetStack(context.Context, string, string) (*controlplane.Stack, error) {
	return f.stack, nil
}

type fakeServers struct{ servers []controlplane.ServerRuntime }

func (f fakeServers) ListServerRuntimesByTenant(context.Context, string, string) ([]controlplane.ServerRuntime, error) {
	return f.servers, nil
}

// fakeProjector adds the selected goals as workloads, like release authoring.
type fakeProjector struct{}

func (fakeProjector) Project(_ context.Context, seed map[string]any, intent specv2.WizardIntent, _ string) (*specv2.Projection, error) {
	raw, _ := json.Marshal(seed)
	var spec map[string]any
	_ = json.Unmarshal(raw, &spec)
	workloads, _ := spec["workloads"].(map[string]any)
	for _, goal := range intent.Goals {
		if _, exists := workloads[goal]; !exists {
			workloads[goal] = map[string]any{"placement": map[string]any{"siteRefs": []any{}, "nodeRefs": []any{}, "requiresRoles": []any{"worker"}}}
		}
	}
	return &specv2.Projection{Spec: spec, NodeID: "main"}, nil
}

type fakeChecker struct {
	outcome  string
	proposal map[string]any
}

func (f *fakeChecker) Feasibility(_ context.Context, spec map[string]any, _ specv2.InventoryDocument) (specv2.FeasibilityReport, error) {
	f.proposal = spec
	return specv2.FeasibilityReport{APIVersion: specv2.FeasibilityAPIVersion, Outcome: f.outcome, CLIVersion: "test"}, nil
}

// memoryRecords is a minimal in-memory RecommendationAuthority.
type memoryRecords struct {
	records  map[string]*actions.Recommendation
	accepted []actions.AcceptRecommendation
}

func (m *memoryRecords) Propose(_ context.Context, input actions.ProposeRecommendation) (*actions.Recommendation, error) {
	for _, record := range m.records {
		if record.Scope == input.Scope && record.ProposalDigest == input.ProposalDigest && record.State == actions.RecommendationOpen {
			return record, nil
		}
	}
	record := &actions.Recommendation{ID: "rec-" + input.ProposalDigest[7:15], Scope: input.Scope, State: actions.RecommendationOpen,
		ProposalDigest: input.ProposalDigest, SnapshotDigest: input.SnapshotDigest, MaterialDigest: input.MaterialDigest,
		Payload: input.Payload, ExpiresAt: input.Now.Add(time.Hour)}
	m.records[record.ID] = record
	return record, nil
}

func (m *memoryRecords) GetRecommendation(_ context.Context, _, _, id string, _ time.Time) (*actions.Recommendation, error) {
	if record, ok := m.records[id]; ok {
		return record, nil
	}
	return nil, actions.ErrRecommendationNotFound
}

func (m *memoryRecords) ListRecommendations(context.Context, string, string, string, time.Time) ([]actions.Recommendation, error) {
	return nil, nil
}

func (m *memoryRecords) Accept(_ context.Context, request actions.AcceptRecommendation) (actions.AcceptOutcome, error) {
	m.accepted = append(m.accepted, request)
	record := m.records[request.ID]
	if !request.Fresh.Verified {
		return actions.AcceptOutcome{}, actions.ErrRecommendationState
	}
	if request.Fresh.MaterialDigest != record.MaterialDigest {
		replacement, _ := m.Propose(context.Background(), request.Fresh)
		record.State, record.SupersededBy = actions.RecommendationSuperseded, replacement.ID
		return actions.AcceptOutcome{Superseded: record, Replacement: replacement}, nil
	}
	record.State = actions.RecommendationAccepted
	return actions.AcceptOutcome{Accepted: record}, nil
}

func (m *memoryRecords) Reject(context.Context, string, string, string, string, time.Time) (*actions.Recommendation, error) {
	return nil, nil
}

func (m *memoryRecords) Snooze(context.Context, string, string, string, time.Time, string, time.Time) (*actions.Recommendation, error) {
	return nil, nil
}

func (m *memoryRecords) MarkExecuting(context.Context, string, string, string, string, time.Time) (*actions.Recommendation, error) {
	return nil, nil
}

func (m *memoryRecords) Finish(context.Context, string, string, string, bool, string, time.Time) (*actions.Recommendation, error) {
	return nil, nil
}

func twoNodeService(t *testing.T, checker *fakeChecker, workers []core.Worker) (*Service, *memoryRecords) {
	t.Helper()
	spec := map[string]any{
		"apiVersion": specv2.SpecAPIVersion, "kind": "StackSpec", "kit": map[string]any{"slug": specv2.KitSlugBasement},
		"metadata": map[string]any{"name": "home", "stackId": "home"},
		"nodes": []any{
			map[string]any{"id": "main", "roles": []any{"controller", "worker"}, "enabled": true},
			map[string]any{"id": "worker-1", "roles": []any{"worker"}, "enabled": true},
		},
		"workloads": map[string]any{},
	}
	stack := &controlplane.Stack{ID: "stack-1", TenantID: "tenant", OwnerSubjectID: "owner", Name: "home", Config: map[string]any{"stack_spec_v2": spec}}
	servers := []controlplane.ServerRuntime{
		{ID: runtimeidentity.StackServerID("stack-1", "main"), StackID: "stack-1", OwnerSubjectID: "owner", WorkerID: "w-main", Name: "nas", ConnectionState: "connected"},
		{ID: runtimeidentity.StackServerID("stack-1", "worker-1"), StackID: "stack-1", OwnerSubjectID: "owner", WorkerID: "w-mini", Name: "mini-pc", ConnectionState: "connected"},
	}
	records := &memoryRecords{records: map[string]*actions.Recommendation{}}
	catalog, err := stackkitrelease.PinnedUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		Stacks: fakeStacks{stack: stack}, Servers: fakeServers{servers: servers},
		Workers:   func(context.Context, string, string) ([]core.Worker, error) { return workers, nil },
		Catalog:   func() (stackkitrelease.UseCaseCatalog, error) { return catalog, nil },
		Projector: fakeProjector{}, Checker: checker, Records: records,
	}, records
}

func worker(id string, cpu, ramMB int) core.Worker {
	return core.Worker{ID: id, Status: "connected", Capabilities: core.WorkerCapabilities{CPU: cpu, RAM: ramMB, Disk: 200, Arch: "amd64", AMD64MicroarchitectureLevel: 3}}
}

// A verified distribution over the deployment's two nodes is recorded,
// written into the proposal as one nodeRef per workload, and accepted after
// revalidation; a material change on the fresh snapshot supersedes it.
func TestPreviewAndAcceptOnTwoOwnerNodes(t *testing.T) {
	checker := &fakeChecker{outcome: "feasible"}
	workers := []core.Worker{worker("w-main", 4, 8192), worker("w-mini", 4, 16384)}
	service, records := twoNodeService(t, checker, workers)
	scope := Scope{TenantID: "tenant", OwnerID: "owner"}
	selection := Selection{Flow: FlowAddUseCase, StackID: "stack-1", Goals: []string{"vault", "photos"}}

	view, err := service.Preview(context.Background(), scope, selection)
	if err != nil {
		t.Fatal(err)
	}
	if view.ID == "" || view.State != actions.RecommendationOpen || view.Verification.Status != VerificationVerified {
		t.Fatalf("verified advice must be recorded: %+v", view)
	}
	nodes := map[string]string{}
	for _, assignment := range view.Assignments {
		nodes[assignment.Workload] = assignment.SpecNodeRef
		if len(assignment.Reasons) == 0 || assignment.Reasons[0].Text == "" {
			t.Fatalf("%s has no plain reason", assignment.Workload)
		}
	}
	if nodes["vault"] != "main" || nodes["photos"] != "worker-1" {
		t.Fatalf("want vault on the tighter main node and photos apart from it, got %v", nodes)
	}
	workloads := checker.proposal["workloads"].(map[string]any)
	refs := workloads["photos"].(map[string]any)["placement"].(map[string]any)["nodeRefs"].([]any)
	if len(refs) != 1 || refs[0] != "worker-1" {
		t.Fatalf("the verified proposal must carry one nodeRef, got %v", refs)
	}

	projection, _ := fakeProjector{}.Project(context.Background(), service.Stacks.(fakeStacks).stack.Config["stack_spec_v2"].(map[string]any), specv2.WizardIntent{Goals: selection.Goals}, "")
	accepted, err := service.AcceptForRun(context.Background(), scope, specv2.LayoutRecommendationAcceptance{ID: view.ID, ProposalDigest: view.ProposalDigest}, "stack-1", FlowAddUseCase, projection, "run-1")
	if err != nil || accepted.Replacement != nil || accepted.Assignments["photos"] != "worker-1" {
		t.Fatalf("equal fresh decision must accept: %v %+v", err, accepted)
	}
	if got := records.accepted[0].Fresh.SnapshotDigest; got == "" {
		t.Fatal("acceptance must carry the fresh snapshot digest")
	}
	// A same-key retry of the accepting run carries out what it accepted,
	// even when its workloads were already saved and nothing is left to plan.
	records.records[view.ID].Acceptance = &actions.Acceptance{AuditCorrelation: "run-1"}
	retried, err := service.AcceptForRun(context.Background(), scope, specv2.LayoutRecommendationAcceptance{ID: view.ID, ProposalDigest: view.ProposalDigest}, "stack-1", FlowAddUseCase, projection, "run-1")
	if err != nil || retried.Assignments["photos"] != "worker-1" {
		t.Fatalf("the accepting run must replay: %v %+v", err, retried)
	}

	// The mini PC went away: the fresh plan differs, so the advice is replaced.
	service.Workers = func(context.Context, string, string) ([]core.Worker, error) {
		return []core.Worker{worker("w-main", 4, 8192)}, nil
	}
	again, err := service.Preview(context.Background(), scope, Selection{Flow: FlowAddUseCase, StackID: "stack-1", Goals: []string{"vault", "photos", "media"}})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == "" {
		t.Fatalf("second preview: %+v", again)
	}
	service.Workers = func(context.Context, string, string) ([]core.Worker, error) { return workers, nil }
	wider, _ := fakeProjector{}.Project(context.Background(), service.Stacks.(fakeStacks).stack.Config["stack_spec_v2"].(map[string]any), specv2.WizardIntent{Goals: []string{"vault", "photos", "media"}}, "")
	changed, err := service.AcceptForRun(context.Background(), scope, specv2.LayoutRecommendationAcceptance{ID: again.ID, ProposalDigest: again.ProposalDigest}, "stack-1", FlowAddUseCase, wider, "run-2")
	if err != nil || changed.Replacement == nil {
		t.Fatalf("a material change must supersede: %v %+v", err, changed)
	}
}

// Unverified advice is never recorded, so it can never be accepted.
func TestUnverifiedAdviceIsNotRecorded(t *testing.T) {
	checker := &fakeChecker{outcome: "infeasible"}
	service, records := twoNodeService(t, checker, []core.Worker{worker("w-main", 4, 8192), worker("w-mini", 4, 16384)})
	view, err := service.Preview(context.Background(), Scope{TenantID: "tenant", OwnerID: "owner"}, Selection{Flow: FlowAddUseCase, StackID: "stack-1", Goals: []string{"vault"}})
	if err != nil {
		t.Fatal(err)
	}
	if view.ID != "" || len(records.records) != 0 || view.Verification.Status != VerificationInfeasible {
		t.Fatalf("infeasible proposal recorded: %+v", view)
	}
}

// An unbudgeted data binding leaves a verified distribution verified and says
// on which of the owner's servers free disk space was not checked.
func TestVerifiedAdviceNamesUnbudgetedData(t *testing.T) {
	report := specv2.FeasibilityReport{Outcome: "feasible", Advisories: []specv2.FeasibilityAdvisory{
		{Code: "MODULE_LOAD_UNDECLARED", NodeRef: "cloud-main"}, {Code: "DATA_CAPACITY_INTENT_MISSING", NodeRef: "cloud-main"},
	}}
	verification := verificationFromReport(report)
	if verification.Status != VerificationVerified || len(verification.Reasons) != 1 || verification.Reasons[0].Code != reasonDataBudgetUnset {
		t.Fatalf("verification = %+v", verification)
	}
	named := withNodeNames(verification, layout.Snapshot{Nodes: []layout.Node{{SpecNodeRef: "cloud-main", Name: "homelab-f0dd9c"}}})
	if reason := named.Reasons[0]; reason.Params["node"] != "homelab-f0dd9c" || reason.Text != dataBudgetUnsetText("homelab-f0dd9c") ||
		verification.Reasons[0].Params["node"] != "cloud-main" {
		t.Fatalf("named = %+v, cached = %+v", named.Reasons, verification.Reasons)
	}
	if unknown := verificationFromReport(specv2.FeasibilityReport{Outcome: "unknown", Advisories: report.Advisories}); len(unknown.Reasons) != 0 {
		t.Fatalf("an unverified outcome gained an advisory note: %+v", unknown.Reasons)
	}
}
