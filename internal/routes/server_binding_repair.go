package routes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/trust"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// BindingRepairMinter issues the one-time install command that pairs one
// existing server again. The token is returned to the caller once.
type BindingRepairMinter interface {
	MintServerRePair(ctx context.Context, tenantID, userID string, server controlplane.ServerRuntime) (token string, expiresAt time.Time, err error)
}

// ErrBindingRepairStackMissing is returned when the server names a stack this
// owner cannot pair against.
var ErrBindingRepairStackMissing = errors.New("binding repair stack missing")

type bindingRepairAuthority struct {
	stores trust.RouteStores
}

// NewBindingRepairAuthority returns the minter, or nil when the stack or
// worker store is not wired.
func NewBindingRepairAuthority(stores trust.RouteStores) BindingRepairMinter {
	if stores.Stacks == nil || stores.Workers == nil {
		return nil
	}
	return bindingRepairAuthority{stores: stores}
}

func (a bindingRepairAuthority) MintServerRePair(ctx context.Context, tenantID, userID string, server controlplane.ServerRuntime) (string, time.Time, error) {
	var stack *controlplane.Stack
	if stackID := strings.TrimSpace(server.StackID); stackID != "" {
		loaded, err := a.stores.Stacks.GetStack(ctx, tenantID, stackID)
		if err != nil {
			if errors.Is(err, controlplane.ErrNotFound) {
				return "", time.Time{}, ErrBindingRepairStackMissing
			}
			return "", time.Time{}, err
		}
		if loaded == nil || strings.TrimSpace(loaded.OwnerSubjectID) != strings.TrimSpace(userID) {
			return "", time.Time{}, ErrBindingRepairStackMissing
		}
		stack = loaded
	}
	role := strings.TrimSpace(stringFromAnyMap(server.Metadata, nodehandoff.KeyServerNodeRole))
	if role == "" {
		role = "foundation"
	} else {
		role = nodehandoff.NormalizeNodeRole(role)
	}
	var services []string
	if server.Metadata != nil {
		services = nodehandoff.ServiceKeysFromAny(server.Metadata[nodehandoff.KeyRequestedServices])
	}
	req := trust.WithPlannedServer(trust.PairingTokenParams{
		ServerProvisioningMode: "install-command",
		EnvironmentClass:       bindingRepairEnvironment(server),
		NodeRole:               role,
		StackKit:               stringFromAnyMap(server.Metadata, "stackkit"),
		Services:               services,
	}, server.ID)
	minted, err := trust.MintStackPairingToken(ctx, a.stores, tenantID, userID, stack, req)
	if err != nil || minted.Token == "" {
		if err == nil {
			err = errors.New("pairing token was not issued")
		}
		return "", time.Time{}, err
	}
	return minted.Token, minted.ExpiresAt, nil
}

func bindingRepairEnvironment(server controlplane.ServerRuntime) string {
	switch serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget).EnvironmentClass {
	case serverregistry.EnvironmentLocal:
		return string(serverregistry.EnvironmentLocal)
	case serverregistry.EnvironmentCloud:
		return string(serverregistry.EnvironmentCloud)
	default:
		return ""
	}
}

func (h serverRuntimeHandlers) bindingRepairPair(e *httpx.Event) error {
	e.Response.Header().Set("Cache-Control", "private, no-store")
	server, err := h.ownedServer(e, "techstack.servers.binding-repair")
	if err != nil || server == nil {
		return err
	}
	if h.bindingRepair == nil || strings.TrimSpace(server.WorkerID) == "" || h.workers == nil {
		return bindingRepairNotRequired(e)
	}
	worker, err := h.workers.GetWorker(e.Request.Context(), server.TenantID, strings.TrimSpace(server.WorkerID))
	if err != nil || worker == nil {
		if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
		}
		return bindingRepairNotRequired(e)
	}
	capabilities := serverCapabilitiesFromWorker(*worker)
	if capabilities == nil || capabilities.BindingRepair == nil || !capabilities.BindingRepair.Required {
		return bindingRepairNotRequired(e)
	}
	token, expiresAt, err := h.bindingRepair.MintServerRePair(e.Request.Context(), server.TenantID, server.OwnerSubjectID, *server)
	if errors.Is(err, ErrBindingRepairStackMissing) {
		return httpx.NotFound(e, "Stack not found")
	}
	if err != nil {
		slog.Warn("server_binding_repair_mint_failed", "server_id", server.ID, "error", err.Error())
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "A pairing command could not be prepared", nil)
	}
	return httpx.Success(e, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": expiresAt,
	})
}

func bindingRepairNotRequired(e *httpx.Event) error {
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "This server does not need to be paired again", map[string]any{
		"reason_code": "binding_repair_not_required",
	})
}
