package tenantdb

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"
)

// ContextSchemaSQL installs the kombify_ctx schema: the key and role-class
// tables and the in-database verifier that RLS policies read. Consumers ship
// it as a migration applied by the migration (owner) role through the
// governed migration path. It is idempotent.
//
//go:embed sql/kombify_ctx_v1.sql
var ContextSchemaSQL string

// ContextSchemaVersion versions the schema; the signed wire format remains v1.
const ContextSchemaVersion = 2

// RoleClass classifies a database login role in kombify_ctx.role_class.
type RoleClass string

const (
	// RoleClassRuntime is a user-facing service role: RLS-bound, owns
	// nothing, reaches tenant data only through a verified context.
	RoleClassRuntime RoleClass = "runtime"
	// RoleClassSystem is a cross-tenant system role (jobs, projections,
	// billing) with its own credential and policies targeted TO it.
	RoleClassSystem RoleClass = "system"
)

var ErrInvalidRoleClass = errors.New("tenantdb: invalid role class")

// RegisterContextKey stores an issuer key in kombify_ctx.keys. Run it only
// from the governed provisioning path with the migration (owner) role; the
// same key goes to the issuer (NewContextSigner) and never to a runtime
// service. A zero notAfter means the key does not expire; rotate by
// registering a new key ID and setting notAfter on the old one.
func RegisterContextKey(ctx context.Context, owner *sql.DB, keyID string, secret []byte, notBefore, notAfter time.Time) error {
	return registerContextKey(ctx, owner, keyID, secret, notBefore, notAfter, nil)
}

// RegisterContextKeyForAudiences confines a new issuer to the listed database logins.
// A key ID cannot later change its secret or audience scope through registration.
func RegisterContextKeyForAudiences(ctx context.Context, owner *sql.DB, keyID string, secret []byte, notBefore, notAfter time.Time, audiences []string) error {
	if len(audiences) == 0 {
		return fmt.Errorf("%w: audience list is empty", ErrInvalidSignerKey)
	}
	seen := make(map[string]bool, len(audiences))
	for _, audience := range audiences {
		if audience == "" || seen[audience] {
			return fmt.Errorf("%w: empty or repeated audience", ErrInvalidSignerKey)
		}
		seen[audience] = true
	}
	return registerContextKey(ctx, owner, keyID, secret, notBefore, notAfter, audiences)
}

func registerContextKey(ctx context.Context, owner *sql.DB, keyID string, secret []byte, notBefore, notAfter time.Time, audiences []string) error {
	if _, err := NewContextSigner(keyID, secret); err != nil {
		return err
	}
	if notBefore.IsZero() {
		notBefore = time.Now()
	}
	var expiry any
	if !notAfter.IsZero() {
		expiry = notAfter
	}
	res, err := owner.ExecContext(ctx, `
		INSERT INTO kombify_ctx.keys (key_id, secret, not_before, not_after, audiences)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (key_id) DO UPDATE
		SET not_after = EXCLUDED.not_after
		WHERE kombify_ctx.keys.secret = EXCLUDED.secret
		  AND kombify_ctx.keys.audiences IS NOT DISTINCT FROM EXCLUDED.audiences`, keyID, secret, notBefore, expiry, audiences)
	if err != nil {
		return fmt.Errorf("tenantdb: register context key %s: %w", keyID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: key id %s is already registered with a different secret or audience scope", ErrInvalidSignerKey, keyID)
	}
	return nil
}

// RegisterRoleClass records role as a runtime or system role. Run it with the
// migration (owner) role, normally as part of the consumer's migration that
// creates the role.
func RegisterRoleClass(ctx context.Context, owner *sql.DB, role string, class RoleClass) error {
	if class != RoleClassRuntime && class != RoleClassSystem {
		return fmt.Errorf("%w: %q", ErrInvalidRoleClass, class)
	}
	_, err := owner.ExecContext(ctx, `
		INSERT INTO kombify_ctx.role_class (role_name, class) VALUES ($1, $2)
		ON CONFLICT (role_name) DO UPDATE SET class = EXCLUDED.class`, role, string(class))
	if err != nil {
		return fmt.Errorf("tenantdb: register role %s: %w", role, err)
	}
	return nil
}
