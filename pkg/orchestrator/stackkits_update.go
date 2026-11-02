package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/stackkitsupdate"
)

// ErrStackKitsUpdateUnavailable means the node's update state cannot be read
// or changed here (no worker store, or a store without the update keys).
var ErrStackKitsUpdateUnavailable = errors.New("StackKits update management is unavailable")

// StackKitsUpdateView is the StackKits update state of one node: its setting,
// installed and pending release, the proposal for the pending one and what
// happened to the last update.
type StackKitsUpdateView struct {
	WorkerID  string `json:"worker_id"`
	StackID   string `json:"stack_id"`
	Behavior  string `json:"behavior"`
	Installed string `json:"installed,omitempty"`
	Available string `json:"available,omitempty"`
	// ChangelogURL is the public StackKits changelog.
	ChangelogURL string `json:"changelog_url,omitempty"`
	// Proposal is present while a release is available.
	Proposal *stackkitsupdate.Proposal `json:"proposal,omitempty"`
	// Started is the update Techstack started without a click.
	Started *controlplane.WorkerStackKitsUpdateAttempt `json:"started,omitempty"`
	// LastUpgrade is the outcome of the last governed upgrade on the node; a
	// failed one was rolled back to Installed.
	LastUpgrade *controlplane.WorkerStackKitsTarget `json:"last_upgrade,omitempty"`
}

func (o *Orchestrator) stackKitsUpdateStore() (controlplane.WorkerStackKitsUpdateStore, error) {
	if o == nil {
		return nil, ErrStackKitsUpdateUnavailable
	}
	store, ok := o.workerStore.(controlplane.WorkerStackKitsUpdateStore)
	if o.workerStore == nil || !ok {
		return nil, ErrStackKitsUpdateUnavailable
	}
	return store, nil
}

// ownedStackKitsWorker reads a node of the owner; ownerID empty skips the
// owner check for Techstack's own evaluation.
func (o *Orchestrator) ownedStackKitsWorker(ctx context.Context, tenantID, ownerID, workerID string) (*controlplane.Worker, error) {
	if o == nil || o.workerStore == nil {
		return nil, ErrStackKitsUpdateUnavailable
	}
	worker, err := o.workerStore.GetWorker(ctx, tenantID, workerID)
	if err != nil {
		return nil, err
	}
	if worker == nil || (ownerID != "" && worker.OwnerSubjectID != ownerID) {
		return nil, controlplane.ErrNotFound
	}
	return worker, nil
}

// StackKitsUpdate reads the update state of the owner's node.
func (o *Orchestrator) StackKitsUpdate(ctx context.Context, tenantID, ownerID, workerID string) (StackKitsUpdateView, error) {
	worker, err := o.ownedStackKitsWorker(ctx, tenantID, ownerID, workerID)
	if err != nil {
		return StackKitsUpdateView{}, err
	}
	return o.stackKitsUpdateView(ctx, worker), nil
}

// SetStackKitsUpdateBehavior stores the owner's update setting of a node and
// evaluates the pending update at once, so "install automatically" starts it.
func (o *Orchestrator) SetStackKitsUpdateBehavior(ctx context.Context, tenantID, ownerID, workerID, behavior string) (StackKitsUpdateView, error) {
	if behavior != controlplane.StackKitsUpdateApprove && behavior != controlplane.StackKitsUpdateAutomatic {
		return StackKitsUpdateView{}, fmt.Errorf("unknown update behavior %q", behavior)
	}
	store, err := o.stackKitsUpdateStore()
	if err != nil {
		return StackKitsUpdateView{}, err
	}
	if _, err := o.ownedStackKitsWorker(ctx, tenantID, ownerID, workerID); err != nil {
		return StackKitsUpdateView{}, err
	}
	if err := store.SetWorkerStackKitsUpdateBehavior(ctx, tenantID, workerID, controlplane.WorkerStackKitsUpdateBehavior{
		Behavior: behavior, SetBy: ownerID, SetAt: time.Now().UTC(),
	}); err != nil {
		return StackKitsUpdateView{}, err
	}
	if _, err := o.EvaluateStackKitsUpdate(ctx, tenantID, workerID); err != nil {
		o.log.Warn("stackkits_update_evaluation_failed", "worker_id", workerID, "error", err.Error())
	}
	return o.StackKitsUpdate(ctx, tenantID, ownerID, workerID)
}

