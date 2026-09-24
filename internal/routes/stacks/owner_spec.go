package stacks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	tsauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

const (
	ownerSpecBootstrapTokenSecretEnv = "TECHSTACK_BOOTSTRAP_TOKEN_SECRET" // #nosec G101 -- environment variable name, not a secret value.
	ownerSpecBootstrapTokenTTL       = 15 * time.Minute
	ownerSpecBootstrapTokenIssuer    = "techstack.owner-spec"
	ownerSpecBootstrapTokenAudience  = "stackkit-admin-bootstrap"
	ownerSpecReadScope               = "read:owner-spec"
	ownerSpecAuditReasonKey          = "reason"

	ownerSpecActionTokenIssued = "owner_spec_token_issued"
	ownerSpecActionRead        = "owner_spec_read"
	ownerSpecActionDenied      = "owner_spec_denied"
)

var (
	errOwnerSpecTokenForbidden = errors.New("owner spec token forbidden")
	errOwnerSpecTokenInvalid   = errors.New("owner spec token invalid")

	ownerSpecProcessSecretOnce sync.Once
	ownerSpecProcessSecret     []byte
)

type ownerSpecBootstrapClaims struct {
	StackID  string   `json:"stack_id"`
	OwnerID  string   `json:"owner_id"`
	TenantID string   `json:"tenant_id,omitempty"`
	Scopes   []string `json:"scopes"`
	jwt.RegisteredClaims
}

type ownerSpecBootstrapAccess struct {
	Token     string
	Endpoint  string
	ExpiresAt time.Time
}

// complete reports whether the access carries everything a runtime bootstrap
// needs (token, endpoint, and a non-zero expiry). It encapsulates the guard so
// callers stay free of a multi-clause complex conditional.
func (a ownerSpecBootstrapAccess) complete() bool {
	return a.Token != "" && a.Endpoint != "" && !a.ExpiresAt.IsZero()
}

type ownerSpecResponse struct {
	StackID   string            `json:"stack_id"`
	Identity  ownerSpecIdentity `json:"identity"`
	Scopes    []string          `json:"scopes"`
	ExpiresAt string            `json:"expires_at"`
}

type ownerSpecIdentity struct {
	Owner    ownerSpecOwner    `json:"owner"`
	Recovery ownerSpecRecovery `json:"recovery"`
}

