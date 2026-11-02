package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Techstack-owned worker keys of StackKits update management (D-2026-11-02-03).
// Heartbeats and Guard inventory never replace them.
const (
	// WorkerStackKitsUpdateBehaviorResourceKey holds the node's update setting.
	WorkerStackKitsUpdateBehaviorResourceKey = "stackkits_update_behavior"
	// WorkerStackKitsUpdateAttemptResourceKey holds the one update Techstack
	// started without a click: automatically or as a security patch.
	WorkerStackKitsUpdateAttemptResourceKey = "stackkits_update_attempt"
)

// Update behaviors of a node. The global default is approve.
const (
	StackKitsUpdateApprove   = "approve"
	StackKitsUpdateAutomatic = "automatic"
)

// Triggers of an update Techstack starts without a click.
const (
	StackKitsUpdateTriggerAutomatic = "automatic"
	StackKitsUpdateTriggerSecurity  = "security"
)

// WorkerStackKitsUpdateBehavior is the update setting of a node.
type WorkerStackKitsUpdateBehavior struct {
	Behavior string    `json:"behavior"`
	SetBy    string    `json:"set_by,omitempty"`
	SetAt    time.Time `json:"set_at"`
}

// WorkerStackKitsUpdateAttempt is the update Techstack started for a release
// without a click. One release is started at most once this way; a failed one
// waits for the owner's Approve.
type WorkerStackKitsUpdateAttempt struct {
	Version   string    `json:"version"`
	Trigger   string    `json:"trigger"`
	JobID     string    `json:"job_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// Patched says what a security patch fixed and why, from its release
	// notes, so the owner still reads it after the update moved the node.
	Patched []StackKitsPatchNote `json:"patched,omitempty"`
}

// StackKitsPatchNote is one security release note: the app and the fix.
type StackKitsPatchNote struct {
	App  string `json:"app,omitempty"`
	Text string `json:"text"`
}

// WorkerStackKitsUpdateStore persists the update setting and the started
// update of a node.
type WorkerStackKitsUpdateStore interface {
	SetWorkerStackKitsUpdateBehavior(ctx context.Context, tenantID, workerID string, behavior WorkerStackKitsUpdateBehavior) error
	// ClaimWorkerStackKitsUpdateAttempt records the attempt only when the node
	// has none for that release yet; false means another evaluation owns it.
	ClaimWorkerStackKitsUpdateAttempt(ctx context.Context, tenantID, workerID string, attempt WorkerStackKitsUpdateAttempt) (bool, error)
	// ReleaseWorkerStackKitsUpdateAttempt removes an attempt for that release
	// whose job never started, so a later evaluation may start it.
	ReleaseWorkerStackKitsUpdateAttempt(ctx context.Context, tenantID, workerID, version string) error
	RecordWorkerStackKitsUpdateAttemptJob(ctx context.Context, tenantID, workerID, version, jobID string) error
}

// WorkerStackKitsUpdateBehaviorOf reads a node's update setting; a node without
// one approves each update.
func WorkerStackKitsUpdateBehaviorOf(worker *Worker) WorkerStackKitsUpdateBehavior {
	var behavior WorkerStackKitsUpdateBehavior
	if worker == nil || !decodeWorkerResource(worker.Resources[WorkerStackKitsUpdateBehaviorResourceKey], &behavior) || behavior.Behavior != StackKitsUpdateAutomatic {
		return WorkerStackKitsUpdateBehavior{Behavior: StackKitsUpdateApprove, SetBy: behavior.SetBy, SetAt: behavior.SetAt}
	}
	return behavior
}

// WorkerStackKitsUpdateAttemptOf reads the update Techstack started without a click.
func WorkerStackKitsUpdateAttemptOf(worker *Worker) (WorkerStackKitsUpdateAttempt, bool) {
	var attempt WorkerStackKitsUpdateAttempt
	if worker == nil || !decodeWorkerResource(worker.Resources[WorkerStackKitsUpdateAttemptResourceKey], &attempt) || strings.TrimSpace(attempt.Version) == "" {
		return WorkerStackKitsUpdateAttempt{}, false
	}
	return attempt, true
}

func decodeWorkerResource(value any, target any) bool {
	if value == nil {
		return false
	}
	raw, err := json.Marshal(value)
	return err == nil && json.Unmarshal(raw, target) == nil
}

func resourceValue(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(raw, &out)
}

func stackKitsUpdateBehaviorValue(behavior WorkerStackKitsUpdateBehavior) (map[string]any, error) {
	if behavior.Behavior != StackKitsUpdateApprove && behavior.Behavior != StackKitsUpdateAutomatic {
		return nil, fmt.Errorf("controlplane: unknown StackKits update behavior")
	}
	if behavior.SetAt.IsZero() {
		behavior.SetAt = time.Now().UTC()
	}
	return resourceValue(behavior)
}

func stackKitsUpdateAttemptValue(attempt WorkerStackKitsUpdateAttempt) (map[string]any, error) {
	attempt.Version = strings.TrimSpace(attempt.Version)
	if attempt.Version == "" || (attempt.Trigger != StackKitsUpdateTriggerAutomatic && attempt.Trigger != StackKitsUpdateTriggerSecurity) {
		return nil, fmt.Errorf("controlplane: a StackKits update attempt needs a release and a trigger")
	}
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = time.Now().UTC()
	}
	return resourceValue(attempt)
}

func (s *PostgresStore) patchWorkerResources(ctx context.Context, tenantID, workerID string, patch map[string]any) error {
	raw, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			UPDATE workers
			SET resources_json = COALESCE(resources_json, '{}'::jsonb) || $3::jsonb, updated_at = now()
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, workerID, string(raw))
		if execErr != nil {
			return execErr
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr == nil && rows == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) SetWorkerStackKitsUpdateBehavior(ctx context.Context, tenantID, workerID string, behavior WorkerStackKitsUpdateBehavior) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	value, err := stackKitsUpdateBehaviorValue(behavior)
	if err != nil {
		return err
	}
	return s.patchWorkerResources(ctx, tenantID, workerID, map[string]any{WorkerStackKitsUpdateBehaviorResourceKey: value})
}

func (s *PostgresStore) ClaimWorkerStackKitsUpdateAttempt(ctx context.Context, tenantID, workerID string, attempt WorkerStackKitsUpdateAttempt) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("controlplane: database not configured")
	}
	value, err := stackKitsUpdateAttemptValue(attempt)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(map[string]any{WorkerStackKitsUpdateAttemptResourceKey: value})
	if err != nil {
		return false, err
	}
	claimed := false
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			UPDATE workers
			SET resources_json = COALESCE(resources_json, '{}'::jsonb) || $3::jsonb, updated_at = now()
			WHERE tenant_id = $1 AND id = $2
				AND (resources_json->'stackkits_update_attempt'->>'version') IS DISTINCT FROM $4
		`, tenantID, workerID, string(raw), value["version"])
		if execErr != nil {
			return execErr
		}
		rows, rowsErr := result.RowsAffected()
		claimed = rowsErr == nil && rows == 1
		return rowsErr
	})
	return claimed, err
}

