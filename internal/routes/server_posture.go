package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// serverPostureStepUpMaxAge bounds how old the multi-factor sign-in behind a
// repair, or behind the approval of automatic repair, may be. Both let the Guard
// change the host's firewall, ssh or update configuration.
const serverPostureStepUpMaxAge = 300 * time.Second

// Stable refusal reasons of the posture routes.
const (
	postureReasonStepUp      = "server_security_step_up_required"
	postureReasonNotReady    = "guard_not_connected"
	postureReasonUnavailable = "security_posture_unavailable"
)

// ServerPosture is the kombify Guard posture service as the server routes use
// it. Nil in editions that run no posture; the read model then carries none.
type ServerPosture interface {
	View(posture *guardposture.Posture) guardposture.View
	Views(ctx context.Context, tenantID string) (map[string]guardposture.View, error)
	RequestVerify(ctx context.Context, identity guardposture.Identity, displayName string) error
	Findings(ctx context.Context, tenantID, workerID string, limit int) ([]guardposture.FindingRecord, error)
	Repairs(ctx context.Context, tenantID, workerID string, limit int) ([]guardposture.Repair, error)
	RequestRepair(ctx context.Context, request guardposture.RepairRequest) (*guardposture.Repair, error)
	AutoRepair(ctx context.Context, tenantID, workerID string) (*guardposture.AutoRepairPolicy, error)
	SetAutoRepair(ctx context.Context, tenantID, workerID, actor string, enabled bool, controls []string, expiresAt time.Time) (*guardposture.AutoRepairPolicy, error)
}

// postureViews reads the tenant's posture views once. A read failure leaves the
// sections absent instead of failing the inventory, which never depends on
// posture.
func (h serverRuntimeHandlers) postureViews(ctx context.Context, tenantID string) map[string]guardposture.View {
	if h.posture == nil {
		return nil
	}
	views, err := h.posture.Views(ctx, tenantID)
	if err != nil {
		slog.Warn("guard_posture_read_failed", "error", err.Error())
		return nil
	}
	if views == nil {
		views = map[string]guardposture.View{}
	}
	return views
}

// applyPosture attaches the persisted posture to a server that has a Guard. A
// Guard that never reported is shown as unknown, never as secure.
func (h serverRuntimeHandlers) applyPosture(item *serverRuntimeResponse, workerID string, views map[string]guardposture.View) {
	if h.posture == nil || views == nil || strings.TrimSpace(workerID) == "" {
		return
	}
	view, ok := views[strings.TrimSpace(workerID)]
	if !ok {
		view = h.posture.View(nil)
	}
	item.Security, item.Recovery, item.Updates = &view.Security, &view.Recovery, &view.Updates
}

// postureServer authenticates the owner, proves the server is theirs and
// returns it with its Guard identity. A server of another tenant or owner is
// indistinguishable from a missing one.
func (h serverRuntimeHandlers) postureServer(e *httpx.Event, capability string, mutating bool) (*controlplane.ServerRuntime, guardposture.Identity, string, error) {
	if h.posture == nil {
		return nil, guardposture.Identity{}, "", httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Security posture is unavailable", map[string]any{"reason": postureReasonUnavailable})
	}
	server, err := h.ownedServer(e, capability)
	if err != nil || server == nil {
		return nil, guardposture.Identity{}, "", err
	}
	if strings.TrimSpace(server.WorkerID) == "" {
		return nil, guardposture.Identity{}, "", httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This server has no kombify Guard", map[string]any{"reason": postureReasonNotReady, "guidance": "Pair the server so a Guard reports its security posture."})
	}
	if mutating {
		ownerID, _, _ := authenticatedUser(e)
		if err := authorizeOwnerServerOperate(e.Request.Context(), h.policy, h.store, inventoryScope{tenantID: server.TenantID, ownerID: ownerID}, server.ID); err != nil {
			return nil, guardposture.Identity{}, "", writeInventoryHTTPError(e, err)
		}
	}
	identity := guardposture.Identity{TenantID: server.TenantID, WorkerID: strings.TrimSpace(server.WorkerID), ServerID: server.ID, OwnerSubjectID: server.OwnerSubjectID}
	return server, identity, firstNonEmpty(server.DisplayName, server.Name), nil
}

