package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// getHomelab resolves the caller's homelab umbrella (ADR-0036) together with
// its kit deployments. On the control-plane lane the deployments list shares
// ownedStackItems with GET /api/v1/stacks; on the PocketBase-only lane it
// serves the same records through legacyOwnedStacks (unordered, unlimited)
// rather than listStacks' filtered query. A missing homelab row with existing
// deployments is legal (pre-backfill lanes, PocketBase-only self-host) and
// yields homelab: null.
func (h crudRouteHandlers) getHomelab(e *httpx.Event) error {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return err
	}
	tenantID := tenantIDFromRequest(e)

	if h.stackStore == nil || tenantID == "" {
		if guardErr := tenantguard.RequireTenant(tenantID, "techstack.homelab.get"); guardErr != nil {
			return guardErr
		}
	}

	items, err := h.ownedStackItems(e.Request.Context(), ownerID, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resolve homelab", nil)
	}

	var homelabPayload map[string]any
	if h.homelabStore != nil && tenantID != "" {
		homelab, hlErr := h.homelabStore.GetHomelabByOwner(e.Request.Context(), tenantID, ownerID)
		switch {
		case hlErr == nil:
			homelabPayload = homelabItem(homelab)
		case !errors.Is(hlErr, controlplane.ErrNotFound):
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resolve homelab", nil)
		}
	}

	if homelabPayload == nil && len(items) == 0 {
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "No homelab provisioned yet", map[string]any{
			detailsKeyReasonCode: "homelab_not_found",
			detailsKeyRetryable:  false,
			"user_guidance": map[string]any{
				"title":      "Create your homelab",
				"body":       "Run the creation wizard to set up your first server.",
				"next_steps": []string{"Open the creation wizard at /stacks/new."},
			},
		})
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"homelab":         homelabPayload,
		"kit_deployments": items,
	})
}

// maxHomelabNameLength bounds the rename input; the column itself is text.
const maxHomelabNameLength = 100

type renameHomelabRequest struct {
	Name string `json:"name"`
}

// renameHomelab lets the owner name their homelab. Every homelab starts with a
// generated name ("homelab" — migration 044's backfill and the wizard default),
// which is a placeholder, not a product name: without this route the dashboard
// title can never become the name the operator actually chose.
func (h crudRouteHandlers) renameHomelab(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID := tenantIDFromRequest(e)
	// A missing tenant and a missing store are different denials: the first is
	// an identity problem the caller can fix by re-authenticating with an
	// organization scope, the second is a deployment shape they cannot. Route
	// them separately so neither is reported as the other - getHomelab uses
	// the same guard.
	if guardErr := tenantguard.RequireTenant(tenantID, "techstack.homelab.rename"); guardErr != nil {
		return guardErr
	}
	if h.homelabStore == nil || tenantID == "" {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Homelab settings require the Postgres control plane", map[string]any{
				detailsKeyReasonCode: "homelab_store_unavailable",
				detailsKeyRetryable:  false,
				"user_guidance": map[string]any{
					"title": "Not available on this deployment",
					"body":  "Renaming the homelab needs the Postgres control plane; this deployment runs the legacy store.",
					"next_steps": []string{
						"Migrate this deployment to the Postgres control plane to manage homelab settings.",
					},
				},
			})
	}

	var request renameHomelabRequest
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		return httpx.BadRequest(e, "Invalid JSON")
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len([]rune(name)) > maxHomelabNameLength {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Homelab name must be between 1 and 100 characters", map[string]any{
				detailsKeyReasonCode: "homelab_name_invalid",
				detailsKeyRetryable:  false,
				"user_guidance": map[string]any{
					"title": "Choose a name",
					"body":  "Enter a name of at most 100 characters for your homelab.",
				},
			})
	}

	// Resolving by owner is the authorization: a caller can only ever rename
	// their own homelab, never one addressed by id.
	homelab, hlErr := h.homelabStore.GetHomelabByOwner(e.Request.Context(), tenantID, ownerID)
	if hlErr != nil {
		if errors.Is(hlErr, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "No homelab provisioned yet")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resolve homelab", nil)
	}
	renamed, renameErr := h.homelabStore.UpdateHomelabName(e.Request.Context(), tenantID, homelab.ID, name)
	if renameErr != nil {
		if errors.Is(renameErr, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "No homelab provisioned yet")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to rename homelab", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"homelab": homelabItem(renamed)})
}

// ownedStackItems returns the caller's merged kit-deployment projection:
// control-plane rows for (tenant, owner) plus any legacy PocketBase rows not
// yet migrated, deduplicated by stack id.
func (h crudRouteHandlers) ownedStackItems(ctx context.Context, ownerID, tenantID string) ([]map[string]any, error) {
	result := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	if h.stackStore != nil && tenantID != "" {
		stacks, err := h.stackStore.ListStacksByTenant(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		for _, stack := range stacks {
			if stack.OwnerSubjectID == ownerID {
				seen[stack.ID] = struct{}{}
				result = append(result, stackListItemFromStore(stack))
			}
		}
	}
	legacyStacks, legacyErr := h.legacyOwnedStacks(ownerID, tenantID)
	if legacyErr != nil {
		return nil, legacyErr
	}
	for _, stack := range legacyStacks {
		if _, ok := seen[stack.Id]; ok {
			continue
		}
		result = append(result, stackListItem(stack))
	}
	return result, nil
}

func homelabItem(homelab *controlplane.Homelab) map[string]any {
	if homelab == nil {
		return nil
	}
	item := map[string]any{
		"id":              homelab.ID,
		creationNameField: homelab.Name,
		// Whether the name was chosen or merely generated is a fact the client
		// cannot derive from the string: renaming to exactly the generated
		// name is legal, and the generated name is not a stable contract.
		"named":   homelab.NamedAt != nil,
		"intent":  homelab.Intent,
		"created": formatAPITime(homelab.CreatedAt),
		"updated": formatAPITime(homelab.UpdatedAt),
	}
	if homelab.NamedAt != nil {
		item["named_at"] = formatAPITime(*homelab.NamedAt)
	}
	return item
}
