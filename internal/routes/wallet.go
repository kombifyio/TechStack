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

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/google/uuid"
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
	wst       controlplane.WalletStore
	ast       controlplane.ActivityStore
	encryptor *auth.SecretEncryptor
	now       func() time.Time
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
func RegisterWalletRoutesWithConfig(r *httpx.Router, cfg WalletRouteConfig) {
	h := walletRouteHandlers{
		wst:       cfg.Store,
		ast:       cfg.Activity,
		encryptor: auth.GetEncryptor(),
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
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.list")
	if tenantErr != nil {
		return tenantErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	stackID := strings.TrimSpace(e.Request.URL.Query().Get("stack_id"))
	items, err := h.wst.ListWalletItems(e.Request.Context(), tenantID, stackID)
	if err != nil {
		return h.walletStoreError(e, err, "failed to list wallet items")
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if !walletItemOwnedBy(item.Metadata, ownerID) {
			continue
		}
		out = append(out, walletItemResponse(item, false))
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"items": out})
}

func (h walletRouteHandlers) create(e *httpx.Event) error {
	ownerID, authErr := requireAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.create")
	if tenantErr != nil {
		return tenantErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	payload := map[string]any{}
	if bindErr := e.BindBody(&payload); bindErr != nil {
		return httpx.BadRequest(e, "invalid request body", nil)
	}
	item, err := walletItemFromPayload(tenantID, ownerID, firstNonEmptyWallet(walletString(payload["id"]), uuid.NewString()), payload, h.encryptor)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "failed to encrypt wallet secret", nil)
	}
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
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.update")
	if tenantErr != nil {
		return tenantErr
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
	item, err := walletItemFromPayload(tenantID, ownerID, itemID, merged, h.encryptor)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "failed to encrypt wallet secret", nil)
	}
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
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.delete")
	if tenantErr != nil {
		return tenantErr
	}
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet store is not configured", nil)
	}

	itemID := strings.TrimSpace(e.Request.PathValue("id"))
	if itemID == "" {
		return httpx.BadRequest(e, "wallet item id is required")
	}
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
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.reauth-proof")
	if tenantErr != nil {
		return tenantErr
	}
	if h.wst == nil || h.ast == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet custody is not configured", nil)
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
	if _, findErr := h.findRevealableWalletItem(e, tenantID, itemID, ownerID, req.Reason); findErr != nil {
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
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.reveal")
	if tenantErr != nil {
		return tenantErr
	}
	if h.wst == nil || h.ast == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wallet custody is not configured", nil)
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
	if err := h.appendWalletRevealAudit(e, itemID, ownerID, walletRevealAction, walletAuditStatusSuccess, "wallet item revealed", map[string]any{
		walletAuditReasonKey: normalizeWalletRevealReason(reason),
		"has_secret":         strings.TrimSpace(secret) != "",
		"has_totp":           strings.TrimSpace(totp) != "",
	}); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "failed to audit wallet reveal", nil)
	}

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
	_ = h.appendWalletRevealAudit(e, itemID, ownerID, action, status, details, metadata)
}

func (h walletRouteHandlers) appendWalletRevealAudit(e *httpx.Event, itemID, ownerID, action, status, details string, metadata map[string]any) error {
	if h.ast == nil || e == nil || e.Request == nil {
		return errors.New("wallet activity store is not configured")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wallet.audit")
	if tenantErr != nil {
		return tenantErr
	}
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
	return err
}

func walletItemFromPayload(tenantID, ownerID, itemID string, payload map[string]any, encryptor *auth.SecretEncryptor) (controlplane.WalletItem, error) {
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
	for _, field := range []string{"secret", "totp"} {
		encrypted, err := auth.EncryptIfNeeded(encryptor, walletString(metadata[field]))
		if err != nil {
			return controlplane.WalletItem{}, fmt.Errorf("encrypt %s: %w", field, err)
		}
		if encrypted != "" || metadata[field] != nil {
			metadata[field] = encrypted
		}
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
	}, nil
}

func walletItemResponse(item controlplane.WalletItem, includeSecret bool) map[string]any {
	out := cloneWalletMetadata(item.Metadata)
	out["id"] = item.ID
	out["kind"] = firstNonEmptyWallet(walletString(out["kind"]), item.ItemType, "other")
	kitDeploymentID := firstNonEmptyWallet(walletString(out["kit_deployment_id"]), walletString(out["stack_id"]), item.StackID)
	delete(out, "stack_id")
	if kitDeploymentID != "" {
		out["kit_deployment_id"] = kitDeploymentID
	}
	sourceType := firstNonEmptyWallet(walletString(out["source_type"]), item.Provider)
	if sourceType == "stack" {
		sourceType = "kit_deployment"
	}
	out["source_type"] = sourceType
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
