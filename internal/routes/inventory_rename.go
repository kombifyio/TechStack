package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	inventoryDisplayNameField     = "display_name"
	inventoryHomelabNameField     = "name"
	inventoryRenameRequestMaxSize = 4096
)

// inventoryRenamedServer is the rename result: the canonical inventory item
// plus the owner-chosen name. display_name stays a Techstack-local field here
// until the pinned runtime inventory contract carries it; the canonical
// `name` already reports the owner-chosen name when one is set.
type inventoryRenamedServer struct {
	inventoryServer
	DisplayName *string `json:"display_name"`
}

type inventoryRenameServerRequest struct {
	// Raw keeps "absent" (nil) apart from an explicit null, which clears.
	DisplayName json.RawMessage `json:"display_name"`
}

// httpRenameServer serves PATCH /api/v1/inventory/servers/{serverId}.
func (h inventoryHandlers) httpRenameServer(e *httpx.Event) error {
	if err := rejectInventoryScopeOverrides(e, nil); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	scope, err := inventoryScopeFromEvent(e)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	var request inventoryRenameServerRequest
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, inventoryRenameRequestMaxSize))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		return writeInventoryHTTPError(e, inventoryValidationError("invalid_json", "Invalid JSON"))
	}
	if len(request.DisplayName) == 0 {
		return writeInventoryHTTPError(e, inventoryValidationError("display_name_required", "display_name is required; send null to clear it"))
	}
	displayName := ""
	if string(request.DisplayName) != "null" {
		if unmarshalErr := json.Unmarshal(request.DisplayName, &displayName); unmarshalErr != nil {
			return writeInventoryHTTPError(e, inventoryServerDisplayNameInvalid())
		}
	}
	result, err := h.app.renameServer(e.Request.Context(), scope, e.Request.PathValue("serverId"), displayName)
	if err != nil {
		return writeInventoryHTTPError(e, err)
	}
	return httpx.Success(e, http.StatusOK, result)
}

// renameServer writes the owner-chosen display name. An empty name clears it.
// Authorization is the shared write decision (signed techstack.inventory.write
// plus FGA); the store write is additionally bound to tenant, server and the
// authenticated owner, and a miss is reported as a denial so a caller cannot
// learn whether another owner's server exists.
func (a *inventoryApplication) renameServer(ctx context.Context, scope inventoryScope, serverID, rawDisplayName string) (inventoryRenamedServer, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return inventoryRenamedServer{}, inventoryValidationError("server_id_required", "Server ID is required")
	}
	displayName := ""
	if strings.TrimSpace(rawDisplayName) != "" {
		normalized, err := controlplane.NormalizeOwnerChosenName(rawDisplayName)
		if err != nil {
			return inventoryRenamedServer{}, inventoryServerDisplayNameInvalid()
		}
		displayName = normalized
	}
	if _, err := a.authorize(ctx, scope, InventoryActionWrite, controlplane.InventoryReadTargetServer, serverID); err != nil {
		return inventoryRenamedServer{}, err
	}
	if a.renamer == nil {
		return inventoryRenamedServer{}, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "inventory_write_unavailable", message: "Inventory writes are unavailable"}
	}
	server, err := a.renamer.RenameInventoryServer(ctx, scope.tenantID, scope.ownerID, serverID, displayName)
	if errors.Is(err, controlplane.ErrNotFound) {
		return inventoryRenamedServer{}, &inventoryError{status: http.StatusForbidden, reasonCode: "inventory_access_denied", message: "Inventory access denied", cause: err}
	}
	if err != nil {
		return inventoryRenamedServer{}, inventoryStoreError(err)
	}
	result := inventoryRenamedServer{inventoryServer: a.projectServer(*server, nil, a.now().UTC())}
	if server.DisplayName != "" {
		chosen := server.DisplayName
		result.DisplayName = &chosen
	}
	return result, nil
}

// renameHomelab is the MCP form of PATCH /api/v1/homelab. It shares the
// owner-resolved domain rename with the REST route and adds the signed write
// entitlement and FGA decision every inventory write needs.
func (a *inventoryApplication) renameHomelab(ctx context.Context, scope inventoryScope, name string) (map[string]any, error) {
	if _, err := a.authorize(ctx, scope, InventoryActionWrite, controlplane.InventoryReadTargetTools, ""); err != nil {
		return nil, err
	}
	if a.homelabs == nil {
		return nil, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "homelab_store_unavailable", message: "Homelab settings are unavailable"}
	}
	homelab, err := controlplane.RenameOwnedHomelab(ctx, a.homelabs, scope.tenantID, scope.ownerID, name)
	switch {
	case errors.Is(err, controlplane.ErrInvalidOwnerChosenName):
		return nil, inventoryValidationError("homelab_name_invalid", "Homelab name must be between 1 and 30 characters")
	case errors.Is(err, controlplane.ErrNotFound):
		return nil, inventoryNotFound("homelab_not_found", "No homelab provisioned yet")
	case err != nil:
		return nil, inventoryStoreError(err)
	}
	return map[string]any{"homelab": map[string]any{
		"id": homelab.ID, "name": homelab.Name, "named": homelab.NamedAt != nil,
		"updated": homelab.UpdatedAt.UTC().Format(time.RFC3339),
	}}, nil
}

func inventoryServerDisplayNameInvalid() *inventoryError {
	return inventoryValidationError("server_display_name_invalid", "Server name must be between 1 and 100 characters without control characters")
}
