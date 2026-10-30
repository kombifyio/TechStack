package routes

import (
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/pkg/httpx"
	"io"
	"net/http"
	"strings"
)

// homeAssistantAttachRequest is the attachment body: the attachment itself
// and, in place of its token, the Wallet item that holds the token.
type homeAssistantAttachRequest struct {
	homeassistant.AttachOwner
	TokenWalletItemID string `json:"token_wallet_item_id,omitempty"`
}

func RegisterHomeAssistantEnrollmentRoutes(r *httpx.Router, store *homeassistant.EnrollmentStore, wallet WalletSecretReferences) {
	r.GET("/api/v1/home-assistant/owners", func(e *httpx.Event) error {
		owner, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenant, err := requireRegistryRouteTenant(e, owner, "techstack.home_assistant.bind")
		if err != nil {
			return err
		}
		if store == nil {
			return httpx.NewAPIError(503, "unavailable", "Local owner enrollment is unavailable", nil)
		}
		result, err := store.List(e.Request.Context(), tenant, owner, e.Request.URL.Query().Get("stack_id"))
		if err != nil {
			return httpx.NewAPIError(409, "enrollment_unavailable", "Owned enrollments could not be read", nil)
		}
		return httpx.Success(e, 200, result)
	})
	r.POST("/api/v1/home-assistant/owners/attach", func(e *httpx.Event) error {
		owner, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenant, err := requireRegistryRouteTenant(e, owner, "techstack.home_assistant.bind")
		if err != nil {
			return err
		}
		if store == nil {
			return httpx.NewAPIError(503, "unavailable", "Local owner enrollment is unavailable", nil)
		}
		var body homeAssistantAttachRequest
		decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 16384))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&body); err != nil {
			return httpx.BadRequest(e, "Invalid owner attachment", nil)
		}
		var trailing any
		if !errors.Is(decoder.Decode(&trailing), io.EOF) {
			return httpx.BadRequest(e, "Invalid owner attachment", nil)
		}
		req := body.AttachOwner
		if strings.TrimSpace(body.TokenWalletItemID) != "" {
			if req.Token != "" {
				return httpx.BadRequest(e, "Name a Wallet item or a token, not both", nil)
			}
			if req.Token, err = wallet.Token(e.Request.Context(), tenant, owner, body.TokenWalletItemID, req.URL); err != nil {
				return writeWalletReferenceDenial(e, err)
			}
			defer func() { req.Token = "" }()
		}
		if err = store.Attach(e.Request.Context(), tenant, owner, req); err != nil {
			return httpx.NewAPIError(409, "attachment_rejected", "An owned pending enrollment, current runtime and authenticated native instance are required", nil)
		}
		return httpx.Success(e, 200, map[string]any{"binding_ref": req.BindingRef, "attached": true})
	})
}
