package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// WorkerStackKitsTargetResourceKey holds the StackKits release Techstack
// approved for a worker's node. Techstack owns it; heartbeats never replace it.
const WorkerStackKitsTargetResourceKey = "stackkits_target"

// Outcomes of the owner-approved governed `stackkit upgrade` to a target.
const (
	StackKitsTargetSucceeded = "succeeded"
	StackKitsTargetFailed    = "failed"
)

// WorkerStackKitsTarget is the owner-approved StackKits release of a node and
// the outcome of the governed upgrade to it. The runtime bundle endpoint
// serves a published release to a deployed node only while it is the target
// of a succeeded upgrade.
type WorkerStackKitsTarget struct {
	Version    string    `json:"version"`
	Outcome    string    `json:"outcome"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	JobID      string    `json:"job_id,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// WorkerStackKitsTargetRecorder records a node's approved StackKits target.
type WorkerStackKitsTargetRecorder interface {
	RecordWorkerStackKitsTarget(ctx context.Context, tenantID, workerID string, target WorkerStackKitsTarget) error
}

// WorkerStackKitsTargetOf reads the recorded target of a worker.
func WorkerStackKitsTargetOf(worker *Worker) (WorkerStackKitsTarget, bool) {
	var target WorkerStackKitsTarget
	if worker == nil || worker.Resources[WorkerStackKitsTargetResourceKey] == nil {
		return target, false
	}
	raw, err := json.Marshal(worker.Resources[WorkerStackKitsTargetResourceKey])
	if err != nil || json.Unmarshal(raw, &target) != nil || strings.TrimSpace(target.Version) == "" {
		return WorkerStackKitsTarget{}, false
	}
	return target, true
}

func stackKitsTargetValue(target WorkerStackKitsTarget) (map[string]any, error) {
	target.Version = strings.TrimSpace(target.Version)
	if target.Version == "" || (target.Outcome != StackKitsTargetSucceeded && target.Outcome != StackKitsTargetFailed) {
		return nil, fmt.Errorf("controlplane: StackKits target needs a version and an outcome")
	}
	if target.RecordedAt.IsZero() {
		target.RecordedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	return value, json.Unmarshal(raw, &value)
}

func (s *PostgresStore) RecordWorkerStackKitsTarget(ctx context.Context, tenantID, workerID string, target WorkerStackKitsTarget) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	value, err := stackKitsTargetValue(target)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{WorkerStackKitsTargetResourceKey: value})
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			UPDATE workers
			SET resources_json = COALESCE(resources_json, '{}'::jsonb) || $3::jsonb, updated_at = now()
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, workerID, string(patch))
		if execErr != nil {
			return execErr
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr == nil && rows == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *MemoryStore) RecordWorkerStackKitsTarget(_ context.Context, tenantID, workerID string, target WorkerStackKitsTarget) error {
	value, err := stackKitsTargetValue(target)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(tenantID, workerID)
	worker, ok := s.worker[key]
	if !ok {
		return ErrNotFound
	}
	worker.Resources = cloneMap(worker.Resources)
	if worker.Resources == nil {
		worker.Resources = map[string]any{}
	}
	worker.Resources[WorkerStackKitsTargetResourceKey] = value
	s.worker[key] = worker
	return nil
}
