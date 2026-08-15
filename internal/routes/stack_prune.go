// Stack prune endpoint; filter/param literals intentionally mirror the legacy
// PocketBase query schema.
//
//nolint:goconst
package routes

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/pocketbase/pocketbase/core"
)

// stackPruneResponse is the payload of POST /api/v1/stacks/prune-orphans. It is
// deliberately distinct from a destructive reset: provider-backed rows are
// handed to the canonical decommission lifecycle, while only true local
// orphans are archived here.
type stackPruneResponse struct {
	Message           string   `json:"message"`
	PrunedStacks      int      `json:"pruned_stacks"`
	PrunedLegacy      int      `json:"pruned_legacy"`
	SkippedActive     int      `json:"skipped_active"`
	SkippedOtherOwner int      `json:"skipped_other_owner"`
	Warnings          []string `json:"warnings,omitempty"`
}

// pruneOrphanStacks removes only true orphan stack entries from the
// control-plane store the dashboard reads, plus archives matching legacy
// PocketBase rows that the list endpoint still merges in. A live provider lease
// is handed to the canonical idempotent decommission lifecycle and is never
// archived directly by this route.
//
// Control-plane Postgres is the sole stack authority (PocketBase retirement: the
// runtime fails closed at startup without it — cmd/techstack/startup.go), so
// prune is control-plane-first and fail-closed if the store is somehow absent.
// Lease attachment is enumerated once (not per-stack) and prune is fail-closed
// on a lease list error — nothing is deleted if provider attachment is unknown.
//
// Lease attachment intentionally stays CancelledAt-based: a live lease is
// provider-billed infrastructure and its dashboard row must go through
// decommission, not local archive.
func (h stackLifecycleRouteHandlers) pruneOrphanStacks(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	tenantID := requestTenantID(e, ownerID)
	ctx := e.Request.Context()
	targetStackID := strings.TrimSpace(e.Request.URL.Query().Get("stack_id"))

	if h.stacks == nil || strings.TrimSpace(tenantID) == "" {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack cleanup requires the control-plane store; it is not available for this request.",
			map[string]any{
				"error_code":  "PRUNE_STORE_UNAVAILABLE",
				"reason_code": "control_plane_store_missing",
				"retryable":   true,
				"user_guidance": map[string]any{
					"title": "Aufräumen momentan nicht möglich",
					"body":  "Die verwaltete Stack-Liste ist gerade nicht erreichbar. Bitte später erneut versuchen.",
					"next_steps": []map[string]any{
						{"id": "retry", "label": "Erneut versuchen", "kind": "retry"},
					},
				},
			})
	}
	if h.leases == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack cleanup requires lease attachment authority; it is not available for this request.",
			map[string]any{
				"error_code":  "PRUNE_AUTHORITY_UNAVAILABLE",
				"reason_code": "lease_authority_missing",
				"retryable":   true,
				"user_guidance": map[string]any{
					"title": "Aufräumen momentan nicht möglich",
					"body":  "Die Lease-Zuordnung ist gerade nicht erreichbar. Es wurde nichts gelöscht.",
					"next_steps": []map[string]any{
						{"id": "retry", "label": "Erneut versuchen", "kind": "retry"},
					},
				},
			})
	}

	stacks, err := h.stacks.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to list stacks", nil)
	}

	// Enumerate provider lease attachment ONCE for the tenant (avoids an N+1 over
	// the full lease list on every stack). Fail-closed: if it cannot be listed,
	// skip all deletes.
	liveLeaseStacks, danglingLeases, leaseErr := h.liveLeaseStackIDs(ctx, tenantID)

	warnings := []string{}
	failClosed := leaseErr != nil
	if failClosed {
		warnings = append(warnings, "Cleanup skipped: could not verify lease attachment (fail-closed, nothing deleted).")
	}
	if danglingLeases > 0 {
		warnings = append(warnings, fmt.Sprintf("%d live lease(s) without a stack_id link were ignored; check them in Operations.", danglingLeases))
	}

	pruned, skipped, skippedOtherOwner := 0, 0, 0
	for _, s := range stacks {
		if targetStackID != "" && s.ID != targetStackID {
			continue
		}
		if strings.TrimSpace(s.OwnerSubjectID) != strings.TrimSpace(ownerID) {
			skippedOtherOwner++
			continue
		}
		if failClosed {
			skipped++
			continue
		}
		if liveLeaseStacks[s.ID] {
			if err := h.decommissionStackLease(ctx, tenantID, ownerID, s.ID); err != nil {
				warnings = append(warnings, fmt.Sprintf("Stack %s decommission warning: %s", s.ID, err.Error()))
			}
			skipped++
			continue
		}
		if err := h.stacks.SoftDeleteStack(ctx, tenantID, s.ID); err != nil {
			warnings = append(warnings, fmt.Sprintf("Stack %s prune warning: %s", s.ID, err.Error()))
			continue
		}
		pruned++
	}

	prunedLegacy := 0
	if !failClosed {
		var legacySkipped int
		prunedLegacy, legacySkipped, warnings = h.pruneLegacyStacks(legacyPruneInput{
			ctx:             ctx,
			ownerID:         ownerID,
			tenantID:        tenantID,
			targetStackID:   targetStackID,
			liveLeaseStacks: liveLeaseStacks,
			warnings:        warnings,
		})
		skipped += legacySkipped
	}

	return httpx.Success(e, http.StatusOK, stackPruneResponse{
		Message:           "Orphan stacks pruned",
		PrunedStacks:      pruned,
		PrunedLegacy:      prunedLegacy,
		SkippedActive:     skipped,
		SkippedOtherOwner: skippedOtherOwner,
		Warnings:          warnings,
	})
}

