package auth

import (
	"encoding/json"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/auth/sso"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

type stackIdentityRequest struct {
	Name           string `json:"name"`
	CharacterID    string `json:"characterId"`
	AnimationStyle string `json:"animationStyle"`
}

type stackIdentityResponse struct {
	StackIdentity *sso.StackIdentity `json:"stack_identity,omitempty"`
	Editable      bool               `json:"editable"`
}

func getStackIdentity(app core.App, deployMode config.DeploymentMode) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if err := CheckAuth(e); err != nil {
			return err
		}

		// e.Auth is the edge/Auth0 principal, which does not carry the persisted
		// stack_identity. Load the local user record by the authenticated id to
		// read it (mirrors updateStackIdentity). A missing record yields a nil
		// identity rather than an error.
		var stored *sso.StackIdentity
		if userID, ok := AuthUserID(e); ok {
			if user, err := app.FindRecordById("users", userID); err == nil {
				stored = getStoredStackIdentity(user)
			}
		}

		return httpx.Success(e, http.StatusOK, stackIdentityResponse{
			StackIdentity: stored,
			Editable:      !deployMode.IsSaaS(),
		})
	}
}

func updateStackIdentity(app core.App, deployMode config.DeploymentMode) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		if deployMode.IsSaaS() {
			return httpx.Forbidden(e, "Stack identity is managed by kombify Cloud in SaaS mode")
		}

		if err := CheckAuth(e); err != nil {
			return err
		}
		userID, _ := AuthUserID(e)

		var req stackIdentityRequest
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return httpx.BadRequest(e, "Invalid request body: "+err.Error())
		}

		identity := normalizeStackIdentity(&sso.StackIdentity{
			Name:           req.Name,
			CharacterID:    req.CharacterID,
			AnimationStyle: req.AnimationStyle,
		})
		if identity == nil {
			return httpx.BadRequest(e, "name, characterId and animationStyle are required")
		}

		user, err := app.FindRecordById("users", userID)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load user profile", nil)
		}

		if err := saveUserStackIdentity(app, user, identity); err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to save stack identity", nil)
		}

		return httpx.Success(e, http.StatusOK, stackIdentityResponse{
			StackIdentity: getStoredStackIdentity(user),
			Editable:      true,
		})
	}
}

func getStoredStackIdentity(user *core.Record) *sso.StackIdentity {
	if user == nil {
		return nil
	}

	raw := user.Get("stack_identity")
	if raw == nil {
		return nil
	}

	switch value := raw.(type) {
	case map[string]any:
		return normalizeStackIdentity(&sso.StackIdentity{
			Name:           toString(value["name"]),
			CharacterID:    toString(value["characterId"]),
			AnimationStyle: toString(value["animationStyle"]),
		})
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(value), &payload); err != nil {
			return nil
		}

		return normalizeStackIdentity(&sso.StackIdentity{
			Name:           toString(payload["name"]),
			CharacterID:    toString(payload["characterId"]),
			AnimationStyle: toString(payload["animationStyle"]),
		})
	default:
		return nil
	}
}

func saveUserStackIdentity(app core.App, user *core.Record, identity *sso.StackIdentity) error {
	normalized := normalizeStackIdentity(identity)
	if normalized == nil {
		user.Set("stack_identity", nil)
		return app.Save(user)
	}

	user.Set("stack_identity", map[string]any{
		"name":           normalized.Name,
		"characterId":    normalized.CharacterID,
		"animationStyle": normalized.AnimationStyle,
	})

	return app.Save(user)
}

func normalizeStackIdentity(identity *sso.StackIdentity) *sso.StackIdentity {
	if identity == nil {
		return nil
	}

	name := strings.TrimSpace(identity.Name)
	characterID := strings.TrimSpace(identity.CharacterID)
	animationStyle := strings.TrimSpace(identity.AnimationStyle)

	if name == "" || characterID == "" || animationStyle == "" {
		return nil
	}

	if len(name) > 80 || len(characterID) > 40 || len(animationStyle) > 60 {
		return nil
	}

	return &sso.StackIdentity{
		Name:           name,
		CharacterID:    characterID,
		AnimationStyle: animationStyle,
	}
}

func toString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}

	return ""
}
