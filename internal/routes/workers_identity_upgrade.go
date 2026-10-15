package routes

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// identityUpgradeStepUpMaxAge bounds the multi-factor sign-in behind an owner's
// approval of a legacy key bind: the approval decides which key may command the
// server, so it needs the same fresh step-up as releasing an unresolved outcome.
const identityUpgradeStepUpMaxAge = outcomeResolutionStepUpMaxAge

type identityUpgradeCandidate struct {
	KeyThumbprint string    `json:"key_thumbprint"`
	RequestedAt   time.Time `json:"requested_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// identityUpgradeView is what the owner sees of one server's identity upgrade.
type identityUpgradeView struct {
	WorkerID        string `json:"worker_id"`
	IdentityBinding string `json:"identity_binding"`
	// Code is guard_identity_upgrade_required while the server authenticates
	// with its bearer credential alone; it is empty once a key is bound.
	Code       string                    `json:"code,omitempty"`
	Pending    *identityUpgradeCandidate `json:"pending,omitempty"`
	ApprovedBy string                    `json:"approved_by,omitempty"`
	ApprovedAt *time.Time                `json:"approved_at,omitempty"`
	// Hostname names the server on an approval card; it is only set by the
	// owner's list of pending upgrades.
	Hostname string `json:"hostname,omitempty"`
}

func identityUpgradeViewOf(workerID string, identity *controlplane.WorkerIdentity) identityUpgradeView {
	view := identityUpgradeView{
		WorkerID: workerID, IdentityBinding: controlplane.IdentityBindingLegacyBearer, Code: guardidentity.CodeIdentityUpgradeRequired,
	}
	if identity == nil {
		return view
	}
	switch identity.State {
	case controlplane.WorkerIdentityUpgradePending:
		view.Pending = &identityUpgradeCandidate{
			KeyThumbprint: identity.Thumbprint, RequestedAt: identity.IssuedAt.UTC(),
			ExpiresAt: identity.IssuedAt.UTC().Add(controlplane.WorkerIdentityUpgradeWindow),
		}
	case controlplane.WorkerIdentityBound, controlplane.WorkerIdentityIssuedUnconfirmed:
		view.IdentityBinding, view.Code = controlplane.IdentityBindingHostKey, ""
		view.ApprovedBy, view.ApprovedAt = identity.ApprovedBy, identity.ApprovedAt
	}
	return view
}

// ownedWorkerForIdentity authenticates the owner and proves the worker is theirs
// inside their tenant; a foreign or missing worker reads the same.
func (h workerRouteHandlers) ownedWorkerForIdentity(e *httpx.Event, capability string) (ownerID, tenantID, workerID string, ok bool) {
	ownerID, authErr := requireWorkerOwner(e)
	if authErr != nil || ownerID == "" {
		return "", "", "", false
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
	if tenantErr != nil {
		return "", "", "", false
	}
	if h.identityStore == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard identity is unavailable", nil)
		return "", "", "", false
	}
	workerID = strings.TrimSpace(e.Request.PathValue("id"))
	worker, getErr := h.wst.GetWorker(e.Request.Context(), tenantID, workerID)
	switch {
	case errors.Is(getErr, controlplane.ErrNotFound) || (getErr == nil && (worker == nil || worker.OwnerSubjectID != ownerID)):
		_ = httpx.NotFound(e, "Worker not found")
		return "", "", "", false
	case getErr != nil:
		_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to read worker", nil)
		return "", "", "", false
	}
	return ownerID, tenantID, workerID, true
}

// identityUpgradeStatus shows the owner whether a server still authenticates with
// its bearer credential alone and which key, if any, awaits approval.
func (h workerRouteHandlers) identityUpgradeStatus(e *httpx.Event) error {
	_, tenantID, workerID, ok := h.ownedWorkerForIdentity(e, "techstack.workers.identity.upgrade.read")
	if !ok {
		return nil
	}
	identity, err := h.identityStore.GetWorkerIdentity(e.Request.Context(), tenantID, workerID)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard identity is unavailable", nil)
	}
	if errors.Is(err, controlplane.ErrNotFound) {
		identity = nil
	}
	return httpx.Success(e, http.StatusOK, identityUpgradeViewOf(workerID, identity))
}

type identityUpgradeApproveRequest struct {
	KeyThumbprint string `json:"key_thumbprint"`
}

// identityUpgradeApprove binds the pending key the owner names. The owner
// compares the fingerprint with the one the Guard logs on the server, so a key
// proposed by someone who merely holds a copy of the bearer credential is not
// bound by accident.
func (h workerRouteHandlers) identityUpgradeApprove(e *httpx.Event) error {
	ownerID, tenantID, workerID, ok := h.ownedWorkerForIdentity(e, "techstack.workers.identity.upgrade.approve")
	if !ok {
		return nil
	}
	if !entitlementGateOrHosted(h.entitlements).FreshMultiFactor(e.Request.Context(), time.Now().UTC(), identityUpgradeStepUpMaxAge) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "A fresh multi-factor sign-in is required to approve a server key", map[string]any{
			"reason": "identity_upgrade_step_up_required", "retryable": true,
			"guidance": "Sign in again with a second factor and repeat the request within five minutes.",
		})
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 4097))
	var request identityUpgradeApproveRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if readErr != nil || len(raw) > 4096 || decoder.Decode(&request) != nil || strings.TrimSpace(request.KeyThumbprint) == "" {
		return httpx.BadRequest(e, "key_thumbprint is required", nil)
	}
	identity, err := h.identityStore.ApproveWorkerIdentityUpgrade(e.Request.Context(), controlplane.WorkerIdentityUpgradeDecision{
		TenantID: tenantID, WorkerID: workerID, Thumbprint: request.KeyThumbprint, ActorID: ownerID, Now: time.Now().UTC(),
	})
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "No pending key with that fingerprint is waiting for approval", map[string]any{
			"reason": "identity_upgrade_not_pending", "retryable": false,
		})
	case err != nil:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to approve the server key", nil)
	}
	slog.Warn("guard_identity_upgrade_approved", "tenant_id", tenantID, "worker_id", workerID,
		"key_thumbprint", identity.Thumbprint, "actor", ownerID)
	return httpx.Success(e, http.StatusOK, identityUpgradeViewOf(workerID, identity))
}

// identityUpgradeReject discards a pending key. It is the owner's reset when the
// key is not the server's: a rejected key must ask again, and rotating the
// server credential ends the holder's access to the bind endpoint.
func (h workerRouteHandlers) identityUpgradeReject(e *httpx.Event) error {
	ownerID, tenantID, workerID, ok := h.ownedWorkerForIdentity(e, "techstack.workers.identity.upgrade.reject")
	if !ok {
		return nil
	}
	if err := h.identityStore.RejectWorkerIdentityUpgrade(e.Request.Context(), tenantID, workerID); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to reject the server key", nil)
	}
	slog.Warn("guard_identity_upgrade_rejected", "tenant_id", tenantID, "worker_id", workerID, "actor", ownerID)
	identity, err := h.identityStore.GetWorkerIdentity(e.Request.Context(), tenantID, workerID)
	if err != nil {
		identity = nil
	}
	return httpx.Success(e, http.StatusOK, identityUpgradeViewOf(workerID, identity))
}

// identityUpgradeList is the owner's one-step view of every server of theirs
// that waits for a key approval, so an approval card can be built without
// knowing the worker ids. Approving still goes through identityUpgradeApprove
// and its fresh second factor; a listing grants nothing.
func (h workerRouteHandlers) identityUpgradeList(e *httpx.Event) error {
	ownerID, authErr := requireWorkerOwner(e)
	if authErr != nil || ownerID == "" {
		return nil
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.workers.identity.upgrade.read")
	if tenantErr != nil {
		return nil
	}
	if h.identityStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard identity is unavailable", nil)
	}
	pending, err := h.identityStore.ListWorkerIdentityUpgrades(e.Request.Context(), tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard identity is unavailable", nil)
	}
	now := time.Now().UTC()
	upgrades := make([]identityUpgradeView, 0, len(pending))
	for index := range pending {
		identity := pending[index]
		if !identity.IssuedAt.UTC().Add(controlplane.WorkerIdentityUpgradeWindow).After(now) {
			continue
		}
		worker, getErr := h.wst.GetWorker(e.Request.Context(), tenantID, identity.WorkerID)
		if getErr != nil || worker == nil || worker.OwnerSubjectID != ownerID {
			continue
		}
		view := identityUpgradeViewOf(identity.WorkerID, &identity)
		view.Hostname = worker.Hostname
		upgrades = append(upgrades, view)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"upgrades": upgrades})
}
