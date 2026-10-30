package tenantdb

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// LocalCredentialClass selects a credential verifier, never a subject or scope.
type LocalCredentialClass string

const (
	LocalOwner      LocalCredentialClass = "owner"
	LocalReadOnly   LocalCredentialClass = "readonly"
	LocalMCP        LocalCredentialClass = "mcp"
	LocalBackground LocalCredentialClass = "background"
)

var ErrInvalidLocalCredential = errors.New("tenantdb: invalid local credential")

// LocalContextSchemaSQL adds the Local credential adapter. Apply ContextSchemaSQL
// first, then this schema, then the consumer's governed verifier migration. It
// creates no credential store, issuer key or Local installation. Without the
// consumer-owned verifier every Local token is rejected.
//
//go:embed sql/kombify_ctx_local_v1.sql
var LocalContextSchemaSQL string

// NewLocalContextToken wraps the actual presented credential. The database, not
// this constructor or the caller, derives its identity and authority. Do not use
// an ambient owner credential to replace a missing request credential.
func NewLocalContextToken(class LocalCredentialClass, credential string) (string, error) {
	if !validLocalClass(class) || len(credential) < 16 || len(credential) > 2048 {
		return "", ErrInvalidLocalCredential
	}
	for _, c := range []byte(credential) {
		allowedCredentialByte := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !allowedCredentialByte {
			return "", ErrInvalidLocalCredential
		}
	}
	return "local1." + string(class) + "." + base64.RawURLEncoding.EncodeToString([]byte(credential)), nil
}

func validLocalClass(class LocalCredentialClass) bool {
	return class == LocalOwner || class == LocalReadOnly || class == LocalMCP || class == LocalBackground
}

// LocalClaims contains only database-derived capability metadata. The subject
// and organization are returned exclusively through VerifiedContext. Signed-v1
// contexts have no LocalClaims; absence must never imply an unrestricted grant.
type LocalClaims struct {
	CredentialClass LocalCredentialClass `json:"credential_class"`
	ClientID        string               `json:"client_id"`
	Scopes          []string             `json:"scopes"`
	Modules         json.RawMessage      `json:"modules"`
	Resources       json.RawMessage      `json:"resources"`
	Purpose         string               `json:"purpose"`
	Generation      int64                `json:"generation"`
	ExpiresAt       time.Time            `json:"expires_at"`
}

// ApplyLocalContextTokenPgx uses the same database authority as every other
// tenant context. Call first in the transaction, then compare VerifiedContext
// with the authenticated principal. SQL policies must enforce Local scopes too.
func ApplyLocalContextTokenPgx(ctx context.Context, tx PgxTxQuerier, token string) (VerifiedContext, LocalClaims, error) {
	if !strings.HasPrefix(token, "local1.") {
		return VerifiedContext{}, LocalClaims{}, ErrInvalidLocalCredential
	}
	verified, err := ApplyContextTokenPgx(ctx, tx, token)
	if err != nil {
		return VerifiedContext{}, LocalClaims{}, err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT kombify_ctx.local_claims()`).Scan(&raw); err != nil {
		return VerifiedContext{}, LocalClaims{}, ErrContextRejected
	}
	var claims LocalClaims
	if err := json.Unmarshal(raw, &claims); err != nil || !validLocalClass(claims.CredentialClass) || claims.Generation < 1 || claims.ClientID == "" || claims.ExpiresAt.IsZero() {
		return VerifiedContext{}, LocalClaims{}, ErrContextRejected
	}
	return verified, claims, nil
}
