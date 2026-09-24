package providercontrol

import (
	"context"
	"fmt"
	"strings"
)

const listRunnableTenantPageQuery = `
	SELECT tenant_id
	FROM provider_control_list_runnable_tenants($1, $2)
`

const listDueProviderDecommissionTenantPageQuery = `
	SELECT tenant_id
	FROM provider_control_list_due_decommission_wait_tenants($1, $2)
`

// ListRunnableTenants returns one stable keyset page for the native scheduler.
// It deliberately does not set app.tenant_id. The dedicated NOBYPASSRLS role
// can invoke only the migration-owned bounded projection; it has no direct
// access to either provider_operations or the secret-free tenant directory.
func (l *PostgresLedger) ListRunnableTenants(
	ctx context.Context,
	afterTenantID string,
	limit int,
) (RunnableTenantPage, error) {
	return l.listTenantPage(ctx, afterTenantID, limit, listRunnableTenantPageQuery)
}

// ListDueProviderDecommissionTenants returns one stable keyset page of
// tenants with a due, durable destroy wait. Unlike runnable native operations,
// this directory remains discoverable after the provider records terminal
// absence but before the queue process has resumed the waiting job.
func (l *PostgresLedger) ListDueProviderDecommissionTenants(
	ctx context.Context,
	afterTenantID string,
	limit int,
) (RunnableTenantPage, error) {
	return l.listTenantPage(ctx, afterTenantID, limit, listDueProviderDecommissionTenantPageQuery)
}

func (l *PostgresLedger) listTenantPage(
	ctx context.Context,
	afterTenantID string,
	limit int,
	query string,
) (RunnableTenantPage, error) {
	if l == nil || l.db == nil {
		return RunnableTenantPage{}, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	if limit < 1 || limit > 100 {
		return RunnableTenantPage{}, fmt.Errorf("%w: tenant limit from 1 to 100 is required", ErrInvalidRequest)
	}
	afterTenantID = strings.TrimSpace(afterTenantID)
	rows, err := l.db.QueryContext(ctx, query, afterTenantID, limit+1)
	if err != nil {
		return RunnableTenantPage{}, fmt.Errorf("providercontrol: list tenant directory: %w", err)
	}
	defer func() { _ = rows.Close() }()

	tenantIDs := make([]string, 0, limit+1)
	for rows.Next() {
		var tenantID string
		if scanErr := rows.Scan(&tenantID); scanErr != nil {
			return RunnableTenantPage{}, fmt.Errorf("providercontrol: scan runnable tenant: %w", scanErr)
		}
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			return RunnableTenantPage{}, fmt.Errorf("providercontrol: scheduler query returned an empty tenant id")
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return RunnableTenantPage{}, fmt.Errorf("providercontrol: iterate runnable tenants: %w", rowsErr)
	}
	page := RunnableTenantPage{TenantIDs: tenantIDs}
	if len(tenantIDs) > limit {
		page.TenantIDs = tenantIDs[:limit]
		page.NextCursor = page.TenantIDs[len(page.TenantIDs)-1]
	}
	return page, nil
}

var _ RunnableTenantSource = (*PostgresLedger)(nil)
var _ DueProviderDecommissionTenantSource = (*PostgresLedger)(nil)