func (o *Orchestrator) stackKitsUpdateView(ctx context.Context, worker *controlplane.Worker) StackKitsUpdateView {
	view := StackKitsUpdateView{
		WorkerID: worker.ID, StackID: worker.StackID,
		Behavior: controlplane.WorkerStackKitsUpdateBehaviorOf(worker).Behavior,
	}
	view.Installed, view.Available = nodeStackKitsReleases(worker)
	if attempt, ok := controlplane.WorkerStackKitsUpdateAttemptOf(worker); ok {
		view.Started = &attempt
	}
	if target, ok := controlplane.WorkerStackKitsTargetOf(worker); ok {
		view.LastUpgrade = &target
	}
	if notes, err := stackkitrelease.PinnedReleaseNotes(); err == nil {
		view.ChangelogURL = notes.ChangelogURL
	}
	if view.Available != "" {
		proposal, _ := o.stackKitsProposal(ctx, worker, view.Installed, view.Available)
		view.Proposal = &proposal
	}
	return view
}

// stackKitsProposal ranks the pending release notes by the use cases the
// node's stack activates. A stack without a readable spec or catalog still
// gets the proposal, with every change under "the rest"; resolved is false
// then, and Techstack starts nothing for that node on its own.
func (o *Orchestrator) stackKitsProposal(ctx context.Context, worker *controlplane.Worker, installed, available string) (proposal stackkitsupdate.Proposal, resolved bool) {
	notes, err := stackkitrelease.PinnedReleaseNotes()
	if err != nil {
		o.log.Warn("stackkits_release_notes_unavailable", "error", err.Error())
	}
	var catalog stackkitrelease.UseCaseCatalog
	var activated []stackkitsupdate.UseCase
	if stack, stackErr := o.findStackForJob(ctx, worker.StackID, ProvisionStackOptions{RequestContext: ctx, TenantID: worker.TenantID, OwnerID: worker.OwnerSubjectID}); stackErr == nil {
		spec, specOK := stack.config["stack_spec_v2"].(map[string]any)
		if read, catalogErr := jobs.UseCaseCatalogForStack(ctx, worker.TenantID, worker.StackID); catalogErr == nil && specOK {
			catalog, resolved = read, true
			activated = stackkitsupdate.ActivatedUseCases(catalog, spec)
		}
	}
	return stackkitsupdate.Propose(notes, activated, installed, available), resolved && err == nil
}

// nodeStackKitsReleases reads the installed and the pending release the
// node's Guard last reported.
func nodeStackKitsReleases(worker *controlplane.Worker) (installed, available string) {
	raw, err := json.Marshal(worker.Resources["runtime_convergence"])
	if err != nil {
		return "", ""
	}
	var snapshot runtimeconvergence.Snapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return "", ""
	}
	if snapshot, err = runtimeconvergence.Normalize(snapshot); err != nil {
		return "", ""
	}
	for _, component := range snapshot.Components {
		if component.Name != runtimeconvergence.StackKitsRuntimeComponent {
			continue
		}
		installed = component.Version
		if component.Release != nil {
			installed = component.Release.Version
		}
		if component.UpdateAvailable != nil {
			available = component.UpdateAvailable.Version
		}
	}
	return installed, available
}

