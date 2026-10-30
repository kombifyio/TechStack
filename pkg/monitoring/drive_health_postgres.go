package monitoring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kombifyio/techstack/pkg/db/tenantcontext"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/google/uuid"
)

type PostgresDriveHealthStore struct{ db *sql.DB }

func NewPostgresDriveHealthStore(db *sql.DB) (*PostgresDriveHealthStore, error) {
	if db == nil {
		return nil, errors.New("drive health database is required")
	}
	return &PostgresDriveHealthStore{db: db}, nil
}
func (p *PostgresDriveHealthStore) tenant(ctx context.Context, tenant string, fn func(*sql.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = tenantcontext.ApplyTenant(ctx, tx, tenant); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func hydrateDriveState(s *DriveServerState, tenant, server, worker, epoch string, sequence int64) {
	s.TenantID = tenant
	s.ServerID = server
	s.WorkerID = worker
	s.SourceEpoch = epoch
	s.Sequence = sequence
	for i := range s.Alerts {
		s.Alerts[i].TenantID = tenant
	}
	for i := range s.Drives {
		for n := range s.Drives[i].Alerts {
			s.Drives[i].Alerts[n].TenantID = tenant
		}
	}
}
func (p *PostgresDriveHealthStore) Update(ctx context.Context, id guardtelemetry.Identity, fn func(*DriveServerState) ([]DriveAlert, error)) error {
	return p.tenant(ctx, id.TenantID, func(tx *sql.Tx) error {
		initial := DriveServerState{ServerID: id.ServerID, WorkerID: id.WorkerID, Drives: []DriveState{}, Alerts: []DriveAlert{}}
		body, _ := json.Marshal(initial)
		if _, err := tx.ExecContext(ctx, `INSERT INTO guard_drive_health(tenant_id,server_id,worker_id,snapshot_json) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,server_id) DO NOTHING`, id.TenantID, id.ServerID, id.WorkerID, body); err != nil {
			return err
		}
		var state DriveServerState
		var worker, epoch string
		var sequence int64
		if err := tx.QueryRowContext(ctx, `SELECT snapshot_json,worker_id,source_epoch,source_sequence FROM guard_drive_health WHERE tenant_id=$1 AND server_id=$2 FOR UPDATE`, id.TenantID, id.ServerID).Scan(&body, &worker, &epoch, &sequence); err != nil {
			return err
		}
		if err := json.Unmarshal(body, &state); err != nil {
			return fmt.Errorf("decode drive state: %w", err)
		}
		hydrateDriveState(&state, id.TenantID, id.ServerID, worker, epoch, sequence)
		events, err := fn(&state)
		if err != nil {
			return err
		}
		body, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE guard_drive_health SET worker_id=$3,source_epoch=$4,source_sequence=$5,snapshot_json=$6,updated_at=now() WHERE tenant_id=$1 AND server_id=$2`, id.TenantID, id.ServerID, state.WorkerID, state.SourceEpoch, state.Sequence, body); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO guard_drive_health_tenants(tenant_id) VALUES($1) ON CONFLICT(tenant_id) DO UPDATE SET refreshed_at=now()`, id.TenantID); err != nil {
			return err
		}
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO guard_drive_health_outbox(tenant_id,event_id,server_id,episode_id,event_json) VALUES($1,$2,$3,$4,$5)`, id.TenantID, uuid.NewString(), id.ServerID, event.ID, payload); err != nil {
				return err
			}
		}
		return nil
	})
}
func (p *PostgresDriveHealthStore) List(ctx context.Context, tenant, server string) ([]DriveServerState, error) {
	out := []DriveServerState{}
	err := p.tenant(ctx, tenant, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT server_id,worker_id,source_epoch,source_sequence,snapshot_json FROM guard_drive_health WHERE tenant_id=$1 AND ($2='' OR server_id=$2) ORDER BY server_id`, tenant, server)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s DriveServerState
			var server, worker, epoch string
			var sequence int64
			var body []byte
			if err = rows.Scan(&server, &worker, &epoch, &sequence, &body); err != nil {
				return err
			}
			if err = json.Unmarshal(body, &s); err != nil {
				return err
			}
			hydrateDriveState(&s, tenant, server, worker, epoch, sequence)
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// Deliver leases a bounded batch. A crash retries after the lease expires; the
// stable episode ID lets notification consumers deduplicate at-least-once delivery.
func (p *PostgresDriveHealthStore) Deliver(ctx context.Context, tenant string, n Notifier) error {
	type event struct {
		id    string
		order int64
		alert DriveAlert
	}
	var pending []event
	err := p.tenant(ctx, tenant, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM guard_drive_health_outbox WHERE tenant_id=$1 AND event_id IN (SELECT event_id FROM guard_drive_health_outbox WHERE tenant_id=$1 AND delivered_at<now()-interval '90 days' LIMIT 1000)`, tenant); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `WITH pending AS (SELECT tenant_id,event_id FROM guard_drive_health_outbox candidate WHERE tenant_id=$1 AND delivered_at IS NULL AND (lease_until IS NULL OR lease_until<now()) AND NOT EXISTS (SELECT 1 FROM guard_drive_health_outbox earlier WHERE earlier.tenant_id=candidate.tenant_id AND earlier.episode_id=candidate.episode_id AND earlier.delivered_at IS NULL AND earlier.event_order<candidate.event_order) ORDER BY event_order LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE guard_drive_health_outbox o SET lease_until=now()+interval '2 minutes' FROM pending p WHERE o.tenant_id=p.tenant_id AND o.event_id=p.event_id RETURNING o.event_id,o.event_json,o.event_order`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e event
			var body []byte
			if err = rows.Scan(&e.id, &body, &e.order); err != nil {
				return err
			}
			if err = json.Unmarshal(body, &e.alert); err != nil {
				return err
			}
			e.alert.TenantID = tenant
			pending = append(pending, e)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].order < pending[j].order })
	for _, e := range pending {
		notification := driveNotification(e.alert)
		notification.Labels["event_id"] = e.id
		notifyErr := n.Notify(ctx, notification)
		err = p.tenant(ctx, tenant, func(tx *sql.Tx) error {
			if notifyErr != nil {
				_, err := tx.ExecContext(ctx, `UPDATE guard_drive_health_outbox SET lease_until=$3 WHERE tenant_id=$1 AND event_id=$2`, tenant, e.id, time.Now().Add(time.Minute))
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE guard_drive_health_outbox SET delivered_at=now(),lease_until=NULL WHERE tenant_id=$1 AND event_id=$2`, tenant, e.id)
			return err
		})
		if err != nil {
			return err
		}
		if notifyErr != nil {
			return notifyErr
		}
	}
	return nil
}

func (p *PostgresDriveHealthStore) ListTenants(ctx context.Context, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := p.db.QueryContext(ctx, `SELECT tenant_id FROM guard_drive_health_tenants WHERE tenant_id>$1 OR ($1='' AND tenant_id='') ORDER BY tenant_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var tenant string
		if err = rows.Scan(&tenant); err != nil {
			return nil, err
		}
		out = append(out, tenant)
	}
	return out, rows.Err()
}