// legacyPruneInput bundles the context the legacy sweep needs so the helper
// stays within the argument budget.
type legacyPruneInput struct {
	ctx             context.Context
	ownerID         string
	tenantID        string
	targetStackID   string
	liveLeaseStacks map[string]bool
	warnings        []string
}

// pruneLegacyStacks soft-deletes orphaned legacy PocketBase stack rows the list
// endpoint still merges into the dashboard. These rows are exactly the "dead
// cards": they exist only in the retired store, so the control-plane prune loop
// never saw them. Provider-backed rows use the same decommission branch as the
// control-plane loop; lease-free rows are archived locally.
func (h stackLifecycleRouteHandlers) pruneLegacyStacks(input legacyPruneInput) (int, int, []string) {
	if h.app == nil {
		return 0, 0, input.warnings
	}
	if _, err := h.app.FindCollectionByNameOrId("stacks"); err != nil { // pocketbase-migration-compat: legacy dead-card sweep during PB retirement
		return 0, 0, input.warnings
	}
	records, err := h.app.FindRecordsByFilter( // pocketbase-migration-compat: legacy dead-card sweep during PB retirement
		"stacks",
		"owner_id = {:ownerId}",
		"-created",
		0, 0,
		map[string]any{"ownerId": input.ownerID},
	)
	if err != nil {
		return 0, 0, append(input.warnings, "Legacy cleanup skipped: could not list legacy stacks (nothing deleted).")
	}

	pruned, skipped := 0, 0
	warnings := input.warnings
	now := time.Now().UTC()
	for _, record := range records {
		if input.targetStackID != "" && record.Id != input.targetStackID {
			continue
		}
		if !record.GetDateTime("deleted_at").IsZero() {
			continue
		}
		recordTenantID := strings.TrimSpace(record.GetString("tenant_id"))
		if recordTenantID != "" && recordTenantID != input.tenantID {
			continue
		}
		if input.liveLeaseStacks[record.Id] {
			if err := h.decommissionStackLease(input.ctx, input.tenantID, input.ownerID, record.Id); err != nil {
				warnings = append(warnings, fmt.Sprintf("Legacy stack %s decommission warning: %s", record.Id, err.Error()))
			}
			skipped++
			continue
		}
		if err := h.removeLegacyOrphan(record, now); err != nil {
			warnings = append(warnings, fmt.Sprintf("Legacy stack %s prune warning: %s", record.Id, err.Error()))
			continue
		}
		pruned++
	}
	return pruned, skipped, warnings
}

// decommissionStackLease hands an exact stack/owner/tenant scope to the
// existing idempotent provider lifecycle. It intentionally does not archive a
// local projection: that remains the lifecycle's responsibility.
func (h stackLifecycleRouteHandlers) decommissionStackLease(ctx context.Context, tenantID, ownerID, stackID string) error {
	if h.decommissioner == nil {
		return fmt.Errorf("managed lease decommission authority is unavailable")
	}
	_, err := h.decommissioner.DecommissionManagedLeases(ctx, jobruntime.ManagedLeaseDecommissionRequest{
		StackID:  strings.TrimSpace(stackID),
		TenantID: strings.TrimSpace(tenantID),
		OwnerID:  strings.TrimSpace(ownerID),
	})
	return err
}

// removeLegacyOrphan removes a row from the retired PocketBase projection.
// Newer compatibility schemas support a soft-delete timestamp; older production
// schemas do not. Those older rows are already proven to have no live lease, so
// a hard delete is the only deterministic cleanup path.
func (h stackLifecycleRouteHandlers) removeLegacyOrphan(record *core.Record, now time.Time) error {
	if record.Collection().Fields.GetByName("deleted_at") != nil {
		record.Set("deleted_at", now)
		return h.app.Save(record) // pocketbase-migration-compat: legacy dead-card sweep during PB retirement
	}
	return h.app.Delete(record) // pocketbase-migration-compat: old schemas have no tombstone field
}

// liveLeaseStackIDs returns the set of stack ids that have any live
// (non-canceled) tenant lease, plus the count of live leases that carry no
// stack_id link. Prune is a tenant-wide destructive safety decision, so caller
// visibility must never hide an attachment here.
func (h stackLifecycleRouteHandlers) liveLeaseStackIDs(ctx context.Context, tenantID string) (map[string]bool, int, error) {
	out := map[string]bool{}
	if h.leases == nil {
		return out, 0, nil
	}
	leases, err := h.leases.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, 0, err
	}
	dangling := 0
	for _, lease := range leases {
		if lease.CancelledAt != nil {
			continue
		}
		if sid := strings.TrimSpace(lease.Metadata["stack_id"]); sid != "" {
			out[sid] = true
		} else {
			dangling++
		}
	}
	return out, dangling, nil
}
