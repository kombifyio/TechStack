package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// monitoringVisibleServers is the same owner and stack visibility the inventory
// server collection applies ($1 tenant, $2 owner or empty for a tenant scope),
// as a derived table so the monitoring reads cannot see a row the collection
// read would not return.
const monitoringVisibleServers = `
	SELECT servers.id, servers.created_at, servers.stack_id, servers.worker_id,
		COALESCE(NULLIF(btrim(servers.display_name), ''), servers.name) AS label
	FROM servers
	WHERE servers.tenant_id = $1 AND ($2 = '' OR servers.owner_subject_id = $2)
		AND (NULLIF(btrim(servers.stack_id), '') IS NULL OR EXISTS (
			SELECT 1 FROM stacks owned_stack
			WHERE owned_stack.tenant_id = servers.tenant_id
				AND owned_stack.id = servers.stack_id
				AND owned_stack.owner_subject_id = servers.owner_subject_id
				AND ($2 = '' OR owned_stack.owner_subject_id = $2)
				AND owned_stack.deleted_at IS NULL
		))`

var (
	_ InventoryMonitoringReader = (*PostgresStore)(nil)
	_ ServerMetricRecorder      = (*PostgresStore)(nil)
)

// RecordServerMetricSample folds one Guard host report into the fine and coarse
// buckets of the server and prunes the server's expired buckets whenever a new
// fine bucket opens. It runs in its own tenant transaction.
func (s *PostgresStore) RecordServerMetricSample(ctx context.Context, tenantID, serverID string, at time.Time, sample MetricSample) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	tenantID, serverID = strings.TrimSpace(tenantID), strings.TrimSpace(serverID)
	if tenantID == "" || serverID == "" {
		return fmt.Errorf("controlplane: metric sample requires tenant and server identity")
	}
	if sample.CPU == nil && sample.Memory == nil && sample.Disk == nil {
		return nil
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		fineOpened := false
		for _, resolution := range []time.Duration{MetricFineResolution, MetricCoarseResolution} {
			bucket := MetricBucket{Start: MetricBucketStart(at, resolution)}
			bucket.Add(sample)
			var inserted bool
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO server_metric_samples AS m (
					tenant_id, server_id, resolution_seconds, bucket_start,
					cpu_sum, cpu_count, memory_sum, memory_count, disk_sum, disk_count
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (tenant_id, server_id, resolution_seconds, bucket_start) DO UPDATE SET
					cpu_sum = m.cpu_sum + EXCLUDED.cpu_sum,
					cpu_count = m.cpu_count + EXCLUDED.cpu_count,
					memory_sum = m.memory_sum + EXCLUDED.memory_sum,
					memory_count = m.memory_count + EXCLUDED.memory_count,
					disk_sum = m.disk_sum + EXCLUDED.disk_sum,
					disk_count = m.disk_count + EXCLUDED.disk_count,
					updated_at = now()
				RETURNING (xmax = 0)
			`, tenantID, serverID, int(resolution/time.Second), bucket.Start,
				bucket.CPUSum, bucket.CPUCount, bucket.MemorySum, bucket.MemoryCount,
				bucket.DiskSum, bucket.DiskCount).Scan(&inserted); err != nil {
				return err
			}
			if resolution == MetricFineResolution && inserted {
				fineOpened = true
			}
		}
		if !fineOpened {
			return nil
		}
		fineCutoff, coarseCutoff := MetricRetentionCutoffs(at)
		_, err := tx.ExecContext(ctx, `
			DELETE FROM server_metric_samples
			WHERE tenant_id = $1 AND server_id = $2 AND (
				(resolution_seconds = $3 AND bucket_start < $4) OR
				(resolution_seconds = $5 AND bucket_start < $6)
			)
		`, tenantID, serverID,
			int(MetricFineResolution/time.Second), fineCutoff,
			int(MetricCoarseResolution/time.Second), coarseCutoff)
		return err
	})
}

// recordGuardHostMetrics is the best-effort metrics tap of an accepted Guard
// inventory observation: a failure is logged and never fails the registry
// write that already committed.
func (s *PostgresStore) recordGuardHostMetrics(ctx context.Context, result *GuardInventoryProjectionResult, event ServerEvent) {
	if result == nil || result.ServerEvent == nil || !result.ServerEvent.Applied || result.ServerEvent.Server == nil {
		return
	}
	host, _ := event.Runtime.Metadata[serverEventHostKey].(map[string]any)
	sample, ok := HostMetricSample(host)
	if !ok {
		return
	}
	server := result.ServerEvent.Server
	if err := s.RecordServerMetricSample(ctx, server.TenantID, server.ID, server.UpdatedAt, sample); err != nil {
		slog.Warn("server_metric_sample_failed", "server_id", server.ID, "error", err.Error())
	}
}

func monitoringScopeTenantOwner(scope InventoryReadScope) (string, string, error) {
	if !inventoryScopeAllowsServerCollection(scope) {
		return "", "", ErrInvalidInventoryReadScope
	}
	return scope.tenantID, scope.ownerSubjectID, nil
}

// ListInventoryMetricHistory implements InventoryMonitoringReader.
func (s *PostgresStore) ListInventoryMetricHistory(ctx context.Context, scope InventoryReadScope, window MetricRange, now time.Time) ([]ServerMetricSeries, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerID, err := monitoringScopeTenantOwner(scope)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	from := MetricBucketStart(now.Add(-window.Window), window.Step)
	buckets := map[string][]MetricBucket{}
	order := []string{}
	err = s.withTenantInventorySnapshot(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT v.id, m.bucket_start, m.cpu_sum, m.cpu_count, m.memory_sum, m.memory_count, m.disk_sum, m.disk_count
			FROM (`+monitoringVisibleServers+` ORDER BY servers.created_at, servers.id LIMIT $5) v
			LEFT JOIN server_metric_samples m
				ON m.tenant_id = $1 AND m.server_id = v.id AND m.resolution_seconds = $3
				AND m.bucket_start >= $4 AND m.bucket_start <= $6
			ORDER BY v.created_at, v.id, m.bucket_start
		`, tenantID, ownerID, int(window.Resolution/time.Second), from, InventoryMonitoringServerLimit, now)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var serverID string
			var start sql.NullTime
			var cpuSum, memorySum, diskSum sql.NullFloat64
			var cpuCount, memoryCount, diskCount sql.NullInt64
			if scanErr := rows.Scan(&serverID, &start, &cpuSum, &cpuCount, &memorySum, &memoryCount, &diskSum, &diskCount); scanErr != nil {
				return scanErr
			}
			if _, seen := buckets[serverID]; !seen {
				buckets[serverID] = nil
				order = append(order, serverID)
			}
			if !start.Valid {
				continue
			}
			buckets[serverID] = append(buckets[serverID], MetricBucket{
				Start:  start.Time.UTC(),
				CPUSum: cpuSum.Float64, CPUCount: int(cpuCount.Int64),
				MemorySum: memorySum.Float64, MemoryCount: int(memoryCount.Int64),
				DiskSum: diskSum.Float64, DiskCount: int(diskCount.Int64),
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	series := make([]ServerMetricSeries, 0, len(order))
	for _, serverID := range order {
		series = append(series, ServerMetricSeries{ServerID: serverID, Points: DownsampleMetricBuckets(buckets[serverID], window.Step)})
	}
	return series, nil
}

// ListInventoryTransitions implements InventoryMonitoringReader. A first
// observation (no previous state) is a registration, not a change, and is not
// returned.
func (s *PostgresStore) ListInventoryTransitions(ctx context.Context, scope InventoryReadScope, since time.Time, limit int) ([]InventoryTransitionEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerID, err := monitoringScopeTenantOwner(scope)
	if err != nil {
		return nil, err
	}
	limit = clampTransitionLimit(limit)
	var events []InventoryTransitionEvent
	err = s.withTenantInventorySnapshot(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			WITH visible AS (`+monitoringVisibleServers+`)
			SELECT kind, id, name, server_id, dimension, from_state, to_state, reason_code, occurred_at FROM (
				SELECT 'server' AS kind, t.server_id AS id, v.label AS name, t.server_id AS server_id,
					t.dimension AS dimension, t.from_state AS from_state, t.to_state AS to_state,
					COALESCE(t.reason_code, '') AS reason_code, t.observed_at AS occurred_at
				FROM server_state_transitions t
				JOIN visible v ON v.id = t.server_id
				WHERE t.tenant_id = $1 AND t.dimension IN ('lifecycle', 'connection', 'health')
					AND t.from_state IS NOT NULL AND t.observed_at >= $3
				UNION ALL
				SELECT 'service', svc.id, svc.name, svc.server_id,
					t.dimension, t.from_state, t.to_state,
					COALESCE(t.reason_code, ''), t.observed_at
				FROM service_state_transitions t
				JOIN services svc ON svc.tenant_id = t.tenant_id AND svc.id = t.service_id
				JOIN visible v ON v.id = svc.server_id
				WHERE t.tenant_id = $1 AND t.dimension IN ('observed', 'health')
					AND t.from_state IS NOT NULL AND t.observed_at >= $3
			) e
			ORDER BY occurred_at DESC, kind, id, dimension
			LIMIT $4
		`, tenantID, ownerID, since.UTC(), limit)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var event InventoryTransitionEvent
			var dimension string
			var from sql.NullString
			if scanErr := rows.Scan(&event.Kind, &event.ID, &event.Name, &event.ServerID, &dimension,
				&from, &event.To, &event.ReasonCode, &event.At); scanErr != nil {
				return scanErr
			}
			event.Field = monitoringTransitionField(event.Kind, dimension)
			if event.Field == "" {
				continue
			}
			event.From = from.String
			event.At = event.At.UTC()
			events = append(events, event)
		}
		return rows.Err()
	})
	return events, err
}

// ListInventoryBackupFacts implements InventoryMonitoringReader.
func (s *PostgresStore) ListInventoryBackupFacts(ctx context.Context, scope InventoryReadScope, now time.Time) ([]InventoryBackupFacts, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID, ownerID, err := monitoringScopeTenantOwner(scope)
	if err != nil {
		return nil, err
	}
	var facts []InventoryBackupFacts
	err = s.withTenantInventorySnapshot(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `
			SELECT v.id, COALESCE(v.stack_id, ''), COALESCE(v.worker_id, ''),
				EXISTS (
					SELECT 1 FROM stack_backup_stores b
					WHERE b.tenant_id = $1 AND b.stack_id = v.stack_id
						AND b.token_revoked_at IS NULL AND b.wiped_at IS NULL
				),
				u.stored_bytes, u.measured_at,
				sch.cadence, sch.enabled, sch.last_run_at
			FROM (`+monitoringVisibleServers+` ORDER BY servers.created_at, servers.id LIMIT $3) v
			LEFT JOIN stack_backup_usage u ON u.tenant_id = $1 AND u.stack_id = v.stack_id
			LEFT JOIN stack_backup_schedules sch ON sch.tenant_id = $1 AND sch.stack_id = v.stack_id
			ORDER BY v.created_at, v.id
		`, tenantID, ownerID, InventoryMonitoringServerLimit)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var fact InventoryBackupFacts
			var storedBytes sql.NullInt64
			var measuredAt, lastRunAt sql.NullTime
			var cadence sql.NullString
			var enabled sql.NullBool
			if scanErr := rows.Scan(&fact.ServerID, &fact.StackID, &fact.WorkerID, &fact.OffsiteConfigured,
				&storedBytes, &measuredAt, &cadence, &enabled, &lastRunAt); scanErr != nil {
				return scanErr
			}
			if storedBytes.Valid {
				value := storedBytes.Int64
				fact.StoredBytes = &value
			}
			if measuredAt.Valid {
				value := measuredAt.Time.UTC()
				fact.UsageMeasuredAt = &value
			}
			if cadence.Valid {
				schedule := BackupScheduleFact{Cadence: cadence.String, Enabled: enabled.Bool}
				if lastRunAt.Valid {
					value := lastRunAt.Time.UTC()
					schedule.LastRunAt = &value
				}
				fact.Schedule = &schedule
			}
			facts = append(facts, fact)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		_ = rows.Close()
		runs, runsErr := backupRunsByStackTx(ctx, tx, tenantID, ownerID, now)
		if runsErr != nil {
			return runsErr
		}
		for i := range facts {
			facts[i].Runs = runs[facts[i].StackID]
		}
		return nil
	})
	return facts, err
}

// backupRunsByStackTx reads the finished backup jobs of the visible servers'
// stacks inside the window of the day summary. The stack set is the same
// scope-bound derived table as the facts query, so no stack outside the policy
// scope can contribute a run.
func backupRunsByStackTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID string, now time.Time) (map[string][]BackupRunFact, error) {
	runs := map[string][]BackupRunFact{}
	rows, err := tx.QueryContext(ctx, `
		SELECT j.stack_id, j.state, j.completed_at
		FROM jobs j
		WHERE j.tenant_id = $1 AND j.type = 'backup' AND j.state IN ('completed', 'failed')
			AND j.completed_at IS NOT NULL AND j.completed_at >= $4
			AND j.stack_id IN (
				SELECT v.stack_id FROM (`+monitoringVisibleServers+` ORDER BY servers.created_at, servers.id LIMIT $3) v
				WHERE v.stack_id IS NOT NULL
			)
		ORDER BY j.completed_at DESC
		LIMIT 2000
	`, tenantID, ownerID, InventoryMonitoringServerLimit, now.UTC().AddDate(0, 0, -(BackupRunWindow+1)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var stackID, state string
		var completedAt time.Time
		if scanErr := rows.Scan(&stackID, &state, &completedAt); scanErr != nil {
			return nil, scanErr
		}
		runs[stackID] = append(runs[stackID], BackupRunFact{At: completedAt.UTC(), State: state})
	}
	return runs, rows.Err()
}
