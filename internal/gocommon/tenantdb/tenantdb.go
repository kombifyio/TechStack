// Package tenantdb binds Postgres row-level security to a verified tenant
// context and asserts that database roles cannot escape it.
//
// Isolation holds by construction, even when service code is faulty
// (D-2026-10-22-02, docs/TENANT-ISOLATION.md):
//
//   - The tenant context is a signed context token minted by the trusted
//     issuer (the managed edge) from the authenticated principal. Services
//     forward it (ContextTokenMiddleware, WithContextTx) but never hold a
//     signing key. The database verifies it in kombify_ctx.verified_context()
//     (ContextSchemaSQL) and RLS policies read only kombify_ctx.subject() and
//     kombify_ctx.org_id(). A missing, forged, expired or foreign-audience
//     token matches zero rows, so service code cannot assert, forge or widen
//     the tenant context.
//   - No setting widens access. Cross-tenant system work runs under separate
//     system roles with their own credentials and policies targeted TO them
//     (AssertSystemRole, OpenSystemDB).
//   - Every service calls AssertRuntimeRole at boot and refuses to start when
//     its role is a superuser, bypasses RLS, owns objects, can run DDL or can
//     SET ROLE.
//
// The GUC helpers WithScopedTx, SetScope and SetServiceScope trust whatever
// the application asserts and are deprecated.
package tenantdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
)

var (
	ErrNoIdentity = errors.New("tenantdb: no identity in context")
	ErrNoUserID   = errors.New("tenantdb: identity has no UserID")
)

// WithScopedTx extracts the identity from ctx, opens a transaction, sets the
// RLS session GUCs, runs fn, and commits. If fn returns an error the
// transaction is rolled back and the error is returned.
//
// The identity MUST have at least a UserID; OrgID is set when present (empty
// string means "no org filter" — RLS policies handle this via COALESCE).
//
// Deprecated: since v0.8.4. RLS that reads app.user_id or app.org_id trusts
// whatever the application sets. Use WithContextTx with policies on
// kombify_ctx.subject() and kombify_ctx.org_id(); see docs/TENANT-ISOLATION.md.
func WithScopedTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	return WithScopedTxOpts(ctx, db, nil, fn)
}

// WithScopedTxOpts is like WithScopedTx but accepts explicit TxOptions.
//
// Deprecated: since v0.8.4. Use WithContextTxOpts.
func WithScopedTxOpts(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	id := identity.FromContext(ctx)
	if id == nil {
		return ErrNoIdentity
	}
	if id.UserID == "" {
		return ErrNoUserID
	}

	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("tenantdb: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := SetScope(ctx, tx, id); err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// SetScope sets the RLS session GUCs on an existing transaction. Use this
// when you manage the transaction lifecycle yourself (e.g. inside a larger
// unit-of-work). Both app.user_id and app.org_id are always set so RLS
// predicates that test either value work correctly.
//
// Deprecated: since v0.8.4. Use ApplyContextToken.
func SetScope(ctx context.Context, tx *sql.Tx, id *identity.Identity) error {
	if id == nil || id.UserID == "" {
		return ErrNoUserID
	}
	orgID := id.OrgID
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.user_id', $1, true)`, id.UserID); err != nil {
		return fmt.Errorf("tenantdb: set app.user_id: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.org_id', $1, true)`, orgID); err != nil {
		return fmt.Errorf("tenantdb: set app.org_id: %w", err)
	}
	return nil
}

// SetServiceScope sets the app.service_scope GUC that legacy RLS policies
// used to grant cross-tenant access.
//
// Deprecated: since v0.8.4, and removed once every consumer has migrated
// (docs/TENANT-ISOLATION.md "Migration window"). Any code holding the
// database credential can set this GUC, so a policy that branches on it is a
// backdoor around RLS (D-2026-10-22-02). RLS policies must not read
// app.service_scope; AssertRuntimeRole fails while any policy does. Run
// cross-tenant work under a system role instead (OpenSystemDB).
func SetServiceScope(ctx context.Context, tx *sql.Tx, scope string) error {
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.service_scope', $1, true)`, scope); err != nil {
		return fmt.Errorf("tenantdb: set app.service_scope: %w", err)
	}
	return nil
}
