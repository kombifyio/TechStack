package stackrouting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/notifications"
)

// DomainMonitoringPostgresStore is an internal collector boundary, never an
// untrusted observation ingestion API. Endpoint targets are derived from canonical
// inventory by its caller; revisioned routing targets additionally lock desired state.
type DomainMonitoringPostgresStore struct {
	routing       *PostgresStore
	notifications notifications.ProductEventTxEnqueuer
}

func NewDomainMonitoringPostgresStore(db *sql.DB, outbox notifications.ProductEventTxEnqueuer) *DomainMonitoringPostgresStore {
	return &DomainMonitoringPostgresStore{routing: NewPostgresStore(db), notifications: outbox}
}

func (s *DomainMonitoringPostgresStore) Load(ctx context.Context, p Principal, id string, now time.Time) (*DomainRow, error) {
	if s == nil || s.routing == nil || s.routing.db == nil {
		return nil, ErrUnavailable
	}
	if p.OwnerSubjectID == "" || id == "" {
		return nil, ErrInvalid
	}
	var out *DomainRow
	err := s.routing.withTenant(ctx, p.TenantID, func(tx *sql.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT m.snapshot_json::text FROM stack_domain_monitoring m JOIN stacks st ON st.tenant_id=m.tenant_id AND st.id=m.stack_id WHERE m.tenant_id=$1 AND m.owner_subject_id=$2 AND m.id=$3 AND st.owner_subject_id=$2 AND st.deleted_at IS NULL`, p.TenantID, p.OwnerSubjectID, id).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var row DomainRow
		if err = json.Unmarshal(raw, &row); err != nil {
			return err
		}
		row = RefreshDomain(row, now)
		out = &row
		return nil
	})
	return out, normalizeDomainStoreError(err)
}
func (s *DomainMonitoringPostgresStore) ListOwner(ctx context.Context, p Principal, now time.Time) ([]DomainRow, error) {
	if s == nil || s.routing == nil || s.routing.db == nil {
		return nil, ErrUnavailable
	}
	if p.OwnerSubjectID == "" {
		return nil, ErrInvalid
	}
	out := []DomainRow{}
	err := s.routing.withTenant(ctx, p.TenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT m.snapshot_json::text FROM stack_domain_monitoring m JOIN stacks st ON st.tenant_id=m.tenant_id AND st.id=m.stack_id WHERE m.tenant_id=$1 AND m.owner_subject_id=$2 AND st.owner_subject_id=$2 AND st.deleted_at IS NULL ORDER BY m.id`, p.TenantID, p.OwnerSubjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			var row DomainRow
			if err = json.Unmarshal(raw, &row); err != nil {
				return err
			}
			out = append(out, RefreshDomain(row, now))
		}
		return rows.Err()
	})
	return out, normalizeDomainStoreError(err)
}

