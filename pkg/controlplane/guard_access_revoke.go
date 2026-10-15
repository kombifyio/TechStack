package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RevokeGuardAccess ends a Guard's access in one transaction: its runtime
// credential, its worker projection and its host-key binding. It is the effect
// of an owner detaching the key binding, minus the server lifecycle change, and
// is what a Connect-initiated revoke-everywhere applies. Missing rows are not
// an error, so the call is idempotent.
func (s *PostgresStore) RevokeGuardAccess(ctx context.Context, tenantID, workerID string, now time.Time) error {
	tenantID, workerID = strings.TrimSpace(tenantID), strings.TrimSpace(workerID)
	if tenantID == "" || workerID == "" {
		return fmt.Errorf("controlplane: tenant and worker are required to revoke Guard access")
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE techstack_agent_enrollments
			SET revoked_at = COALESCE(revoked_at, $3)
			WHERE tenant_id = $1 AND agent_id = $2
		`, tenantID, workerID, now.UTC()); err != nil {
			return fmt.Errorf("controlplane: revoke Guard enrollment: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE workers SET status = 'revoked', updated_at = $3
			WHERE tenant_id = $1 AND id = $2 AND status <> 'revoked'
		`, tenantID, workerID, now.UTC()); err != nil {
			return fmt.Errorf("controlplane: revoke Guard worker projection: %w", err)
		}
		return revokeWorkerIdentityTx(ctx, tx, tenantID, workerID, now)
	})
}

// RevokeGuardAccess is the in-memory equivalent used by tests and the local
// store: worker status and host-key binding end together.
func (s *MemoryStore) RevokeGuardAccess(ctx context.Context, tenantID, workerID string, now time.Time) error {
	s.mu.Lock()
	key := workerKey(tenantID, workerID)
	if worker, ok := s.worker[key]; ok {
		worker.Status = "revoked"
		worker.UpdatedAt = s.now()
		s.worker[key] = worker
	}
	s.mu.Unlock()
	return s.RevokeWorkerIdentity(ctx, tenantID, workerID, now)
}
