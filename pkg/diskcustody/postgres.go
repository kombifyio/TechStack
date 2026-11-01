// Package diskcustody keeps the control-plane state that used to live only on
// the instance's data disk in the runtime database, so a deploy can start the
// new instance before the old one stops (kombify-Techstack-6vse8): the
// managed-target SSH host-key pins and the stacks' managed spec files.
package diskcustody

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/unifier"
)

const (
	pinSourceObserved   = "observed"
	pinSourceDiskImport = "disk_import"

	specSourceWrite      = "write"
	specSourceDiskImport = "disk_import"

	specOperationTimeout = 30 * time.Second
)

// schemaPending maps a missing custody table (the binary runs before its
// forward migration, D-2026-10-27-06) to the caller's "not rolled out yet"
// sentinel, so it keeps its previous local behavior until the table exists.
func schemaPending(err error, pending error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		return fmt.Errorf("%w: %s", pending, pgErr.Message)
	}
	return err
}

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// HostKeyPins is the runtime_host_key_pins table: one pin per normalized
// host:port, never overwritten by the runtime. A pin is bound to the managed
// lease that took it and retired by the database once that lease's provider
// resources are proven absent (migration 176).
type HostKeyPins struct {
	DB *sql.DB
}

var _ jobs.RuntimeHostKeyPinBackend = HostKeyPins{}

// LoadPins returns every pin.
func (p HostKeyPins) LoadPins(ctx context.Context) (map[string]string, error) {
	if p.DB == nil {
		return nil, errors.New("host key pin database is not configured")
	}
	rows, err := p.DB.QueryContext(ctx, `SELECT address, fingerprint FROM runtime_host_key_pins`)
	if err != nil {
		return nil, schemaPending(err, jobs.ErrRuntimeHostKeyPinStorePending)
	}
	defer rows.Close()
	pins := map[string]string{}
	for rows.Next() {
		var address, fingerprint string
		if err := rows.Scan(&address, &fingerprint); err != nil {
			return nil, err
		}
		pins[address] = fingerprint
	}
	return pins, rows.Err()
}

