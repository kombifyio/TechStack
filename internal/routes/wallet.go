package routes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
)

const (
	walletRevealAction       = "wallet_reveal"
	walletRevealActionDenied = "wallet_reveal_denied"
	walletReauthWindow       = 5 * time.Minute
	walletAuditReasonKey     = "reason"
	walletAuditStatusSuccess = "success"
	walletAuditStatusWarning = "warning"
	walletAuditStatusError   = "error"
	walletAuditStatusInfo    = "info"

	walletRevealAssertionPurpose         = "techstack.wallet.reveal.v1"
	walletPlatformReauthAssertionPurpose = "kombify.cloud.wallet.reauth.v1"
)

type walletRouteHandlers struct {
	app core.App
	wst controlplane.WalletStore
	ast controlplane.ActivityStore
	now func() time.Time
}

type WalletRouteConfig struct {
	Store    controlplane.WalletStore
	Activity controlplane.ActivityStore
}

type walletRevealRequest struct {
	Reason           string `json:"reason"`
	CurrentPassword  string `json:"current_password"`
	ReauthTimestamp  string `json:"reauth_timestamp"`
	ReauthSignature  string `json:"reauth_signature"`
	ReauthAssertion  string `json:"reauth_assertion"`
	PlatformReauthAt string `json:"platform_reauth_at"`
}

type walletRevealResponse struct {
	ID         string `json:"id"`
	Secret     string `json:"secret,omitempty"`
	TOTP       string `json:"totp,omitempty"`
	RevealedAt string `json:"revealed_at"`
}

type walletReauthProofResponse struct {
	ID               string `json:"id"`
	ReauthTimestamp  string `json:"reauth_timestamp"`
	ReauthSignature  string `json:"reauth_signature"`
	ExpiresAt        string `json:"expires_at"`
	ReauthMethodHint string `json:"reauth_method_hint"`
}

// RegisterWalletRoutesWithConfig exposes server-side wallet secret reveal endpoints.
func RegisterWalletRoutesWithConfig(r *httpx.Router, app core.App, cfg WalletRouteConfig) {
	h := walletRouteHandlers{
		app: app,
		wst: cfg.Store,
		ast: cfg.Activity,
	}
	r.GET("/api/v1/wallet", h.list)
	r.POST("/api/v1/wallet", h.create)
	r.PATCH("/api/v1/wallet/{id}", h.update)
	r.DELETE("/api/v1/wallet/{id}", h.delete)
	r.POST("/api/v1/wallet/{id}/reauth-proof", h.reauthProof)
	r.POST("/api/v1/wallet/{id}/reveal", h.reveal)
}

func (h walletRouteHandlers) list(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	tenantID := requestTenantID(e, ownerID)
	stackID := strings.TrimSpace(e.Request.URL.Query().Get("stack_id"))
	items, err := h.wst.ListWalletItems(e.Request.Context(), tenantID, stackID)
	if err != nil {
		return h.walletStoreError(e, err, "failed to list wallet items")
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, walletItemResponse(item, false))
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"items": out})
}

