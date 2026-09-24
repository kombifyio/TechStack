// Stack and worker projection cleanup endpoint.
package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

const (
	stackPruneModeDryRun = "dry_run"
	stackPruneModeApply  = "apply"
	stackPruneMaxBody    = 4096
	workerOrphanAge      = 24 * time.Hour
)

var (
	e2eProjectionNamePattern = regexp.MustCompile(`^e2e-[a-z0-9][a-z0-9._-]{0,126}$`)
	runtimeCloudNamePattern  = regexp.MustCompile(`^runtime-cloud-(ionos|centron)-([0-9]{14})$`)
)

type stackPruneRequest struct {
	Mode    string `json:"mode"`
	Digest  string `json:"digest,omitempty"`
	StackID string `json:"stack_id,omitempty"`
}

type stackPruneCandidate struct {
	ResourceType string `json:"resource_type"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Class        string `json:"class"`
	Reason       string `json:"reason"`
	revision     string
}

type stackPruneApplied struct {
	Stacks  int `json:"stacks"`
	Workers int `json:"workers"`
	Total   int `json:"total"`
}

// stackPruneResponse is returned by both phases. Dry-run returns the exact
// candidate set and digest; apply echoes that set and reports projection-only
// mutations after revalidating the digest.
type stackPruneResponse struct {
	Mode       string                `json:"mode"`
	Message    string                `json:"message"`
	Digest     string                `json:"digest"`
	Candidates []stackPruneCandidate `json:"candidates"`
	Applied    stackPruneApplied     `json:"applied"`
	Warnings   []string              `json:"warnings,omitempty"`
}

type stackPrunePlan struct {
	response stackPruneResponse
	command  controlplane.OrphanProjectionCleanup
}

// pruneOrphanStacks is a two-phase product boundary for control-plane residue.
// It never invokes provider adapters, decommissioners, runtimes, jobs, or
// reconciliation. Apply accepts only the digest of a freshly recomputed plan.
func (h stackLifecycleRouteHandlers) pruneOrphanStacks(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.stacks.prune")
	if tenantErr != nil {
		return tenantErr
	}

	var request stackPruneRequest
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, stackPruneMaxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Cleanup mode and request body are required", map[string]any{"reason_code": "cleanup_request_invalid"})
	}
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	request.Digest = strings.TrimSpace(request.Digest)
	request.StackID = strings.TrimSpace(request.StackID)
	if request.Mode != stackPruneModeDryRun && request.Mode != stackPruneModeApply {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Cleanup mode must be dry_run or apply", map[string]any{"reason_code": "cleanup_mode_invalid"})
	}
	if request.Mode == stackPruneModeApply && request.Digest == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Apply requires the exact dry-run digest", map[string]any{"reason_code": "cleanup_digest_required"})
	}

	plan, planErr := h.planOrphanProjectionCleanup(e.Request.Context(), tenantID, ownerID, request.StackID)
	if planErr != nil {
		return h.stackPruneError(e, planErr)
	}
	if request.Mode == stackPruneModeDryRun {
		plan.response.Mode = stackPruneModeDryRun
		plan.response.Message = "Control-plane cleanup plan ready"
		return httpx.Success(e, http.StatusOK, plan.response)
	}
	if request.Digest != plan.response.Digest {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Cleanup inventory changed; review a new dry-run plan", map[string]any{
			"reason_code":    "cleanup_plan_drift",
			"current_digest": plan.response.Digest,
		})
	}

	cleanupStore, ok := h.orphanProjectionStore()
	if !ok {
		return h.stackPruneError(e, errProjectionCleanupStoreUnavailable)
	}
	if err := cleanupStore.ApplyOrphanProjectionCleanup(e.Request.Context(), plan.command); err != nil {
		if errors.Is(err, controlplane.ErrConflict) || errors.Is(err, controlplane.ErrNotFound) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Cleanup inventory changed; review a new dry-run plan", map[string]any{"reason_code": "cleanup_apply_drift"})
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Control-plane cleanup could not be applied", nil)
	}
	plan.response.Mode = stackPruneModeApply
	plan.response.Message = "Control-plane cleanup applied"
	for _, candidate := range plan.response.Candidates {
		switch candidate.ResourceType {
		case "stack_projection":
			plan.response.Applied.Stacks++
		case "worker_projection":
			plan.response.Applied.Workers++
		}
	}
	plan.response.Applied.Total = plan.response.Applied.Stacks + plan.response.Applied.Workers
	return httpx.Success(e, http.StatusOK, plan.response)
}

var errProjectionCleanupStoreUnavailable = errors.New("projection cleanup store unavailable")

func (h stackLifecycleRouteHandlers) stackPruneError(e *httpx.Event, err error) error {
	if errors.Is(err, errProjectionCleanupStoreUnavailable) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Cleanup authority is not available", map[string]any{
			"reason_code": "cleanup_authority_unavailable", "retryable": true,
		})
	}
	return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Cleanup inventory could not be verified", nil)
}

func (h stackLifecycleRouteHandlers) orphanProjectionStore() (controlplane.OrphanProjectionStore, bool) {
	if store, ok := h.stacks.(controlplane.OrphanProjectionStore); ok && store != nil {
		return store, true
	}
	store, ok := h.workers.(controlplane.OrphanProjectionStore)
	return store, ok && store != nil
}

func (h stackLifecycleRouteHandlers) planOrphanProjectionCleanup(ctx context.Context, tenantID, ownerID, targetID string) (stackPrunePlan, error) {
	stacks, workers, attachedLeaseStacks, danglingLeases, err := h.loadOrphanProjectionInventory(ctx, tenantID)
	if err != nil {
		return stackPrunePlan{}, err
	}
	now := time.Now().UTC()
	if h.now != nil {
		now = h.now().UTC()
	}
	staleBefore := now.Add(-workerOrphanAge)
	activeStacks, recentWorkerStacks := indexOrphanProjectionInventory(stacks, workers, staleBefore)
	plan := stackPrunePlan{
		response: stackPruneResponse{Candidates: make([]stackPruneCandidate, 0)},
		command: controlplane.OrphanProjectionCleanup{
			TenantID: tenantID, OwnerSubjectID: ownerID, StaleBefore: staleBefore, AppliedAt: now,
		},
	}
	if danglingLeases > 0 {
		plan.response.Warnings = append(plan.response.Warnings, fmt.Sprintf("%d lease(s) without a deployment link remain protected", danglingLeases))
	}
	appendOrphanStackCandidates(&plan, stacks, ownerID, targetID, attachedLeaseStacks, recentWorkerStacks)
	appendOrphanWorkerCandidates(&plan, workers, ownerID, targetID, staleBefore, activeStacks, attachedLeaseStacks)
	finalizeOrphanProjectionPlan(&plan, tenantID, ownerID, targetID)
	return plan, nil
}

func (h stackLifecycleRouteHandlers) loadOrphanProjectionInventory(
	ctx context.Context,
	tenantID string,
) ([]controlplane.Stack, []controlplane.Worker, map[string]bool, int, error) {
	if h.stacks == nil || h.workers == nil || h.leases == nil {
		return nil, nil, nil, 0, errProjectionCleanupStoreUnavailable
	}
	if _, ok := h.orphanProjectionStore(); !ok {
		return nil, nil, nil, 0, errProjectionCleanupStoreUnavailable
	}
	stacks, err := h.stacks.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	workers, err := h.workers.ListWorkersByTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	attachedLeaseStacks, danglingLeases, err := h.attachedLeaseStackIDs(ctx, tenantID)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return stacks, workers, attachedLeaseStacks, danglingLeases, nil
}

func indexOrphanProjectionInventory(
	stacks []controlplane.Stack,
	workers []controlplane.Worker,
	staleBefore time.Time,
) (map[string]controlplane.Stack, map[string]bool) {
	activeStacks := make(map[string]controlplane.Stack, len(stacks))
	recentWorkerStacks := make(map[string]bool)
	for _, stack := range stacks {
		activeStacks[stack.ID] = stack
	}
	for _, worker := range workers {
		if worker.LastSeenAt == nil || !worker.LastSeenAt.Before(staleBefore) {
			recentWorkerStacks[strings.TrimSpace(worker.StackID)] = true
		}
	}
	return activeStacks, recentWorkerStacks
}

func appendOrphanStackCandidates(
	plan *stackPrunePlan,
	stacks []controlplane.Stack,
	ownerID string,
	targetID string,
	attachedLeaseStacks map[string]bool,
	recentWorkerStacks map[string]bool,
) {
	for _, stack := range stacks {
		if targetID != "" && stack.ID != targetID {
			continue
		}
		class, reason, eligible := orphanProjectionIdentity(stack.Name)
		if !eligible || strings.TrimSpace(stack.OwnerSubjectID) != strings.TrimSpace(ownerID) || stackIsDemoAnchor(stack) || attachedLeaseStacks[stack.ID] || recentWorkerStacks[stack.ID] {
			continue
		}
		plan.response.Candidates = append(plan.response.Candidates, stackPruneCandidate{
			ResourceType: "stack_projection", ID: stack.ID, Name: stack.Name, Class: class + "_stack", Reason: reason + "_without_live_lease_or_recent_worker", revision: stack.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
		plan.command.Stacks = append(plan.command.Stacks, controlplane.OrphanStackProjection{ID: stack.ID, Name: stack.Name, UpdatedAt: stack.UpdatedAt})
	}
}

func appendOrphanWorkerCandidates(
	plan *stackPrunePlan,
	workers []controlplane.Worker,
	ownerID string,
	targetID string,
	staleBefore time.Time,
	activeStacks map[string]controlplane.Stack,
	attachedLeaseStacks map[string]bool,
) {
	for _, worker := range workers {
		if targetID != "" && worker.ID != targetID && worker.StackID != targetID {
			continue
		}
		class, reason, eligible := orphanProjectionIdentity(worker.Hostname)
		_, stackActive := activeStacks[strings.TrimSpace(worker.StackID)]
		if !eligible || strings.TrimSpace(worker.OwnerSubjectID) != strings.TrimSpace(ownerID) || stackActive || attachedLeaseStacks[strings.TrimSpace(worker.StackID)] || worker.LastSeenAt == nil || !worker.LastSeenAt.Before(staleBefore) {
			continue
		}
		plan.response.Candidates = append(plan.response.Candidates, stackPruneCandidate{
			ResourceType: "worker_projection", ID: worker.ID, Name: worker.Hostname, Class: class + "_worker", Reason: reason + "_stale_worker_without_active_stack_or_live_lease", revision: worker.UpdatedAt.UTC().Format(time.RFC3339Nano) + "/" + worker.LastSeenAt.UTC().Format(time.RFC3339Nano),
		})
		plan.command.Workers = append(plan.command.Workers, controlplane.OrphanWorkerProjection{
			ID: worker.ID, StackID: worker.StackID, Hostname: worker.Hostname, LastSeenAt: *worker.LastSeenAt, UpdatedAt: worker.UpdatedAt,
		})
	}
}

func finalizeOrphanProjectionPlan(plan *stackPrunePlan, tenantID, ownerID, targetID string) {
	sort.Slice(plan.response.Candidates, func(i, j int) bool {
		if plan.response.Candidates[i].ResourceType == plan.response.Candidates[j].ResourceType {
			return plan.response.Candidates[i].ID < plan.response.Candidates[j].ID
		}
		return plan.response.Candidates[i].ResourceType < plan.response.Candidates[j].ResourceType
	})
	sort.Slice(plan.command.Stacks, func(i, j int) bool { return plan.command.Stacks[i].ID < plan.command.Stacks[j].ID })
	sort.Slice(plan.command.Workers, func(i, j int) bool { return plan.command.Workers[i].ID < plan.command.Workers[j].ID })
	plan.response.Digest = stackPrunePlanDigest(tenantID, ownerID, targetID, plan.response.Candidates)
}

func orphanProjectionIdentity(name string) (class, reason string, eligible bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(normalized, "demo") || strings.Contains(normalized, "protected") {
		return "", "", false
	}
	if e2eProjectionNamePattern.MatchString(normalized) {
		return "e2e_test", "owned_e2e_projection", true
	}
	if match := runtimeCloudNamePattern.FindStringSubmatch(normalized); len(match) == 3 {
		if _, err := time.Parse("20060102150405", match[2]); err != nil {
			return "", "", false
		}
		return "runtime_cloud_test", "owned_runtime_cloud_projection", true
	}
	return "", "", false
}

func stackPrunePlanDigest(tenantID, ownerID, targetID string, candidates []stackPruneCandidate) string {
	type digestCandidate struct {
		ResourceType string `json:"resource_type"`
		ID           string `json:"id"`
		Name         string `json:"name"`
		Class        string `json:"class"`
		Reason       string `json:"reason"`
		Revision     string `json:"revision"`
	}
	payload := struct {
		Schema     string            `json:"schema"`
		TenantID   string            `json:"tenant_id"`
		OwnerID    string            `json:"owner_id"`
		TargetID   string            `json:"target_id,omitempty"`
		Candidates []digestCandidate `json:"candidates"`
	}{Schema: "techstack.orphan-projection-cleanup/v1", TenantID: tenantID, OwnerID: ownerID, TargetID: targetID, Candidates: make([]digestCandidate, 0, len(candidates))}
	for _, candidate := range candidates {
		payload.Candidates = append(payload.Candidates, digestCandidate{
			ResourceType: candidate.ResourceType, ID: candidate.ID, Name: candidate.Name, Class: candidate.Class, Reason: candidate.Reason, Revision: candidate.revision,
		})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// stackIsDemoAnchor protects the canonical shared public-demo asset regardless
// of a test-like display name.
func stackIsDemoAnchor(stack controlplane.Stack) bool {
	for _, values := range []map[string]any{stack.RuntimeSummary, stack.Config} {
		if flag, ok := values["demo_anchor"].(bool); ok && flag {
			return true
		}
	}
	return false
}

// attachedLeaseStackIDs enumerates attachment once for the complete tenant.
// Canceled managed leases remain attached until provider absence is proven by
// their owning lifecycle; this endpoint never attempts to create that proof.
func (h stackLifecycleRouteHandlers) attachedLeaseStackIDs(ctx context.Context, tenantID string) (map[string]bool, int, error) {
	out := map[string]bool{}
	leases, err := h.leases.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, 0, err
	}
	dangling := 0
	for _, lease := range leases {
		if lease.CancelledAt != nil && !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) {
			continue
		}
		if stackID := strings.TrimSpace(lease.Metadata["stack_id"]); stackID != "" {
			out[stackID] = true
		} else {
			dangling++
		}
	}
	return out, dangling, nil
}
