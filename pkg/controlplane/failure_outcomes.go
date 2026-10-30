package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Failure resolution outcomes (knowledge-search plan section 5.8 step 7): the
// outcome of a resolution an agent applied through a product procedure is a
// class A row of the tenant (migration 172). The procedure ran as a job of the
// tenant; the outcome is that job's terminal state, so a caller can attribute
// an outcome but never choose it. The context is the one of the tenant's own
// failure occurrence with the same signature.

var (
	failureResolutionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	failureSignaturePattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	failureProcedurePattern    = regexp.MustCompile(`^(stackkits|techstack):[a-z0-9][a-z0-9._:/@#-]{1,199}$`)

	// ErrFailureOutcomeInvalid is a malformed outcome request.
	ErrFailureOutcomeInvalid = errors.New("controlplane: invalid failure outcome")
	// ErrFailureOutcomeJobNotTerminal is a procedure job that has not
	// completed or failed yet; a canceled job applied nothing.
	ErrFailureOutcomeJobNotTerminal = errors.New("controlplane: the procedure job has not completed or failed")
	// ErrFailureOutcomeNoOccurrence is a signature the tenant never recorded.
	ErrFailureOutcomeNoOccurrence = errors.New("controlplane: the tenant has no failure occurrence with this signature before the job, on its stack")
)

// RecordFailureOutcomeRequest attributes a finished procedure job to the
// resolution it applied.
type RecordFailureOutcomeRequest struct {
	ResolutionID string
	Signature    string
	Procedure    string
	JobID        string
	RecordedBy   string
}

// FailureResolutionOutcome is one recorded outcome of the tenant.
type FailureResolutionOutcome struct {
	ResolutionID string            `json:"resolution_id"`
	Signature    string            `json:"signature"`
	Procedure    string            `json:"procedure"`
	JobID        string            `json:"job_id"`
	StackID      string            `json:"stack_id,omitempty"`
	Outcome      string            `json:"outcome"`
	Context      map[string]string `json:"context"`
	CompletedAt  time.Time         `json:"completed_at"`
	RecordedAt   time.Time         `json:"recorded_at"`
}

// FailureResolutionOutcomeSummary counts the tenant's outcomes per resolution
// and signature. It holds no installation, job or stack identifier.
type FailureResolutionOutcomeSummary struct {
	ResolutionID string    `json:"resolution_id"`
	Signature    string    `json:"signature"`
	Succeeded    int       `json:"succeeded"`
	Failed       int       `json:"failed"`
	LastOutcome  time.Time `json:"last_outcome"`
}

// FailureOutcomeStore records and reads the tenant's own resolution outcomes.
type FailureOutcomeStore interface {
	RecordFailureOutcome(ctx context.Context, tenantID string, req RecordFailureOutcomeRequest) (FailureResolutionOutcome, bool, error)
	ListFailureOutcomes(ctx context.Context, tenantID, resolutionID string, limit int) ([]FailureResolutionOutcome, []FailureResolutionOutcomeSummary, error)
}

func validateFailureOutcomeRequest(req *RecordFailureOutcomeRequest) error {
	req.ResolutionID = strings.ToLower(strings.TrimSpace(req.ResolutionID))
	req.Signature = strings.ToLower(strings.TrimSpace(req.Signature))
	req.Procedure = strings.TrimSpace(req.Procedure)
	req.JobID = strings.TrimSpace(req.JobID)
	req.RecordedBy = strings.TrimSpace(req.RecordedBy)
	switch {
	case !failureResolutionIDPattern.MatchString(req.ResolutionID):
		return fmt.Errorf("%w: resolution_id is the id of a failure resolution", ErrFailureOutcomeInvalid)
	case !failureSignaturePattern.MatchString(req.Signature):
		return fmt.Errorf("%w: signature is 64 lowercase hex characters", ErrFailureOutcomeInvalid)
	case !failureProcedurePattern.MatchString(req.Procedure):
		return fmt.Errorf("%w: procedure is <stackkits|techstack>:<procedure reference>", ErrFailureOutcomeInvalid)
	case req.JobID == "" || len(req.JobID) > 128:
		return fmt.Errorf("%w: job_id names the job that ran the procedure", ErrFailureOutcomeInvalid)
	}
	if len(req.RecordedBy) > 256 {
		req.RecordedBy = req.RecordedBy[:256]
	}
	return nil
}

const failureOutcomeColumns = `resolution_id, signature, procedure_ref, receipt_ref, target_ref, outcome,
	stackkit_module, stackkit_version, app_version, os, os_version, arch,
	container_runtime, provider, device_class, completed_at, recorded_at`

func scanFailureOutcome(row interface{ Scan(...any) error }) (FailureResolutionOutcome, error) {
	var out FailureResolutionOutcome
	var module, moduleVersion, appVersion, osName, osVersion, arch, runtime, provider, deviceClass string
	if err := row.Scan(&out.ResolutionID, &out.Signature, &out.Procedure, &out.JobID, &out.StackID, &out.Outcome,
		&module, &moduleVersion, &appVersion, &osName, &osVersion, &arch, &runtime, &provider, &deviceClass,
		&out.CompletedAt, &out.RecordedAt); err != nil {
		return out, err
	}
	out.Context = map[string]string{}
	for key, value := range map[string]string{
		"stackkit_module": module, "stackkit_version": moduleVersion, "app_version": appVersion,
		"os": osName, "os_version": osVersion, "arch": arch, "container_runtime": runtime,
		"provider": provider, "device_class": deviceClass,
	} {
		if value != "" {
			out.Context[key] = value
		}
	}
	out.CompletedAt, out.RecordedAt = out.CompletedAt.UTC(), out.RecordedAt.UTC()
	return out, nil
}

