// Package layoutadvice turns an owner's selection into one explained,
// StackKits-verified layout recommendation and carries it through the durable
// Recommendation/v1 record. It is the single path shared by the
// recommendation routes and the wizard runs, so the advice shown and the
// advice accepted are computed the same way.
//
// Order of one computation: project the selection onto the stored StackSpec
// through the pinned release (authoring stays StackKits'), plan with the
// Unifier's Layout Planner over the deployment's owner nodes, write one
// nodeRef per planned workload into the proposal, verify that proposal
// against exact inventory with `stackkit feasibility`, and only then propose
// a record. Nothing is executed here (owner decision D2).
package layoutadvice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/layout"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/unifier"
)

// Flows that show a layout recommendation.
const (
	FlowAddServer  = "add-server"
	FlowAddUseCase = "add-use-case"

	stackSpecV2Key = "stack_spec_v2"
	plannedPrefix  = "planned:"
)

// Verification statuses of the StackKits feasibility check.
const (
	VerificationVerified    = "verified"
	VerificationInfeasible  = "infeasible"
	VerificationUnknown     = "unknown"
	VerificationUnavailable = "unavailable"
	VerificationSkipped     = "skipped"
)

var (
	// ErrStackNotFound covers a missing or foreign kit deployment.
	ErrStackNotFound = errors.New("layoutadvice: kit deployment not found")
	// ErrNoSpec means the deployment has no canonical StackSpec yet.
	ErrNoSpec = errors.New("layoutadvice: kit deployment has no canonical StackSpec")
	// ErrSelectionMismatch means a run's selection is not the accepted one.
	ErrSelectionMismatch = errors.New("layoutadvice: the run does not carry the accepted selection")
	// ErrUnverifiable means the fresh state cannot be planned and verified, so
	// shown advice cannot be accepted now.
	ErrUnverifiable = errors.New("layoutadvice: the recommendation cannot be revalidated against the current homelab")
	// ErrUnavailable means a required dependency is not configured.
	ErrUnavailable = errors.New("layoutadvice: layout recommendations are unavailable")
)

// StackReader loads kit deployments.
type StackReader interface {
	GetStack(ctx context.Context, tenantID, stackID string) (*controlplane.Stack, error)
}

// ServerReader lists canonical servers.
type ServerReader interface {
	ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]controlplane.ServerRuntime, error)
}

// JobReader reads the job that carries out an accepted recommendation.
type JobReader interface {
	GetJob(ctx context.Context, tenantID, jobID string) (*controlplane.Job, error)
}

// WorkerInventory returns the owner's registry workers with their facts.
type WorkerInventory func(ctx context.Context, tenantID, ownerID string) ([]core.Worker, error)

// Service computes and records layout recommendations.
type Service struct {
	Stacks    StackReader
	Servers   ServerReader
	Workers   WorkerInventory
	Catalog   func() (stackkitrelease.UseCaseCatalog, error)
	Projector specv2.Projector
	Checker   specv2.FeasibilityChecker
	Records   actions.RecommendationAuthority
	Jobs      JobReader
	Planner   layout.Planner
	Now       func() time.Time

	cacheMu sync.Mutex
	cache   map[string]cachedVerification
}

type cachedVerification struct {
	verification Verification
	at           time.Time
}

const verificationCacheTTL = 10 * time.Minute

// Scope is the authenticated owner scope of a request.
type Scope struct {
	TenantID string
	OwnerID  string
}

// Selection is the owner's question, stored with the record so acceptance
// re-asks exactly it.
type Selection struct {
	Flow           string                       `json:"flow"`
	StackID        string                       `json:"stack_id"`
	Goals          []string                     `json:"goals"`
	ComputeProfile string                       `json:"compute_profile,omitempty"`
	Settings       map[string]map[string]any    `json:"use_case_settings,omitempty"`
	Capabilities   map[string]map[string]string `json:"use_case_capabilities,omitempty"`
	// PlannedNode is the node an add-server run brings, when its size is
	// known before it enrolls (a managed offering or a hypervisor guest).
	PlannedNode *PlannedNode `json:"planned_node,omitempty"`
}

// PlannedNode is a node with a declared size.
type PlannedNode struct {
	Name      string   `json:"name"`
	CPUCores  int      `json:"cpu_cores"`
	RAMGB     float64  `json:"ram_gb"`
	StorageGB int      `json:"storage_gb"`
	Arch      string   `json:"arch,omitempty"`
	Source    string   `json:"source"`
	Roles     []string `json:"roles,omitempty"`
	Transport string   `json:"transport,omitempty"`
}