func (h walletRouteHandlers) create(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	payload := map[string]any{}
	if bindErr := e.BindBody(&payload); bindErr != nil {
		return httpx.BadRequest(e, "invalid request body", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	item := walletItemFromPayload(tenantID, ownerID, firstNonEmptyWallet(walletString(payload["id"]), uuid.NewString()), payload)
	saved, err := h.wst.UpsertWalletItem(e.Request.Context(), item)
	if err != nil {
		return h.walletStoreError(e, err, "failed to create wallet item")
	}
	return httpx.Success(e, http.StatusCreated, walletItemResponse(*saved, false))
}

func (h walletRouteHandlers) update(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	itemID := strings.TrimSpace(e.Request.PathValue("id"))
	if itemID == "" {
		return httpx.BadRequest(e, "wallet item id is required")
	}
	payload := map[string]any{}
	if bindErr := e.BindBody(&payload); bindErr != nil {
		return httpx.BadRequest(e, "invalid request body", nil)
	}

	tenantID := requestTenantID(e, ownerID)
	existing, err := h.wst.GetWalletItem(e.Request.Context(), tenantID, itemID)
	if err != nil {
		return h.walletStoreError(e, err, "wallet item not found")
	}
	if !walletItemOwnedBy(existing.Metadata, ownerID) {
		return httpx.NotFound(e, "wallet item not found")
	}
	merged := cloneWalletMetadata(existing.Metadata)
	for key, value := range payload {
		merged[key] = value
	}
	merged["id"] = itemID
	item := walletItemFromPayload(tenantID, ownerID, itemID, merged)
	if item.InstanceID == "" {
		item.InstanceID = existing.InstanceID
	}
	if item.StackID == "" {
		item.StackID = existing.StackID
	}
	saved, err := h.wst.UpsertWalletItem(e.Request.Context(), item)
	if err != nil {
		return h.walletStoreError(e, err, "failed to update wallet item")
	}
	return httpx.Success(e, http.StatusOK, walletItemResponse(*saved, false))
}

func (h walletRouteHandlers) delete(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	itemID := strings.TrimSpace(e.Request.PathValue("id"))
	if itemID == "" {
		return httpx.BadRequest(e, "wallet item id is required")
	}
	tenantID := requestTenantID(e, ownerID)
	existing, err := h.wst.GetWalletItem(e.Request.Context(), tenantID, itemID)
	if err != nil {
		return h.walletStoreError(e, err, "wallet item not found")
	}
	if !walletItemOwnedBy(existing.Metadata, ownerID) {
		return httpx.NotFound(e, "wallet item not found")
	}
	if err := h.wst.DeleteWalletItem(e.Request.Context(), tenantID, itemID); err != nil {
		return h.walletStoreError(e, err, "failed to delete wallet item")
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"ok": true})
}

func (h walletRouteHandlers) reauthProof(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}

	itemID := strings.TrimSpace(e.Request.PathValue("id"))
	if itemID == "" {
		h.recordWalletRevealAuditRequest(e, "", ownerID, walletRevealActionDenied, walletAuditStatusWarning, "missing wallet item id", nil)
		return httpx.BadRequest(e, "wallet item id is required")
	}

	var req walletRevealRequest
	if bindErr := e.BindBody(&req); bindErr != nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "invalid request body", nil)
		return httpx.BadRequest(e, "invalid request body", nil)
	}
	if h.wst != nil {
		tenantID := requestTenantID(e, ownerID)
		if _, findErr := h.findRevealableWalletItem(e, tenantID, itemID, ownerID, req.Reason); findErr != nil {
			return findErr(e)
		}
	} else if _, findErr := h.findRevealableWalletRecord(itemID, ownerID, req.Reason); findErr != nil {
		return findErr(e)
	}
	if platformErr := h.verifyFreshPlatformReauth(e, ownerID, itemID, req); platformErr != nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, platformErr.Error(), map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(req.Reason),
		})
		return httpx.Forbidden(e, "Fresh platform re-authentication required")
	}

	secret := walletReauthSecret()
	if secret == "" {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusError, "trusted re-authentication secret is not configured", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(req.Reason),
		})
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "trusted re-authentication is not configured", nil)
	}

	now := h.currentTime().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := hex.EncodeToString(signWalletRevealAssertion(ownerID, itemID, timestamp, secret))
	return httpx.Success(e, http.StatusOK, walletReauthProofResponse{
		ID:               itemID,
		ReauthTimestamp:  timestamp,
		ReauthSignature:  signature,
		ExpiresAt:        now.Add(walletReauthWindow).UTC().Format(time.RFC3339),
		ReauthMethodHint: "kombify-cloud",
	})
}

