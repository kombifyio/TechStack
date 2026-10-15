package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// UserNoticeAdmissionStore enforces the per-node rate limit of the opt-in
// `user_notification` agent-channel class (SERVER-CONNECTION-STANDARD
// section 9). It counts accepted notices per node inside a sliding window and
// keeps no text, recipient or audience.
type UserNoticeAdmissionStore interface {
	// AdmitUserNotice records one accepted notice for the node unless the node
	// already has limit notices inside window. A refusal reports how long until
	// the oldest counted notice leaves the window.
	AdmitUserNotice(ctx context.Context, tenantID, stackID string, now time.Time, limit int, window time.Duration) (admitted bool, retryAfter time.Duration, err error)
}

func (s *PostgresStore) AdmitUserNotice(ctx context.Context, tenantID, stackID string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if s == nil || s.db == nil {
		return false, 0, fmt.Errorf("controlplane: database not configured")
	}
	if tenantID == "" || stackID == "" || limit < 1 || window <= 0 {
		return false, 0, fmt.Errorf("controlplane: user notice admission requires a node, a limit and a window")
	}
	now = now.UTC()
	var admitted bool
	var retryAfter time.Duration
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// Serialize admissions of one node so two replicas cannot both pass the
		// last slot.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "user-notice:"+tenantID+":"+stackID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM user_notice_admissions
			WHERE tenant_id = $1 AND stack_id = $2 AND admitted_at < $3`,
			tenantID, stackID, now.Add(-2*window)); err != nil {
			return err
		}
		var count int
		var oldest sql.NullTime
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*), min(admitted_at) FROM user_notice_admissions
			WHERE tenant_id = $1 AND stack_id = $2 AND admitted_at > $3`,
			tenantID, stackID, now.Add(-window)).Scan(&count, &oldest); err != nil {
			return err
		}
		if count >= limit {
			if oldest.Valid {
				retryAfter = oldest.Time.Add(window).Sub(now)
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_notice_admissions (tenant_id, stack_id, admitted_at) VALUES ($1, $2, $3)`,
			tenantID, stackID, now); err != nil {
			return err
		}
		admitted = true
		return nil
	})
	if err != nil {
		return false, 0, err
	}
	if !admitted && retryAfter < time.Second {
		retryAfter = time.Second
	}
	return admitted, retryAfter, nil
}

// MemoryUserNoticeAdmissionStore is the in-memory limiter of tests and the
// embedded development loop without a database.
type MemoryUserNoticeAdmissionStore struct {
	mu       sync.Mutex
	admitted map[string][]time.Time
}

func NewMemoryUserNoticeAdmissionStore() *MemoryUserNoticeAdmissionStore {
	return &MemoryUserNoticeAdmissionStore{admitted: map[string][]time.Time{}}
}

func (s *MemoryUserNoticeAdmissionStore) AdmitUserNotice(_ context.Context, tenantID, stackID string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" || limit < 1 || window <= 0 {
		return false, 0, fmt.Errorf("controlplane: user notice admission requires a node, a limit and a window")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := tenantID + "\x00" + stackID
	var live []time.Time
	for _, at := range s.admitted[key] {
		if at.After(now.Add(-window)) {
			live = append(live, at)
		}
	}
	if len(live) >= limit {
		s.admitted[key] = live
		retry := live[0].Add(window).Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry, nil
	}
	s.admitted[key] = append(live, now)
	return true, 0, nil
}

var (
	_ UserNoticeAdmissionStore = (*PostgresStore)(nil)
	_ UserNoticeAdmissionStore = (*MemoryUserNoticeAdmissionStore)(nil)
)