// LookupPin returns the pin of one address.
func (p HostKeyPins) LookupPin(ctx context.Context, address string) (string, bool, error) {
	if p.DB == nil {
		return "", false, errors.New("host key pin database is not configured")
	}
	var fingerprint string
	err := p.DB.QueryRowContext(ctx, `SELECT fingerprint FROM runtime_host_key_pins WHERE address = $1`, address).Scan(&fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return fingerprint, true, nil
}

// PinFirst stores an observed pin unless the address already has one and
// returns the pin that holds. A bound pin is written in the lease's tenant
// scope with the lease in app.runtime_host_key_lease; the admit trigger of
// migration 176 keeps the binding only for that tenant's lease recording the
// pinned host and refuses a lease already proven absent. Before migration 176
// the setting is ignored and the pin is stored unbound.
func (p HostKeyPins) PinFirst(ctx context.Context, address, fingerprint string, binding jobs.RuntimeHostKeyBinding) (string, error) {
	if p.DB == nil {
		return "", errors.New("host key pin database is not configured")
	}
	if binding.TenantID == "" || binding.LeaseID == "" {
		slog.Default().Warn("runtime_host_key_pin_unbound", "address", address, "reason", "no managed lease")
		return p.pinFirst(ctx, address, fingerprint, pinSourceObserved)
	}
	tx, err := db.BeginTenantTransaction(ctx, p.DB, binding.TenantID)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.runtime_host_key_lease', $1, true)`, binding.LeaseID); err != nil {
		_ = tx.Rollback()
		return "", err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO runtime_host_key_pins (address, fingerprint, source)
		VALUES ($1, $2, $3) ON CONFLICT (address) DO NOTHING`, address, fingerprint, pinSourceObserved)
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if rows, _ := inserted.RowsAffected(); rows == 1 {
		p.warnUnbound(ctx, address, binding)
	}
	pinned, ok, err := p.LookupPin(ctx, address)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("host key pin for %s was not stored", address)
	}
	return pinned, nil
}

// warnUnbound reports a new pin the database stored without its lease: it
// does not retire with the lease, so a recycled address would refuse the next
// server again.
func (p HostKeyPins) warnUnbound(ctx context.Context, address string, binding jobs.RuntimeHostKeyBinding) {
	var bound bool
	err := p.DB.QueryRowContext(ctx, `SELECT lease_binding IS NOT NULL FROM runtime_host_key_pins WHERE address = $1`, address).Scan(&bound)
	if err != nil || bound {
		return
	}
	slog.Default().Warn("runtime_host_key_pin_unbound", "address", address, "tenant_id", binding.TenantID, "lease_id", binding.LeaseID)
}

// RetiredPins returns the candidate addresses whose given fingerprint the
// database retired. Before migration 176 no pin has ever been retired.
func (p HostKeyPins) RetiredPins(ctx context.Context, candidates map[string]string) (map[string]bool, error) {
	retired := map[string]bool{}
	if len(candidates) == 0 {
		return retired, nil
	}
	if p.DB == nil {
		return nil, errors.New("host key pin database is not configured")
	}
	addresses := make([]string, 0, len(candidates))
	fingerprints := make([]string, 0, len(candidates))
	for address, fingerprint := range candidates {
		addresses = append(addresses, address)
		fingerprints = append(fingerprints, fingerprint)
	}
	rows, err := p.DB.QueryContext(ctx, `SELECT DISTINCT retired.address
		FROM runtime_host_key_pin_retirements AS retired
		JOIN unnest($1::text[], $2::text[]) AS candidate(address, fingerprint)
		  ON candidate.address = retired.address AND candidate.fingerprint = retired.fingerprint`,
		addresses, fingerprints)
	if pgErrorCode(err) == "42P01" {
		return retired, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		retired[address] = true
	}
	return retired, rows.Err()
}

func (p HostKeyPins) pinFirst(ctx context.Context, address, fingerprint, source string) (string, error) {
	if p.DB == nil {
		return "", errors.New("host key pin database is not configured")
	}
	if _, err := p.DB.ExecContext(ctx, `INSERT INTO runtime_host_key_pins (address, fingerprint, source)
		VALUES ($1, $2, $3) ON CONFLICT (address) DO NOTHING`, address, fingerprint, source); err != nil {
		return "", err
	}
	pinned, ok, err := p.LookupPin(ctx, address)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("host key pin for %s was not stored", address)
	}
	return pinned, nil
}

// SpecFiles is the tenant-isolated stack_spec_files table.
type SpecFiles struct {
	DB *sql.DB
}

var _ jobs.SpecFileStore = SpecFiles{}

// SpecFiles binds one stack's custody.
func (s SpecFiles) SpecFiles(tenantID, stackID string) unifier.SpecFileMirror {
	return stackSpecFiles{db: s.DB, tenantID: strings.TrimSpace(tenantID), stackID: strings.TrimSpace(stackID)}
}

type stackSpecFiles struct {
	db       *sql.DB
	tenantID string
	stackID  string
}

func (s stackSpecFiles) withTenant(fn func(context.Context, *sql.Tx) error) error {
	if s.db == nil {
		return errors.New("spec file database is not configured")
	}
	if s.tenantID == "" || s.stackID == "" {
		return errors.New("spec file custody requires tenant and stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), specOperationTimeout)
	defer cancel()
	tx, err := db.BeginTenantTransaction(ctx, s.db, s.tenantID)
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s stackSpecFiles) PutSpecFile(filename string, data []byte) error {
	if !unifier.IsManagedSpecFilename(filename) {
		return fmt.Errorf("unsupported managed spec filename %q", filename)
	}
	return s.withTenant(func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO stack_spec_files (tenant_id, stack_id, filename, content, sha256, source)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, stack_id, filename) DO UPDATE
			SET content = EXCLUDED.content, sha256 = EXCLUDED.sha256, source = EXCLUDED.source, updated_at = now()
			WHERE stack_spec_files.sha256 IS DISTINCT FROM EXCLUDED.sha256`,
			s.tenantID, s.stackID, filename, data, sha256Hex(data), specSourceWrite)
		return err
	})
}

func (s stackSpecFiles) LoadSpecFiles() (map[string][]byte, error) {
	files := map[string][]byte{}
	err := s.withTenant(func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT filename, content, sha256 FROM stack_spec_files
			WHERE tenant_id = $1 AND stack_id = $2`, s.tenantID, s.stackID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var filename, digest string
			var content []byte
			if err := rows.Scan(&filename, &content, &digest); err != nil {
				return err
			}
			if sha256Hex(content) != digest {
				return fmt.Errorf("stack spec file %s does not match its recorded digest", filename)
			}
			files[filename] = content
		}
		return rows.Err()
	})
	if err != nil {
		return nil, schemaPending(err, unifier.ErrSpecMirrorPending)
	}
	return files, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