func (h walletRouteHandlers) reveal(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}

	itemID := strings.TrimSpace(e.Request.PathValue("id"))
	if itemID == "" {
		h.recordWalletRevealAuditRequest(e, "", ownerID, walletRevealActionDenied, walletAuditStatusWarning, "missing wallet item id", nil)
		return httpx.BadRequest(e, "wallet item id is required")
	}

	var req walletRevealRequest
	if bindErr := e.BindBody(&req); bindErr != nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "invalid request body", nil)
		return httpx.BadRequest(e, "invalid request body", nil)
	}

	if h.wst != nil {
		tenantID := requestTenantID(e, ownerID)
		item, findErr := h.findRevealableWalletItem(e, tenantID, itemID, ownerID, req.Reason)
		if findErr != nil {
			return findErr(e)
		}
		if reauthErr := h.verifyWalletRevealReauth(e, ownerID, itemID, req); reauthErr != nil {
			h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, reauthErr.Error(), map[string]any{
				walletAuditReasonKey: normalizeWalletRevealReason(req.Reason),
			})
			return httpx.Forbidden(e, "Fresh re-authentication required")
		}
		return h.revealWalletValues(e, itemID, ownerID, req.Reason, item.ID, walletString(item.Metadata["secret"]), walletString(item.Metadata["totp"]))
	}

	record, findErr := h.findRevealableWalletRecord(itemID, ownerID, req.Reason)
	if findErr != nil {
		return findErr(e)
	}
	if reauthErr := h.verifyWalletRevealReauth(e, ownerID, itemID, req); reauthErr != nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, reauthErr.Error(), map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(req.Reason),
		})
		return httpx.Forbidden(e, "Fresh re-authentication required")
	}
	return h.revealWalletValues(e, itemID, ownerID, req.Reason, record.Id, record.GetString("secret"), record.GetString("totp"))
}

func (h walletRouteHandlers) revealWalletValues(e *httpx.Event, itemID, ownerID, reason, responseID, secretValue, totpValue string) error {
	secret, err := walletRevealField(secretValue)
	if err != nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusError, "failed to decrypt wallet secret", nil)
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "failed to reveal wallet item", nil)
	}
	totp, err := walletRevealField(totpValue)
	if err != nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusError, "failed to decrypt wallet totp", nil)
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "failed to reveal wallet item", nil)
	}

	revealedAt := h.currentTime().UTC().Format(time.RFC3339)
	h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealAction, walletAuditStatusSuccess, "wallet item revealed", map[string]any{
		walletAuditReasonKey: normalizeWalletRevealReason(reason),
		"has_secret":         strings.TrimSpace(secret) != "",
		"has_totp":           strings.TrimSpace(totp) != "",
	})

	return httpx.Success(e, http.StatusOK, walletRevealResponse{
		ID:         responseID,
		Secret:     secret,
		TOTP:       totp,
		RevealedAt: revealedAt,
	})
}

func (h walletRouteHandlers) verifyWalletRevealReauth(e *httpx.Event, ownerID, itemID string, req walletRevealRequest) error {
	// Password-based re-auth (PocketBase user-record ValidatePassword) is retired:
	// Auth0 owns credentials post-PB. Re-authentication is proven via the trusted
	// HMAC signature/timestamp assertion below (or the platform reauth proof).

	timestamp := firstNonEmptyWallet(
		req.ReauthTimestamp,
		req.PlatformReauthAt,
		e.Request.Header.Get("X-Kombify-Reauth-Timestamp"),
		e.Request.Header.Get("X-TechStack-Reauth-Timestamp"),
	)
	signature := firstNonEmptyWallet(
		req.ReauthSignature,
		req.ReauthAssertion,
		e.Request.Header.Get("X-Kombify-Reauth-Signature"),
		e.Request.Header.Get("X-TechStack-Reauth-Signature"),
	)
	if timestamp == "" || signature == "" {
		return errors.New("fresh re-authentication proof missing")
	}

	secret := walletReauthSecret()
	if secret == "" {
		return errors.New("trusted re-authentication secret is not configured")
	}

	if err := verifyWalletSignature(ownerID, itemID, timestamp, signature, secret, walletRevealAssertionPurpose, h.currentTime()); err != nil {
		return fmt.Errorf("trusted re-authentication proof invalid: %w", err)
	}
	return nil
}

