package executioncustody

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/kombifyio/techstack/pkg/db"
)

var errRequestReplay = errors.New("execution request was already admitted")

// claimEnvelope binds a genuine Gateway admission to one semantic request in
// the same durable audit authority as native transport replay. Both the binding
// and the one-create claim survive process restart and concurrent receivers.
func (g *GatewayAdmission) claimEnvelope(ctx context.Context, grant CallerGrant, key, purpose [32]byte, create bool) error {
	if g.audit == nil {
		return errors.New("Gateway admission audit is unavailable")
	}
	tx, err := db.BeginTenantTransaction(ctx, g.audit, grant.TenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, digest := hex.EncodeToString(key[:]), hex.EncodeToString(purpose[:])
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "execution.gateway/"+grant.TenantID+"/"+id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT action, details_json->>'purpose' FROM audit_events WHERE tenant_id=$1 AND actor_subject_id=$2 AND resource_type='native_gateway_admission' AND resource_id=$3`, grant.TenantID, grant.Caller, id)
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var action, prior string
		if err = rows.Scan(&action, &prior); err != nil {
			rows.Close()
			return err
		}
		if prior != digest || action == "execution.gateway.created" {
			rows.Close()
			return errRequestReplay
		}
		exists = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !exists || create {
		action := "execution.gateway.bound"
		if create {
			action = "execution.gateway.created"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(tenant_id,actor_subject_id,action,resource_type,resource_id,details_json) VALUES($1,$2,$3,'native_gateway_admission',$4,jsonb_build_object('purpose',$5::text))`, grant.TenantID, grant.Caller, action, id, digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// claimRequest commits to the existing tenant audit before an effect. The
// transaction lock serializes the exact caller/request check across replicas;
// the retained audit row makes restart or a lost ACK fail closed too. Domain
// retries still go through the existing provider/capability authorities with a
// newly authenticated request, never a transport's automatic redispatch.
func (p *Provider) claimRequest(ctx context.Context, grant CallerGrant, requestID, path string) error {
	tx, err := db.BeginTenantTransaction(ctx, p.AuditDatabase, grant.TenantID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const resourceType = "native_execution_request"
	identity := grant.TenantID + "/" + grant.Caller + "/" + requestID
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, identity); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE tenant_id=$1 AND actor_subject_id=$2 AND action='execution.request.admitted' AND resource_type=$3 AND resource_id=$4)`, grant.TenantID, grant.Caller, resourceType, requestID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errRequestReplay
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(tenant_id,actor_subject_id,action,resource_type,resource_id,details_json) VALUES($1,$2,'execution.request.admitted',$3,$4,jsonb_build_object('path',$5::text))`, grant.TenantID, grant.Caller, resourceType, requestID, path); err != nil {
		return err
	}
	return tx.Commit()
}

func mutates(action string) bool {
	switch action {
	case "worker-binding/sign", "worker-binding/local-grant", "worker-binding/predecessor-recovery", "issue", "bind", "provider/create", "provider/decommission", "provider/power", "provider/ssh-access", "provider/reconnect", "provider/discover", "provider/resolve", "provider/admit-customer-guest":
		return true
	default:
		return false
	}
}
