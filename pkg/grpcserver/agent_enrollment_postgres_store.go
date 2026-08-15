// Package grpcserver — Postgres-backed AgentEnrollmentStore.
//
// Schema is defined by the migration in pkg/db/migrations
// (techstack_agent_enrollments). Tenant isolation is enforced via
// pkg/db.WithTenant which sets the `app.tenant_id` GUC for each query;
// the table's RLS policy filters on current_setting('app.tenant_id', true).
package grpcserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pgkdb "github.com/kombifyio/techstack/pkg/db"
)

// PostgresAgentEnrollmentStore reads agent enrollment rows from the Techstack
// runtime database (kombify-runtime-db in SaaS; any self-host Postgres).
type PostgresAgentEnrollmentStore struct {
	db *pgkdb.DB
}

// NewPostgresAgentEnrollmentStore returns a Postgres-backed store. Caller
// must ensure migrations have been applied (pkg/db.DB.Migrate).
func NewPostgresAgentEnrollmentStore(db *pgkdb.DB) *PostgresAgentEnrollmentStore {
	return &PostgresAgentEnrollmentStore{db: db}
}

// Get implements AgentEnrollmentStore. Tenant isolation runs through
// pkg/db.WithTenant — the SET LOCAL app.tenant_id is used by RLS policies.
func (s *PostgresAgentEnrollmentStore) Get(ctx context.Context, tenantID, agentID string) (*AgentEnrollment, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("agent_enrollment: database not configured")
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(agentID) == "" {
		return nil, fmt.Errorf("agent_enrollment: tenant_id and agent_id are required")
	}

	var out *AgentEnrollment
	err := s.db.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
		const q = `
			SELECT agent_id, tenant_id, cert_serial, cert_fingerprint,
			       allowed_command_classes, enrolled_at, revoked_at, enrolled_by
			FROM techstack_agent_enrollments
			WHERE tenant_id = $1 AND agent_id = $2
		`
		row := tx.QueryRowContext(ctx, q, tenantID, agentID)
		var e AgentEnrollment
		var classesJSON []byte
		var revokedAt sql.NullTime
		if err := row.Scan(
			&e.AgentID, &e.TenantID, &e.CertSerial, &e.CertFingerprint,
			&classesJSON, &e.EnrolledAt, &revokedAt, &e.EnrolledBy,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrEnrollmentNotFound
			}
			return fmt.Errorf("agent_enrollment: scan: %w", err)
		}
		if len(classesJSON) > 0 {
			if err := json.Unmarshal(classesJSON, &e.AllowedCommandClasses); err != nil {
				return fmt.Errorf("agent_enrollment: parse allowed_command_classes: %w", err)
			}
		}
		if revokedAt.Valid {
			t := revokedAt.Time
			e.RevokedAt = &t
		}
		out = &e
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Upsert inserts or refreshes an enrollment row. Used by the operator-side
// enrollment flow (Phase 7.1 ships with this minimal admin helper; a
// proper Operator API lands in a follow-up).
func (s *PostgresAgentEnrollmentStore) Upsert(ctx context.Context, e AgentEnrollment) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("agent_enrollment: database not configured")
	}
	if strings.TrimSpace(e.TenantID) == "" || strings.TrimSpace(e.AgentID) == "" {
		return fmt.Errorf("agent_enrollment: tenant_id and agent_id are required")
	}
	if e.EnrolledAt.IsZero() {
		e.EnrolledAt = time.Now().UTC()
	}

	classesJSON, err := json.Marshal(e.AllowedCommandClasses)
	if err != nil {
		return fmt.Errorf("agent_enrollment: marshal allowed_command_classes: %w", err)
	}

	return s.db.WithTenant(ctx, e.TenantID, func(tx *sql.Tx) error {
		const q = `
			INSERT INTO techstack_agent_enrollments (
				agent_id, tenant_id, cert_serial, cert_fingerprint,
				allowed_command_classes, enrolled_at, revoked_at, enrolled_by
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
			ON CONFLICT (tenant_id, agent_id) DO UPDATE SET
				cert_serial = EXCLUDED.cert_serial,
				cert_fingerprint = EXCLUDED.cert_fingerprint,
				allowed_command_classes = EXCLUDED.allowed_command_classes,
				enrolled_at = EXCLUDED.enrolled_at,
				revoked_at = EXCLUDED.revoked_at,
				enrolled_by = EXCLUDED.enrolled_by
		`
		_, err := tx.ExecContext(ctx, q,
			e.AgentID, e.TenantID, e.CertSerial, e.CertFingerprint,
			string(classesJSON), e.EnrolledAt, nullTime(e.RevokedAt), e.EnrolledBy,
		)
		return err
	})
}

func nullTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return *t
}
