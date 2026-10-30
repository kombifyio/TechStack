package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/failureknowledge"
)

// FailureSignatureSummary groups one tenant's occurrences by signature.
type FailureSignatureSummary struct {
	Signature         string    `json:"signature"`
	NormalizerVersion string    `json:"normalizer_version"`
	Component         string    `json:"component"`
	Phase             string    `json:"phase"`
	ErrorCode         string    `json:"error_code"`
	LatestTemplate    string    `json:"latest_template"`
	LatestExitStatus  *int      `json:"latest_exit_status"`
	Occurrences       int       `json:"occurrences"`
	Targets           int       `json:"targets"`
	FirstSeen         time.Time `json:"first_seen"`
	LastSeen          time.Time `json:"last_seen"`
	// RecentOccurrences and PreviousOccurrences count the last window and the
	// window before it, so a reader sees whether the signature keeps growing.
	RecentOccurrences    int    `json:"recent_occurrences"`
	PreviousOccurrences  int    `json:"previous_occurrences"`
	LatestStackKitModule string `json:"latest_stackkit_module"`
}

// FailureOccurrenceStore reads the tenant's own failure occurrences.
type FailureOccurrenceStore interface {
	ListFailureSignatures(ctx context.Context, tenantID string, limit int, window time.Duration) ([]FailureSignatureSummary, error)
}

// recordFailureOccurrenceTx stores the occurrence of a job that just became
// failed inside the same snapshot transaction, so each failed job yields one
// row. The job is the receipt and its stack the target. A savepoint keeps it
// best effort: a refused row is rolled back alone and never fails the job write.
func recordFailureOccurrenceTx(ctx context.Context, tx *sql.Tx, job *Job, occurrence *failureknowledge.Occurrence) {
	if job == nil || occurrence == nil || job.State != jobStateFailed {
		return
	}
	targetKind := ""
	if job.StackID != "" {
		targetKind = "stack"
	}
	occurredAt := job.UpdatedAt
	if job.CompletedAt != nil {
		occurredAt = *job.CompletedAt
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT failure_occurrence"); err != nil {
		slog.Warn("failure_occurrence_skipped", "job_id", job.ID, "error", err.Error())
		return
	}
	o, c := occurrence, occurrence.Context
	var exitStatus any
	if o.ExitStatus != nil {
		exitStatus = *o.ExitStatus
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO failure_occurrences (
			tenant_id, receipt_kind, receipt_ref, target_kind, target_ref, signature,
			normalizer_version, component, phase, error_code, exit_status, template,
			stackkit_module, stackkit_version, app_version, os, os_version, arch,
			container_runtime, provider, device_class, outcome, occurred_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			$13, $14, $15, $16, $17, $18, $19, $20, $21, 'failed', $22)
		ON CONFLICT (tenant_id, receipt_kind, receipt_ref) DO NOTHING`,
		job.TenantID, "job", job.ID, targetKind, job.StackID,
		o.Signature, o.NormalizerVersion, o.Component, o.Phase, o.ErrorCode, exitStatus, o.Template,
		c.StackKitModule, c.StackKitVersion, c.AppVersion, c.OS, c.OSVersion, c.Arch,
		c.ContainerRuntime, c.Provider, c.DeviceClass, occurredAt.UTC())
	if err != nil {
		slog.Warn("failure_occurrence_skipped", "job_id", job.ID, "error", err.Error())
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT failure_occurrence")
		return
	}
	_, _ = tx.ExecContext(ctx, "RELEASE SAVEPOINT failure_occurrence")
}

// failureOccurrenceForStoredJob normalizes a job that a store transition
// failed directly (lease reclaim, stale-job abandon, relocation recovery) with
// the component, phase and error-code rules of the orchestrator capture.
func failureOccurrenceForStoredJob(job *Job, errorCode string) *failureknowledge.Occurrence {
	if job == nil {
		return nil
	}
	support, _ := job.Result["support_context"].(map[string]any)
	occurrence := failureknowledge.Normalize(failureknowledge.Input{
		Component: firstNonEmpty(stringValue(job.Result["capability"]), "techstack.job."+job.Type),
		Phase:     firstNonEmpty(job.Step, stringValue(support["failed_step"])),
		ErrorCode: firstNonEmpty(
			errorCode,
			stringValue(support["stackkit_reason_code"]),
			stringValue(job.Result["reason_code"]),
			stringValue(job.Result["error_code"]),
		),
		Text: firstNonEmpty(job.Error, job.ErrorDetails),
	})
	return &occurrence
}

// ListFailureSignatures returns the tenant's own occurrences grouped by
// signature, most recently seen first, with the counts of the last window and
// the window before it. Row security limits every read to the tenant in the
// transaction context.
func (s *PostgresStore) ListFailureSignatures(ctx context.Context, tenantID string, limit int, window time.Duration) ([]FailureSignatureSummary, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	now := time.Now().UTC()
	recentFrom, previousFrom := now.Add(-window), now.Add(-2*window)
	out := []FailureSignatureSummary{}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT signature, normalizer_version, component, phase, error_code,
				count(*), count(DISTINCT NULLIF(target_ref, '')), min(occurred_at), max(occurred_at),
				(array_agg(template ORDER BY occurred_at DESC, receipt_ref DESC))[1],
				(array_agg(exit_status ORDER BY occurred_at DESC, receipt_ref DESC))[1],
				count(*) FILTER (WHERE occurred_at >= $3),
				count(*) FILTER (WHERE occurred_at >= $4 AND occurred_at < $3),
				(array_agg(stackkit_module ORDER BY occurred_at DESC, receipt_ref DESC))[1]
			FROM failure_occurrences
			WHERE tenant_id = $1
			GROUP BY signature, normalizer_version, component, phase, error_code
			ORDER BY max(occurred_at) DESC, signature
			LIMIT $2`, tenantID, limit, recentFrom, previousFrom)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var summary FailureSignatureSummary
			var exitStatus sql.NullInt64
			if err := rows.Scan(&summary.Signature, &summary.NormalizerVersion, &summary.Component, &summary.Phase,
				&summary.ErrorCode, &summary.Occurrences, &summary.Targets, &summary.FirstSeen, &summary.LastSeen,
				&summary.LatestTemplate, &exitStatus, &summary.RecentOccurrences, &summary.PreviousOccurrences,
				&summary.LatestStackKitModule); err != nil {
				return err
			}
			if exitStatus.Valid {
				status := int(exitStatus.Int64)
				summary.LatestExitStatus = &status
			}
			summary.FirstSeen, summary.LastSeen = summary.FirstSeen.UTC(), summary.LastSeen.UTC()
			out = append(out, summary)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ FailureOccurrenceStore = (*PostgresStore)(nil)
