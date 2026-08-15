package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/kombifyio/techstack/internal/pocketbase_migration"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

// setupRequest is the JSON body for the first-run setup wizard.
type setupRequest struct {
	Mode     string `json:"mode"` // "local" or "cloud"
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Password string `json:"password,omitempty"`
}

// LocalOwnerSetup is the canonical local first-run owner payload.
type LocalOwnerSetup struct {
	Name     string
	Email    string
	Password string
}

// LocalSetupProvisioner wires the local first-run payload into the current
// local auth authority. Production uses the V2 authlocal store; nil preserves
// the legacy PocketBase compatibility behavior for narrow tests.
type LocalSetupProvisioner func(context.Context, LocalOwnerSetup) error

// handleSetup handles the first-run setup wizard.
// POST /api/v1/auth/setup — public, one-shot.
// Blocked in SaaS mode (platform pre-configures) and when auth_config already exists.
func handleSetup(app core.App, deployMode config.DeploymentMode, provisionLocal LocalSetupProvisioner) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if deployMode.IsSaaS() {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
				"Setup is managed by the platform in SaaS mode", nil)
		}

		// Check if setup already completed (auth_config record exists)
		existing, _ := app.FindFirstRecordByFilter("auth_config", "id != ''", nil)
		if existing != nil {
			return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
				"Setup already completed", nil)
		}

		var req setupRequest
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return httpx.BadRequest(e, "Invalid request body")
		}

		switch req.Mode {
		case "local":
			return setupLocal(app, e, req, provisionLocal)
		case "cloud":
			return setupCloud(app, e)
		default:
			return httpx.BadRequest(e, "mode must be 'local' or 'cloud'")
		}
	}
}

// setupLocal creates the first local admin in the configured local auth
// authority, keeps the bounded PocketBase compatibility user, and writes the
// auth_config marker.
func setupLocal(app core.App, e *httpx.Event, req setupRequest, provisionLocal LocalSetupProvisioner) error {
	if req.Email == "" || req.Password == "" || req.Name == "" {
		return httpx.BadRequest(e, "name, email, and password are required for local setup")
	}
	if len(req.Password) < 8 {
		return httpx.BadRequest(e, "password must be at least 8 characters")
	}

	if provisionLocal != nil {
		if err := provisionLocal(e.Request.Context(), LocalOwnerSetup{
			Name:     req.Name,
			Email:    req.Email,
			Password: req.Password,
		}); err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
				"Failed to configure local login: "+err.Error(), nil)
		}
	}

	// Create the admin user in PocketBase's users collection
	usersCollection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to find users collection", nil)
	}

	user := core.NewRecord(usersCollection)
	user.Set("email", req.Email)
	user.Set("name", req.Name)
	user.Set("verified", true)
	user.Set("role", "admin")
	user.SetPassword(req.Password)

	if err := app.Save(user); err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Failed to create user: "+err.Error(), nil)
	}

	// Create auth_config record (marks setup as done)
	if err := createAuthConfig(app, "local", true); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to save auth config: "+err.Error(), nil)
	}

	return httpx.Success(e, http.StatusCreated, map[string]any{
		"message": "Setup complete",
		"mode":    "local",
	})
}

// setupCloud writes the auth_config record for cloud mode.
// No user creation needed — users authenticate via kombify Cloud.
func setupCloud(app core.App, e *httpx.Event) error {
	if err := createAuthConfig(app, "cloud", false); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to save auth config: "+err.Error(), nil)
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"message":  "Setup complete — redirect to cloud login",
		"mode":     "cloud",
		"redirect": "cloud",
	})
}

// createAuthConfig creates the singleton auth_config record.
func createAuthConfig(app core.App, mode string, allowLocal bool) error {
	if err := pocketbase_migration.EnsureAuthConfigCollection(app); err != nil {
		return err
	}

	collection, err := app.FindCollectionByNameOrId("auth_config")
	if err != nil {
		return err
	}

	record := core.NewRecord(collection)
	record.Set("mode", mode)
	record.Set("allow_local_login", allowLocal)

	return app.Save(record)
}
