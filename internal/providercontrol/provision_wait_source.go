package providercontrol

import (
	"context"
	"fmt"
	"strings"
)

const listProviderProvisionWaitPageQuery = `
	SELECT tenant_id, operation_id
	FROM provider_control_list_provider_provision_waits($1, $2, $3)
`

// ProviderProvisionWaitPage is one bounded, stable keyset page of provider
// operation references. It deliberately contains no tenant-wide payload or
// job enumeration; the orchestrator revalidates each reference through its
// tenant-scoped control-plane job query.
type ProviderProvisionWaitPage struct {
	Operations []OperationRef
	NextCursor string
}

// ProviderProvisionWaitSource discovers durable provider-provision waits for
// startup recovery. Implementations return operation references directly so a
// restart sweep never enumerates tenants as an intermediate authority.
type ProviderProvisionWaitSource interface {
	ListProviderProvisionWaits(context.Context, string, int) (ProviderProvisionWaitPage, error)
}

// ListProviderProvisionWaits returns one provider-neutral operation-reference
// page from the migration-owned secret-free recovery projection. It does not
// set app.tenant_id and therefore must remain restricted to the provider
// control runtime role's allowlisted security-definer function.
func (l *PostgresLedger) ListProviderProvisionWaits(
	ctx context.Context,
	cursor string,
	limit int,
) (ProviderProvisionWaitPage, error) {
	if l == nil || l.db == nil {
		return ProviderProvisionWaitPage{}, fmt.Errorf("%w: ledger database is not configured", ErrInvalidRequest)
	}
	if limit < 1 || limit > 100 {
		return ProviderProvisionWaitPage{}, fmt.Errorf("%w: provider provision wait limit from 1 to 100 is required", ErrInvalidRequest)
	}
	afterTenantID, afterOperationID, err := splitProviderProvisionWaitCursor(cursor)
	if err != nil {
		return ProviderProvisionWaitPage{}, err
	}
	rows, err := l.db.QueryContext(ctx, listProviderProvisionWaitPageQuery,
		afterTenantID, afterOperationID, limit+1)
	if err != nil {
		return ProviderProvisionWaitPage{}, fmt.Errorf("providercontrol: list provider provision waits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	operations := make([]OperationRef, 0, limit+1)
	for rows.Next() {
		var operation OperationRef
		if scanErr := rows.Scan(&operation.TenantID, &operation.OperationID); scanErr != nil {
			return ProviderProvisionWaitPage{}, fmt.Errorf("providercontrol: scan provider provision wait: %w", scanErr)
		}
		operation.TenantID = strings.TrimSpace(operation.TenantID)
		operation.OperationID = strings.TrimSpace(operation.OperationID)
		if operation.TenantID == "" || operation.OperationID == "" {
			return ProviderProvisionWaitPage{}, fmt.Errorf("providercontrol: recovery projection returned an empty operation reference")
		}
		operations = append(operations, operation)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return ProviderProvisionWaitPage{}, fmt.Errorf("providercontrol: iterate provider provision waits: %w", rowsErr)
	}

	page := ProviderProvisionWaitPage{Operations: operations}
	if len(operations) > limit {
		page.Operations = operations[:limit]
		last := page.Operations[len(page.Operations)-1]
		page.NextCursor = providerProvisionWaitCursor(last)
	}
	return page, nil
}

func providerProvisionWaitCursor(operation OperationRef) string {
	return strings.TrimSpace(operation.TenantID) + "\x00" + strings.TrimSpace(operation.OperationID)
}

func splitProviderProvisionWaitCursor(cursor string) (string, string, error) {
	if strings.TrimSpace(cursor) == "" {
		return "", "", nil
	}
	separator := strings.IndexByte(cursor, 0)
	if separator < 1 || separator == len(cursor)-1 {
		return "", "", fmt.Errorf("%w: provider provision wait cursor is malformed", ErrInvalidRequest)
	}
	afterTenantID := strings.TrimSpace(cursor[:separator])
	afterOperationID := strings.TrimSpace(cursor[separator+1:])
	if afterTenantID == "" || afterOperationID == "" {
		return "", "", fmt.Errorf("%w: provider provision wait cursor is incomplete", ErrInvalidRequest)
	}
	return afterTenantID, afterOperationID, nil
}

var _ ProviderProvisionWaitSource = (*PostgresLedger)(nil)
