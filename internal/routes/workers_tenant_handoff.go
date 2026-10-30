package routes

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Cross-tenant handoff (Guard G04, guardbinding/handoff.go). The prior owner
// confirms that the command evidence a detached or retired server still holds
// may leave it; the rebind of that server to another tenant presents the
// server's removal receipt and consumes the confirmation once. A rebind that
// declares another tenant's held payloads, or a receipt the prior tenant never
// confirmed, is refused before the pairing capability is claimed.

const tenantHandoffStepUpMaxAge = outcomeResolutionStepUpMaxAge

type tenantHandoffConfirmRequest struct {
	Disposition string `json:"disposition"`
}

type tenantHandoffConfirmResponse struct {
	HandoffID   string    `json:"handoff_id"`
	WorkerID    string    `json:"worker_id"`
	Disposition string    `json:"disposition"`
	ExpiresAt   time.Time `json:"expires_at"`
	// Confirmation is shown once. The server operator passes it to
	// `techstack agent handoff --confirmation-file`.
	Confirmation string `json:"confirmation"`
}

// confirmTenantHandoff is the custodian's confirmation for one retired worker.
func (h workerRouteHandlers) confirmTenantHandoff(e *httpx.Event) error {
	ownerID, tenantID, workerID, ok := h.ownedWorkerForIdentity(e, "techstack.workers.tenant_handoff.confirm")
	if !ok {
		return nil
	}
	store, ok := h.identityStore.(controlplane.GuardTenantHandoffStore)
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard handoff is unavailable", nil)
	}
	now := time.Now().UTC()
	if !entitlementGateOrHosted(h.entitlements).FreshMultiFactor(e.Request.Context(), now, tenantHandoffStepUpMaxAge) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "A fresh multi-factor sign-in is required to release a server's command evidence", map[string]any{
			"reason": "tenant_handoff_step_up_required", "retryable": true,
			"guidance": "Sign in again with a second factor and repeat the request within five minutes.",
		})
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 1025))
	var request tenantHandoffConfirmRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if readErr != nil || len(raw) > 1024 || decoder.Decode(&request) != nil || !guardbinding.ValidHandoffDisposition(request.Disposition) {
		return httpx.BadRequest(e, "disposition must be export or quarantine", nil)
	}
	worker, err := h.wst.GetWorker(e.Request.Context(), tenantID, workerID)
	switch {
	case errors.Is(err, controlplane.ErrNotFound) || (err == nil && worker == nil):
		return httpx.NotFound(e, "Worker not found")
	case err != nil:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to read worker", nil)
	}
	// The confirmation is bound to the host key the worker used: only a pairing
	// that proves that key can consume it.
	identity, identityErr := h.identityStore.GetWorkerIdentity(e.Request.Context(), tenantID, workerID)
	switch {
	case errors.Is(identityErr, controlplane.ErrNotFound) || (identityErr == nil && (identity == nil || strings.TrimSpace(identity.Thumbprint) == "")):
		// Without a host key nothing binds the confirmation to this server.
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This server never bound a host key, so its command evidence cannot be handed off to a specific server", map[string]any{
			"reason": "tenant_handoff_unbindable",
		})
	case identityErr != nil:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard identity is unavailable", nil)
	}
	keyThumbprint := identity.Thumbprint
	// Only a server that can no longer act for this tenant may hand its
	// evidence off: a live worker still delivers it itself.
	if !strings.EqualFold(worker.Status, workerStatusRevoked) && !strings.EqualFold(worker.Status, managedRuntimeStatusRejected) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Detach or retire the server before handing off its command evidence", map[string]any{
			"reason": "tenant_handoff_worker_active",
		})
	}
	confirmation, err := guardbinding.NewHandoffConfirmation(tenantID, request.Disposition, now)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create the handoff confirmation", nil)
	}
	if err := store.ConfirmGuardTenantHandoff(e.Request.Context(), controlplane.GuardTenantHandoff{
		TenantID: tenantID, HandoffID: confirmation.HandoffID, WorkerID: workerID,
		ServerID: strings.TrimSpace(stringFromAny(worker.Capabilities[workerFieldServerID])), Disposition: confirmation.Disposition,
		ConfirmationSHA256: guardbinding.ConfirmationHash(confirmation.Code), ConfirmedBy: ownerID,
		ConfirmedAt: now, ExpiresAt: confirmation.ExpiresAt, KeyThumbprint: keyThumbprint,
	}); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to record the handoff confirmation", nil)
	}
	slog.Warn("guard_tenant_handoff_confirmed", "tenant_id", tenantID, "worker_id", workerID, "handoff_id", confirmation.HandoffID,
		"disposition", confirmation.Disposition, "confirmed_by", ownerID)
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusCreated, tenantHandoffConfirmResponse{
		HandoffID: confirmation.HandoffID, WorkerID: workerID, Disposition: confirmation.Disposition,
		ExpiresAt: confirmation.ExpiresAt, Confirmation: confirmation.Token(),
	})
}

