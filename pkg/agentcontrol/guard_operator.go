package agentcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

const (
	// DefaultNeedsOperatorAfter is how long an unresolved outcome_unknown is
	// reconciled automatically before it is surfaced as needing an operator.
	DefaultNeedsOperatorAfter = 30 * time.Minute
	// MaxOperatorNoteBytes bounds the operator's explanation of a resolution.
	MaxOperatorNoteBytes = 400
	// maxUnresolvedOutcomes bounds one operator listing.
	maxUnresolvedOutcomes = 100
	// maxOperatorNotices bounds the resolution notices handed to a Guard at once.
	maxOperatorNotices = 16
)

// Operator-facing states of an unresolved outcome.
const (
	// OutcomeReconciling means the Guard is still trying to settle the outcome.
	OutcomeReconciling = "reconciling"
	// OutcomeNeedsOperator means the outcome stayed unresolved past the policy
	// time. The fence is not released: only an operator resolution releases it.
	OutcomeNeedsOperator = "needs_operator"
)

// SetNeedsOperatorAfter overrides the time after which an unresolved outcome is
// shown as needing an operator. Non-positive values keep the default.
func (h *Hub) SetNeedsOperatorAfter(d time.Duration) {
	if h == nil || d <= 0 {
		return
	}
	h.mu.Lock()
	h.needsOperatorAfter = d
	h.mu.Unlock()
}

func (h *Hub) needsAfter() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.needsOperatorAfter <= 0 {
		return DefaultNeedsOperatorAfter
	}
	return h.needsOperatorAfter
}

// UnresolvedOutcome is one outcome_unknown command that still fences its worker.
type UnresolvedOutcome struct {
	CommandID string `json:"command_id"`
	Operation string `json:"operation"`
	// State is OutcomeReconciling or OutcomeNeedsOperator.
	State string `json:"state"`
	// Revision is the committed revision an operator resolution must supersede.
	Revision        int64     `json:"revision"`
	Reason          string    `json:"reason,omitempty"`
	Escalated       bool      `json:"escalated"`
	UnresolvedSince time.Time `json:"unresolved_since"`
	NeedsOperatorAt time.Time `json:"needs_operator_at"`
}

// OperatorResolution is an authenticated operator's decision on one unresolved
// outcome.
type OperatorResolution struct {
	TenantID  string
	AgentID   string
	CommandID string
	// ActorID is the authenticated operator subject; it is recorded with the
	// revision and never taken from the request body.
	ActorID     string
	Disposition string
	Note        string
	// ExpectedRevision is the revision the operator saw; a newer one makes the
	// resolution stale.
	ExpectedRevision int64
}