// View is what clients render: one recommendation with plain reasons.
type View struct {
	ID             string                 `json:"id,omitempty"`
	APIVersion     string                 `json:"api_version"`
	State          string                 `json:"state"`
	Scope          string                 `json:"scope"`
	StackID        string                 `json:"stack_id"`
	Flow           string                 `json:"flow"`
	Outcome        layout.Outcome         `json:"outcome"`
	ProposalDigest string                 `json:"proposal_digest,omitempty"`
	SnapshotDigest string                 `json:"snapshot_digest"`
	MaterialDigest string                 `json:"material_digest,omitempty"`
	ExpiresAt      *time.Time             `json:"expires_at,omitempty"`
	SupersededBy   string                 `json:"superseded_by,omitempty"`
	CatalogRelease string                 `json:"catalog_release"`
	Planner        layout.PlannerIdentity `json:"planner"`
	Nodes          []NodeView             `json:"nodes"`
	Assignments    []AssignmentView       `json:"assignments"`
	Existing       []ExistingView         `json:"existing,omitempty"`
	Notes          []layout.Reason        `json:"notes,omitempty"`
	Conflicts      []layout.Conflict      `json:"conflicts,omitempty"`
	Verification   Verification           `json:"verification"`
}

// NodeView is one node of the layout.
type NodeView struct {
	ID           string              `json:"id"`
	SpecNodeRef  string              `json:"spec_node_ref,omitempty"`
	Name         string              `json:"name"`
	Primary      bool                `json:"primary,omitempty"`
	Planned      bool                `json:"planned,omitempty"`
	State        string              `json:"state"`
	Availability layout.Availability `json:"availability"`
	Capacity     layout.Resources    `json:"capacity"`
	Reserved     layout.Resources    `json:"reserved"`
	Workloads    []string            `json:"workloads"`
}

// AssignmentView explains one placement.
type AssignmentView struct {
	Workload    string          `json:"workload"`
	Workloads   []string        `json:"workloads,omitempty"`
	Title       string          `json:"title"`
	Goal        string          `json:"goal"`
	NodeID      string          `json:"node_id"`
	NodeName    string          `json:"node_name"`
	SpecNodeRef string          `json:"spec_node_ref,omitempty"`
	Band        string          `json:"band"`
	Provenance  string          `json:"provenance"`
	Reasons     []layout.Reason `json:"reasons"`
}

// ExistingView is a deployed workload that stays where it is.
type ExistingView struct {
	Workload string `json:"workload"`
	NodeID   string `json:"node_id"`
}

// Verification is the StackKits feasibility evidence for the shown option.
type Verification struct {
	Status          string          `json:"status"`
	Outcome         string          `json:"outcome,omitempty"`
	CLIVersion      string          `json:"cli_version,omitempty"`
	SpecDigest      string          `json:"spec_digest,omitempty"`
	InventoryDigest string          `json:"inventory_digest,omitempty"`
	Reasons         []layout.Reason `json:"reasons,omitempty"`
}

// recordPayload is the record body: the question and the shown answer.
type recordPayload struct {
	Selection Selection `json:"selection"`
	View      View      `json:"view"`
}

