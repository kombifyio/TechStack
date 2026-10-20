package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

type stackKitLifecycleRequest struct {
	AgentID       string `json:"agent_id"`
	Operation     string `json:"operation"`
	TargetRelease string `json:"target_release,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
	Offline       bool   `json:"offline,omitempty"`
	OwnerApproved bool   `json:"owner_approved,omitempty"`
	// StackKit names the local execution binding. When omitted, the kit the
	// stack's last managed rollout applied is used; the StackSpec path always
	// comes from that rollout.
	StackKit string `json:"stackkit,omitempty"`
	// SnapshotAnchorID names the snapshot backup_restore stages, or the
	// existing anchor a restore_drill drills.
	SnapshotAnchorID      string `json:"snapshot_anchor_id,omitempty"`
	RestoreResultID       string `json:"restore_result_id,omitempty"`
	ActivationOperationID string `json:"activation_operation_id,omitempty"`
	RestorePlanHash       string `json:"restore_plan_hash,omitempty"`
	// CandidateSpec is the proposed canonical StackSpec an
	// advanced_change_set creates and applies; an apply that carries one runs
	// as that change set (optional for drift_reconcile,
	// which otherwise reconciles to the workspace StackSpec).
	CandidateSpec json.RawMessage `json:"candidate_spec,omitempty"`
	// RollbackTargetRef is the executor-state snapshot id or change-set id a
	// rollback returns to.
	RollbackTargetRef string `json:"rollback_target_ref,omitempty"`
	// ai_provider: the provider's https base URL and the Wallet item holding
	// its API key, or ai_provider_disable to remove it.
	AIProviderBaseURL string `json:"ai_provider_base_url,omitempty"`
	WalletItemID      string `json:"wallet_item_id,omitempty"`
	WalletItemRotate  bool   `json:"wallet_item_rotate,omitempty"`
	AIProviderDisable bool   `json:"ai_provider_disable,omitempty"`
	// ssh_trust_recipient: the owner's fresh challenge the node answers;
	// ssh_trust_apply: the Wallet's sealed SSH delivery envelope (JSON text,
	// public material only). Refused for every other operation.
	RecipientChallenge string `json:"recipient_challenge,omitempty"`
	SealedDelivery     string `json:"sealed_delivery,omitempty"`
}

type stackKitLifecycleResponse struct {
	JobID           string `json:"job_id"`
	KitDeploymentID string `json:"kit_deployment_id"`
	AgentID         string `json:"agent_id"`
	Operation       string `json:"operation"`
	Status          string `json:"status"`
}

// stackLockGuardedOperations are the stack-scoped operations that would write
// to a service the owner has locked. A stack apply that silently overrode a
// service lock would make the lock a lie, so the whole operation is refused and
// the offending services are named. Read-only operations (plan, verify,
// drift_detect) stay available: an owner has to be able to inspect a stack that
// carries a lock.
var stackLockGuardedOperations = map[string]bool{
	jobs.StackKitLifecycleApply:          true,
	jobs.StackKitLifecycleDriftReconcile: true,
	jobs.StackKitLifecycleUpgrade:        true,
	// Advanced change sets and rollbacks rewrite the stack's services.
	jobs.StackKitLifecycleAdvancedChangeSet:     true,
	jobs.StackKitLifecycleRollback:              true,
	jobs.StackKitLifecycleBackupRestoreActivate: true,
	// ai_provider reconciles the stack with the new provider setting.
	jobs.StackKitLifecycleAIProvider: true,
}

// lockedStackServices returns the ids of the stack's locked services for an
// operation that would mutate them. It fails closed: if the service projection
// cannot be read, the operation is refused rather than run past an unknown
// guardrail.
func (h crudRouteHandlers) lockedStackServices(
	e *httpx.Event,
	tenantID, stackID, operation string,
) ([]string, error) {
	locked, err := h.lockedStackServicesContext(e.Request.Context(), tenantID, stackID, operation)
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service lock state is unavailable", nil)
	}
	return locked, nil
}

func (h crudRouteHandlers) lockedStackServicesContext(ctx context.Context, tenantID, stackID, operation string) ([]string, error) {
	if !stackLockGuardedOperations[operation] {
		return nil, nil
	}
	if h.serviceStore == nil {
		return nil, errors.New("service lock state is unavailable")
	}
	services, err := h.serviceStore.ListServiceRuntimes(ctx, tenantID, stackID, "")
	if err != nil {
		return nil, err
	}
	locked := make([]string, 0, len(services))
	for i := range services {
		if services[i].MutationLock.Locked() {
			locked = append(locked, services[i].ID)
		}
	}
	return locked, nil
}

// refuseDuringServerMaintenance keeps stack operations off a node that is
// being rebooted or updated: an active maintenance job on the requested agent
// or on any server of the stack refuses the operation. It is a read-only
// check and takes no lock; the maintenance runner's claim re-checks the node
// for stack work. It fails closed when the maintenance state cannot be read.
//
// A request without an agent id is bound to an agent later by the
// orchestrator, so maintenance on that agent is seen here only through the
// stack scope (the server's stack id).
func (h crudRouteHandlers) refuseDuringServerMaintenance(e *httpx.Event, tenantID, stackID, agentID string) error {
	active, err := h.activeStackMaintenanceContext(e.Request.Context(), tenantID, stackID, agentID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance state is unavailable", nil)
	}
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A server of this stack is under maintenance", map[string]interface{}{
		"reason": "maintenance_active", "job_id": active.ID, "server_id": active.ServerID,
	})
}

func (h crudRouteHandlers) activeStackMaintenanceContext(ctx context.Context, tenantID, stackID, agentID string) (*controlplane.ServerMaintenanceJob, error) {
	store, ok := h.serverStore.(controlplane.ServerMaintenanceStore)
	if !ok {
		return nil, errors.New("server maintenance state is unavailable")
	}
	return store.ActiveServerMaintenanceJob(ctx, tenantID, controlplane.ServerMaintenanceScope{AgentID: agentID, StackID: stackID})
}

func (h crudRouteHandlers) startStackKitLifecycle(e *httpx.Event) error {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return err
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stackkit.lifecycle")
	if tenantErr != nil {
		return tenantErr
	}
	if h.orch == nil {
		return httpx.Error(
			e,
			http.StatusServiceUnavailable,
			ksapi.ErrCodeUnavailable,
			"Typed StackKits lifecycle is unavailable",
			nil,
		)
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return httpx.BadRequest(e, "Stack is required")
	}
	var request stackKitLifecycleRequest
	if err := decodeStrictJSONBody(e.Request.Body, &request); err != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	input := jobs.StackKitLifecycleRequest{
		StackID:               stackID,
		TenantID:              tenantID,
		OwnerID:               ownerID,
		AgentID:               request.AgentID,
		Operation:             request.Operation,
		TargetRelease:         request.TargetRelease,
		DryRun:                request.DryRun,
		Offline:               request.Offline,
		OwnerApproved:         request.OwnerApproved,
		StackKit:              request.StackKit,
		SnapshotAnchorID:      request.SnapshotAnchorID,
		RestoreResultID:       request.RestoreResultID,
		ActivationOperationID: request.ActivationOperationID,
		RestorePlanHash:       request.RestorePlanHash,
		CandidateSpecJSON:     []byte(request.CandidateSpec),
		RollbackTargetRef:     request.RollbackTargetRef,
		AIProviderBaseURL:     request.AIProviderBaseURL,
		WalletItemID:          request.WalletItemID,
		WalletItemRotate:      request.WalletItemRotate,
		AIProviderDisable:     request.AIProviderDisable,
		RecipientChallenge:    request.RecipientChallenge,
		SealedDelivery:        []byte(request.SealedDelivery),
	}
	if (request.RecipientChallenge != "" || request.SealedDelivery != "") && !jobs.IsSSHTrustOperation(strings.ToLower(strings.TrimSpace(request.Operation))) {
		return httpx.BadRequest(e, "recipient_challenge and sealed_delivery belong to ssh_trust_recipient and ssh_trust_apply only")
	}
	// A changed StackSpec is applied as an Owner-approved Advanced change set
	// (every managed deployment is Advanced). The typed apply never reads a
	// candidate, so it must not be accepted alongside one and silently ignore it.
	if operation := strings.ToLower(strings.TrimSpace(input.Operation)); operation == jobs.StackKitLifecycleApply && len(input.CandidateSpecJSON) > 0 {
		input.Operation = jobs.StackKitLifecycleAdvancedChangeSet
	}
	if strings.ToLower(strings.TrimSpace(input.Operation)) == jobs.StackKitLifecycleBackupRestoreActivate {
		if len(e.Request.Header.Values("Idempotency-Key")) != 1 {
			return httpx.BadRequest(e, "Exactly one bounded Idempotency-Key is required for restore activation")
		}
		input, err = jobs.PrepareRestoreActivationIdempotency(input, e.Request.Header.Get("Idempotency-Key"))
		if err != nil {
			return httpx.BadRequest(e, err.Error())
		}
		if original, replayed, replayErr := h.orch.ReplayRestoreActivationRequest(e.Request.Context(), input); replayErr != nil {
			var conflict *orchestrator.RestoreActivationRequestConflict
			if errors.As(replayErr, &conflict) {
				details := map[string]interface{}{}
				if conflict.JobID != "" {
					details["job_id"] = conflict.JobID
					details["kit_deployment_id"] = conflict.StackID
				}
				return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, conflict.Error(), details)
			}
			if errors.Is(replayErr, controlplane.ErrNotFound) {
				return httpx.NotFound(e, "Stack not found")
			}
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Original restore acceptance is unavailable", nil)
		} else if replayed {
			return httpx.Success(e, http.StatusAccepted, stackKitLifecycleResponse{JobID: original, KitDeploymentID: stackID, AgentID: input.AgentID, Operation: input.Operation, Status: "queued"})
		}
	}
	normalized, err := jobs.NormalizeStackKitLifecycleRequest(input)
	if err != nil {
		return httpx.BadRequest(e, err.Error())
	}
	if locked, lockErr := h.lockedStackServices(e, tenantID, stackID, normalized.Operation); lockErr != nil {
		return lockErr
	} else if len(locked) > 0 {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Stack contains locked services", map[string]interface{}{
				"reason": "service_locked", "locked_service_ids": locked,
			})
	}
	if maintenanceErr := h.refuseDuringServerMaintenance(e, tenantID, stackID, normalized.AgentID); maintenanceErr != nil {
		return maintenanceErr
	}
	jobID, err := h.orch.EnqueueStackKitLifecycle(e.Request.Context(), normalized)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "Stack or agent not found")
		}
		var denied *orchestrator.ManagedBackupDeniedError
		if errors.As(err, &denied) {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "Managed restore drill backup admission denied", denied.Details)
		}
		if errors.Is(err, orchestrator.ErrStackKitBackupCustodyManaged) {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
				"Backups for this StackKit run through the managed backup schedule",
				map[string]interface{}{
					"reason_code": "managed_backup_custody",
					"retryable":   false,
					"user_guidance": map[string]interface{}{
						"title": "Backups for this stack are managed",
						"body":  "This StackKit keeps its backups in kombify-managed storage, so they run on the stack's backup schedule under your plan's backup entitlement.",
						"next_steps": []string{
							"Use the stack's backup schedule to run managed backups.",
						},
					},
				})
		}
		if errors.Is(err, orchestrator.ErrStackKitLifecycleUnavailable) {
			return httpx.Error(
				e,
				http.StatusServiceUnavailable,
				ksapi.ErrCodeUnavailable,
				"Typed StackKits lifecycle is unavailable",
				map[string]interface{}{"reason": err.Error()},
			)
		}
		return httpx.Error(
			e,
			http.StatusInternalServerError,
			ksapi.ErrCodeInternal,
			"Failed to enqueue typed StackKits lifecycle operation",
			nil,
		)
	}
	return httpx.Success(e, http.StatusAccepted, stackKitLifecycleResponse{
		JobID:           jobID,
		KitDeploymentID: stackID,
		AgentID:         normalized.AgentID,
		Operation:       normalized.Operation,
		Status:          "queued",
	})
}