func (h walletRouteHandlers) verifyFreshPlatformReauth(e *httpx.Event, ownerID, itemID string, req walletRevealRequest) error {
	timestamp := firstNonEmptyWallet(
		req.ReauthTimestamp,
		req.PlatformReauthAt,
		e.Request.Header.Get("X-Kombify-Reauth-Timestamp"),
		e.Request.Header.Get("X-TechStack-Reauth-Timestamp"),
	)
	signature := firstNonEmptyWallet(
		req.ReauthSignature,
		req.ReauthAssertion,
		e.Request.Header.Get("X-Kombify-Reauth-Signature"),
		e.Request.Header.Get("X-TechStack-Reauth-Signature"),
	)
	if timestamp == "" || signature == "" {
		return errors.New("fresh platform re-authentication proof missing")
	}
	secret := walletReauthSecret()
	if secret == "" {
		return errors.New("trusted re-authentication secret is not configured")
	}
	if err := verifyWalletSignature(ownerID, itemID, timestamp, signature, secret, walletPlatformReauthAssertionPurpose, h.currentTime()); err != nil {
		return fmt.Errorf("trusted platform re-authentication proof invalid: %w", err)
	}
	return nil
}

func verifyWalletSignature(ownerID, itemID, timestamp, signature, secret, purpose string, now time.Time) error {
	at, err := parseWalletReauthTimestamp(timestamp)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	if diff := now.Sub(at); diff > walletReauthWindow || diff < -walletReauthWindow {
		return fmt.Errorf("reauthentication proof outside %s window", walletReauthWindow)
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(signature), "sha256="))
	if err != nil {
		return fmt.Errorf("invalid signature encoding")
	}
	if hmac.Equal(provided, signWalletAssertion(purpose, ownerID, itemID, strings.TrimSpace(timestamp), secret)) {
		return nil
	}
	return fmt.Errorf("signature mismatch")
}

func signWalletRevealAssertion(ownerID, itemID, timestamp, secret string) []byte {
	return signWalletAssertion(walletRevealAssertionPurpose, ownerID, itemID, timestamp, secret)
}

func signWalletAssertion(purpose, ownerID, itemID, timestamp, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(purpose))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(ownerID))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(itemID))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(timestamp))
	return mac.Sum(nil)
}

func parseWalletReauthTimestamp(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, fmt.Errorf("reauthentication timestamp missing")
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unix, 0).UTC(), nil
	}
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid reauthentication timestamp")
	}
	return at.UTC(), nil
}

func walletReauthSecret() string {
	if secret := strings.TrimSpace(os.Getenv("TECHSTACK_REAUTH_SECRET")); secret != "" {
		return secret
	}
	return strings.TrimSpace(os.Getenv("EDGE_AUTH_SECRET"))
}

func walletRevealField(value string) (string, error) {
	return auth.DecryptIfNeeded(auth.GetEncryptor(), value)
}

func walletRecordRevealable(record *core.Record) bool {
	if record == nil {
		return false
	}
	return record.GetBool("revealable") ||
		record.GetBool("has_secret") ||
		record.GetBool("has_totp") ||
		strings.TrimSpace(record.GetString("secret")) != "" ||
		strings.TrimSpace(record.GetString("totp")) != "" ||
		strings.TrimSpace(record.GetString("item_class")) == "recovery"
}

func walletItemRevealable(item *controlplane.WalletItem) bool {
	if item == nil {
		return false
	}
	metadata := item.Metadata
	return walletBool(metadata["revealable"]) ||
		walletBool(metadata["has_secret"]) ||
		walletBool(metadata["has_totp"]) ||
		strings.TrimSpace(walletString(metadata["secret"])) != "" ||
		strings.TrimSpace(walletString(metadata["totp"])) != "" ||
		strings.TrimSpace(walletString(metadata["item_class"])) == "recovery"
}

