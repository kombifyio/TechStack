// Package backupjobs projects the StackKits backup cadence into the control
// plane and drives due backups through the entitlement gate and the job queue.
//
// StackKits owns the schedule authority; this package only executes it. The
// projection exists because the control plane otherwise has no way to know a
// stack is due: BackupPolicyRef on service runtime placement is a reference
// string rather than a cadence, and stack_backup_stores has no cross-tenant
// listing.
package backupjobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const tenantGUC = "app.tenant_id"

// Cadence is the closed schedule vocabulary, mirroring #BackupScheduleV1.
type Cadence string

const (
	CadenceHourly Cadence = "hourly"
	CadenceDaily  Cadence = "daily"
	CadenceWeekly Cadence = "weekly"
)

var weekdayIndex = map[string]time.Weekday{
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
}

// Schedule is one stack's projected cadence.
type Schedule struct {
	TenantID       string
	StackID        string
	OwnerID        string
	StackName      string
	Cadence        Cadence
	HourUTC        int
	MinuteUTC      int
	WeekdayUTC     string
	IncludeContent bool
	Enabled        bool
}

func (s Schedule) normalize() (Schedule, error) {
	s.TenantID = strings.TrimSpace(s.TenantID)
	s.StackID = strings.TrimSpace(s.StackID)
	s.OwnerID = strings.TrimSpace(s.OwnerID)
	s.StackName = strings.TrimSpace(s.StackName)
	if s.TenantID == "" || s.StackID == "" {
		return Schedule{}, fmt.Errorf("backup schedule requires exact tenant and stack identity")
	}
	switch s.Cadence {
	case CadenceHourly, CadenceDaily, CadenceWeekly:
	default:
		return Schedule{}, fmt.Errorf("backup schedule cadence %q is not admissible", s.Cadence)
	}
	if s.HourUTC < 0 || s.HourUTC > 23 || s.MinuteUTC < 0 || s.MinuteUTC > 59 {
		return Schedule{}, fmt.Errorf("backup schedule time must be a valid UTC hour and minute")
	}
	if s.WeekdayUTC == "" {
		s.WeekdayUTC = "sunday"
	}
	s.WeekdayUTC = strings.ToLower(s.WeekdayUTC)
	if _, ok := weekdayIndex[s.WeekdayUTC]; !ok {
		return Schedule{}, fmt.Errorf("backup schedule weekday %q is not admissible", s.WeekdayUTC)
	}
	return s, nil
}

// NextDue returns the first firing strictly after after. Jitter is not applied
// here: the schedule is deterministic so a missed pass cannot silently shift a
// stack's slot, and spreading load is the scanner's batch size to solve.
func (s Schedule) NextDue(after time.Time) time.Time {
	after = after.UTC()
	switch s.Cadence {
	case CadenceHourly:
		candidate := time.Date(after.Year(), after.Month(), after.Day(), after.Hour(), s.MinuteUTC, 0, 0, time.UTC)
		for !candidate.After(after) {
			candidate = candidate.Add(time.Hour)
		}
		return candidate
	case CadenceWeekly:
		candidate := time.Date(after.Year(), after.Month(), after.Day(), s.HourUTC, s.MinuteUTC, 0, 0, time.UTC)
		target := weekdayIndex[s.WeekdayUTC]
		for candidate.Weekday() != target || !candidate.After(after) {
			candidate = candidate.AddDate(0, 0, 1)
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), s.HourUTC, s.MinuteUTC, 0, 0, time.UTC)
		}
		return candidate
	default:
		candidate := time.Date(after.Year(), after.Month(), after.Day(), s.HourUTC, s.MinuteUTC, 0, 0, time.UTC)
		for !candidate.After(after) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate
	}
}

// Due names one stack the scanner should back up now.
type Due struct {
	TenantID       string
	StackID        string
	OwnerID        string
	StackName      string
	IncludeContent bool
}

// Store persists and reads the projection.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("backup schedule store requires a database handle")
	}
	return &Store{db: db}, nil
}

