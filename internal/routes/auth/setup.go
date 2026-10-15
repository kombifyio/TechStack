package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/google/uuid"
)

// setupRequest is the JSON body for the first-run setup wizard.
type setupRequest struct {
	Mode            string `json:"mode"` // "local" or "cloud"
	Name            string `json:"name,omitempty"`
	Email           string `json:"email,omitempty"`
	Password        string `json:"password,omitempty"`
	CurrentPassword string `json:"current_password,omitempty"`
}

// LocalOwnerSetup is the canonical local first-run owner payload.
type LocalOwnerSetup struct {
	Name            string
	Email           string
	Password        string
	CurrentPassword string
}

// LocalSetupProvisioner wires the local first-run payload into the current
// local auth authority. Production uses the V2 authlocal store; nil means the
// canonical local login authority is unavailable and setup must fail closed.
type LocalSetupProvisioner func(context.Context, LocalOwnerSetup) error

// handleSetup handles the first-run setup wizard.
// POST /api/v1/auth/setup — public, one-shot.
// Blocked in SaaS mode and after instance auth_config completes setup. A
// partially claimed owner retries only through the canonical provisioner.
func handleRuntimeSetup(runtime RuntimeStore, deployMode config.DeploymentMode, provisionLocal LocalSetupProvisioner, owners LocalOwnerLookup) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if deployMode.IsSaaS() {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
				"Setup is managed by the platform in SaaS mode", nil)
		}

		ownerPresent, err := canonicalOwnerPresent(e.Request.Context(), owners)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to read local owner store", nil)
		}
		// Check if setup already completed (auth_config record exists).
		existing, err := runtime.authConfig(e.Request.Context())
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to read auth configuration", nil)
		}
		if existing != nil {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
				"Setup already completed", nil)
		}

		var req setupRequest
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			if ownerPresent {
				return httpx.Forbidden(e, "Setup already completed")
			}
			return httpx.BadRequest(e, "Invalid request body")
		}

		switch req.Mode {
		case "local":
			return setupRuntimeLocal(runtime, e, req, provisionLocal)
		case "cloud":
			if ownerPresent {
				return httpx.Forbidden(e, "Setup already completed")
			}
			// Legacy navigation is read-only; it cannot strand the local owner.
			return httpx.Success(e, http.StatusOK, map[string]any{
				"message": "Continue with cloud login", "mode": "cloud", "redirect": "cloud",
			})
		default:
			return httpx.BadRequest(e, "mode must be 'local' or 'cloud'")
		}
	}
}

// setupLocal creates the first local admin in the configured local auth
// authority, projects its canonical membership, and writes instance auth_config.
func setupRuntimeLocal(runtime RuntimeStore, e *httpx.Event, req setupRequest, provisionLocal LocalSetupProvisioner) error {
	if req.Email == "" || req.Password == "" || req.Name == "" {
		return httpx.BadRequest(e, "name, email, and password are required for local setup")
	}
	if len(req.Password) < 8 {
		return httpx.BadRequest(e, "password must be at least 8 characters")
	}

	if provisionLocal == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal,
			"Local login authority is unavailable", nil)
	}
	if err := runtime.Validate(); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Canonical auth persistence is unavailable", nil)
	}
	if err := provisionLocal(e.Request.Context(), LocalOwnerSetup{
		Name:            req.Name,
		Email:           req.Email,
		Password:        req.Password,
		CurrentPassword: req.CurrentPassword,
	}); err != nil {
		switch {
		case errors.Is(err, authlocal.ErrInvalidCreds):
			return httpx.Unauthorized(e, "Invalid local owner proof")
		case errors.Is(err, authlocal.ErrLockedByOperator):
			return httpx.Forbidden(e, "Local owner setup is locked")
		case errors.Is(err, authlocal.ErrAlreadyClaimed), errors.Is(err, authlocal.ErrConcurrentUpdate):
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Local owner setup changed; retry with the current owner credentials", nil)
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to configure local login: "+err.Error(), nil)
	}

	ctx := e.Request.Context()
	if err := runtime.ProjectLocalOwner(ctx, req.Name, req.Email); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Failed to project local owner", nil)
	}
	if _, err := runtime.Persistence.UpsertAuthConfig(ctx, controlplane.AuthConfig{
		ID: uuid.NewString(), TenantID: runtime.TenantID, InstanceID: runtime.InstanceID,
		Mode: "local", Config: map[string]any{"allow_local_login": true},
	}); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to save auth config", nil)
	}

	return httpx.Success(e, http.StatusCreated, map[string]any{
		"message": "Setup complete",
		"mode":    "local",
	})
}
