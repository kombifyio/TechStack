package vmleases

import (
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimelease"
	productlease "github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

// RuntimeContract projects Techstack-owned provider/billing state onto the
// closed neutral lease admitted by runtime consumers.
func RuntimeContract(lease productlease.Lease, revision uint64, runtimeServerID string, now time.Time) (runtimelease.Lease, error) {
	desired := runtimelease.DesiredState(strings.TrimSpace(string(lease.DesiredState)))
	projection := runtimelease.Lease{
		ID: runtimelease.LeaseID(strings.TrimSpace(string(lease.ID))), Revision: revision,
		TenantID: strings.TrimSpace(lease.Subject.OrgID), OwnerID: strings.TrimSpace(lease.Subject.ID),
		ServerID:             runtimelease.RuntimeServerID(strings.TrimSpace(runtimeServerID)),
		ResourceGenerationID: runtimelease.ResourceGenerationID(ResourceGenerationID(lease)),
		DesiredState:         desired, ValidFrom: lease.ValidFrom.UTC(), ValidUntil: lease.ValidUntil.UTC(),
		CancelledAt: lease.CancelledAt,
	}
	if !lease.RenewedAt.IsZero() {
		renewed := lease.RenewedAt.UTC()
		projection.RenewedAt = &renewed
	}
	if err := projection.Validate(now.UTC()); err != nil {
		return runtimelease.Lease{}, fmt.Errorf("vmleases: neutral runtime lease projection: %w", err)
	}
	return projection, nil
}