// computed is one complete evaluation.
type computed struct {
	view        View
	computation unifier.LayoutComputation
	proposal    map[string]any
	assignments map[string]string
	material    string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Preview computes, verifies and records the recommendation for a selection.
// A record exists only for a verified option; anything else is returned as a
// view without a record, so unverified advice is never accepted.
func (s *Service) Preview(ctx context.Context, scope Scope, selection Selection) (View, error) {
	if s == nil || s.Stacks == nil || s.Servers == nil || s.Workers == nil || s.Catalog == nil || s.Projector == nil || s.Records == nil {
		return View{}, ErrUnavailable
	}
	selection = normalizeSelection(selection)
	stack, spec, err := s.ownedSpec(ctx, scope, selection.StackID)
	if err != nil {
		return View{}, err
	}
	projection, err := s.project(ctx, stack, spec, selection)
	if err != nil {
		return View{}, err
	}
	result, err := s.evaluate(ctx, scope, stack, spec, projection, selection)
	if err != nil {
		return View{}, err
	}
	if result.view.Outcome != layout.OutcomeFeasible || result.view.Verification.Status != VerificationVerified {
		result.view.State = "unverified"
		return result.view, nil
	}
	record, err := s.Records.Propose(ctx, s.proposal(scope, stack, selection, result))
	if err != nil {
		return View{}, err
	}
	return viewFromRecord(*record, result.view), nil
}

// RunAcceptance is the outcome of accepting inside a wizard run.
type RunAcceptance struct {
	// Assignments maps each planned workload to its StackSpec node.
	Assignments map[string]string
	Record      *actions.Recommendation
	// Replacement is set when revalidation found a material change; the run
	// must stop and show it.
	Replacement *View
}

// AcceptForRun revalidates the shown recommendation against a fresh snapshot
// and the run's own projected proposal, then accepts it. The run writes the
// returned assignments into its spec only when Replacement is nil.
func (s *Service) AcceptForRun(ctx context.Context, scope Scope, acceptance specv2.LayoutRecommendationAcceptance, stackID, flow string, projection *specv2.Projection, correlationID string) (RunAcceptance, error) {
	if s == nil || s.Records == nil || s.Stacks == nil || s.Servers == nil || s.Workers == nil || s.Catalog == nil {
		return RunAcceptance{}, ErrUnavailable
	}
	record, err := s.Records.GetRecommendation(ctx, scope.TenantID, scope.OwnerID, acceptance.ID, s.now())
	if err != nil {
		return RunAcceptance{}, err
	}
	var payload recordPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return RunAcceptance{}, fmt.Errorf("layoutadvice: read recommendation: %w", err)
	}
	if payload.Selection.StackID != stackID || payload.Selection.Flow != flow || projection == nil {
		return RunAcceptance{}, ErrSelectionMismatch
	}
	if acceptedByRun(*record, correlationID) {
		// A same-key retry of the run that accepted it: its workloads may
		// already be saved, so re-planning would find nothing to place. It
		// carries out exactly what it accepted.
		return RunAcceptance{Assignments: assignmentsFromView(payload.View), Record: record}, nil
	}
	stack, spec, err := s.ownedSpec(ctx, scope, stackID)
	if err != nil {
		return RunAcceptance{}, err
	}
	fresh, err := s.evaluate(ctx, scope, stack, spec, projection, payload.Selection)
	if err != nil {
		return RunAcceptance{}, err
	}
	if fresh.view.Outcome != layout.OutcomeFeasible || fresh.view.Verification.Status != VerificationVerified {
		// Never accept advice that the current homelab no longer verifies.
		return RunAcceptance{}, ErrUnverifiable
	}
	// Every workload the run adds must be one the plan placed; anything
	// else was never planned or verified.
	for workload := range addedWorkloads(spec, projection.Spec) {
		if _, planned := fresh.assignments[workload]; !planned {
			return RunAcceptance{}, fmt.Errorf("%w: %s was not part of the recommendation", ErrSelectionMismatch, workload)
		}
	}
	freshProposal := s.proposal(scope, stack, payload.Selection, fresh)
	outcome, err := s.Records.Accept(ctx, actions.AcceptRecommendation{
		TenantID: scope.TenantID, OwnerSubjectID: scope.OwnerID, ID: record.ID,
		ProposalDigest: acceptance.ProposalDigest, Fresh: freshProposal, CorrelationID: correlationID, Now: s.now(),
	})
	if err != nil {
		return RunAcceptance{}, err
	}
	if outcome.Replacement != nil {
		view := viewFromRecord(*outcome.Replacement, fresh.view)
		return RunAcceptance{Replacement: &view}, nil
	}
	assignments := fresh.assignments
	if outcome.Replayed {
		assignments = assignmentsFromView(payload.View)
	}
	return RunAcceptance{Assignments: assignments, Record: outcome.Accepted}, nil
}

// Get returns one record as a view.
func (s *Service) Get(ctx context.Context, scope Scope, id string) (View, error) {
	if s == nil || s.Records == nil {
		return View{}, ErrUnavailable
	}
	record, err := s.Records.GetRecommendation(ctx, scope.TenantID, scope.OwnerID, id, s.now())
	if err != nil {
		return View{}, err
	}
	return storedView(s.settle(ctx, scope, *record))
}

// settle projects a terminal state of the carrying job onto an executing
// record, so the lifecycle reaches done or failed without a second writer.
func (s *Service) settle(ctx context.Context, scope Scope, record actions.Recommendation) actions.Recommendation {
	if record.State != actions.RecommendationExecuting || record.ExecutionID == "" || s.Jobs == nil {
		return record
	}
	job, err := s.Jobs.GetJob(ctx, scope.TenantID, record.ExecutionID)
	if err != nil || job == nil {
		return record
	}
	state := strings.ToLower(strings.TrimSpace(job.State))
	if state != "completed" && state != "failed" && state != "cancelled" && state != "canceled" {
		return record
	}
	finished, err := s.Records.Finish(ctx, scope.TenantID, scope.OwnerID, record.ID, state == "completed", "job_"+state, s.now())
	if err != nil || finished == nil {
		return record
	}
	return *finished
}

// List returns the owner's records for one deployment scope.
func (s *Service) List(ctx context.Context, scope Scope, stackID string) ([]View, error) {
	if s == nil || s.Records == nil {
		return nil, ErrUnavailable
	}
	records, err := s.Records.ListRecommendations(ctx, scope.TenantID, scope.OwnerID, scopeKey(stackID), s.now())
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(records))
	for _, record := range records {
		view, err := storedView(s.settle(ctx, scope, record))
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// MarkExecuting links an accepted record to the job that carries it out.
func (s *Service) MarkExecuting(ctx context.Context, scope Scope, id, jobID string) error {
	if s == nil || s.Records == nil || strings.TrimSpace(jobID) == "" {
		return nil
	}
	_, err := s.Records.MarkExecuting(ctx, scope.TenantID, scope.OwnerID, id, jobID, s.now())
	if errors.Is(err, actions.ErrRecommendationState) {
		return nil // an idempotent run replay already linked it
	}
	return err
}

func storedView(record actions.Recommendation) (View, error) {
	var payload recordPayload
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return View{}, fmt.Errorf("layoutadvice: read recommendation: %w", err)
	}
	return viewFromRecord(record, payload.View), nil
}

