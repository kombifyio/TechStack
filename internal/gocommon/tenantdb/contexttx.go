package tenantdb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
)

// TxQuerier is the part of *sql.Tx that ApplyContextToken needs.
type TxQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// VerifiedContext is the tenant context the database accepted.
type VerifiedContext struct {
	Subject string
	OrgID   string
}

// WithContextTx opens a transaction bound to the signed context token in ctx
// (see ContextTokenMiddleware), runs fn and commits. The database verifies the
// token before fn runs; a rejected token returns ErrContextRejected and fn is
// not called. When ctx also carries an identity (edgeauth), its user and org
// must equal the verified claims.
func WithContextTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	return WithContextTxOpts(ctx, db, nil, fn)
}

// WithContextTxOpts is like WithContextTx but accepts explicit TxOptions.
func WithContextTxOpts(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	token := ContextTokenFromContext(ctx)
	if token == "" {
		return ErrNoContextToken
	}
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("tenantdb: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	verified, err := ApplyContextToken(ctx, tx, token)
	if err != nil {
		return err
	}
	if err := matchIdentity(identity.FromContext(ctx), verified); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyContextToken binds token to an open transaction and returns the
// context the database verified. Use it when you manage the transaction
// yourself; the binding ends with the transaction.
func ApplyContextToken(ctx context.Context, tx TxQuerier, token string) (VerifiedContext, error) {
	if token == "" {
		return VerifiedContext{}, ErrNoContextToken
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('`+ContextTokenSetting+`', $1, true)`, token); err != nil {
		return VerifiedContext{}, fmt.Errorf("tenantdb: set %s: %w", ContextTokenSetting, err)
	}
	var subject, orgID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT subject, org_id FROM kombify_ctx.verified_context()`).Scan(&subject, &orgID); err != nil {
		return VerifiedContext{}, fmt.Errorf("tenantdb: verify context: %w", err)
	}
	if !subject.Valid || subject.String == "" {
		return VerifiedContext{}, ErrContextRejected
	}
	return VerifiedContext{Subject: subject.String, OrgID: orgID.String}, nil
}

func matchIdentity(id *identity.Identity, verified VerifiedContext) error {
	if id == nil {
		return nil
	}
	if id.UserID != verified.Subject || id.OrgID != verified.OrgID {
		return ErrContextMismatch
	}
	return nil
}