// EvaluateStackKitsUpdate starts a node's pending update when its setting is
// automatic or the release carries a security patch for the node. It is
// idempotent and bounded: a release is claimed once per node in the worker
// row before the job is queued. A stack with another operation in flight, a
// locked service or a server under maintenance is left for a later
// evaluation, as the owner's Approve would be refused there. Nothing starts
// while the node's installed release, its use cases or the release notes are
// unknown. It returns the started job, if any.
func (o *Orchestrator) EvaluateStackKitsUpdate(ctx context.Context, tenantID, workerID string) (string, error) {
	store, err := o.stackKitsUpdateStore()
	if err != nil {
		return "", err
	}
	worker, err := o.ownedStackKitsWorker(ctx, tenantID, "", workerID)
	if err != nil {
		return "", err
	}
	installed, available := nodeStackKitsReleases(worker)
	if available == "" || strings.TrimSpace(worker.StackID) == "" || strings.TrimSpace(worker.OwnerSubjectID) == "" {
		return "", nil
	}
	if _, ok := stackkitrelease.ParseReleaseVersion(installed); !ok {
		return "", nil
	}
	proposal, resolved := o.stackKitsProposal(ctx, worker, installed, available)
	if !resolved {
		return "", nil
	}
	var started *controlplane.WorkerStackKitsUpdateAttempt
	if attempt, ok := controlplane.WorkerStackKitsUpdateAttemptOf(worker); ok {
		started = &attempt
	}
	var target *controlplane.WorkerStackKitsTarget
	if recorded, ok := controlplane.WorkerStackKitsTargetOf(worker); ok {
		target = &recorded
	}
	behavior := controlplane.WorkerStackKitsUpdateBehaviorOf(worker).Behavior
	start := stackkitsupdate.Decide(behavior, proposal, started, target)
	if start == stackkitsupdate.StartNone {
		return "", nil
	}
	if blocked, blockedErr := o.stackKitsUpdateBlocked(ctx, tenantID, worker.StackID, workerID); blockedErr != nil || blocked {
		return "", blockedErr
	}
	attempt := controlplane.WorkerStackKitsUpdateAttempt{Version: available, Trigger: string(start), StartedAt: time.Now().UTC()}
	if start == stackkitsupdate.StartSecurity {
		for _, change := range proposal.Security.Changes {
			if len(attempt.Patched) < 8 {
				attempt.Patched = append(attempt.Patched, controlplane.StackKitsPatchNote{App: change.Scope, Text: change.Text})
			}
		}
	}
	claimed, err := store.ClaimWorkerStackKitsUpdateAttempt(ctx, tenantID, workerID, attempt)
	if err != nil || !claimed {
		return "", err
	}
	// The same governed upgrade the owner's Approve starts: the Guard runs it
	// on the node's installed CLI, verifies it and rolls back on failure.
	jobID, err := o.EnqueueStackKitLifecycle(ctx, jobs.StackKitLifecycleRequest{
		StackID: worker.StackID, TenantID: tenantID, OwnerID: worker.OwnerSubjectID, AgentID: workerID,
		Operation: jobs.StackKitLifecycleUpgrade, OwnerApproved: true, TargetRelease: available,
	})
	if err != nil {
		if releaseErr := store.ReleaseWorkerStackKitsUpdateAttempt(ctx, tenantID, workerID, available); releaseErr != nil {
			o.log.Warn("stackkits_update_attempt_release_failed", "worker_id", workerID, "error", releaseErr.Error())
		}
		return "", fmt.Errorf("start the %s StackKits update: %w", start, err)
	}
	if err := store.RecordWorkerStackKitsUpdateAttemptJob(ctx, tenantID, workerID, available, jobID); err != nil {
		o.log.Warn("stackkits_update_attempt_job_unrecorded", "worker_id", workerID, "job_id", jobID, "error", err.Error())
	}
	o.log.Info("stackkits_update_started", "worker_id", workerID, "stack_id", worker.StackID, "release", available, "trigger", string(start), "job_id", jobID)
	return jobID, nil
}

// stackKitsUpdateBlocked reports what keeps an update from starting now: a
// queued, running or waiting operation on the stack, a locked service, or a
// server of the stack under maintenance. The owner's Approve route refuses the
// same; a state that cannot be read blocks.
func (o *Orchestrator) stackKitsUpdateBlocked(ctx context.Context, tenantID, stackID, agentID string) (bool, error) {
	if o.jobStore == nil {
		return true, ErrStackKitsUpdateUnavailable
	}
	recent, err := o.jobStore.ListJobsByStack(ctx, tenantID, stackID, 50)
	if err != nil {
		return true, err
	}
	for _, job := range recent {
		switch strings.ToLower(job.State) {
		case "pending", "queued", "running", "waiting", "provisioning":
			return true, nil
		}
	}
	candidates := []any{o.cfg.StackStore, o.stackStore, o.workerStore, o.jobStore}
	services, ok := firstStore[controlplane.ServiceRuntimeStore](candidates)
	if !ok {
		return true, ErrStackKitsUpdateUnavailable
	}
	rows, err := services.ListServiceRuntimes(ctx, tenantID, stackID, "")
	if err != nil {
		return true, err
	}
	for i := range rows {
		if rows[i].MutationLock.Locked() {
			return true, nil
		}
	}
	maintenance, ok := firstStore[controlplane.ServerMaintenanceStore](candidates)
	if !ok {
		return true, ErrStackKitsUpdateUnavailable
	}
	_, err = maintenance.ActiveServerMaintenanceJob(ctx, tenantID, controlplane.ServerMaintenanceScope{AgentID: agentID, StackID: stackID})
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	return true, err
}

func firstStore[T any](candidates []any) (T, bool) {
	for _, candidate := range candidates {
		if store, ok := candidate.(T); ok && candidate != nil {
			return store, true
		}
	}
	var zero T
	return zero, false
}
