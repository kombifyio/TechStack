package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// WorkerPosture is the kombify Guard posture service as the worker routes use
// it. Nil in editions without posture.
type WorkerPosture interface {
	Ingest(ctx context.Context, identity guardposture.Identity, displayName string, body []byte) (guardposture.Directive, error)
	ObserveGuard(ctx context.Context, identity guardposture.Identity, displayName string, obs guardposture.GuardObservation) error
	AcceptLocalOperation(ctx context.Context, identity guardposture.Identity, displayName string, generation int64, evidence guardposture.LocalOperationEvidence) (guardbinding.Acknowledgement, error)
	LocalRepairGrant(ctx context.Context, identity guardposture.Identity, generation int64) (*guardbinding.LocalRepairGrant, error)
}

// posturePollState is the answer a Guard receives where posture does not apply.
const posturePollDisabled = "disabled"

// workerPosture is the Guard's posture channel: a report of what it observed on
// the host, a check-in that only asks whether a fresh observation is wanted,
// or evidence of one repair the Guard admitted under a local policy. The
// identity is the authenticated worker's, never the body's; a report the
// control plane cannot read is refused and leaves no pass behind.
func (h workerRouteHandlers) workerPosture(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	body, readErr := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, guardposture.MaxReportBytes))
	if readErr != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	authCtx, ok := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	if h.posture == nil {
		return httpx.Success(e, http.StatusOK, map[string]any{"posture": map[string]any{"state": posturePollDisabled}})
	}
	identity := guardPostureIdentity(authCtx)
	displayName := guardPostureDisplayName(authCtx)
	var evidence guardposture.LocalOperationEvidence
	if json.Unmarshal(body, &evidence) == nil && evidence.OperationID != "" {
		ack, err := h.posture.AcceptLocalOperation(e.Request.Context(), identity, displayName, credentialGeneration(authCtx), evidence)
		if err != nil {
			var denial *guardbinding.Denial
			if errors.As(err, &denial) && denial != nil {
				return writeGuardDenial(e, denial)
			}
			slog.Warn("guard_local_operation_failed", "worker_id", identity.WorkerID, "operation_id", evidence.OperationID, "error", err.Error())
			httpx.RecordDependencyFailure(e, "workers.posture.local_operation", err)
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard posture is unavailable", nil)
		}
		return httpx.Success(e, http.StatusOK, ack)
	}
	directive, err := h.posture.Ingest(e.Request.Context(), identity, displayName, body)
	switch {
	case errors.Is(err, guardposture.ErrReportInvalid):
		return httpx.BadRequest(e, "Invalid posture report", nil)
	case err != nil:
		slog.Warn("guard_posture_ingest_failed", "worker_id", identity.WorkerID, "error", err.Error())
		httpx.RecordDependencyFailure(e, "workers.posture.ingest", err)
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard posture is unavailable", nil)
	}
	directive.LocalPolicy = h.localRepairGrant(e.Request.Context(), authCtx, identity)
	return httpx.Success(e, http.StatusOK, map[string]any{"posture": directive})
}

// localRepairGrant is the signed grant of the owner's active repair approval
// for this Guard, or nil. Every answer carries the current state: a Guard
// drops the grant it holds when an answer has none, so a withdrawal or expiry
// leaves it observe-only. Without a binding signing key no grant is issued,
// because a Guard never admits an unsigned one.
func (h workerRouteHandlers) localRepairGrant(ctx context.Context, authCtx runtimeAgentAuthContext, identity guardposture.Identity) *guardbinding.LocalRepairGrant {
	signer, err := h.bindingSignerFor(identity.TenantID, identity.WorkerID)
	if err != nil || signer == nil {
		return nil
	}
	grant, err := h.posture.LocalRepairGrant(ctx, identity, credentialGeneration(authCtx))
	if err != nil {
		slog.Warn("guard_local_repair_grant_failed", "worker_id", identity.WorkerID, "error", err.Error())
		return nil
	}
	if grant == nil {
		return nil
	}
	if err := signer.SignLocalGrant(grant); err != nil {
		slog.Warn("guard_local_repair_grant_sign_failed", "worker_id", identity.WorkerID, "error", err.Error())
		return nil
	}
	return grant
}

func guardPostureIdentity(authCtx runtimeAgentAuthContext) guardposture.Identity {
	return guardposture.Identity{
		TenantID: authCtx.TenantID, WorkerID: authCtx.RuntimeAgentID,
		ServerID:       firstNonEmpty(authCtx.ServerID, runtimeServerIDForWorker(authCtx.RuntimeAgentID)),
		OwnerSubjectID: authCtx.OwnerID,
	}
}

func guardPostureDisplayName(authCtx runtimeAgentAuthContext) string {
	if authCtx.Worker == nil {
		return ""
	}
	return strings.TrimSpace(authCtx.Worker.Hostname)
}

// observeGuardPosture records the Guard's own update and operation state from
// its heartbeat. It is best effort: posture must never fail a heartbeat, which
// owns liveness.
func (h workerRouteHandlers) observeGuardPosture(ctx context.Context, authCtx runtimeAgentAuthContext, req workerHeartbeatRequest) {
	if h.posture == nil || req.GuardPosture == nil || strings.TrimSpace(authCtx.OwnerID) == "" {
		return
	}
	posture := req.GuardPosture.Normalize()
	obs := guardposture.GuardObservation{
		Mode: posture.Mode, UpdateState: posture.UpdateState, UpdateVersion: posture.UpdateVersion,
		UpdateReason: posture.UpdateReason, DenyMutations: posture.DenyMutations,
	}
	if req.GuardJournal != nil && req.GuardJournal.Normalize().UnresolvedOutcomeUnknown > 0 {
		obs.NeedsOperator = h.outcomesNeedOperator(ctx, authCtx)
	}
	if err := h.posture.ObserveGuard(ctx, guardPostureIdentity(authCtx), guardPostureDisplayName(authCtx), obs); err != nil {
		slog.Warn("guard_posture_observe_failed", "worker_id", authCtx.RuntimeAgentID, "error", err.Error())
	}
}

// outcomesNeedOperator reports whether an unresolved outcome has waited past the
// reconciliation policy, the only state that asks a person to act.
func (h workerRouteHandlers) outcomesNeedOperator(ctx context.Context, authCtx runtimeAgentAuthContext) bool {
	control, ok := h.typedControl.(WorkerOutcomeControl)
	if !ok {
		return false
	}
	outcomes, err := control.ListUnresolvedOutcomes(ctx, authCtx.TenantID, authCtx.RuntimeAgentID)
	if err != nil {
		return false
	}
	for _, item := range outcomes {
		if item.State == agentcontrol.OutcomeNeedsOperator {
			return true
		}
	}
	return false
}
