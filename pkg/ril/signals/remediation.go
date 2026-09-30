package signals

import (
	"context"
	"database/sql"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// RemediationSubject is the tenant-scoped identity a remediation template may
// bind. It is derived from the normalized observation, never from a client.
type RemediationSubject struct {
	TenantID       string
	OwnerSubjectID string
	ServerID       string
	ServiceID      string
	AlertRule      string
	Source         Source
	Severity       Severity
}

// RemediationTemplate builds the governed action template for one remediable
// signal. It runs inside the Emit transaction under the signal tenant's RLS
// scope, so any stack, plan or target lookup sees exactly that tenant's rows.
// Returning ok=false keeps the signal notification-only.
type RemediationTemplate func(ctx context.Context, tx *sql.Tx, subject RemediationSubject) (template actions.ActionTemplate, ok bool, err error)

// RemediationPolicy maps a Techstack monitoring alert rule name to the
// governed action template that remediates it. Which rules are remediable is
// a Techstack/RIL decision; producers and clients cannot supply a template.
type RemediationPolicy map[string]RemediationTemplate

// DefaultRemediationPolicy is intentionally empty. A rule may enter it only
// when its template's StackKits primitive is "executor-bound" in the catalog
// (foundation/architecture_v2_catalog.cue _architectureV2RILActionPrimitives)
// and Techstack's dispatcher executes it (internal/rilactionexecution). Today
// the only executor-bound primitive is the read-only verify-stackkit-state;
// restart-service, rotate-certificate, check-backup, apply-stackkit-change and
// rollback-stackkit-change are contract-only, and no primitive remediates CPU,
// memory or disk pressure. No current alert rule therefore has an implemented
// remediation, and every monitoring signal stays notification-only.
func DefaultRemediationPolicy() RemediationPolicy { return RemediationPolicy{} }