func viewFromRecord(record actions.Recommendation, view View) View {
	expires := record.ExpiresAt
	view.ID, view.State, view.ProposalDigest, view.MaterialDigest = record.ID, record.State, record.ProposalDigest, record.MaterialDigest
	view.ExpiresAt, view.SupersededBy = &expires, record.SupersededBy
	return view
}

func assignmentsFromView(view View) map[string]string {
	out := map[string]string{}
	for _, assignment := range view.Assignments {
		for _, workload := range assignmentWorkloads(assignment.Workload, assignment.Workloads) {
			out[workload] = assignment.SpecNodeRef
		}
	}
	return out
}

// assignmentWorkloads are the StackSpec workloads one placed unit covers.
func assignmentWorkloads(unit string, workloads []string) []string {
	if len(workloads) == 0 {
		return []string{unit}
	}
	return workloads
}

func normalizeSelection(selection Selection) Selection {
	selection.Flow = strings.TrimSpace(selection.Flow)
	selection.StackID = strings.TrimSpace(selection.StackID)
	seen := map[string]bool{}
	goals := make([]string, 0, len(selection.Goals))
	for _, goal := range selection.Goals {
		goal = strings.ToLower(strings.TrimSpace(goal))
		if goal != "" && !seen[goal] {
			seen[goal] = true
			goals = append(goals, goal)
		}
	}
	sort.Strings(goals)
	selection.Goals = goals
	return selection
}

func scopeKey(stackID string) string { return "stack:" + strings.TrimSpace(stackID) }

func (s *Service) ownedSpec(ctx context.Context, scope Scope, stackID string) (*controlplane.Stack, map[string]any, error) {
	if strings.TrimSpace(stackID) == "" {
		return nil, nil, ErrStackNotFound
	}
	stack, err := s.Stacks.GetStack(ctx, scope.TenantID, stackID)
	if err != nil || stack == nil || stack.OwnerSubjectID != scope.OwnerID || stack.DeletedAt != nil {
		return nil, nil, ErrStackNotFound
	}
	spec, _ := stack.Config[stackSpecV2Key].(map[string]any)
	if spec == nil || specv2.RequireCanonicalV2(spec) != nil {
		return nil, nil, ErrNoSpec
	}
	return stack, spec, nil
}

// project runs the same release projection a wizard run performs.
func (s *Service) project(ctx context.Context, stack *controlplane.Stack, spec map[string]any, selection Selection) (*specv2.Projection, error) {
	metadata, _ := spec["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	intent := specv2.WizardIntent{
		Schema: specv2.WizardIntentSchema, RunKind: specv2.RunKindExpansion, Name: firstNonEmpty(name, stack.Name),
		Goals: selection.Goals, UseCaseSettings: selection.Settings, UseCaseCapabilities: selection.Capabilities,
		KitAssignment: specv2.KitAssignment{Mode: specv2.KitAssignmentExtend, KitDeploymentID: stack.ID},
	}
	if selection.Flow == FlowAddServer {
		intent.KitAssignment.Mode = specv2.KitAssignmentJoin
		if planned := selection.PlannedNode; planned != nil {
			intent.Server = specv2.ServerIntent{Roles: planned.Roles, Transport: planned.Transport}
		}
	}
	if len(intent.UseCaseSettings) == 0 {
		intent.UseCaseSettings = nil
	}
	if len(intent.UseCaseCapabilities) == 0 {
		intent.UseCaseCapabilities = nil
	}
	return s.Projector.Project(ctx, spec, intent, stack.HomelabID)
}