// RecordFailureOutcome stores the outcome of the procedure job. It is
// idempotent per resolution and job: a repeated call returns the stored row
// and created=false.
func (s *PostgresStore) RecordFailureOutcome(ctx context.Context, tenantID string, req RecordFailureOutcomeRequest) (FailureResolutionOutcome, bool, error) {
	if s == nil || s.db == nil {
		return FailureResolutionOutcome{}, false, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return FailureResolutionOutcome{}, false, fmt.Errorf("controlplane: tenant id required")
	}
	if err := validateFailureOutcomeRequest(&req); err != nil {
		return FailureResolutionOutcome{}, false, err
	}
	var out FailureResolutionOutcome
	created := false
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var state, stackID string
		var completedAt sql.NullTime
		var createdAt, updatedAt time.Time
		err := tx.QueryRowContext(ctx, `SELECT state, COALESCE(stack_id, ''), completed_at, created_at, updated_at FROM jobs WHERE tenant_id = $1 AND id = $2`,
			tenantID, req.JobID).Scan(&state, &stackID, &completedAt, &createdAt, &updatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		outcome := ""
		switch state {
		case jobStateCompleted:
			outcome = "succeeded"
		case jobStateFailed:
			outcome = "failed"
		default:
			return ErrFailureOutcomeJobNotTerminal
		}
		finished := updatedAt
		if completedAt.Valid {
			finished = completedAt.Time
		}
		targetKind := ""
		if stackID != "" {
			targetKind = "stack"
		}
		// The job must answer a failure: an occurrence of the signature that
		// happened before the job was created, on the job's stack when both
		// name one. Its context is the one of the latest such occurrence. A
		// procedure reference on the job itself is not available yet, so the
		// procedure stays the caller's attribution.
		result, err := tx.ExecContext(ctx, `
			INSERT INTO failure_resolution_outcomes (
				tenant_id, resolution_id, receipt_kind, receipt_ref, target_kind, target_ref,
				signature, procedure_ref, stackkit_module, stackkit_version, app_version, os,
				os_version, arch, container_runtime, provider, device_class, outcome,
				recorded_by, completed_at
			)
			SELECT $1, $2, 'job', $3, $4, $5, $6, $7,
				o.stackkit_module, o.stackkit_version, o.app_version, o.os, o.os_version, o.arch,
				o.container_runtime, o.provider, o.device_class, $8, $9, $10
			FROM failure_occurrences o
			WHERE o.tenant_id = $1 AND o.signature = $6 AND o.occurred_at <= $11
				AND ($5 = '' OR o.target_ref = '' OR o.target_ref = $5)
			ORDER BY (o.target_ref = $5 AND $5 <> '') DESC, o.occurred_at DESC, o.receipt_ref DESC
			LIMIT 1
			ON CONFLICT (tenant_id, resolution_id, receipt_kind, receipt_ref) DO NOTHING`,
			tenantID, req.ResolutionID, req.JobID, targetKind, stackID, req.Signature, req.Procedure,
			outcome, req.RecordedBy, finished.UTC(), createdAt.UTC())
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		created = inserted == 1
		out, err = scanFailureOutcome(tx.QueryRowContext(ctx, `SELECT `+failureOutcomeColumns+`
			FROM failure_resolution_outcomes
			WHERE tenant_id = $1 AND resolution_id = $2 AND receipt_kind = 'job' AND receipt_ref = $3`,
			tenantID, req.ResolutionID, req.JobID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFailureOutcomeNoOccurrence
		}
		return err
	})
	if err != nil {
		return FailureResolutionOutcome{}, false, err
	}
	return out, created, nil
}

// ListFailureOutcomes returns the tenant's own outcomes, newest first, and
// the per-resolution counts. resolutionID narrows both when set.
func (s *PostgresStore) ListFailureOutcomes(ctx context.Context, tenantID, resolutionID string, limit int) ([]FailureResolutionOutcome, []FailureResolutionOutcomeSummary, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, nil, fmt.Errorf("controlplane: tenant id required")
	}
	resolutionID = strings.ToLower(strings.TrimSpace(resolutionID))
	if resolutionID != "" && !failureResolutionIDPattern.MatchString(resolutionID) {
		return nil, nil, fmt.Errorf("%w: resolution_id is the id of a failure resolution", ErrFailureOutcomeInvalid)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	items := []FailureResolutionOutcome{}
	summaries := []FailureResolutionOutcomeSummary{}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+failureOutcomeColumns+`
			FROM failure_resolution_outcomes
			WHERE tenant_id = $1 AND ($2 = '' OR resolution_id = $2)
			ORDER BY completed_at DESC, receipt_ref DESC
			LIMIT $3`, tenantID, resolutionID, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			item, err := scanFailureOutcome(rows)
			if err != nil {
				rows.Close()
				return err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		rows, err = tx.QueryContext(ctx, `
			SELECT resolution_id, signature,
				count(*) FILTER (WHERE outcome = 'succeeded'),
				count(*) FILTER (WHERE outcome = 'failed'),
				max(completed_at)
			FROM failure_resolution_outcomes
			WHERE tenant_id = $1 AND ($2 = '' OR resolution_id = $2)
			GROUP BY resolution_id, signature
			ORDER BY max(completed_at) DESC, resolution_id, signature
			LIMIT 200`, tenantID, resolutionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var summary FailureResolutionOutcomeSummary
			if err := rows.Scan(&summary.ResolutionID, &summary.Signature, &summary.Succeeded, &summary.Failed, &summary.LastOutcome); err != nil {
				return err
			}
			summary.LastOutcome = summary.LastOutcome.UTC()
			summaries = append(summaries, summary)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	return items, summaries, nil
}

var _ FailureOutcomeStore = (*PostgresStore)(nil)