// Upsert projects one stack's cadence. It is called from the deploy path, so
// the schedule tracks the plan rather than drifting from it.
func (s *Store) Upsert(ctx context.Context, schedule Schedule, now time.Time) error {
	normalized, err := schedule.normalize()
	if err != nil {
		return err
	}
	next := normalized.NextDue(now)
	return s.withTenant(ctx, normalized.TenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO stack_backup_schedules (
				tenant_id, stack_id, owner_id, stack_name, cadence,
				hour_utc, minute_utc, weekday_utc, include_content, enabled, next_due_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (tenant_id, stack_id) DO UPDATE SET
				owner_id = EXCLUDED.owner_id,
				stack_name = EXCLUDED.stack_name,
				cadence = EXCLUDED.cadence,
				hour_utc = EXCLUDED.hour_utc,
				minute_utc = EXCLUDED.minute_utc,
				weekday_utc = EXCLUDED.weekday_utc,
				include_content = EXCLUDED.include_content,
				enabled = EXCLUDED.enabled,
				-- next_due_at is only pulled earlier, never pushed out by a
				-- redeploy: a stack that redeploys daily must not postpone its
				-- own backup forever.
				next_due_at = LEAST(stack_backup_schedules.next_due_at, EXCLUDED.next_due_at),
				updated_at = now()
		`, normalized.TenantID, normalized.StackID, normalized.OwnerID, normalized.StackName,
			string(normalized.Cadence), normalized.HourUTC, normalized.MinuteUTC,
			normalized.WeekdayUTC, normalized.IncludeContent, normalized.Enabled, next)
		return execErr
	})
}

// ListDue reads due stacks across tenants through the SECURITY DEFINER
// function. The scanner is control-plane and tenant RLS correctly hides other
// tenants from a tenant-scoped connection, so the listing is delegated rather
// than the connection being granted BYPASSRLS.
func (s *Store) ListDue(ctx context.Context, now time.Time, afterTenant, afterStack string, limit int) ([]Due, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, stack_id, owner_id, stack_name, include_content
		FROM techstack_list_due_backup_schedules($1, $2, $3, $4)
	`, now.UTC(), strings.TrimSpace(afterTenant), strings.TrimSpace(afterStack), limit)
	if err != nil {
		return nil, fmt.Errorf("list due backup schedules: %w", err)
	}
	defer rows.Close()
	due := make([]Due, 0, limit)
	for rows.Next() {
		var item Due
		if err := rows.Scan(&item.TenantID, &item.StackID, &item.OwnerID, &item.StackName, &item.IncludeContent); err != nil {
			return nil, fmt.Errorf("scan due backup schedule: %w", err)
		}
		due = append(due, item)
	}
	return due, rows.Err()
}

// MarkRan advances a stack past its current slot. It runs whether the backup
// succeeded or was denied: a stack whose slot is never advanced is re-selected
// on every pass, which turns one denied tenant into an unbounded retry loop
// against the entitlement chain.
func (s *Store) MarkRan(ctx context.Context, tenantID, stackID string, now time.Time) error {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return fmt.Errorf("advancing a backup schedule requires exact tenant and stack identity")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var schedule Schedule
		var cadence string
		if err := tx.QueryRowContext(ctx, `
			SELECT cadence, hour_utc, minute_utc, weekday_utc
			FROM stack_backup_schedules WHERE tenant_id = $1 AND stack_id = $2
		`, tenantID, stackID).Scan(&cadence, &schedule.HourUTC, &schedule.MinuteUTC, &schedule.WeekdayUTC); err != nil {
			return err
		}
		schedule.Cadence = Cadence(cadence)
		_, err := tx.ExecContext(ctx, `
			UPDATE stack_backup_schedules
			SET last_run_at = $3, next_due_at = $4, updated_at = now()
			WHERE tenant_id = $1 AND stack_id = $2
		`, tenantID, stackID, now.UTC(), schedule.NextDue(now))
		return err
	})
}

func (s *Store) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