func (h walletRouteHandlers) findRevealableWalletItem(e *httpx.Event, tenantID, itemID, ownerID, reason string) (*controlplane.WalletItem, func(*httpx.Event) error) {
	item, err := h.wst.GetWalletItem(e.Request.Context(), tenantID, itemID)
	if err != nil || item == nil {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "wallet item not found", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(reason),
		})
		return nil, func(e *httpx.Event) error { return httpx.NotFound(e, "wallet item not found") }
	}
	if !walletItemOwnedBy(item.Metadata, ownerID) {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "wallet item owned by another user", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(reason),
		})
		return nil, func(e *httpx.Event) error { return httpx.NotFound(e, "wallet item not found") }
	}
	if !walletItemRevealable(item) {
		h.recordWalletRevealAuditRequest(e, itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "wallet item is not revealable", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(reason),
		})
		return nil, func(e *httpx.Event) error { return httpx.Forbidden(e, "Wallet item is not revealable") }
	}
	return item, nil
}

func (h walletRouteHandlers) findRevealableWalletRecord(itemID, ownerID, reason string) (*core.Record, func(*httpx.Event) error) {
	record, err := h.app.FindRecordById("wallet", itemID)
	if err != nil || record == nil {
		h.recordWalletRevealAudit(itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "wallet item not found", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(reason),
		})
		return nil, func(e *httpx.Event) error { return httpx.NotFound(e, "wallet item not found") }
	}
	if strings.TrimSpace(record.GetString("owner_id")) != ownerID {
		h.recordWalletRevealAudit(itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "wallet item owned by another user", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(reason),
		})
		return nil, func(e *httpx.Event) error { return httpx.NotFound(e, "wallet item not found") }
	}
	if !walletRecordRevealable(record) {
		h.recordWalletRevealAudit(itemID, ownerID, walletRevealActionDenied, walletAuditStatusWarning, "wallet item is not revealable", map[string]any{
			walletAuditReasonKey: normalizeWalletRevealReason(reason),
		})
		return nil, func(e *httpx.Event) error { return httpx.Forbidden(e, "Wallet item is not revealable") }
	}
	return record, nil
}

func (h walletRouteHandlers) walletStoreError(e *httpx.Event, err error, fallback string) error {
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return httpx.NotFound(e, fallback)
	case errors.Is(err, controlplane.ErrConflict):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, fallback, nil)
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, fallback, nil)
	}
}

func (h walletRouteHandlers) recordWalletRevealAuditRequest(e *httpx.Event, itemID, ownerID, action, status, details string, metadata map[string]any) {
	if h.ast == nil || e == nil || e.Request == nil {
		h.recordWalletRevealAudit(itemID, ownerID, action, status, details, metadata)
		return
	}
	tenantID := requestTenantID(e, ownerID)
	eventID := "wallet:" + firstNonEmptyWallet(itemID, "unknown") + ":" + action + ":" + strconv.FormatInt(h.currentTime().UTC().UnixNano(), 10)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["resource_type"] = "wallet"
	metadata["resource_id"] = itemID
	_, err := h.ast.AppendActivity(e.Request.Context(), controlplane.ActivityEvent{
		ID:             eventID,
		TenantID:       tenantID,
		ActorSubjectID: ownerID,
		Action:         action,
		Category:       "wallet",
		Severity:       normalizeWalletAuditStatus(status),
		Message:        details,
		Details:        metadata,
	})
	if err != nil {
		h.recordWalletRevealAudit(itemID, ownerID, action, status, details, metadata)
	}
}

