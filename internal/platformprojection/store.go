package platformprojection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/lib/pq"
)

type source struct{ db *sql.DB }

func (s source) ListServerProjectionTenants(ctx context.Context, after string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id FROM (
 SELECT tenant_id FROM public.servers UNION SELECT tenant_id FROM public.server_registry_outbox_prune_tenants
 ) tenants WHERE tenant_id>$1 ORDER BY tenant_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var tenant string
		if err = rows.Scan(&tenant); err != nil {
			return nil, err
		}
		out = append(out, tenant)
	}
	return out, rows.Err()
}

func (s source) ListProjectedServers(ctx context.Context, tenant string) ([]serverregistry.ProjectedServer, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	servers, err := readServers(ctx, tx, tenant)
	if err != nil {
		return nil, err
	}
	// Preserve the canonical legacy server_id=NULL mapping, including default-instance suppression.
	rows, err := tx.QueryContext(ctx, `SELECT server_id,service_key,name FROM (
 SELECT server_id,service_key,name,stack_id,id FROM public.services WHERE tenant_id=$1 AND server_id IS NOT NULL
 UNION ALL
 SELECT mapped.id,lower(btrim(legacy.service_key)),legacy.name,legacy.stack_id,legacy.id FROM public.services legacy
 LEFT JOIN LATERAL (
 SELECT candidate.id FROM public.servers candidate
 WHERE candidate.tenant_id=legacy.tenant_id AND candidate.stack_id=legacy.stack_id
 AND (candidate.id=legacy.node_id OR candidate.node_id=legacy.node_id)
 ORDER BY (candidate.id=legacy.node_id) DESC,candidate.updated_at DESC,candidate.id LIMIT 1
 ) mapped ON true
 WHERE legacy.tenant_id=$1 AND legacy.server_id IS NULL AND btrim(legacy.service_key)<>''
 AND NOT EXISTS (SELECT 1 FROM public.services measured WHERE measured.tenant_id=legacy.tenant_id
 AND measured.stack_id=legacy.stack_id AND measured.server_id=mapped.id
 AND lower(btrim(measured.service_key))=lower(btrim(legacy.service_key)) AND lower(btrim(measured.service_instance))='default')
 ) projected ORDER BY stack_id,service_key,id`, tenant)
	if err != nil {
		return nil, err
	}
	names := map[string][]string{}
	for rows.Next() {
		var id sql.NullString
		var key, name string
		if err = rows.Scan(&id, &key, &name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		label := safeLabel(firstNonEmptyString(name, key), 96)
		if id.Valid && label != "" {
			names[strings.TrimSpace(id.String)] = append(names[strings.TrimSpace(id.String)], label)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]serverregistry.ProjectedServer, 0, len(servers))
	for _, server := range servers {
		out = append(out, Server(server, names[strings.TrimSpace(server.ID)]))
	}
	return out, nil
}

type publisher struct{ db *sql.DB }

func (p publisher) PublishServers(ctx context.Context, tenant string, servers []serverregistry.ProjectedServer) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range servers {
		if s.TenantID != tenant || strings.TrimSpace(s.ServerID) == "" {
			return errors.New("platform projection batch identity mismatch")
		}
		observed, parseErr := time.Parse(time.RFC3339Nano, s.ObservedAt)
		if parseErr != nil {
			return errors.New("platform projection observation required")
		}
		var retired any
		if s.Retired {
			retired = observed
		}
		names := s.ServiceNames
		if names == nil {
			names = []string{}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO public.infra_user_servers (
 server_id,tenant_id,owner_id,org_id,name,provider,stack_id,stackkit_name,stackkit_version,lifecycle_state,connection_state,health_state,platform_os,service_names,service_count,aggregate_revision,retired_at,observed_at
 ) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
 ON CONFLICT(server_id) DO UPDATE SET
 tenant_id=EXCLUDED.tenant_id,owner_id=EXCLUDED.owner_id,org_id=EXCLUDED.org_id,name=EXCLUDED.name,provider=EXCLUDED.provider,stack_id=EXCLUDED.stack_id,
 stackkit_name=EXCLUDED.stackkit_name,stackkit_version=EXCLUDED.stackkit_version,lifecycle_state=EXCLUDED.lifecycle_state,connection_state=EXCLUDED.connection_state,
 health_state=EXCLUDED.health_state,platform_os=EXCLUDED.platform_os,service_names=EXCLUDED.service_names,service_count=EXCLUDED.service_count,
 aggregate_revision=EXCLUDED.aggregate_revision,retired_at=EXCLUDED.retired_at,observed_at=EXCLUDED.observed_at,updated_at=NOW()
 WHERE EXCLUDED.aggregate_revision>=infra_user_servers.aggregate_revision`, s.ServerID, tenant, s.OwnerID, s.OrgID, s.Name, s.Provider, s.StackID, s.StackkitName, s.StackkitVersion, s.LifecycleState, s.ConnectionState, s.HealthState, s.PlatformOS, pq.Array(names), s.ServiceCount, s.AggregateRevision, retired, observed)
		if err != nil {
			return err
		}
	}
	// Count the retained current view, not this bounded batch; mark and payload commit together.
	_, err = tx.ExecContext(ctx, `INSERT INTO public.infra_projection_marks(tenant_id,server_count,observed_at)
 VALUES($1,(SELECT count(*) FROM public.infra_user_servers WHERE tenant_id=$1 AND retired_at IS NULL),now())
 ON CONFLICT(tenant_id) DO UPDATE SET server_count=EXCLUDED.server_count,observed_at=EXCLUDED.observed_at`, tenant)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func readServers(ctx context.Context, tx *sql.Tx, tenant string) ([]serverregistry.Aggregate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,tenant_id,COALESCE(stack_id,''),owner_subject_id,name,lifecycle_state,connection_state,health_state,revision,metadata_json,decommissioned_at,updated_at FROM public.servers WHERE tenant_id=$1 ORDER BY id`, tenant)
	if err != nil {
		return nil, err
	}
	var servers []serverregistry.Aggregate
	for rows.Next() {
		var server serverregistry.Aggregate
		var metadata []byte
		var retired sql.NullTime
		if err = rows.Scan(&server.ID, &server.TenantID, &server.StackID, &server.OwnerSubjectID, &server.Name, &server.LifecycleState, &server.ConnectionState, &server.HealthState, &server.Revision, &metadata, &retired, &server.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(metadata, &server.Metadata); err != nil {
			_ = rows.Close()
			return nil, errors.New("invalid platform server metadata")
		}
		if retired.Valid {
			server.DecommissionedAt = &retired.Time
		}
		servers = append(servers, server)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return servers, nil
}