// evaluate plans over a fresh snapshot and verifies the proposal.
func (s *Service) evaluate(ctx context.Context, scope Scope, stack *controlplane.Stack, spec map[string]any, projection *specv2.Projection, selection Selection) (computed, error) {
	catalog, err := s.Catalog()
	if err != nil {
		return computed{}, fmt.Errorf("layoutadvice: read the pinned use-case catalog: %w", err)
	}
	input, err := s.layoutInput(ctx, scope, stack, spec, projection, selection, catalog)
	if err != nil {
		return computed{}, err
	}
	computation, err := unifier.ComputeLayout(ctx, catalog, input, s.Planner)
	if err != nil {
		return computed{}, err
	}
	out := computed{computation: computation, assignments: map[string]string{}}
	out.view = buildView(scopeKey(stack.ID), selection, computation, catalog.Release.Tag)
	if computation.Result.Outcome != layout.OutcomeFeasible || len(computation.Result.Options) == 0 {
		out.view.Verification = Verification{Status: VerificationSkipped}
		return out, nil
	}
	option := computation.Result.Options[0]
	for _, assignment := range option.Assignments {
		for _, workload := range assignmentWorkloads(assignment.Workload, assignment.Workloads) {
			out.assignments[workload] = assignment.SpecNodeRef
		}
	}
	out.material = materialDigest(computation.Problem, option)
	out.view.MaterialDigest = out.material
	if len(out.assignments) == 0 {
		out.view.Verification = Verification{Status: VerificationSkipped}
		return out, nil
	}
	proposal, err := proposalSpec(projection.Spec, out.assignments)
	if err != nil {
		// The projection does not carry what the plan places: the run and
		// the recommendation describe different selections.
		return computed{}, fmt.Errorf("%w: %v", ErrSelectionMismatch, err)
	}
	out.proposal = proposal
	out.view.Verification = s.verify(ctx, proposal, inventoryDocument(computation.Snapshot), option.Digest)
	return out, nil
}

func proposalSpec(projected map[string]any, assignments map[string]string) (map[string]any, error) {
	raw, err := json.Marshal(projected)
	if err != nil {
		return nil, err
	}
	var proposal map[string]any
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return nil, err
	}
	if err := specv2.ApplyLayoutNodeRefs(proposal, assignments); err != nil {
		return nil, err
	}
	return proposal, nil
}

func (s *Service) verify(ctx context.Context, proposal map[string]any, inventory specv2.InventoryDocument, proposalDigest string) Verification {
	if s.Checker == nil {
		return Verification{Status: VerificationUnavailable, Reasons: []layout.Reason{{Code: "feasibility_unavailable", Text: "The pinned StackKits CLI is not available here, so the distribution was not checked against your hardware."}}}
	}
	raw, _ := json.Marshal(inventory)
	key := proposalDigest + "\x00" + digest(string(raw))
	s.cacheMu.Lock()
	if cached, ok := s.cache[key]; ok && s.now().Sub(cached.at) < verificationCacheTTL {
		s.cacheMu.Unlock()
		return cached.verification
	}
	s.cacheMu.Unlock()
	report, err := s.Checker.Feasibility(ctx, proposal, inventory)
	var verification Verification
	switch {
	case errors.Is(err, specv2.ErrFeasibilityUnsupported):
		return Verification{Status: VerificationUnavailable, Reasons: []layout.Reason{{Code: "feasibility_unsupported", Text: "The pinned StackKits release cannot check a distribution against your hardware yet."}}}
	case err != nil:
		return Verification{Status: VerificationUnavailable, Reasons: []layout.Reason{{Code: "feasibility_failed", Text: "The check against your hardware could not run: " + err.Error()}}}
	default:
		verification = verificationFromReport(report)
	}
	s.cacheMu.Lock()
	if s.cache == nil {
		s.cache = map[string]cachedVerification{}
	}
	if len(s.cache) > 256 {
		s.cache = map[string]cachedVerification{}
	}
	s.cache[key] = cachedVerification{verification: verification, at: s.now()}
	s.cacheMu.Unlock()
	return verification
}

func verificationFromReport(report specv2.FeasibilityReport) Verification {
	verification := Verification{Outcome: report.Outcome, CLIVersion: report.CLIVersion, SpecDigest: report.SpecDigest, InventoryDigest: report.InventoryDigest}
	switch report.Outcome {
	case "feasible":
		verification.Status = VerificationVerified
	case "infeasible":
		verification.Status = VerificationInfeasible
	default:
		verification.Status = VerificationUnknown
	}
	for _, workload := range report.Workloads {
		if workload.Verdict == "feasible" {
			continue
		}
		for _, reason := range workload.Reasons {
			verification.Reasons = append(verification.Reasons, layout.Reason{
				Code:   "stackkits:" + reason.Code,
				Params: map[string]string{"workload": workload.Workload, "node": reason.NodeRef, "axis": reason.Axis},
				Text:   firstNonEmpty(reason.Message, reason.Code),
			})
		}
	}
	for _, failure := range report.Errors {
		verification.Reasons = append(verification.Reasons, layout.Reason{Code: "stackkits:" + failure.Code, Params: map[string]string{"path": failure.Path}, Text: firstNonEmpty(failure.Message, failure.Code)})
	}
	return verification
}