func (s *PostgresStore) ReleaseWorkerStackKitsUpdateAttempt(ctx context.Context, tenantID, workerID, version string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			UPDATE workers
			SET resources_json = resources_json - 'stackkits_update_attempt', updated_at = now()
			WHERE tenant_id = $1 AND id = $2
				AND resources_json->'stackkits_update_attempt'->>'version' = $3
				AND COALESCE(resources_json->'stackkits_update_attempt'->>'job_id', '') = ''
		`, tenantID, workerID, strings.TrimSpace(version))
		return execErr
	})
}

func (s *PostgresStore) RecordWorkerStackKitsUpdateAttemptJob(ctx context.Context, tenantID, workerID, version, jobID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			UPDATE workers
			SET resources_json = jsonb_set(resources_json, '{stackkits_update_attempt,job_id}', to_jsonb($4::text)), updated_at = now()
			WHERE tenant_id = $1 AND id = $2
				AND resources_json->'stackkits_update_attempt'->>'version' = $3
		`, tenantID, workerID, strings.TrimSpace(version), jobID)
		return execErr
	})
}

func (s *MemoryStore) patchWorkerResource(tenantID, workerID, key string, update func(current any) (any, bool)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := workerKey(tenantID, workerID)
	worker, ok := s.worker[id]
	if !ok {
		return false, ErrNotFound
	}
	next, changed := update(worker.Resources[key])
	if !changed {
		return false, nil
	}
	worker.Resources = cloneMap(worker.Resources)
	if worker.Resources == nil {
		worker.Resources = map[string]any{}
	}
	if next == nil {
		delete(worker.Resources, key)
	} else {
		worker.Resources[key] = next
	}
	s.worker[id] = worker
	return true, nil
}

func (s *MemoryStore) SetWorkerStackKitsUpdateBehavior(_ context.Context, tenantID, workerID string, behavior WorkerStackKitsUpdateBehavior) error {
	value, err := stackKitsUpdateBehaviorValue(behavior)
	if err != nil {
		return err
	}
	_, err = s.patchWorkerResource(tenantID, workerID, WorkerStackKitsUpdateBehaviorResourceKey, func(any) (any, bool) { return value, true })
	return err
}

func (s *MemoryStore) ClaimWorkerStackKitsUpdateAttempt(_ context.Context, tenantID, workerID string, attempt WorkerStackKitsUpdateAttempt) (bool, error) {
	value, err := stackKitsUpdateAttemptValue(attempt)
	if err != nil {
		return false, err
	}
	return s.patchWorkerResource(tenantID, workerID, WorkerStackKitsUpdateAttemptResourceKey, func(current any) (any, bool) {
		var existing WorkerStackKitsUpdateAttempt
		if decodeWorkerResource(current, &existing) && existing.Version == value["version"] {
			return nil, false
		}
		return value, true
	})
}

func (s *MemoryStore) ReleaseWorkerStackKitsUpdateAttempt(_ context.Context, tenantID, workerID, version string) error {
	_, err := s.patchWorkerResource(tenantID, workerID, WorkerStackKitsUpdateAttemptResourceKey, func(current any) (any, bool) {
		var existing WorkerStackKitsUpdateAttempt
		return nil, decodeWorkerResource(current, &existing) && existing.Version == strings.TrimSpace(version) && existing.JobID == ""
	})
	return err
}

func (s *MemoryStore) RecordWorkerStackKitsUpdateAttemptJob(_ context.Context, tenantID, workerID, version, jobID string) error {
	_, err := s.patchWorkerResource(tenantID, workerID, WorkerStackKitsUpdateAttemptResourceKey, func(current any) (any, bool) {
		var existing WorkerStackKitsUpdateAttempt
		if !decodeWorkerResource(current, &existing) || existing.Version != strings.TrimSpace(version) {
			return nil, false
		}
		existing.JobID = jobID
		value, valueErr := resourceValue(existing)
		return value, valueErr == nil
	})
	return err
}
