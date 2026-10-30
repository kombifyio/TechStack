// Package tenantcontext adapts Techstack's transaction scopes to the canonical
// Common database verifier. It has no issuer, signing key or role authority.
package tenantcontext

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
)

// ApplyTenant binds existing command, snapshot and aggregate transactions to
// the same verified context before any payload SQL. A runtime-chosen tenant
// cannot replace the organization verified by the database.
func ApplyTenant(ctx context.Context, tx tenantdb.TxQuerier, tenantID string) error {
	if ctx == nil || strings.TrimSpace(tenantID) == "" {
		return tenantdb.ErrNoIdentity
	}
	return applyIdentityScope(ctx, tx, "app.tenant_id", tenantID)
}

// ApplyUser scopes membership/self-account lookup before tenant selection. A
// signed context may look up only its own subject, never a runtime-chosen user.
func ApplyUser(ctx context.Context, tx tenantdb.TxQuerier, userID string) error {
	if ctx == nil || strings.TrimSpace(userID) == "" {
		return tenantdb.ErrNoUserID
	}
	return applyIdentityScope(ctx, tx, "app.user_id", userID)
}

func applyIdentityScope(ctx context.Context, tx tenantdb.TxQuerier, setting, scope string) error {
	token := tenantdb.ContextTokenFromContext(ctx)
	if token == "" {
		// SET LOCAL masks any session-level token left on a pooled connection.
		// A missing token grants no hosted rows; local/system authorization
		// remains their existing login and product-native identity lifecycle.
		_, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true), set_config('kombify.ctx_token', '', true)", setting, scope)
		return err
	}
	verified, err := tenantdb.ApplyContextToken(ctx, tx, token)
	if err != nil {
		return err
	}
	if id := identity.FromContext(ctx); id != nil &&
		(id.UserID != verified.Subject || id.OrgID != verified.OrgID) {
		return tenantdb.ErrContextMismatch
	}
	expected := verified.OrgID
	if setting == "app.user_id" {
		expected = verified.Subject
	}
	if scope != expected {
		return tenantdb.ErrContextMismatch
	}
	_, err = tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", setting, expected)
	return err
}
