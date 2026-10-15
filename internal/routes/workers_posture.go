package routes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// WorkerPosture is the kombify Guard posture service as the worker routes use
// it. Nil in editions without posture.
type WorkerPosture interface {
	Ingest(ctx context.Context, identity guardposture.Identity, displayName string, body []byte) (guardposture.Directive, error)
	ObserveGuard(ctx context.Context, identity guardposture.Identity, displayName string, obs guardposture.GuardObservation) error
}

// posturePollState is the answer a Guard receives where posture does not apply.
const posturePollDisabled = "disabled"

// workerPosture is the Guard's posture channel: a report of what it observed on
// the host, or a check-in that only asks whether a fresh observation is wanted.
// The identity is the authenticated worker's, never the body's; a report the
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
	directive, err := h.posture.Ingest(e.Request.Context(), identity, guardPostureDisplayName(authCtx), body)
	switch {
	case errors.Is(err, guardposture.ErrReportInvalid):
		return httpx.BadRequest(e, "Invalid posture report", nil)
	case err != nil:
		slog.Warn("guard_posture_ingest_failed", "worker_id", identity.WorkerID, "error", err.Error())
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard posture is unavailable", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"posture": directive})
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
