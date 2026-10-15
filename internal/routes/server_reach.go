package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// serverReachStepUpMaxAge bounds the multi-factor sign-in behind publishing a
// server at a public address.
const serverReachStepUpMaxAge = 300 * time.Second

const (
	reachReasonStepUp      = "server_reach_step_up_required"
	reachReasonUnavailable = "guard_reach_unavailable"
	reachReasonInvalid     = "public_reach_invalid"
)

// ServerReachControl is the owner's side of kombify Guard reach: publishing one
// server at a public kombify.me address, or withdrawing it.
type ServerReachControl interface {
	SetPublic(ctx context.Context, tenantID, workerID, ownerSubject string, want guardreach.PublicReach) (*guardreach.Row, error)
}

type serverReachRequest struct {
	Enabled      bool   `json:"enabled"`
	Service      string `json:"service"`
	TargetPort   int    `json:"target_port"`
	TargetScheme string `json:"target_scheme"`
}

// reachSetPublic publishes or withdraws one server. Nothing is published unless
// the owner enables it here with an explicit service name and port: the relay
// authenticates the Guard, not the visitor, so the owner decides what answers on
// that port. Enabling needs the maintenance entitlement and a fresh second
// factor; withdrawing is never gated.
func (h serverRuntimeHandlers) reachSetPublic(e *httpx.Event) error {
	server, err := h.ownedServer(e, "techstack.servers.reach.set")
	if err != nil || server == nil {
		return err
	}
	control, ok := h.reach.(ServerReachControl)
	if !ok {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard reach is unavailable", map[string]any{"reason": reachReasonUnavailable})
	}
	if strings.TrimSpace(server.WorkerID) == "" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This server has no kombify Guard", map[string]any{"reason": postureReasonNotReady})
	}
	ownerID, _, _ := authenticatedUser(e)
	if err := authorizeInventoryServerOperate(e.Request.Context(), h.policy, inventoryScope{tenantID: server.TenantID, ownerID: ownerID}, server.ID); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 4097))
	if readErr != nil || len(raw) > 4096 {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	var request serverReachRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	want := guardreach.PublicReach{Enabled: request.Enabled, Service: request.Service, Scheme: request.TargetScheme, Port: request.TargetPort}
	if _, invalid := want.Normalize(); invalid != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, invalid.Error(), map[string]any{
			"reason": reachReasonInvalid, "retryable": false,
			"guidance": "Name the service (lowercase letters, digits and hyphens, up to 14 characters) and the port it listens on.",
		})
	}
	if request.Enabled {
		gate := entitlementGateOrHosted(h.postureEntitlements)
		if !gate.SignedGrant(e.Request.Context(), ServerMaintenanceEntitlement) {
			return writeEntitlementDenial(e, "techstack.servers.reach.set", ServerMaintenanceEntitlement)
		}
		if !gate.FreshMultiFactor(e.Request.Context(), h.now().UTC(), serverReachStepUpMaxAge) {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "A fresh multi-factor sign-in is required to publish a server at a public address", map[string]any{
				"reason": reachReasonStepUp, "retryable": true,
				"guidance": "Sign in again with a second factor and repeat the request within five minutes.",
			})
		}
	}
	row, setErr := control.SetPublic(e.Request.Context(), server.TenantID, strings.TrimSpace(server.WorkerID), ownerID, want)
	switch {
	case errors.Is(setErr, guardreach.ErrPublicReachInvalid):
		return httpx.BadRequest(e, setErr.Error(), nil)
	case errors.Is(setErr, guardreach.ErrNotFound):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This server is not enrolled for reach yet", map[string]any{"reason": postureReasonNotReady})
	case setErr != nil:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard reach is unavailable", map[string]any{"reason": reachReasonUnavailable})
	}
	slog.Warn("guard_reach_public_set", "tenant_id", server.TenantID, "worker_id", server.WorkerID, "enabled", request.Enabled,
		"service", want.Service, "target_port", want.Port, "actor", ownerID)
	return httpx.Success(e, http.StatusOK, guardreach.ViewOf(*row))
}
