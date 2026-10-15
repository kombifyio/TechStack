package controlplane

import (
	"context"
	"strings"
)

// LatestServerMaintenanceReader reads the newest completed OS-update plan or
// OS update of one server, for read models that need the pending-update count
// without knowing a plan digest.
type LatestServerMaintenanceReader interface {
	// LatestCompletedServerMaintenance returns the newest completed
	// os_update_plan or os_update job of the server, or ErrNotFound.
	LatestCompletedServerMaintenance(ctx context.Context, tenantID, serverID string) (*ServerMaintenanceJob, error)
}

func (s *PostgresStore) LatestCompletedServerMaintenance(ctx context.Context, tenantID, serverID string) (*ServerMaintenanceJob, error) {
	return queryOneTenantRow(ctx, s, tenantID, `
		SELECT `+serverMaintenanceColumns+`
		FROM server_maintenance_jobs
		WHERE tenant_id = $1 AND server_id = $2
			AND action IN ('os_update_plan', 'os_update') AND state = 'completed' AND completed_at IS NOT NULL
		ORDER BY completed_at DESC
		LIMIT 1
	`, scanServerMaintenanceJob, strings.TrimSpace(serverID))
}

func (s *MemoryStore) LatestCompletedServerMaintenance(_ context.Context, tenantID, serverID string) (*ServerMaintenanceJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var newest *ServerMaintenanceJob
	for _, job := range s.serverMaintenance {
		if job.TenantID != strings.TrimSpace(tenantID) || job.ServerID != strings.TrimSpace(serverID) ||
			job.State != ServerMaintenanceStateCompleted || job.CompletedAt == nil ||
			(job.Action != ServerMaintenanceActionPlan && job.Action != ServerMaintenanceActionUpdate) {
			continue
		}
		if newest == nil || job.CompletedAt.After(*newest.CompletedAt) {
			job := job
			newest = &job
		}
	}
	if newest == nil {
		return nil, ErrNotFound
	}
	return newest, nil
}

var (
	_ LatestServerMaintenanceReader = (*PostgresStore)(nil)
	_ LatestServerMaintenanceReader = (*MemoryStore)(nil)
)