// ListUnresolvedOutcomes returns the commands of one worker whose outcome is
// unknown, oldest first. It never changes state: the needs_operator state is
// derived from the time the outcome first became unknown.
func (h *Hub) ListUnresolvedOutcomes(ctx context.Context, tenantID, agentID string) ([]UnresolvedOutcome, error) {
	tenantID, agentID = strings.TrimSpace(tenantID), strings.TrimSpace(agentID)
	if h == nil || h.db == nil || tenantID == "" || agentID == "" {
		return nil, ErrCommandNotPending
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT c.command_id, COALESCE(c.command_json->>'operation', ''), c.result_revision,
		       COALESCE((SELECT r.reason FROM typed_agent_command_revisions r WHERE r.command_id = c.command_id AND r.revision = c.result_revision), ''),
		       COALESCE((SELECT r.resolution FROM typed_agent_command_revisions r WHERE r.command_id = c.command_id AND r.revision = c.result_revision), ''),
		       COALESCE((SELECT min(r.created_at) FROM typed_agent_command_revisions r WHERE r.command_id = c.command_id AND r.state = 'outcome_unknown'), c.dispatched_at, c.created_at)
		FROM typed_agent_commands c
		WHERE c.tenant_id = $1 AND c.agent_id = $2 AND c.command_family = 'stackkit' AND c.state = 'outcome_unknown'
		ORDER BY c.created_at
		LIMIT $3
	`, tenantID, agentID, maxUnresolvedOutcomes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	after, now := h.needsAfter(), time.Now().UTC()
	var out []UnresolvedOutcome
	for rows.Next() {
		var item UnresolvedOutcome
		var resolution string
		if err := rows.Scan(&item.CommandID, &item.Operation, &item.Revision, &item.Reason, &resolution, &item.UnresolvedSince); err != nil {
			return nil, err
		}
		item.UnresolvedSince = item.UnresolvedSince.UTC()
		item.NeedsOperatorAt = item.UnresolvedSince.Add(after)
		item.Escalated = resolution == guardbinding.ResolutionEscalated
		item.State = OutcomeReconciling
		if !now.Before(item.NeedsOperatorAt) {
			item.State = OutcomeNeedsOperator
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ResolveOutcomeByOperator commits an operator resolution revision for an
// unresolved outcome and releases the worker's fence. The revision is appended,
// never edited: it supersedes the evidence the operator saw (same revision
// number and digest), carries the operator as its actor and is audited with the
// operator's note. Nothing resolves an outcome silently: the expiry policy only
// changes how the outcome is shown.
func (h *Hub) ResolveOutcomeByOperator(ctx context.Context, req OperatorResolution) (guardbinding.ResultReport, error) {
	req.TenantID, req.AgentID, req.CommandID = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.AgentID), strings.TrimSpace(req.CommandID)
	req.ActorID, req.Disposition, req.Note = strings.TrimSpace(req.ActorID), strings.TrimSpace(req.Disposition), strings.TrimSpace(req.Note)
	if h == nil || h.db == nil || req.TenantID == "" || req.AgentID == "" || req.CommandID == "" || req.ActorID == "" {
		return guardbinding.ResultReport{}, ErrCommandNotPending
	}
	if !guardbinding.OperatorDisposition(req.Disposition) || req.Note == "" || len(req.Note) > MaxOperatorNoteBytes || req.ExpectedRevision < 1 {
		return guardbinding.ResultReport{}, guardbinding.Denyf(guardbinding.CodeOperatorNotAllowed, "a disposition, the revision you saw and a note of at most %d bytes are required", MaxOperatorNoteBytes)
	}
	tx, err := h.tenantTx(ctx, req.TenantID)
	if err != nil {
		return guardbinding.ResultReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	var revision int64
	var priorDigest sql.NullString
	var contract sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT state, result_revision, result_digest, contract_version
		FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id = $2 AND command_id = $3 AND command_family = 'stackkit'
		FOR UPDATE
	`, req.TenantID, req.AgentID, req.CommandID).Scan(&state, &revision, &priorDigest, &contract)
	if errors.Is(err, sql.ErrNoRows) {
		return guardbinding.ResultReport{}, ErrCommandNotPending
	}
	if err != nil {
		return guardbinding.ResultReport{}, err
	}
	if state != "outcome_unknown" || contract.String != guardbinding.ContractVersion || !priorDigest.Valid || priorDigest.String == "" {
		return guardbinding.ResultReport{}, guardbinding.Denyf(guardbinding.CodeOutcomeNotUnresolved, "the command is %s, not an unresolved outcome", state)
	}
	if revision != req.ExpectedRevision {
		return guardbinding.ResultReport{}, guardbinding.Denyf(guardbinding.CodeOutcomeRevisionStale, "the outcome is at revision %d, not %d", revision, req.ExpectedRevision)
	}
	report := guardbinding.ResultReport{
		Revision: revision + 1, State: guardbinding.ReportResolved, PriorDigest: priorDigest.String,
		Resolution: req.Disposition, Reason: truncateText(req.Note, guardbinding.MaxReasonBytes),
	}
	if report.Digest, err = report.ComputeDigest(req.CommandID, nil); err != nil {
		return guardbinding.ResultReport{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO typed_agent_command_revisions
			(command_id, revision, tenant_id, state, digest, prior_digest, reason, resolution, truncated, actor_kind, actor_id, note)
		VALUES ($1, $2, $3, 'resolved', $4, $5, $6, $7, false, 'operator', $8, $9)
	`, req.CommandID, report.Revision, req.TenantID, report.Digest, report.PriorDigest, report.Reason, report.Resolution, req.ActorID, req.Note); err != nil {
		return guardbinding.ResultReport{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE typed_agent_commands
		SET state = 'failed', error = $2, completed_at = now(), result_revision = $3, result_digest = $4,
		    acked_at = now(), mutating = false
		WHERE command_id = $1
	`, req.CommandID, operatorResolutionMessage(report), report.Revision, report.Digest); err != nil {
		return guardbinding.ResultReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return guardbinding.ResultReport{}, err
	}
	return report, nil
}

// operatorResolutionMessage is the stored error text of a command an operator
// settled; it tells a waiting caller what the operator asserted.
func operatorResolutionMessage(report guardbinding.ResultReport) string {
	switch report.Resolution {
	case guardbinding.DispositionVerifiedApplied:
		return fmt.Sprintf("outcome_resolved_operator_verified_applied: %s; an operator verified the desired state, refresh the StackKits evidence", report.Reason)
	case guardbinding.DispositionVerifiedNotApplied:
		return fmt.Sprintf("outcome_resolved_operator_verified_not_applied: %s; an operator verified nothing was applied", report.Reason)
	default:
		return fmt.Sprintf("outcome_accepted_unknown_by_operator: %s; the outcome was never verified", report.Reason)
	}
}

// OperatorResolutionsForTenant returns the operator resolution revisions of
// commands the Guard still holds as unsettled. A Guard applies them to its own
// journal, which releases its local fence with the same committed evidence.
func (h *Hub) OperatorResolutionsForTenant(ctx context.Context, tenantID, agentID string, known []string) ([]guardbinding.OperatorResolutionNotice, error) {
	tenantID, agentID = strings.TrimSpace(tenantID), strings.TrimSpace(agentID)
	if h == nil || h.db == nil || tenantID == "" || agentID == "" || len(known) == 0 {
		return nil, nil
	}
	if len(known) > guardbinding.MaxKnownCommands {
		known = known[:guardbinding.MaxKnownCommands]
	}
	encoded, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT r.command_id, r.revision, r.digest, COALESCE(r.prior_digest, ''), r.resolution, r.reason, r.actor_id
		FROM typed_agent_command_revisions r
		JOIN typed_agent_commands c ON c.command_id = r.command_id
		WHERE c.tenant_id = $1 AND c.agent_id = $2 AND c.command_family = 'stackkit'
		  AND r.actor_kind = 'operator' AND r.revision = c.result_revision
		  AND r.command_id = ANY(SELECT jsonb_array_elements_text($3::jsonb))
		ORDER BY r.created_at
		LIMIT $4
	`, tenantID, agentID, string(encoded), maxOperatorNotices)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []guardbinding.OperatorResolutionNotice
	for rows.Next() {
		var notice guardbinding.OperatorResolutionNotice
		if err := rows.Scan(&notice.CommandID, &notice.Revision, &notice.Digest, &notice.PriorDigest, &notice.Resolution, &notice.Reason, &notice.ActorID); err != nil {
			return nil, err
		}
		out = append(out, notice)
	}
	return out, rows.Err()
}