// postureStepUp requires the entitlement that meters maintaining one's own
// server and a fresh multi-factor sign-in, the same gate as releasing an
// unresolved outcome.
func (h serverRuntimeHandlers) postureStepUp(e *httpx.Event) error {
	gate := entitlementGateOrHosted(h.postureEntitlements)
	ctx := e.Request.Context()
	if !gate.SignedGrant(ctx, ServerMaintenanceEntitlement) {
		return writeEntitlementDenial(e, "techstack.servers.security.repair", ServerMaintenanceEntitlement)
	}
	if !gate.FreshMultiFactor(ctx, h.now().UTC(), serverPostureStepUpMaxAge) {
		// Reject, not Error: a helper's caller stops on a non-nil error only.
		return httpx.Reject(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "A fresh multi-factor sign-in is required to change the host's security configuration", map[string]any{
			"reason": postureReasonStepUp, "retryable": true,
			"guidance": "Sign in again with a second factor and repeat the request within five minutes.",
		})
	}
	return nil
}

type postureVerifyResponse struct {
	ServerID    string    `json:"server_id"`
	RequestedAt time.Time `json:"requested_at"`
}

// postureVerify asks the Guard to observe the host now. It changes nothing on
// the host and is answered the next time the Guard checks in.
func (h serverRuntimeHandlers) postureVerify(e *httpx.Event) error {
	server, identity, name, err := h.postureServer(e, "techstack.servers.security.verify", false)
	if err != nil || server == nil {
		return err
	}
	if err := h.posture.RequestVerify(e.Request.Context(), identity, name); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Security posture is unavailable", nil)
	}
	return httpx.Success(e, http.StatusAccepted, postureVerifyResponse{ServerID: server.ID, RequestedAt: h.now().UTC()})
}

type postureFindingResponse struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Scope      string     `json:"scope"`
	Severity   string     `json:"severity"`
	State      string     `json:"state"`
	Expected   string     `json:"expected"`
	Observed   string     `json:"observed"`
	ReasonCode string     `json:"reason_code,omitempty"`
	Capability string     `json:"remediation_capability,omitempty"`
	OpenedAt   time.Time  `json:"opened_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	AlertedAt  *time.Time `json:"alerted_at,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// postureFindings lists the findings ledger of one server: the open findings
// and the recent history, which are the same persisted rows the read model and
// the alerts come from.
func (h serverRuntimeHandlers) postureFindings(e *httpx.Event) error {
	server, identity, _, err := h.postureServer(e, "techstack.servers.security.findings", false)
	if err != nil || server == nil {
		return err
	}
	records, err := h.posture.Findings(e.Request.Context(), identity.TenantID, identity.WorkerID, 200)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Security findings are unavailable", nil)
	}
	open, history := []postureFindingResponse{}, []postureFindingResponse{}
	for _, record := range records {
		item := postureFindingResponse{
			ID: record.ID, Key: record.Key, Scope: record.Scope, Severity: record.Severity, State: record.State,
			Expected: record.Expected, Observed: record.Observed, ReasonCode: record.ReasonCode, Capability: record.Capability,
			OpenedAt: record.OpenedAt, LastSeenAt: record.LastSeenAt, AlertedAt: record.AlertedAt, ResolvedAt: record.ResolvedAt,
		}
		if record.ResolvedAt == nil {
			open = append(open, item)
		} else {
			history = append(history, item)
		}
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{"server_id": server.ID, "open": open, "history": history})
}

func (h serverRuntimeHandlers) postureRepairs(e *httpx.Event) error {
	server, identity, _, err := h.postureServer(e, "techstack.servers.security.repairs", false)
	if err != nil || server == nil {
		return err
	}
	repairs, err := h.posture.Repairs(e.Request.Context(), identity.TenantID, identity.WorkerID, 50)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Security repairs are unavailable", nil)
	}
	if repairs == nil {
		repairs = []guardposture.Repair{}
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{"server_id": server.ID, "repairs": repairs})
}

var postureRepairFields = map[string]bool{"controls": true}

type postureRepairRequest struct {
	Controls []string `json:"controls"`
}

// postureRequestRepair admits a repair of named drifted controls. Every bound is
// enforced where the decision is made, so a retry, a double click or a second
// operator cannot step past one.
func (h serverRuntimeHandlers) postureRequestRepair(e *httpx.Event) error {
	server, identity, name, err := h.postureServer(e, "techstack.servers.security.repair", true)
	if err != nil || server == nil {
		return err
	}
	if !serverregistry.MutationsAllowed(server.ConnectionState) || server.LifecycleState != string(serverregistry.LifecycleActive) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The Guard is not connected", map[string]any{
			"reason": postureReasonNotReady, "guidance": "Wait until the server is connected, then repair again."})
	}
	if err := h.postureStepUp(e); err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 4097))
	if readErr != nil || len(raw) > 4096 || !exactJSONObjectKeys(raw, postureRepairFields) {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	var request postureRepairRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	ownerID, _, _ := authenticatedUser(e)
	repair, repairErr := h.posture.RequestRepair(e.Request.Context(), guardposture.RepairRequest{
		Identity: identity, DisplayName: name, Controls: request.Controls, Mode: guardposture.RepairOperator, Actor: ownerID,
	})
	if repairErr != nil {
		return writePostureRepairError(e, repairErr, h.now().UTC())
	}
	slog.Warn("guard_security_repair_requested", "tenant_id", identity.TenantID, "worker_id", identity.WorkerID, "repair_id", repair.ID,
		"controls", strings.Join(repair.Controls, ","), "actor", ownerID)
	return httpx.Success(e, http.StatusAccepted, repair)
}

