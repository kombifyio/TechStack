package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// supersededByJobIDResultKey mirrors jobs.SupersededByJobIDResultKey; the
// store package cannot import the job runtime.
const supersededByJobIDResultKey = "superseded_by_job_id"

// JobSupersessionRecorder points a terminal source job at the deterministic
// replacement that took over its work, so a client polling the source can
// follow it. It only annotates failed or canceled rows and never changes
// their state.
type JobSupersessionRecorder interface {
	RecordJobSupersededBy(ctx context.Context, tenantID, jobID, replacementJobID string) error
}

func normalizeJobSupersession(tenantID, jobID, replacementJobID string) (string, string, string, error) {
	tenantID = strings.TrimSpace(tenantID)
	jobID = strings.TrimSpace(jobID)
	replacementJobID = strings.TrimSpace(replacementJobID)
	if tenantID == "" || jobID == "" || replacementJobID == "" || jobID == replacementJobID {
		return "", "", "", fmt.Errorf("controlplane: exact source and replacement job identity required")
	}
	return tenantID, jobID, replacementJobID, nil
}

func (s *MemoryStore) RecordJobSupersededBy(_ context.Context, tenantID, jobID, replacementJobID string) error {
	tenantID, jobID, replacementJobID, err := normalizeJobSupersession(tenantID, jobID, replacementJobID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.TenantID != tenantID || (job.State != jobStateFailed && job.State != jobStateCancelled) {
		return ErrConflict
	}
	result := cloneMap(job.Result)
	if result == nil {
		result = map[string]any{}
	}
	result[supersededByJobIDResultKey] = replacementJobID
	job.Result = result
	s.jobs[jobID] = job
	return nil
}

func (s *PostgresStore) RecordJobSupersededBy(ctx context.Context, tenantID, jobID, replacementJobID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID, jobID, replacementJobID, err := normalizeJobSupersession(tenantID, jobID, replacementJobID)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			UPDATE jobs
			SET result_json = COALESCE(result_json, '{}'::jsonb) || jsonb_build_object($3::text, $4::text)
			WHERE tenant_id = $1 AND id = $2 AND state IN ('failed', 'cancelled')
		`, tenantID, jobID, supersededByJobIDResultKey, replacementJobID)
		if execErr != nil {
			return execErr
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if affected == 0 {
			return fmt.Errorf("%w: job %s is missing or not terminal", ErrConflict, jobID)
		}
		return nil
	})
}

var (
	_ JobSupersessionRecorder = (*MemoryStore)(nil)
	_ JobSupersessionRecorder = (*PostgresStore)(nil)
)
