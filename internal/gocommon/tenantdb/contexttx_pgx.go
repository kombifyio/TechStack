package tenantdb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PgxTxQuerier is the part of pgx.Tx (and *pgxpool.Conn) that
// ApplyContextTokenPgx needs.
type PgxTxQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ApplyContextTokenPgx is ApplyContextToken for pgx transactions. Call it as
// the first statement of the transaction.
func ApplyContextTokenPgx(ctx context.Context, tx PgxTxQuerier, token string) (VerifiedContext, error) {
	if token == "" {
		return VerifiedContext{}, ErrNoContextToken
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('`+ContextTokenSetting+`', $1, true)`, token); err != nil {
		return VerifiedContext{}, fmt.Errorf("tenantdb: set %s: %w", ContextTokenSetting, err)
	}
	var subject, orgID *string
	if err := tx.QueryRow(ctx, `SELECT subject, org_id FROM kombify_ctx.verified_context()`).Scan(&subject, &orgID); err != nil {
		return VerifiedContext{}, fmt.Errorf("tenantdb: verify context: %w", err)
	}
	if subject == nil || *subject == "" {
		return VerifiedContext{}, ErrContextRejected
	}
	verified := VerifiedContext{Subject: *subject}
	if orgID != nil {
		verified.OrgID = *orgID
	}
	return verified, nil
}
