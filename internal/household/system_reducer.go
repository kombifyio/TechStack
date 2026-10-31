package household

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/internal/systemdb"
)

const CloudHouseholdSystemRole = "kombify_cloud_household_system"

// SystemReducer is the private consumer of Cloud's canonical lifecycle, not a
// user impersonator. Its connection must never be supplied to the HTTP app.
type SystemReducer struct{ database *sql.DB }

type PendingProjection struct {
	ProjectionRequest
	SpaceID, HouseholdName string
	AttemptedAt            time.Time
}

func NewSystemReducer(ctx context.Context, database *sql.DB) (*SystemReducer, error) {
	if database == nil {
		return nil, errors.New("household system database required")
	}
	if err := systemdb.AssertSystemRoleBesideWidening(ctx, database, CloudHouseholdSystemRole); err != nil {
		return nil, err
	}
	return &SystemReducer{database: database}, nil
}

// Reduce commits the canonical snapshot and its outbox watermark together.
// Reprocessing cannot change the effect payload under an existing generation.
func (r *SystemReducer) Reduce(ctx context.Context) (int, error) {
	var n int
	err := r.database.QueryRowContext(ctx, `SELECT household_reduce_lifecycle(50)`).Scan(&n)
	return n, err
}

// Pending durably reserves one due pair before any external effect. A crash
// leaves a bounded retry delay, never an acknowledgment or a new command right.
// Due-time FIFO rotates failing pairs behind all already-due work.
func (r *SystemReducer) Pending(ctx context.Context) ([]PendingProjection, error) {
	rows, err := r.database.QueryContext(ctx, `WITH due AS (
  SELECT owner_subject,subject FROM household_node_projections
  WHERE status='pending' AND next_attempt_at<=now()
  ORDER BY next_attempt_at,owner_subject,subject FOR UPDATE SKIP LOCKED LIMIT 1
 ), claimed AS (
  UPDATE household_node_projections p SET last_attempt_at=now(),next_attempt_at=now()+interval '5 minutes'
  FROM due WHERE p.owner_subject=due.owner_subject AND p.subject=due.subject RETURNING p.*
 ) SELECT p.space_id,p.owner_subject,p.subject,p.revision,p.desired_action,
  COALESCE(to_json(p.node_apps)::text,'null'),COALESCE(u.email,''),COALESCE(u.name,''),COALESCE(s.name,''),p.last_attempt_at
  FROM claimed p LEFT JOIN users u ON u.auth_subject=p.subject LEFT JOIN spaces s ON s.id=p.space_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []PendingProjection
	for rows.Next() {
		var p PendingProjection
		var raw string
		if err := rows.Scan(&p.SpaceID, &p.OwnerSubject, &p.Member.Subject, &p.Revision, &p.Action, &raw, &p.Member.Email, &p.Member.DisplayName, &p.HouseholdName, &p.AttemptedAt); err != nil {
			return nil, err
		}
		if raw != "null" {
			var catalog []string
			if json.Unmarshal([]byte(raw), &catalog) != nil {
				return nil, errors.New("invalid household desired apps")
			}
			apps, err := ParseNodeApps(catalog)
			if err != nil {
				return nil, err
			}
			p.Apps = AppGrant{Set: true, Apps: apps}
		}
		pending = append(pending, p)
	}
	return pending, rows.Err()
}

// A newly committed lifecycle event invalidates an old acknowledgment even
// before the reducer has materialized its newer desired projection.
func (r *SystemReducer) Acknowledge(ctx context.Context, p PendingProjection, status string) (bool, error) {
	if (status != StatusNone && status != StatusProjected) || (status == StatusNone && p.Action != ActionRemove) {
		return false, errors.New("invalid household acknowledgment")
	}
	result, err := r.database.ExecContext(ctx, `UPDATE household_node_projections p SET status=$4
  WHERE owner_subject=$1 AND subject=$2 AND revision=$3 AND last_attempt_at=$5 AND NOT EXISTS(SELECT 1 FROM household_lifecycle_outbox e
   WHERE e.owner_subject=p.owner_subject AND e.subject=p.subject AND e.revision>p.revision)`, p.OwnerSubject, p.Member.Subject, p.Revision, status, p.AttemptedAt)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// deferAttempt changes only scheduling, not desired state, generation or the
// retained command attempt. A newer lifecycle/claim cannot be delayed by this
// stale result. Add-without-inventory stays pending for periodic rediscovery.
func (r *SystemReducer) deferAttempt(ctx context.Context, p PendingProjection, inventoryMissing bool) error {
	delay := 60
	if inventoryMissing {
		delay = 300
	}
	_, err := r.database.ExecContext(ctx, `UPDATE household_node_projections
  SET next_attempt_at=now()+($5 * interval '1 second')
  WHERE owner_subject=$1 AND subject=$2 AND revision=$3 AND last_attempt_at=$4 AND status='pending'`,
		p.OwnerSubject, p.Member.Subject, p.Revision, p.AttemptedAt, delay)
	return err
}

// ActivationDelivery must call the existing Notifications dispatcher and
// positively read its recipient delivery. It must not queue/store the bearer.
type ActivationDelivery func(context.Context, PendingProjection, ProjectionResult) error

func (r *SystemReducer) ProjectPending(ctx context.Context, service *Service, deliver ActivationDelivery) (int, error) {
	if service == nil || deliver == nil {
		return 0, errors.New("household effect and delivery owners required")
	}
	if _, err := r.Reduce(ctx); err != nil {
		return 0, err
	}
	resolved, failed := 0, 0
	for i := 0; i < 50; i++ {
		pending, err := r.Pending(ctx)
		if err != nil {
			return resolved, err
		}
		if len(pending) == 0 {
			break
		}
		p := pending[0]
		result, err := service.Project(ctx, p.ProjectionRequest)
		if err != nil {
			failed++
			if err := r.deferAttempt(ctx, p, false); err != nil {
				return resolved, err
			}
			continue
		}
		if result.Status == StatusNone && p.Action == ActionAdd {
			if err := r.deferAttempt(ctx, p, true); err != nil {
				return resolved, err
			}
			continue
		}
		if result.ActivationURL != "" {
			if err := deliver(ctx, p, result); err != nil {
				failed++
				if err := r.deferAttempt(ctx, p, false); err != nil {
					return resolved, err
				}
				continue
			}
		}
		if acknowledged, err := r.Acknowledge(ctx, p, result.Status); err != nil {
			failed++
		} else if acknowledged {
			resolved++
		}
	}
	if failed > 0 {
		return resolved, fmt.Errorf("%d household projections remain pending", failed)
	}
	return resolved, nil
}