// admitPriorCustody decides a pairing against the tenant its capability binds
// to. The control plane's own record comes first: every other tenant whose
// worker already used the host key this pairing proved must be covered by a
// receipt, whatever the Guard declares (a missing declaration or receipts file
// passes nothing the control plane knows). The Guard's declaration adds the
// tenants it still holds. It writes the refusal and returns false when the
// binding must not be issued; otherwise it returns the receipts whose
// confirmations consumeHandoffReceipts must take.
func (h workerRouteHandlers) admitPriorCustody(e *httpx.Event, declaration *guardbinding.PriorCustody, tenantID, keyThumbprint string) ([]guardbinding.TenantHandoffReceipt, bool) {
	store, hasStore := h.identityStore.(controlplane.GuardTenantHandoffStore)
	if keyThumbprint != "" && !hasStore {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The server's prior custody cannot be verified; it was not paired", nil)
		return nil, false
	}
	var custody []string
	if keyThumbprint != "" {
		var err error
		if custody, err = store.GuardKeyCustodyTenants(e.Request.Context(), keyThumbprint, tenantID); err != nil {
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The server's prior custody cannot be verified; it was not paired", nil)
			return nil, false
		}
	}
	receipts, denial := declaration.AdmitRebind(tenantID, custody, keyThumbprint != "")
	if denial != nil {
		_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, denial.Message, map[string]any{"reason": denial.Code})
		return nil, false
	}
	if len(receipts) > 0 && !hasStore {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard handoff receipts cannot be verified; the server was not paired", nil)
		return nil, false
	}
	return receipts, true
}

// consumeHandoffReceipts consumes the prior tenants' confirmations of every
// receipt for the binding of consumerWorkerID in consumerTenantID, by a pairing
// that proved keyThumbprint. It writes the refusal and returns false when one
// is not a valid, unexpired confirmation of its prior tenant for that key, or
// was already consumed by another pairing.
func (h workerRouteHandlers) consumeHandoffReceipts(e *httpx.Event, receipts []guardbinding.TenantHandoffReceipt, consumerTenantID, consumerWorkerID, keyThumbprint string, now time.Time) ([]string, bool) {
	if len(receipts) == 0 {
		return nil, true
	}
	store, ok := h.identityStore.(controlplane.GuardTenantHandoffStore)
	if !ok {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard handoff receipts cannot be verified; the server was not paired", nil)
		return nil, false
	}
	digests := make([]string, 0, len(receipts))
	for _, receipt := range receipts {
		digest, err := receipt.Digest()
		if err != nil {
			_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The handoff receipt is malformed", map[string]any{"reason": guardbinding.CodeTenantHandoffInvalid})
			return nil, false
		}
		_, err = store.ConsumeGuardTenantHandoff(e.Request.Context(), controlplane.GuardTenantHandoffConsume{
			TenantID: receipt.PriorTenantID, HandoffID: receipt.HandoffID,
			ConfirmationSHA256: guardbinding.ConfirmationHash(receipt.Confirmation), Disposition: receipt.Disposition,
			ReceiptDigest: digest, ReceiptEntries: receipt.Entries,
			ConsumerTenantID: consumerTenantID, ConsumerWorkerID: consumerWorkerID, KeyThumbprint: keyThumbprint, Now: now,
		})
		switch {
		case errors.Is(err, controlplane.ErrConflict):
			// Unknown, expired, foreign, for another server's key, already
			// consumed by another pairing, or a wrong code read the same.
			_ = httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The handoff receipt was not confirmed for this server by its prior owner, expired or was already used", map[string]any{"reason": guardbinding.CodeTenantHandoffInvalid})
			return nil, false
		case err != nil:
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard handoff receipts cannot be verified; the server was not paired", nil)
			return nil, false
		}
		slog.Info("guard_tenant_handoff_receipted", "handoff_id", receipt.HandoffID, "disposition", receipt.Disposition,
			"entries", receipt.Entries, "consumer_tenant_id", consumerTenantID, "consumer_worker_id", consumerWorkerID)
		digests = append(digests, digest)
	}
	return digests, true
}

type tenantHandoffVerifyRequest struct {
	Confirmation string `json:"confirmation"`
}

// verifyTenantHandoff lets a decommissioned server check a confirmation before
// it removes anything. The request is authenticated by the one-time code and
// by a proof signed with the server's host key and bound to the confirmation;
// it is answered only for an unconsumed, unexpired confirmation that the prior
// owner issued for exactly that key. It consumes nothing.
func (h workerRouteHandlers) verifyTenantHandoff(e *httpx.Event) error {
	store, ok := h.identityStore.(controlplane.GuardTenantHandoffStore)
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard handoff is unavailable", nil)
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 2049))
	var request tenantHandoffVerifyRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if readErr != nil || len(raw) > 2048 || decoder.Decode(&request) != nil {
		return httpx.BadRequest(e, "confirmation is required", nil)
	}
	now := time.Now().UTC()
	confirmation, err := guardbinding.ParseHandoffConfirmation(request.Confirmation, now)
	if err != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, err.Error(), map[string]any{"reason": guardbinding.CodeTenantHandoffInvalid})
	}
	proof, proofErr := verifyGuardProof(e, strings.TrimSpace(request.Confirmation), now)
	if proofErr != nil {
		denyGuardError(e, proofErr)
		return nil
	}
	err = store.VerifyGuardTenantHandoff(e.Request.Context(), confirmation.TenantID, confirmation.HandoffID,
		guardbinding.ConfirmationHash(confirmation.Code), confirmation.Disposition, proof.Thumbprint, now)
	switch {
	case errors.Is(err, controlplane.ErrConflict):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The handoff confirmation was not issued for this server, expired or was already used", map[string]any{"reason": guardbinding.CodeTenantHandoffInvalid})
	case err != nil:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard handoff is unavailable", nil)
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{
		"verified": true, "handoff_id": confirmation.HandoffID, "disposition": confirmation.Disposition,
	})
}
