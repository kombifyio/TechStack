package stacks

import (
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
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	tsauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/httpx"
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

	ownerSpecTokenStatusIssued   = "issued"
	ownerSpecTokenStatusConsumed = "consumed"
)

var (
	errOwnerSpecTokenForbidden = errors.New("owner spec token forbidden")
	errOwnerSpecTokenInvalid   = errors.New("owner spec token invalid")

	ownerSpecProcessSecretOnce sync.Once
	ownerSpecProcessSecret     []byte
)

type ownerSpecBootstrapClaims struct {
	StackID string   `json:"stack_id"`
	OwnerID string   `json:"owner_id"`
	Scopes  []string `json:"scopes"`
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
		h.recordOwnerSpecAudit(stackID, "", ownerSpecActionDenied, "error", "Owner spec denied: missing bootstrap token", map[string]any{
			ownerSpecAuditReasonKey: "missing_token",
		})
		return httpx.Unauthorized(e, "Bootstrap token required")
	}

	now := time.Now().UTC()
	claims, err := verifyOwnerSpecBootstrapToken(rawToken, stackID, now)
	if err != nil {
		h.recordOwnerSpecAudit(stackID, "", ownerSpecActionDenied, "error", "Owner spec denied: invalid bootstrap token", map[string]any{
			ownerSpecAuditReasonKey: ownerSpecTokenErrorReason(err),
		})
		if errors.Is(err, errOwnerSpecTokenForbidden) {
			return httpx.Forbidden(e, "Bootstrap token does not match this stack")
		}
		return httpx.Unauthorized(e, "Invalid or expired bootstrap token")
	}

	stack, err := h.app.FindRecordById("stacks", stackID)
	if err != nil {
		h.recordOwnerSpecAudit(stackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec denied: stack not found", map[string]any{
			ownerSpecAuditReasonKey: "stack_not_found",
		})
		return httpx.NotFound(e, "Stack not found")
	}
	if stack.GetString("owner_id") != claims.OwnerID {
		h.recordOwnerSpecAudit(stackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec denied: owner mismatch", map[string]any{
			ownerSpecAuditReasonKey: "owner_mismatch",
			"token_owner":           claims.OwnerID,
		})
		return httpx.Forbidden(e, "Bootstrap token does not match this stack owner")
	}
	if consumeErr := h.consumeOwnerSpecBootstrapToken(claims, now); consumeErr != nil {
		h.recordOwnerSpecAudit(stackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec denied: bootstrap token already used or unavailable", map[string]any{
			ownerSpecAuditReasonKey: ownerSpecTokenErrorReason(consumeErr),
		})
		if errors.Is(consumeErr, errOwnerSpecTokenInvalid) {
			return httpx.Unauthorized(e, "Invalid or expired bootstrap token")
		}
		return httpx.Forbidden(e, "Bootstrap token has already been used")
	}

	response, responseErr := h.ownerSpecResponse(stack, claims, now)
	if responseErr != nil {
		h.recordOwnerSpecAudit(stackID, claims.OwnerID, ownerSpecActionDenied, "error", "Owner spec unavailable", map[string]any{
			ownerSpecAuditReasonKey: responseErr.Error(),
		})
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Owner spec is not available for this stack", nil)
	}

	h.recordOwnerSpecAudit(stackID, claims.OwnerID, ownerSpecActionRead, "success", "Owner spec fetched by StackKit bootstrap", map[string]any{
		"scopes":     claims.Scopes,
		"expires_at": response.ExpiresAt,
	})
	return httpx.Success(e, http.StatusOK, response)
}

func (h crudRouteHandlers) issueOwnerSpecBootstrapAccess(stackID, ownerID string, now time.Time) (ownerSpecBootstrapAccess, error) {
	token, expiresAt, err := issueOwnerSpecBootstrapToken(stackID, ownerID, now)
	if err != nil {
		return ownerSpecBootstrapAccess{}, err
	}
	claims, verifyErr := verifyOwnerSpecBootstrapToken(token, stackID, now)
	if verifyErr != nil {
		return ownerSpecBootstrapAccess{}, verifyErr
	}
	if storeErr := h.storeOwnerSpecBootstrapToken(claims, expiresAt); storeErr != nil {
		return ownerSpecBootstrapAccess{}, storeErr
	}
	access := ownerSpecBootstrapAccess{
		Token:     token,
		Endpoint:  ownerSpecEndpoint(stackID),
		ExpiresAt: expiresAt,
	}
	h.recordOwnerSpecAudit(stackID, ownerID, ownerSpecActionTokenIssued, "info", "Owner spec bootstrap token issued", map[string]any{
		"scopes":     []string{ownerSpecReadScope},
		"expires_at": expiresAt.Format(time.RFC3339),
	})
	return access, nil
}