// writePostureRepairError maps a structured denial to a response the caller can
// act on: a state problem is a conflict, a bound is a rate limit with a retry
// time.
func writePostureRepairError(e *httpx.Event, err error, now time.Time) error {
	denial, ok := guardposture.IsRepairDenial(err)
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Security repair is unavailable", nil)
	}
	status := http.StatusConflict
	switch denial.Code {
	case guardposture.DenyCooldown, guardposture.DenyAttemptsExhausted, guardposture.DenyCircuitOpen:
		status = http.StatusTooManyRequests
	case guardposture.DenyUnavailable:
		status = http.StatusServiceUnavailable
	case guardposture.DenyControlUnknown:
		status = http.StatusBadRequest
	}
	details := map[string]any{"reason": denial.Code, "guidance": denial.Guidance}
	if denial.Control != "" {
		details["control"] = denial.Control
	}
	if denial.RetryAfter != nil {
		details["retry_after"] = denial.RetryAfter.UTC().Format(time.RFC3339)
		if seconds := int(denial.RetryAfter.Sub(now).Seconds()) + 1; seconds > 0 {
			e.Response.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
	}
	return httpx.Error(e, status, ksapi.ErrCodeConflict, denial.Message, details)
}

func (h serverRuntimeHandlers) postureAutoRepair(e *httpx.Event) error {
	server, identity, _, err := h.postureServer(e, "techstack.servers.security.auto-repair", false)
	if err != nil || server == nil {
		return err
	}
	policy, err := h.posture.AutoRepair(e.Request.Context(), identity.TenantID, identity.WorkerID)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Automatic repair settings are unavailable", nil)
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, autoRepairResponse(server.ID, policy, h.now().UTC()))
}

var postureAutoRepairFields = map[string]bool{"enabled": true, "controls": true, "expires_at": true}

type postureAutoRepairRequest struct {
	Enabled   bool      `json:"enabled"`
	Controls  []string  `json:"controls"`
	ExpiresAt time.Time `json:"expires_at"`
}

// postureSetAutoRepair records the owner's approval of automatic repair for a
// small safe set of controls, or withdraws it. The approval expires and never
// renews itself.
func (h serverRuntimeHandlers) postureSetAutoRepair(e *httpx.Event) error {
	server, identity, _, err := h.postureServer(e, "techstack.servers.security.auto-repair.set", true)
	if err != nil || server == nil {
		return err
	}
	if err := h.postureStepUp(e); err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 4097))
	if readErr != nil || len(raw) > 4096 {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	var request postureAutoRepairRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	if request.Enabled && !exactJSONObjectKeys(raw, postureAutoRepairFields) {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	ownerID, _, _ := authenticatedUser(e)
	policy, setErr := h.posture.SetAutoRepair(e.Request.Context(), identity.TenantID, identity.WorkerID, ownerID, request.Enabled, request.Controls, request.ExpiresAt)
	if setErr != nil {
		if _, isDenial := guardposture.IsRepairDenial(setErr); isDenial || errors.Is(setErr, guardposture.ErrAutoRepairInvalid) {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, setErr.Error(), map[string]any{"reason": "auto_repair_invalid", "allowed_controls": guardposture.AutoRepairSafeControls})
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Automatic repair settings are unavailable", nil)
	}
	slog.Warn("guard_auto_repair_policy_set", "tenant_id", identity.TenantID, "worker_id", identity.WorkerID, "enabled", request.Enabled, "actor", ownerID)
	return httpx.Success(e, http.StatusOK, autoRepairResponse(server.ID, policy, h.now().UTC()))
}

func autoRepairResponse(serverID string, policy *guardposture.AutoRepairPolicy, now time.Time) map[string]any {
	controls := policy.Controls
	if controls == nil {
		controls = []string{}
	}
	allowed := append([]string(nil), guardposture.AutoRepairSafeControls...)
	sort.Strings(allowed)
	return map[string]any{
		"server_id": serverID, "enabled": policy.Enabled, "active": policy.Active(now), "controls": controls,
		"expires_at": policy.ExpiresAt, "approved_by": policy.ApprovedBy, "approved_at": policy.ApprovedAt, "allowed_controls": allowed,
	}
}
