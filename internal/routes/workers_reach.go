package routes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

// workerReachMaxBody bounds one reach exchange: a host proof is at most 4 KiB.
const workerReachMaxBody = 16 << 10

// WorkerReach is the kombify Guard reach service as the worker routes use it.
type WorkerReach interface {
	Enroll(ctx context.Context, enrollment guardreach.Enrollment) error
	Exchange(ctx context.Context, tenantID, workerID string, request guardreach.ExchangeRequest) (*guardreach.Document, error)
}

// Reach document states a Guard receives when reach cannot apply to it.
const (
	reachStateDisabled     = "disabled"
	reachStateNeedsHostKey = "needs_host_key"
)

// enrollGuardReach records a key-bound Guard server for reach. It is best
// effort by design: a reach failure must never block or fail Guard enrollment
// or execution, so errors are logged and dropped.
func (h workerRouteHandlers) enrollGuardReach(ctx context.Context, authCtx runtimeAgentAuthContext, ownerRoles []string) {
	if h.reach == nil || authCtx.Worker == nil {
		return
	}
	placement := "byo"
	if strings.TrimSpace(authCtx.LeaseID) != "" {
		placement = "managed"
	}
	if err := h.reach.Enroll(ctx, guardreach.Enrollment{
		TenantID: authCtx.TenantID, WorkerID: authCtx.RuntimeAgentID, ServerID: authCtx.ServerID,
		StackID: authCtx.StackID, OwnerSubjectID: authCtx.OwnerID, DisplayName: authCtx.Worker.Hostname,
		Placement: placement, OwnerRoles: ownerRoles,
	}); err != nil {
		slog.Warn("guard_reach_enroll_failed", "runtime_agent_id", authCtx.RuntimeAgentID, "error", err.Error())
	}
}

// enrollGuardReachFromConnect records the server the owner just paired, with
// the owner's staff roles as Gateway's staff cohort gate needs them. The owner
// session is only present here, so this is where the roles are captured.
func (h workerRouteHandlers) enrollGuardReachFromConnect(e *httpx.Event, enrollment workerEnrollmentContext) {
	if h.reach == nil || strings.TrimSpace(enrollment.OwnerID) == "" {
		return
	}
	var roles []string
	connectTenant := ""
	if id := identity.FromContext(e.Request.Context()); id != nil {
		roles = id.Roles
		// Gateway scopes Connect by the owner's org id, else usr:<subject>; the
		// request that arrived through Gateway carries exactly that org id.
		if connectTenant = strings.TrimSpace(id.OrgID); connectTenant == "" && strings.TrimSpace(id.UserID) != "" {
			connectTenant = "usr:" + strings.TrimSpace(id.UserID)
		}
	}
	placement := "byo"
	if strings.TrimSpace(enrollment.LeaseID) != "" {
		placement = "managed"
	}
	if err := h.reach.Enroll(e.Request.Context(), guardreach.Enrollment{
		TenantID: enrollment.TenantID, WorkerID: firstNonEmpty(enrollment.RuntimeAgentID, enrollment.WorkerID),
		ServerID: enrollment.ServerID, StackID: enrollment.StackID, OwnerSubjectID: enrollment.OwnerID,
		Placement: placement, OwnerRoles: roles, ConnectTenantID: connectTenant,
	}); err != nil {
		slog.Warn("guard_reach_enroll_failed", "runtime_agent_id", enrollment.RuntimeAgentID, "error", err.Error())
	}
}

// workerReach is the Guard's reach exchange. A Guard with a bound host key
// reports its relay state and hands over any host proof the control plane
// requested; the answer says where and as whom to connect its relay. Legacy
// bearer Guards have no key to prove and receive needs_host_key.
func (h workerRouteHandlers) workerReach(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	var request guardreach.ExchangeRequest
	body, readErr := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, workerReachMaxBody))
	if readErr != nil || (len(body) > 0 && json.Unmarshal(body, &request) != nil) {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	authCtx, ok := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	if h.reach == nil {
		return httpx.Success(e, http.StatusOK, map[string]any{"reach": guardreach.Document{State: reachStateDisabled}})
	}
	if authCtx.IdentityBinding != controlplane.IdentityBindingHostKey {
		return httpx.Success(e, http.StatusOK, map[string]any{"reach": guardreach.Document{State: reachStateNeedsHostKey}})
	}
	document, err := h.reach.Exchange(e.Request.Context(), authCtx.TenantID, authCtx.RuntimeAgentID, request)
	if err == nil && document.State == guardreach.StateNotEnrolled {
		// A Guard paired before reach existed: record it once, then answer.
		h.enrollGuardReach(e.Request.Context(), authCtx, nil)
		document, err = h.reach.Exchange(e.Request.Context(), authCtx.TenantID, authCtx.RuntimeAgentID, request)
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard reach is unavailable", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"reach": document})
}