func walletItemFromPayload(tenantID, ownerID, itemID string, payload map[string]any) controlplane.WalletItem {
	metadata := cloneWalletMetadata(payload)
	metadata["id"] = itemID
	if strings.TrimSpace(walletString(metadata["owner_id"])) == "" {
		metadata["owner_id"] = ownerID
	}
	if strings.TrimSpace(walletString(metadata["has_secret"])) == "" && strings.TrimSpace(walletString(metadata["secret"])) != "" {
		metadata["has_secret"] = true
	}
	if strings.TrimSpace(walletString(metadata["has_totp"])) == "" && strings.TrimSpace(walletString(metadata["totp"])) != "" {
		metadata["has_totp"] = true
	}

	return controlplane.WalletItem{
		ID:          itemID,
		TenantID:    tenantID,
		InstanceID:  walletString(metadata["instance_id"]),
		StackID:     walletString(metadata["stack_id"]),
		ItemType:    firstNonEmptyWallet(walletString(metadata["kind"]), walletString(metadata["item_type"]), "other"),
		Provider:    firstNonEmptyWallet(walletString(metadata["source_type"]), walletString(metadata["provider"])),
		ExternalRef: firstNonEmptyWallet(walletString(metadata["source_ref"]), walletString(metadata["service_id"])),
		Metadata:    metadata,
	}
}

func walletItemResponse(item controlplane.WalletItem, includeSecret bool) map[string]any {
	out := cloneWalletMetadata(item.Metadata)
	out["id"] = item.ID
	out["kind"] = firstNonEmptyWallet(walletString(out["kind"]), item.ItemType, "other")
	out["stack_id"] = firstNonEmptyWallet(walletString(out["stack_id"]), item.StackID)
	out["source_type"] = firstNonEmptyWallet(walletString(out["source_type"]), item.Provider)
	out["source_ref"] = firstNonEmptyWallet(walletString(out["source_ref"]), item.ExternalRef)
	if item.CreatedAt.IsZero() {
		delete(out, "created")
	} else {
		out["created"] = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	if item.UpdatedAt.IsZero() {
		delete(out, "updated")
	} else {
		out["updated"] = item.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !includeSecret {
		delete(out, "secret")
		delete(out, "totp")
	}
	return out
}

func walletItemOwnedBy(metadata map[string]any, ownerID string) bool {
	storedOwner := strings.TrimSpace(walletString(metadata["owner_id"]))
	return storedOwner == "" || storedOwner == ownerID
}

func cloneWalletMetadata(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func walletString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func walletBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func (h walletRouteHandlers) recordWalletRevealAudit(itemID, ownerID, action, status, details string, metadata map[string]any) {
	if h.app == nil {
		return
	}
	collection, err := h.app.FindCollectionByNameOrId("activity_log")
	if err != nil || collection == nil {
		return
	}

	record := core.NewRecord(collection)
	setWalletAuditFieldIfExists(record, collection, "action", action)
	setWalletAuditFieldIfExists(record, collection, "details", details)
	setWalletAuditFieldIfExists(record, collection, "metadata", metadata)
	setWalletAuditFieldIfExists(record, collection, "stack_id", "")
	setWalletAuditFieldIfExists(record, collection, "user_id", ownerID)
	setWalletAuditFieldIfExists(record, collection, "status", normalizeWalletAuditStatus(status))
	setWalletAuditFieldIfExists(record, collection, "target", firstNonEmptyWallet(itemID, "wallet"))
	setWalletAuditFieldIfExists(record, collection, "actor", ownerID)
	setWalletAuditFieldIfExists(record, collection, "resource_type", "wallet")
	setWalletAuditFieldIfExists(record, collection, "resource_id", itemID)
	if err := h.app.Save(record); err != nil {
		h.app.Logger().Warn("failed to persist wallet audit record", "error", err)
	}
}

func setWalletAuditFieldIfExists(record *core.Record, collection *core.Collection, field string, value any) {
	if record == nil || collection == nil || value == nil {
		return
	}
	if collection.Fields.GetByName(field) == nil {
		return
	}
	record.Set(field, value)
}

func normalizeWalletAuditStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case walletAuditStatusSuccess, walletAuditStatusWarning, walletAuditStatusError, walletAuditStatusInfo:
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return walletAuditStatusInfo
	}
}

func normalizeWalletRevealReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 200 {
		return reason[:200]
	}
	return reason
}

func (h walletRouteHandlers) currentTime() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func firstNonEmptyWallet(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