type ownerSpecOwner struct {
	Source string `json:"source"`
	// SourceOrigin carries the provenance when the wire source is mapped onto
	// "local" (e.g. an owner seeded from a linked kombify Cloud profile).
	SourceOrigin string `json:"source_origin,omitempty"`
	Email        string `json:"email,omitempty"`
	Username     string `json:"username,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
}

type ownerSpecRecovery struct {
	PassphraseHash        string `json:"passphrase_hash"`
	PassphraseHashPresent bool   `json:"passphraseHashPresent"`
}

func (h crudRouteHandlers) ownerSpec(e *httpx.Event) error {
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return httpx.BadRequest(e, "Missing stack id")
	}

	rawToken := bearerTokenFromRequest(e.Request)
	if rawToken == "" {
		h.recordOwnerSpecAudit(e.Request.Context(), "", stackID, "", ownerSpecActionDenied, "error", "Owner spec denied: missing bootstrap token", map[string]any{
			ownerSpecAuditReasonKey: "missing_token",
		})
		return httpx.Unauthorized(e, "Bootstrap token required")
	}

	now := time.Now().UTC()
	claims, err := verifyOwnerSpecBootstrapToken(rawToken, stackID, now)
	if err != nil {
		h.recordOwnerSpecAudit(e.Request.Context(), "", stackID, "", ownerSpecActionDenied, "error", "Owner spec denied: invalid bootstrap token", map[string]any{
			ownerSpecAuditReasonKey: ownerSpecTokenErrorReason(err),
		})
		if errors.Is(err, errOwnerSpecTokenForbidden) {
			return httpx.Forbidden(e, "Bootstrap token does not match this stack")
		}
		return httpx.Unauthorized(e, "Invalid or expired bootstrap token")
	}

	if claims.TenantID == "" {
		h.recordOwnerSpecAudit(e.Request.Context(), "", stackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec denied: tenant-bound token required", map[string]any{
			ownerSpecAuditReasonKey: "missing_tenant",
		})
		return httpx.Unauthorized(e, "Invalid or expired bootstrap token")
	}
	_, hasTokenStore := h.walletStore.(controlplane.OwnerSpecTokenStore)
	if h.stackStore == nil || h.walletStore == nil || !hasTokenStore {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Owner spec authority is temporarily unavailable", map[string]any{
			"reason_code": "owner_spec_authority_unavailable", "retryable": true,
		})
	}
	return h.ownerSpecFromControlPlane(e, claims, now)
}

func (h crudRouteHandlers) issueOwnerSpecBootstrapAccessForTenant(ctx context.Context, tenantID, stackID, ownerID string, now time.Time) (ownerSpecBootstrapAccess, error) {
	token, expiresAt, err := issueOwnerSpecBootstrapTokenForTenant(tenantID, stackID, ownerID, now)
	if err != nil {
		return ownerSpecBootstrapAccess{}, err
	}
	claims, verifyErr := verifyOwnerSpecBootstrapToken(token, stackID, now)
	if verifyErr != nil {
		return ownerSpecBootstrapAccess{}, verifyErr
	}
	if storeErr := h.storeOwnerSpecBootstrapToken(ctx, claims, expiresAt); storeErr != nil {
		return ownerSpecBootstrapAccess{}, storeErr
	}
	access := ownerSpecBootstrapAccess{
		Token:     token,
		Endpoint:  ownerSpecEndpoint(stackID),
		ExpiresAt: expiresAt,
	}
	h.recordOwnerSpecAudit(ctx, claims.TenantID, stackID, ownerID, ownerSpecActionTokenIssued, "info", "Owner spec bootstrap token issued", map[string]any{
		"scopes":     []string{ownerSpecReadScope},
		"expires_at": expiresAt.Format(time.RFC3339),
	})
	return access, nil
}

func issueOwnerSpecBootstrapTokenForTenant(tenantID, stackID, ownerID string, now time.Time) (string, time.Time, error) {
	stackID = strings.TrimSpace(stackID)
	ownerID = strings.TrimSpace(ownerID)
	if stackID == "" || ownerID == "" {
		return "", time.Time{}, fmt.Errorf("stack id and owner id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	expiresAt := now.Add(ownerSpecBootstrapTokenTTL)
	jti, err := randomOwnerSpecTokenID()
	if err != nil {
		return "", time.Time{}, err
	}
	claims := ownerSpecBootstrapClaims{
		StackID:  stackID,
		OwnerID:  ownerID,
		TenantID: strings.TrimSpace(tenantID),
		Scopes:   []string{ownerSpecReadScope},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    ownerSpecBootstrapTokenIssuer,
			Subject:   "owner-spec:" + stackID,
			Audience:  jwt.ClaimStrings{ownerSpecBootstrapTokenAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        jti,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(ownerSpecBootstrapTokenSecret())
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

func verifyOwnerSpecBootstrapToken(rawToken, expectedStackID string, now time.Time) (*ownerSpecBootstrapClaims, error) {
	rawToken = strings.TrimSpace(rawToken)
	expectedStackID = strings.TrimSpace(expectedStackID)
	if rawToken == "" || expectedStackID == "" {
		return nil, errOwnerSpecTokenInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	claims := &ownerSpecBootstrapClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuer(ownerSpecBootstrapTokenIssuer),
		jwt.WithAudience(ownerSpecBootstrapTokenAudience),
		jwt.WithTimeFunc(func() time.Time { return now.UTC() }),
	)
	token, err := parser.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (interface{}, error) {
		return ownerSpecBootstrapTokenSecret(), nil
	})
	if err != nil || token == nil || !token.Valid {
		return nil, fmt.Errorf("%w: %v", errOwnerSpecTokenInvalid, err)
	}
	if claims.StackID != expectedStackID {
		return nil, errOwnerSpecTokenForbidden
	}
	if !hasOwnerSpecScope(claims.Scopes) {
		return nil, errOwnerSpecTokenForbidden
	}
	if strings.TrimSpace(claims.OwnerID) == "" {
		return nil, errOwnerSpecTokenInvalid
	}
	if strings.TrimSpace(claims.ID) == "" {
		return nil, errOwnerSpecTokenInvalid
	}
	return claims, nil
}

func randomOwnerSpecTokenID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func ownerSpecTokenHash(jti string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(jti)))
	return hex.EncodeToString(sum[:])
}

func (h crudRouteHandlers) storeOwnerSpecBootstrapToken(ctx context.Context, claims *ownerSpecBootstrapClaims, expiresAt time.Time) error {
	if claims == nil || claims.TenantID == "" {
		return fmt.Errorf("tenant-bound owner spec claims required")
	}
	store, ok := h.walletStore.(controlplane.OwnerSpecTokenStore)
	if !ok || store == nil {
		return fmt.Errorf("canonical owner spec token store unavailable")
	}
	return store.StoreOwnerSpecToken(ctx, controlplane.OwnerSpecToken{
		TokenHash: ownerSpecTokenHash(claims.ID), TenantID: claims.TenantID,
		StackID: claims.StackID, OwnerID: claims.OwnerID, ExpiresAt: expiresAt,
	})
}

func (h crudRouteHandlers) consumeOwnerSpecBootstrapToken(ctx context.Context, claims *ownerSpecBootstrapClaims, now time.Time) error {
	if claims == nil || claims.TenantID == "" {
		return errOwnerSpecTokenInvalid
	}
	store, ok := h.walletStore.(controlplane.OwnerSpecTokenStore)
	if !ok || store == nil {
		return errOwnerSpecTokenInvalid
	}
	err := store.ConsumeOwnerSpecToken(ctx, controlplane.OwnerSpecToken{
		TokenHash: ownerSpecTokenHash(claims.ID), TenantID: claims.TenantID,
		StackID: claims.StackID, OwnerID: claims.OwnerID,
	}, now)
	if errors.Is(err, controlplane.ErrNotFound) {
		return errOwnerSpecTokenForbidden
	}
	return err
}

func (h crudRouteHandlers) ownerSpecFromControlPlane(e *httpx.Event, claims *ownerSpecBootstrapClaims, now time.Time) error {
	stack, err := h.stackStore.GetStack(e.Request.Context(), claims.TenantID, claims.StackID)
	if err != nil || stack == nil || stack.OwnerSubjectID != claims.OwnerID {
		h.recordOwnerSpecAudit(e.Request.Context(), claims.TenantID, claims.StackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec denied: stack owner mismatch", map[string]any{
			ownerSpecAuditReasonKey: "owner_mismatch",
		})
		return httpx.Forbidden(e, "Bootstrap token does not match this stack owner")
	}
	if consumeErr := h.consumeOwnerSpecBootstrapToken(e.Request.Context(), claims, now); consumeErr != nil {
		h.recordOwnerSpecAudit(e.Request.Context(), claims.TenantID, claims.StackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec denied: bootstrap token already used or unavailable", map[string]any{
			ownerSpecAuditReasonKey: ownerSpecTokenErrorReason(consumeErr),
		})
		if errors.Is(consumeErr, errOwnerSpecTokenInvalid) {
			return httpx.Unauthorized(e, "Invalid or expired bootstrap token")
		}
		return httpx.Forbidden(e, "Bootstrap token has already been used")
	}
	response, responseErr := h.ownerSpecResponseFromControlPlane(e.Request.Context(), stack, claims, now)
	if responseErr != nil {
		h.recordOwnerSpecAudit(e.Request.Context(), claims.TenantID, claims.StackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec unavailable", map[string]any{
			ownerSpecAuditReasonKey: responseErr.Error(),
		})
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Owner spec is not available for this stack", nil)
	}
	h.recordOwnerSpecAudit(e.Request.Context(), claims.TenantID, claims.StackID, claims.OwnerID, ownerSpecActionRead, "success", "Owner spec fetched by StackKit bootstrap", map[string]any{
		"scopes": claims.Scopes, "expires_at": response.ExpiresAt,
	})
	return httpx.Success(e, http.StatusOK, response)
}

func (h crudRouteHandlers) ownerSpecResponseFromControlPlane(ctx context.Context, stack *controlplane.Stack, claims *ownerSpecBootstrapClaims, now time.Time) (ownerSpecResponse, error) {
	if stack == nil || claims == nil || h.walletStore == nil {
		return ownerSpecResponse{}, fmt.Errorf("missing stack, claims, or wallet store")
	}
	bootstrap, ok := ownerBootstrapFromRequest(normalizedCreateStackRequest{UserConfig: stack.Config})
	if !ok || !ownerSourceSeedsPocketID(bootstrap.Source) || bootstrap.Email == "" || bootstrap.Username == "" {
		return ownerSpecResponse{}, fmt.Errorf("missing local owner bootstrap")
	}
	recovery, err := h.walletStore.GetWalletItem(ctx, stack.TenantID, fmt.Sprintf("%s:%s", stack.ID, recoveryWalletServiceID))
	if err != nil || recovery == nil {
		return ownerSpecResponse{}, fmt.Errorf("missing recovery wallet entry")
	}
	secret := strings.TrimSpace(stringFromAny(recovery.Metadata["secret"]))
	recoveryHash, decryptErr := tsauth.DecryptIfNeeded(tsauth.GetEncryptor(), secret)
	if decryptErr != nil || !strings.HasPrefix(strings.TrimSpace(recoveryHash), "$argon2id$") {
		return ownerSpecResponse{}, fmt.Errorf("recovery hash unavailable")
	}
	expiresAt := ""
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time.UTC().Format(time.RFC3339)
	} else if !now.IsZero() {
		expiresAt = now.UTC().Format(time.RFC3339)
	}
	return ownerSpecResponse{
		StackID: stack.ID,
		Identity: ownerSpecIdentity{
			Owner:    ownerSpecOwner{Source: ownerSourceLocal, SourceOrigin: ownerSourceOrigin(bootstrap.Source), Email: bootstrap.Email, Username: bootstrap.Username, DisplayName: bootstrap.DisplayName},
			Recovery: ownerSpecRecovery{PassphraseHash: strings.TrimSpace(recoveryHash), PassphraseHashPresent: true},
		},
		Scopes: []string{ownerSpecReadScope}, ExpiresAt: expiresAt,
	}, nil
}

func ownerSpecBootstrapTokenSecret() []byte {
	if configured := strings.TrimSpace(os.Getenv(ownerSpecBootstrapTokenSecretEnv)); configured != "" {
		return []byte(configured)
	}
	ownerSpecProcessSecretOnce.Do(func() {
		ownerSpecProcessSecret = make([]byte, 32)
		if _, err := rand.Read(ownerSpecProcessSecret); err != nil {
			ownerSpecProcessSecret = []byte("techstack-owner-spec-dev-fallback")
		}
	})
	return ownerSpecProcessSecret
}

func ownerSpecResponseFields(access ownerSpecBootstrapAccess) map[string]any {
	if access.Token == "" {
		return nil
	}
	return map[string]any{
		"bootstrap_token":            access.Token,
		"bootstrap_token_expires_at": access.ExpiresAt.UTC().Format(time.RFC3339),
		"owner_spec_endpoint":        access.Endpoint,
		"owner_spec_scopes":          []string{ownerSpecReadScope},
	}
}

func addOwnerSpecResponseFields(response map[string]any, access ownerSpecBootstrapAccess) map[string]any {
	if response == nil {
		response = map[string]any{}
	}
	for key, value := range ownerSpecResponseFields(access) {
		response[key] = value
	}
	return response
}

func ownerSpecEndpoint(stackID string) string {
	return "/api/v1/stacks/" + strings.TrimSpace(stackID) + "/owner-spec"
}

func bearerTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if value == "" {
		return ""
	}
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func hasOwnerSpecScope(scopes []string) bool {
	for _, scope := range scopes {
		if strings.TrimSpace(scope) == ownerSpecReadScope {
			return true
		}
	}
	return false
}

func ownerSpecTokenErrorReason(err error) string {
	switch {
	case errors.Is(err, errOwnerSpecTokenForbidden):
		return "forbidden"
	case errors.Is(err, errOwnerSpecTokenInvalid):
		return "invalid_or_expired"
	default:
		return "unknown"
	}
}

func (h crudRouteHandlers) recordOwnerSpecAudit(ctx context.Context, tenantID, stackID, ownerID, action, status, details string, metadata map[string]any) {
	if h.activityStore == nil || strings.TrimSpace(tenantID) == "" {
		return
	}
	metadata = cloneMapForMutation(metadata)
	metadata["resource_type"] = "stack"
	metadata["resource_id"] = stackID
	_, err := h.activityStore.AppendActivity(ctx, controlplane.ActivityEvent{
		ID: uuid.NewString(), TenantID: tenantID, StackID: stackID,
		ActorSubjectID: ownerID, Action: action, Category: "owner-spec",
		Severity: normalizeOwnerSpecAuditStatus(status), Message: details, Details: metadata,
	})
	if err != nil {
		logger.Default().Warn("owner_spec_audit_failed", "stack_id", stackID, "tenant_id", tenantID, "error", err)
	}
}

func normalizeOwnerSpecAuditStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success", "warning", "error", "info":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "info"
	}
}
