package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

const tenantGUC = "app.tenant_id"

type PostgresStore struct {
	db                   *sql.DB
	serverEventProjector ServerEventProjector
}

// ServerEventProjector records product-local projections inside the same SQL
// transaction as an accepted server aggregate event.
type ServerEventProjector interface {
	ProjectServerEvent(context.Context, *sql.Tx, *ServerEventResult) error
}

type PostgresStoreOption func(*PostgresStore)

func WithServerEventProjector(projector ServerEventProjector) PostgresStoreOption {
	return func(store *PostgresStore) {
		store.serverEventProjector = projector
	}
}

func NewPostgresStore(db *sql.DB, options ...PostgresStoreOption) *PostgresStore {
	store := &PostgresStore{db: db}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	return store
}

// queryOneTenantRow runs a single-row tenant-scoped query through withTenant,
// mapping sql.ErrNoRows to ErrNotFound. The query's $1 is always the tenant id;
// extra args bind from $2 onward.
func queryOneTenantRow[T any](ctx context.Context, s *PostgresStore, tenantID, query string, scan func(rowScanner) (*T, error), args ...any) (*T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out *T
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		queryArgs := append([]any{tenantID}, args...)
		row, err := scan(tx.QueryRowContext(ctx, query, queryArgs...))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// queryTenantRows runs a multi-row tenant-scoped query through withTenant. The
// query's $1 is always the tenant id; extra args bind from $2 onward.
func queryTenantRows[T any](ctx context.Context, s *PostgresStore, tenantID, query string, scan func(rowScanner) (*T, error), args ...any) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	var out []T
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		queryArgs := append([]any{tenantID}, args...)
		rows, err := tx.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			item, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	// The stack-operation advisory lock relies on seeing commits made by the
	// previous lock holder. Pin the tenant transaction to READ COMMITTED even if
	// a deployment changes the database/session default.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