func issueOwnerSpecBootstrapToken(stackID, ownerID string, now time.Time) (string, time.Time, error) {
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
		StackID: stackID,
		OwnerID: ownerID,
		Scopes:  []string{ownerSpecReadScope},
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

func (h crudRouteHandlers) storeOwnerSpecBootstrapToken(claims *ownerSpecBootstrapClaims, expiresAt time.Time) error {
	if h.app == nil || claims == nil {
		return nil
	}
	collection, err := h.app.FindCollectionByNameOrId("owner_spec_tokens")
	if err != nil {
		return fmt.Errorf("owner spec token collection missing: %w", err)
	}
	record := core.NewRecord(collection)
	record.Set("token_hash", ownerSpecTokenHash(claims.ID))
	record.Set("stack_id", claims.StackID)
	record.Set("owner_id", claims.OwnerID)
	record.Set("status", ownerSpecTokenStatusIssued)
	record.Set("expires_at", expiresAt.UTC().Format(time.RFC3339))
	return h.app.Save(record)
}

func (h crudRouteHandlers) consumeOwnerSpecBootstrapToken(claims *ownerSpecBootstrapClaims, now time.Time) error {
	if h.app == nil || claims == nil {
		return errOwnerSpecTokenInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	consumedAt := now.Format(time.RFC3339)
	result, err := h.app.DB().NewQuery(`
		UPDATE {{owner_spec_tokens}}
		SET [[status]] = {:consumed}, [[consumed_at]] = {:consumedAt}
		WHERE [[token_hash]] = {:tokenHash}
			AND [[stack_id]] = {:stackID}
			AND [[owner_id]] = {:ownerID}
			AND [[status]] = {:issued}
			AND [[expires_at]] > {:now}
	`).Bind(dbx.Params{
		"consumed":   ownerSpecTokenStatusConsumed,
		"consumedAt": consumedAt,
		"tokenHash":  ownerSpecTokenHash(claims.ID),
		"stackID":    claims.StackID,
		"ownerID":    claims.OwnerID,
		"issued":     ownerSpecTokenStatusIssued,
		"now":        consumedAt,
	}).Execute()
	if err != nil {
		return fmt.Errorf("%w: %v", errOwnerSpecTokenInvalid, err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr == nil && rows == 1 {
		return nil
	}

	record, err := h.app.FindFirstRecordByFilter(
		"owner_spec_tokens",
		"token_hash = {:tokenHash}",
		map[string]any{"tokenHash": ownerSpecTokenHash(claims.ID)},
	)
	if err != nil || record == nil {
		return errOwnerSpecTokenForbidden
	}
	if record.GetString("stack_id") != claims.StackID || record.GetString("owner_id") != claims.OwnerID {
		return errOwnerSpecTokenForbidden
	}
	if record.GetString("status") == ownerSpecTokenStatusConsumed {
		return errOwnerSpecTokenForbidden
	}
	if expiresAt, parseErr := time.Parse(time.RFC3339, record.GetString("expires_at")); parseErr == nil && !now.Before(expiresAt) {
		return errOwnerSpecTokenInvalid
	}
	return errOwnerSpecTokenForbidden
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

func (h crudRouteHandlers) ownerSpecResponse(stack *core.Record, claims *ownerSpecBootstrapClaims, now time.Time) (ownerSpecResponse, error) {
	if stack == nil || claims == nil {
		return ownerSpecResponse{}, fmt.Errorf("missing stack or claims")
	}

	spec, msg := stackSpecFromRecord(stack)
	if msg != "" {
		return ownerSpecResponse{}, fmt.Errorf("missing stored stack spec")
	}
	bootstrap, ok := ownerBootstrapFromRequest(normalizedCreateStackRequest{UserConfig: spec})
	if !ok || !ownerSourceSeedsPocketID(bootstrap.Source) {
		return ownerSpecResponse{}, fmt.Errorf("missing local owner bootstrap")
	}
	if bootstrap.Email == "" || bootstrap.Username == "" {
		return ownerSpecResponse{}, fmt.Errorf("missing local owner fields")
	}

	recoveryHash, err := h.ownerSpecRecoveryHash(claims.OwnerID, stack.Id)
	if err != nil {
		return ownerSpecResponse{}, err
	}
	expiresAt := ""
	if claims.ExpiresAt != nil {
		expiresAt = claims.ExpiresAt.Time.UTC().Format(time.RFC3339)
	} else if !now.IsZero() {
		expiresAt = now.UTC().Format(time.RFC3339)
	}

	return ownerSpecResponse{
		StackID: stack.Id,
		Identity: ownerSpecIdentity{
			Owner: ownerSpecOwner{
				Source:       ownerSourceLocal,
				SourceOrigin: ownerSourceOrigin(bootstrap.Source),
				Email:        bootstrap.Email,
				Username:     bootstrap.Username,
				DisplayName:  bootstrap.DisplayName,
			},
			Recovery: ownerSpecRecovery{
				PassphraseHash:        recoveryHash,
				PassphraseHashPresent: recoveryHash != "",
			},
		},
		Scopes:    []string{ownerSpecReadScope},
		ExpiresAt: expiresAt,
	}, nil
}

func (h crudRouteHandlers) ownerSpecRecoveryHash(ownerID, stackID string) (string, error) {
	record, err := h.app.FindFirstRecordByFilter(
		"wallet",
		"owner_id = {:ownerID} && stack_id = {:stackID} && service_id = {:serviceID}",
		map[string]any{
			"ownerID":   ownerID,
			"stackID":   stackID,
			"serviceID": recoveryWalletServiceID,
		},
	)
	if err != nil || record == nil {
		return "", fmt.Errorf("missing recovery wallet entry")
	}
	secret := strings.TrimSpace(record.GetString("secret"))
	if secret == "" {
		return "", fmt.Errorf("missing recovery hash")
	}
	decrypted, decryptErr := tsauth.DecryptIfNeeded(tsauth.GetEncryptor(), secret)
	if decryptErr != nil {
		return "", fmt.Errorf("recovery hash unavailable")
	}
	if !strings.HasPrefix(strings.TrimSpace(decrypted), "$argon2id$") {
		return "", fmt.Errorf("invalid recovery hash")
	}
	return strings.TrimSpace(decrypted), nil
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

func (h crudRouteHandlers) recordOwnerSpecAudit(stackID, ownerID, action, status, details string, metadata map[string]any) {
	if h.app == nil {
		return
	}
	collection, err := h.app.FindCollectionByNameOrId("activity_log")
	if err != nil || collection == nil {
		return
	}
	record := core.NewRecord(collection)
	setIfFieldExists(record, collection, "action", action)
	setIfFieldExists(record, collection, "details", details)
	setIfFieldExists(record, collection, "metadata", metadata)
	setIfFieldExists(record, collection, "stack_id", stackID)
	setIfFieldExists(record, collection, "user_id", ownerID)
	if stackID != "" && collection.Fields.GetByName("tenant_id") != nil {
		if stack, findErr := h.app.FindRecordById("stacks", stackID); findErr == nil {
			setIfFieldExists(record, collection, "tenant_id", strings.TrimSpace(stack.GetString("tenant_id")))
		}
	}
	setIfFieldExists(record, collection, "status", normalizeOwnerSpecAuditStatus(status))
	setIfFieldExists(record, collection, "target", firstNonEmpty(stackID, "owner-spec"))
	setIfFieldExists(record, collection, "actor", ownerID)
	setIfFieldExists(record, collection, "resource_type", "stack")
	setIfFieldExists(record, collection, "resource_id", stackID)
	if err := h.app.Save(record); err != nil {
		h.app.Logger().Warn("failed to persist owner spec record", "error", err)
	}
}

func setIfFieldExists(record *core.Record, collection *core.Collection, field string, value any) {
	if record == nil || collection == nil || value == nil {
		return
	}
	if collection.Fields.GetByName(field) == nil {
		return
	}
	record.Set(field, value)
}

func normalizeOwnerSpecAuditStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success", "warning", "error", "info":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "info"
	}
}
