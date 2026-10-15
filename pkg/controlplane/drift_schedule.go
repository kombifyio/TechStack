package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// DriftScheduleTenantDirectory lets the drift scheduler find tenants with a
// live stack. stacks is FORCE RLS, so the scheduler reads the directory, then
// the tenant's stacks inside that tenant's RLS boundary.
type DriftScheduleTenantDirectory interface {
	ListDriftScheduleTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error)
	// CompactDriftScheduleTenant retires the tenant's directory entry once it
	// holds no live stack.
	CompactDriftScheduleTenant(ctx context.Context, tenantID string) error
}

func (s *MemoryStore) ListDriftScheduleTenants(_ context.Context, afterTenantID string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	tenants := make([]string, 0)
	for _, stack := range s.stacks {
		if stack.DeletedAt == nil && stack.TenantID > afterTenantID && !seen[stack.TenantID] {
			seen[stack.TenantID] = true
			tenants = append(tenants, stack.TenantID)
		}
	}
	sort.Strings(tenants)
	if limit > 0 && len(tenants) > limit {
		tenants = tenants[:limit]
	}
	return tenants, nil
}

func (s *MemoryStore) CompactDriftScheduleTenant(context.Context, string) error { return nil }

func (s *PostgresStore) ListDriftScheduleTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("controlplane: drift schedule tenant limit from 1 to 100 is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id FROM public.techstack_list_drift_schedule_tenants($1,$2)
	`, strings.TrimSpace(afterTenantID), limit)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list drift schedule tenants: %w", err)
	}
	return scanTenantIDPage(rows, limit)
}

// CompactDriftScheduleTenant removes the entry only when it was not refreshed
// in the last minute, so a stack written concurrently (whose trigger refreshed
// the entry) is never orphaned from the directory.
func (s *PostgresStore) CompactDriftScheduleTenant(ctx context.Context, tenantID string) error {
	tenantID = strings.TrimSpace(tenantID)
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			SELECT public.techstack_compact_drift_schedule_tenant($1)
		`, tenantID)
		return err
	})
}
