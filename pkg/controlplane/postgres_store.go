package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/jackc/pgx/v5/pgconn"
)

const tenantGUC = "app.tenant_id"

type PostgresStore struct {
	db                   *sql.DB
	backupWorker         bool
	serverEventProjector ServerEventProjector
	serverEventCommitted ServerEventCommitObserver
}

// ServerEventProjector records product-local projections inside the same SQL
// transaction as an accepted server aggregate event.
type ServerEventProjector interface {
	ProjectServerEvent(context.Context, *sql.Tx, *ServerEventResult) error
}

// ServerEventCommitObserver is optionally implemented by a ServerEventProjector.
// ServerEventCommitted runs after the store-owned transaction that accepted the
// event has committed (ApplyServerEvent, server enrollment, Guard inventory
// projection and owner detach), never inside it and never for a rolled-back
// event. It must not block or fail the caller. ApplyServerEventTx leaves the
// commit to its caller and does not call it.
type ServerEventCommitObserver interface {
	ServerEventCommitted(*ServerEventResult)
}

type PostgresStoreOption func(*PostgresStore)

// WithBackupWorkerConflictProjection selects the metadata-only execution fence.
// Database grants still enforce its exact separate login; this option confers no authority.
func WithBackupWorkerConflictProjection() PostgresStoreOption {
	return func(store *PostgresStore) { store.backupWorker = true }
}

func WithServerEventProjector(projector ServerEventProjector) PostgresStoreOption {
	return func(store *PostgresStore) {
		store.serverEventProjector = projector
		store.serverEventCommitted, _ = projector.(ServerEventCommitObserver)
	}
}

// announceServerEvents tells the commit observer about events whose
// transaction has just committed.
func (s *PostgresStore) announceServerEvents(results ...*ServerEventResult) {
	if s == nil || s.serverEventCommitted == nil {
		return
	}
	for _, result := range results {
		if result != nil {
			s.serverEventCommitted.ServerEventCommitted(result)
		}
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

	if err := tenantcontext.ApplyTenant(ctx, tx, tenantID); err != nil {
		return err
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