func (s *Service) proposal(scope Scope, stack *controlplane.Stack, selection Selection, result computed) actions.ProposeRecommendation {
	payload, _ := json.Marshal(recordPayload{Selection: selection, View: result.view})
	proposalDigest := ""
	if len(result.computation.Result.Options) > 0 {
		proposalDigest = result.computation.Result.Options[0].Digest
	}
	return actions.ProposeRecommendation{
		TenantID: scope.TenantID, OwnerSubjectID: scope.OwnerID, Scope: scopeKey(stack.ID), StackID: stack.ID,
		Title:          "Recommended distribution",
		ProposalDigest: proposalDigest, SnapshotDigest: result.computation.Snapshot.Digest, MaterialDigest: result.material,
		Payload: payload, Now: s.now(),
		Verified: result.view.Outcome == layout.OutcomeFeasible && result.view.Verification.Status == VerificationVerified,
	}
}

// addedWorkloads are the workloads a projection adds to the stored spec.
func addedWorkloads(stored, projected map[string]any) map[string]bool {
	before, _ := stored["workloads"].(map[string]any)
	after, _ := projected["workloads"].(map[string]any)
	out := map[string]bool{}
	for id := range after {
		if _, existed := before[id]; !existed {
			out[id] = true
		}
	}
	return out
}