// Save revalidates the owner and target inside a transaction, serializes condition
// episodes and atomically enqueues only transitions. A nil outbox persists facts
// without external delivery; this is also the supported development-probe mode.
func (s *DomainMonitoringPostgresStore) Save(ctx context.Context, target DomainTarget, row DomainRow, now time.Time) (*DomainRow, error) {
	if s == nil || s.routing == nil || s.routing.db == nil {
		return nil, ErrUnavailable
	}
	if strings.TrimSpace(target.OwnerSubjectID) == "" || target.StackID == "" || target.ServerID == "" || validateDomain(target.Domain) != nil || row.ID == "" || row.Domain != target.Domain || row.ServerID != target.ServerID || row.RoutingRevision != target.RoutingRevision || target.RoutingRevision < 0 {
		return nil, ErrInvalid
	}
	if row.StackID != "" && row.StackID != target.StackID {
		return nil, ErrInvalid
	}
	row.StackID = target.StackID
	if row.TLSPort == 0 {
		row.TLSPort = 443
	}
	if row.TLSPort < 1 || row.TLSPort > 65535 {
		return nil, ErrInvalid
	}
	now = now.UTC()
	if row.CheckStartedAt.IsZero() || row.CheckStartedAt.After(now) {
		return nil, ErrInvalid
	}
	var out *DomainRow
	err := s.routing.withTenant(ctx, target.TenantID, func(tx *sql.Tx) error {
		// Routing writers use this same lock before changing a desired revision.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(format('%s:%s:%s', octet_length($1::text), $1::text, $2::text),0))`, target.TenantID, target.StackID); err != nil {
			return err
		}
		var owned string
		if err := tx.QueryRowContext(ctx, `SELECT st.id FROM stacks st JOIN servers sv ON sv.tenant_id=st.tenant_id AND sv.id=$4 AND sv.stack_id=st.id AND sv.owner_subject_id=st.owner_subject_id WHERE st.tenant_id=$1 AND st.id=$2 AND st.owner_subject_id=$3 AND st.deleted_at IS NULL AND sv.lifecycle_state NOT IN ('decommissioning','decommissioned') AND sv.desired_state IN ('running','stopped') FOR SHARE OF st,sv`, target.TenantID, target.StackID, target.OwnerSubjectID, target.ServerID).Scan(&owned); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		desired, err := loadExistingRoutingStateForUpdate(ctx, tx, target.TenantID, target.StackID)
		if err != nil && (!errors.Is(err, ErrNotFound) || target.RoutingRevision > 0) {
			return err
		}
		if target.RoutingRevision > 0 {
			if desired.OwnerSubjectID != target.OwnerSubjectID || desired.ServerID != target.ServerID || desired.Domain != target.Domain || desired.Revision != target.RoutingRevision {
				return ErrRevisionConflict
			}
		} else if desired != nil && desired.ServerID == target.ServerID && desired.Domain == target.Domain {
			return ErrRevisionConflict
		}
		var raw []byte
		var previous *DomainRow
		var attempt time.Time
		err = tx.QueryRowContext(ctx, `SELECT snapshot_json::text,last_attempt_at FROM stack_domain_monitoring WHERE tenant_id=$1 AND id=$2 AND owner_subject_id=$3 FOR UPDATE`, target.TenantID, row.ID, target.OwnerSubjectID).Scan(&raw, &attempt)
		if err == nil {
			if !row.CheckStartedAt.After(attempt) {
				return ErrRevisionConflict
			}
			var p DomainRow
			if err = json.Unmarshal(raw, &p); err != nil {
				return err
			}
			if p.StackID != row.StackID || p.ServerID != row.ServerID || p.Domain != row.Domain || p.TLSPort != row.TLSPort {
				return ErrInvalid
			}
			previous = &p
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		next := EvaluateDomain(row, previous, now)
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO stack_domain_monitoring(tenant_id,id,owner_subject_id,stack_id,server_id,domain,routing_revision,snapshot_json,last_attempt_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9) ON CONFLICT(tenant_id,id) DO UPDATE SET routing_revision=EXCLUDED.routing_revision,snapshot_json=EXCLUDED.snapshot_json,last_attempt_at=EXCLUDED.last_attempt_at WHERE stack_domain_monitoring.owner_subject_id=EXCLUDED.owner_subject_id AND stack_domain_monitoring.stack_id=EXCLUDED.stack_id AND stack_domain_monitoring.server_id=EXCLUDED.server_id AND stack_domain_monitoring.domain=EXCLUDED.domain`, target.TenantID, row.ID, target.OwnerSubjectID, target.StackID, target.ServerID, target.Domain, target.RoutingRevision, encoded, row.CheckStartedAt)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return err
		} else if affected != 1 {
			return ErrRevisionConflict
		}
		if s.notifications != nil {
			if err = s.notifyTransitions(ctx, tx, target, next, previous); err != nil {
				return err
			}
		}
		out = &next
		return nil
	})
	return out, normalizeDomainStoreError(err)
}
func (s *DomainMonitoringPostgresStore) notifyTransitions(ctx context.Context, tx *sql.Tx, target DomainTarget, row DomainRow, previous *DomainRow) error {
	old := map[string]DomainCondition{}
	if previous != nil {
		for _, c := range previous.Conditions {
			old[c.ID] = c
		}
	}
	for _, c := range row.Conditions {
		p, exists := old[c.ID]
		if exists && p.State == c.State && p.Episode == c.Episode {
			continue
		}
		if !exists && c.State != "active" {
			continue
		}
		topic := "system.service-degraded"
		if c.State == "resolved" {
			topic = "system.fix-applied"
		}
		serviceIDs := []string{}
		for _, service := range row.Services {
			serviceIDs = append(serviceIDs, service.ID)
		}
		event := notifications.ProductEvent{Topic: topic, Auth0UserID: target.OwnerSubjectID, OrganizationID: target.TenantID, IdempotencyKey: fmt.Sprintf("domain-monitor:%s:%s:%d:%s", target.TenantID, c.ID, c.Episode, c.State), Payload: map[string]any{"subject": row.Domain, "body": domainConditionDescription(c), "message_id": "domains.kind." + strings.Split(c.Kind, ":")[0], "source_app": "techstack", "domain": row.Domain, "server_id": row.ServerID, "stack_id": target.StackID, "service_ids": serviceIDs, "services": row.Services, "condition": c, "registration": row.Registration, "tls": row.TLS, "dns": row.DNS, "tls_drift": row.TLSDrift, "link_url": "/monitoring"}}
		if err := s.notifications.EnqueueTx(ctx, tx, event); err != nil {
			return err
		}
	}
	return nil
}
func domainConditionDescription(c DomainCondition) string {
	if c.State == "resolved" {
		return "Fresh domain evidence confirms recovery. Open monitoring for the verified values."
	}
	switch c.Kind {
	case "registration_expiring":
		return "Domain registration is expiring. Verify the published registration deadline."
	case "certificate_expiring":
		return "The presented TLS certificate is expiring. Verify certificate renewal."
	case "dns_mismatch":
		return "Observed DNS differs from the saved configuration. Open monitoring for expected and observed records."
	case "certificate_mismatch":
		return "The presented certificate differs from the expected TLS configuration. Open monitoring for the comparison."
	case "registration_unavailable":
		return "The domain registration expiry could not be verified. Registration information remains unknown."
	default:
		return "A previous domain observation has aged beyond its freshness deadline. Current state is unverified."
	}
}
func normalizeDomainStoreError(err error) error {
	if err == nil {
		return nil
	}
	return normalizePersistenceError("domain monitoring", err)
}
