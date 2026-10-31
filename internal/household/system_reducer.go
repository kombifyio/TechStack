package household

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// The first failure travels with the count, so the worker can name its stage.
	var first error
	fail := func(stage string, err error) {
		failed++
		if first == nil {
			first = fmt.Errorf("%s: %w", stage, err)
		}
	}
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
			fail("project "+string(p.Action), err)
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
				fail("deliver activation", err)
				if err := r.deferAttempt(ctx, p, false); err != nil {
					return resolved, err
				}
				continue
			}
		}
		if acknowledged, err := r.Acknowledge(ctx, p, result.Status); err != nil {
			fail("acknowledge", err)
		} else if acknowledged {
			resolved++
		}
	}
	for i := 0; i < 5; i++ {
		swept, err := r.sweepLegacyRemoval(ctx, service)
		if err != nil {
			fail("legacy removal", err)
		}
		if !swept {
			break
		}
	}
	if failed > 0 {
		return resolved, fmt.Errorf("%d household projections remain pending, first at %w", failed, first)
	}
	return resolved, nil
}

// sweepLegacyRemoval claims one current removal that was acknowledged as none
// (no projection binding, platform-nt9a1) and runs the fenced legacy cleanup.
// A claim reschedules the row by 15 minutes, so a refused or unreachable case
// costs one node read per quarter hour. Only a complete cleanup marks the row
// projected, under the same revision, claim and no newer lifecycle event.
func (r *SystemReducer) sweepLegacyRemoval(ctx context.Context, service *Service) (bool, error) {
	var req LegacyRemoval
	var claimedAt time.Time
	var rawEmails string
	err := r.database.QueryRowContext(ctx, `WITH due AS (
  SELECT owner_subject,subject FROM household_node_projections p
  WHERE status='none' AND desired_action='remove' AND next_attempt_at<=now()
   AND NOT EXISTS(SELECT 1 FROM household_lifecycle_outbox e WHERE e.owner_subject=p.owner_subject AND e.subject=p.subject AND e.revision>p.revision)
  ORDER BY next_attempt_at,owner_subject,subject FOR UPDATE SKIP LOCKED LIMIT 1
 ), claimed AS (
  UPDATE household_node_projections p SET last_attempt_at=now(),next_attempt_at=now()+interval '15 minutes'
  FROM due WHERE p.owner_subject=due.owner_subject AND p.subject=due.subject RETURNING p.*
 ), households AS (
  SELECT s.id FROM spaces s, claimed c WHERE s.deleted_at IS NULL AND (s.created_by=c.owner_subject OR EXISTS(
   SELECT 1 FROM space_members o WHERE o.space_id=s.id AND o.subject=c.owner_subject AND o.role='owner' AND o.removed_at IS NULL))
 ) SELECT c.owner_subject,c.subject,c.revision,COALESCE(u.email,''),c.last_attempt_at,
  EXISTS(SELECT 1 FROM space_members m WHERE m.space_id IN(SELECT id FROM households) AND m.subject=c.subject AND m.removed_at IS NULL),
  to_json(ARRAY(SELECT DISTINCT lower(v.email) FROM users v WHERE v.email IS NOT NULL AND (v.auth_subject=c.owner_subject OR v.auth_subject IN(
   SELECT m.subject FROM space_members m WHERE m.space_id IN(SELECT id FROM households) AND m.removed_at IS NULL))))::text
  FROM claimed c LEFT JOIN users u ON u.auth_subject=c.subject`).Scan(&req.OwnerSubject, &req.Subject, &req.Revision, &req.Email, &claimedAt, &req.StillMember, &rawEmails)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var emails []string
	if err := json.Unmarshal([]byte(rawEmails), &emails); err != nil {
		return true, err
	}
	req.MemberEmails = map[string]bool{}
	for _, email := range emails {
		req.MemberEmails[email] = true
	}
	// The audit line names the pair by digest and the node account by stack and
	// username; the command itself stays in household_projection_attempts.
	req.Recheck = func(ctx context.Context) error {
		var current bool
		err := r.database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM household_node_projections p
  WHERE owner_subject=$1 AND subject=$2 AND revision=$3 AND last_attempt_at=$4 AND status='none' AND desired_action='remove'
   AND NOT EXISTS(SELECT 1 FROM household_lifecycle_outbox e WHERE e.owner_subject=p.owner_subject AND e.subject=p.subject AND e.revision>p.revision)
   AND NOT EXISTS(SELECT 1 FROM space_members m JOIN spaces s ON s.id=m.space_id WHERE m.subject=p.subject AND m.removed_at IS NULL AND s.deleted_at IS NULL
    AND (s.created_by=p.owner_subject OR EXISTS(SELECT 1 FROM space_members o WHERE o.space_id=s.id AND o.subject=p.owner_subject AND o.role='owner' AND o.removed_at IS NULL))))`,
			req.OwnerSubject, req.Subject, req.Revision, claimedAt).Scan(&current)
		if err == nil && !current {
			err = errors.New("the removal is no longer current")
		}
		return err
	}
	pair := sha256.Sum256([]byte(req.OwnerSubject + "\n" + req.Subject))
	outcomes, err := service.RemoveLegacyAccount(ctx, req)
	for _, outcome := range outcomes {
		log.Printf("household legacy removal pair=%s revision=%d stack=%s result=%s username=%s", hex.EncodeToString(pair[:8]), req.Revision, outcome.StackID, outcome.Result, outcome.Username)
	}
	if err != nil {
		log.Printf("household legacy removal pair=%s revision=%d pending: %v", hex.EncodeToString(pair[:8]), req.Revision, err)
		return true, err
	}
	_, err = r.database.ExecContext(ctx, `UPDATE household_node_projections p SET status='projected'
  WHERE owner_subject=$1 AND subject=$2 AND revision=$3 AND last_attempt_at=$4 AND status='none' AND desired_action='remove'
   AND NOT EXISTS(SELECT 1 FROM household_lifecycle_outbox e WHERE e.owner_subject=p.owner_subject AND e.subject=p.subject AND e.revision>p.revision)`,
		req.OwnerSubject, req.Subject, req.Revision, claimedAt)
	if err == nil {
		log.Printf("household legacy removal pair=%s revision=%d complete", hex.EncodeToString(pair[:8]), req.Revision)
	}
	return true, err
}