// materialDigest is what acceptance compares after re-planning: which
// workload goes to which node under which catalog and policy, never the
// observation details of the snapshot.
func materialDigest(problem layout.Problem, option layout.Option) string {
	type demand struct {
		Workload    string           `json:"workload"`
		Modules     []string         `json:"modules"`
		Reservation layout.Resources `json:"reservation"`
		Band        string           `json:"band"`
	}
	material := struct {
		Layout  string   `json:"layout"`
		Catalog string   `json:"catalog"`
		Policy  string   `json:"policy"`
		Demands []demand `json:"demands"`
	}{Layout: layout.LayoutDigest(option), Catalog: problem.CatalogRelease, Policy: problem.PolicyProfile}
	for _, d := range problem.Demands {
		material.Demands = append(material.Demands, demand{Workload: d.Workload, Modules: d.Modules, Reservation: d.Reservation, Band: d.Band})
	}
	raw, _ := json.Marshal(material)
	return digest(string(raw))
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// layoutInput builds the snapshot input from the canonical servers of the
// deployment, their registry workers and the stored StackSpec.
func (s *Service) layoutInput(ctx context.Context, scope Scope, stack *controlplane.Stack, spec map[string]any, projection *specv2.Projection, selection Selection, catalog stackkitrelease.UseCaseCatalog) (unifier.LayoutInput, error) {
	servers, err := s.Servers.ListServerRuntimesByTenant(ctx, scope.TenantID, stack.ID)
	if err != nil {
		return unifier.LayoutInput{}, fmt.Errorf("layoutadvice: list servers: %w", err)
	}
	workers, err := s.Workers(ctx, scope.TenantID, scope.OwnerID)
	if err != nil {
		return unifier.LayoutInput{}, fmt.Errorf("layoutadvice: read worker inventory: %w", err)
	}
	workerByID := map[string]core.Worker{}
	for _, worker := range workers {
		workerByID[worker.ID] = worker
	}
	specNodes := nodeRoles(spec)
	primary := primarySpecNode(spec)
	input := unifier.LayoutInput{
		Scope: scopeKey(stack.ID), StackID: stack.ID, OwnerID: scope.OwnerID,
		SpecNodeRefs: map[string]string{}, NodeRoles: map[string][]string{}, RequiresRoles: map[string][]string{},
		Goals: selection.Goals, ComputeProfile: selection.ComputeProfile, Settings: selection.Settings, Capabilities: selection.Capabilities,
	}
	serverOfNode := map[string]string{}
	for _, server := range servers {
		if server.OwnerSubjectID != scope.OwnerID || server.DesiredState == "decommissioned" || server.DecommissionedAt != nil {
			continue
		}
		nodeRef := specNodeOfServer(stack.ID, server, specNodes)
		if nodeRef == "" {
			continue // a server of the tenant that this deployment does not declare
		}
		worker, ok := workerByID[server.WorkerID]
		if !ok {
			worker = core.Worker{ID: server.WorkerID, Name: firstNonEmpty(server.DisplayName, server.Name), Status: "offline"}
		}
		worker.ServerID, worker.StackID = server.ID, stack.ID
		worker.Name = firstNonEmpty(server.DisplayName, server.Name, worker.Name)
		if server.ConnectionState != "connected" && server.ConnectionState != "degraded" {
			worker.Status = "offline"
		}
		input.Workers = append(input.Workers, worker)
		input.SpecNodeRefs[server.ID] = nodeRef
		input.NodeRoles[server.ID] = specNodes[nodeRef]
		serverOfNode[nodeRef] = server.ID
		if nodeRef == primary {
			input.PrimaryServerID = server.ID
		}
	}
	if selection.Flow == FlowAddServer && projection != nil && projection.NodeID != "" && serverOfNode[projection.NodeID] == "" {
		input.Planned = append(input.Planned, plannedLayoutNode(projection.NodeID, projection.Spec, selection.PlannedNode, scope.OwnerID))
		serverOfNode[projection.NodeID] = plannedPrefix + projection.NodeID
	}
	input.Existing = existingComponents(spec, catalog, selection.ComputeProfile, serverOfNode, primary)
	if projection != nil {
		for workload, roles := range authoredRequiredRoles(spec, projection.Spec) {
			input.RequiresRoles[workload] = roles
		}
	}
	return input, nil
}

func plannedLayoutNode(nodeRef string, projected map[string]any, planned *PlannedNode, ownerID string) layout.Node {
	node := layout.Node{
		ID: plannedPrefix + nodeRef, SpecNodeRef: nodeRef, Name: nodeRef, Operator: ownerID,
		TrustClass: layout.TrustOwnerControlled, State: layout.NodePlanned, Roles: nodeRoles(projected)[nodeRef],
		Availability: layout.Availability{Class: layout.AvailabilityUnknown, Source: "none"}, CapacitySource: "unknown",
		Missing: []string{"capacity"},
	}
	if planned != nil {
		node.Name = firstNonEmpty(planned.Name, nodeRef)
		if planned.CPUCores > 0 && planned.RAMGB > 0 {
			node.Capacity = layout.Resources{CPUCores: float64(planned.CPUCores), RAMGB: planned.RAMGB, StorageGB: float64(planned.StorageGB)}
			node.CapacitySource, node.Missing = firstNonEmpty(planned.Source, "declared"), nil
			node.Arch = strings.ToLower(firstNonEmpty(planned.Arch, "amd64"))
		}
	}
	return node
}

func nodeRoles(spec map[string]any) map[string][]string {
	out := map[string][]string{}
	nodes, _ := spec["nodes"].([]any)
	for _, raw := range nodes {
		node, _ := raw.(map[string]any)
		id, _ := node["id"].(string)
		if id == "" {
			continue
		}
		if enabled, set := node["enabled"].(bool); set && !enabled {
			continue
		}
		var roles []string
		list, _ := node["roles"].([]any)
		for _, role := range list {
			if text, ok := role.(string); ok {
				roles = append(roles, text)
			}
		}
		out[id] = roles
	}
	return out
}

func primarySpecNode(spec map[string]any) string {
	nodes, _ := spec["nodes"].([]any)
	first := ""
	for _, raw := range nodes {
		node, _ := raw.(map[string]any)
		id, _ := node["id"].(string)
		if first == "" {
			first = id
		}
		roles, _ := node["roles"].([]any)
		for _, role := range roles {
			if role == specv2.RoleController {
				return id
			}
		}
	}
	return first
}

func specNodeOfServer(stackID string, server controlplane.ServerRuntime, nodes map[string][]string) string {
	if ref, _ := server.Metadata["spec_node_id"].(string); ref != "" {
		if _, declared := nodes[ref]; declared {
			return ref
		}
	}
	for ref := range nodes {
		if runtimeidentity.StackServerID(stackID, ref) == server.ID {
			return ref
		}
	}
	if len(nodes) == 1 && strings.TrimSpace(server.StackID) == stackID {
		for ref := range nodes {
			return ref // the single node of a single-node deployment
		}
	}
	return ""
}

// existingComponents are the deployed workloads: they stay where they are.
func existingComponents(spec map[string]any, catalog stackkitrelease.UseCaseCatalog, profile string, serverOfNode map[string]string, primary string) []layout.Component {
	workloads, _ := spec["workloads"].(map[string]any)
	ids := make([]string, 0, len(workloads))
	for id := range workloads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var components []layout.Component
	for _, id := range ids {
		entry, _ := workloads[id].(map[string]any)
		nodeRef, assumed := workloadNode(entry, primary)
		serverID := serverOfNode[nodeRef]
		if serverID == "" {
			continue
		}
		component := layout.Component{ID: id, Workload: id, NodeID: serverID, Management: layout.ManagementManaged, Assumed: assumed, Band: layout.DefaultBand(id)}
		alternative, _ := entry["alternative"].(string)
		if facts, err := catalog.WorkloadFootprints(id, alternative, profile, ""); err == nil {
			for _, fact := range facts {
				component.Modules = append(component.Modules, fact.Module)
				component.Reservation = component.Reservation.Add(layout.Resources{CPUCores: fact.Reservation.CPUCores, RAMGB: fact.Reservation.RAMGB, StorageGB: fact.Reservation.StorageGB})
			}
		}
		components = append(components, component)
	}
	return components
}

func workloadNode(entry map[string]any, primary string) (string, bool) {
	placement, _ := entry["placement"].(map[string]any)
	refs, _ := placement["nodeRefs"].([]any)
	if len(refs) == 1 {
		if ref, ok := refs[0].(string); ok && ref != "" {
			return ref, false
		}
	}
	return primary, true
}

// authoredRequiredRoles reads requiresRoles of the workloads the projection
// adds, as the pinned release authored them.
func authoredRequiredRoles(stored, projected map[string]any) map[string][]string {
	before, _ := stored["workloads"].(map[string]any)
	after, _ := projected["workloads"].(map[string]any)
	out := map[string][]string{}
	for id, raw := range after {
		if _, existed := before[id]; existed {
			continue
		}
		entry, _ := raw.(map[string]any)
		placement, _ := entry["placement"].(map[string]any)
		roles, _ := placement["requiresRoles"].([]any)
		for _, role := range roles {
			if text, ok := role.(string); ok {
				out[id] = append(out[id], text)
			}
		}
	}
	return out
}

func inventoryDocument(snapshot layout.Snapshot) specv2.InventoryDocument {
	document := specv2.InventoryDocument{SchemaVersion: "stackkit.inventory/v1", Nodes: map[string]specv2.InventoryNodeFact{}}
	for _, node := range snapshot.Nodes {
		if node.SpecNodeRef == "" || node.Capacity.RAMGB <= 0 {
			continue
		}
		fact := specv2.InventoryNodeFact{
			SiteAddress: siteAddress(node.Address),
			Arch:        node.Arch, AMD64MicroarchitectureLevel: node.AMD64Level,
			CPUCores: int(node.Capacity.CPUCores), RAMGB: int(math.Floor(node.Capacity.RAMGB)), StorageGB: int(node.Capacity.StorageGB),
		}
		if node.Accelerator != nil && node.Accelerator.Vendor != "" {
			fact.Accelerators = []specv2.InventoryAccelerator{{Vendor: node.Accelerator.Vendor}}
		}
		document.Nodes[node.SpecNodeRef] = fact
	}
	return document
}

func buildView(scope string, selection Selection, computation unifier.LayoutComputation, release string) View {
	result := computation.Result
	view := View{
		APIVersion: actions.RecommendationAPIVersion, State: "preview", Scope: scope, StackID: selection.StackID, Flow: selection.Flow,
		Outcome: result.Outcome, SnapshotDigest: computation.Snapshot.Digest, CatalogRelease: release, Planner: result.Planner,
		Notes: result.Notes, Conflicts: result.Conflicts, Nodes: []NodeView{}, Assignments: []AssignmentView{},
	}
	names := map[string]string{}
	for _, node := range computation.Snapshot.Nodes {
		names[node.ID] = node.Name
	}
	loads := map[string]layout.NodeLoad{}
	if len(result.Options) > 0 {
		view.ProposalDigest = result.Options[0].Digest
		for _, load := range result.Options[0].Loads {
			loads[load.NodeID] = load
		}
		for _, assignment := range result.Options[0].Assignments {
			title := assignment.Workload
			for _, demand := range computation.Problem.Demands {
				if demand.Workload == assignment.Workload && demand.Title != "" {
					title = demand.Title
				}
			}
			view.Assignments = append(view.Assignments, AssignmentView{
				Workload: assignment.Workload, Workloads: assignment.Workloads, Title: title, Goal: assignment.Goal, NodeID: assignment.NodeID, NodeName: names[assignment.NodeID],
				SpecNodeRef: assignment.SpecNodeRef, Band: assignment.Band, Provenance: assignment.Provenance, Reasons: assignment.Reasons,
			})
		}
	}
	for _, node := range computation.Snapshot.Nodes {
		load := loads[node.ID]
		workloads := load.Workloads
		if workloads == nil {
			workloads = []string{}
		}
		view.Nodes = append(view.Nodes, NodeView{
			ID: node.ID, SpecNodeRef: node.SpecNodeRef, Name: node.Name, Primary: node.Primary, Planned: node.State == layout.NodePlanned,
			State: node.State, Availability: node.Availability, Capacity: node.Capacity, Reserved: load.Reserved, Workloads: workloads,
		})
	}
	for _, component := range computation.Snapshot.Components {
		view.Existing = append(view.Existing, ExistingView{Workload: component.Workload, NodeID: component.NodeID})
	}
	return view
}

// acceptedByRun reports whether the record was accepted by this exact run.
func acceptedByRun(record actions.Recommendation, correlationID string) bool {
	switch record.State {
	case actions.RecommendationAccepted, actions.RecommendationExecuting, actions.RecommendationDone:
	default:
		return false
	}
	return record.Acceptance != nil && strings.TrimSpace(correlationID) != "" &&
		record.Acceptance.AuditCorrelation == strings.TrimSpace(correlationID)
}

// siteAddress keeps an observed node IP only when it is a concrete unicast
// address; anything else stays absent, so StackKits reports it unknown.
func siteAddress(value string) string {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	address = address.Unmap().WithZone("")
	if !address.IsGlobalUnicast() || address.IsLoopback() {
		return ""
	}
	return address.String()
}
