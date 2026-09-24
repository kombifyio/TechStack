package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/internal/substrateprovision"
)

// The migration workflow only observes the VM that native admission reserved.
// It cannot choose another VM after a lost submission or retry.
type homeAssistantSubstrateProvisioner struct {
	db      *sql.DB
	service *substrateprovision.Service
	request substrateprovision.Request
}

func (p *homeAssistantSubstrateProvisioner) PrepareIsolatedGuest(ctx context.Context) (homeassistant.AdmittedHAOSGuest, error) {
	if p.request.Placement.ProfileID != "haos" || !p.request.Placement.Isolated {
		return homeassistant.AdmittedHAOSGuest{}, errors.New("migration requires an isolated HAOS placement")
	}
	guest, err := p.service.Prepare(ctx, p.request)
	if err != nil {
		return homeassistant.AdmittedHAOSGuest{}, err
	}
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return homeassistant.AdmittedHAOSGuest{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, p.request.TenantID); err != nil {
		return homeassistant.AdmittedHAOSGuest{}, err
	}
	var payload []byte
	var status, phase string
	err = tx.QueryRowContext(ctx, `SELECT d.spec_json,o.status,o.phase
 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id
 JOIN provider_operations o ON o.tenant_id=g.tenant_id AND o.operation_id=g.operation_id AND o.lease_id=g.lease_id
 JOIN provider_desired_spec_revisions d ON d.tenant_id=o.tenant_id AND d.lease_id=o.lease_id AND d.revision=o.desired_spec_revision
 WHERE g.tenant_id=$1 AND g.lease_id=$2 AND g.operation_id=$3 AND l.owner_subject_id=$4`, p.request.TenantID, guest.LeaseID, guest.OperationID, p.request.OwnerID).Scan(&payload, &status, &phase)
	if err != nil {
		return homeassistant.AdmittedHAOSGuest{}, err
	}
	var spec substrate.ExecutionSpec
	if json.Unmarshal(payload, &spec) != nil || spec.Action != "provision" || spec.Guest.ProfileID != "haos" || !spec.Guest.Isolated || spec.Guest.ID != guest.GuestID {
		return homeassistant.AdmittedHAOSGuest{}, errors.New("retained migration guest specification unavailable")
	}
	if status == "failed" || status == "denied" {
		return homeassistant.AdmittedHAOSGuest{}, errors.New("native migration guest admission or execution failed; inspect its retained operation")
	}
	return homeassistant.AdmittedHAOSGuest{LeaseID: guest.LeaseID, OperationID: guest.OperationID, Spec: spec.Guest, Ready: status == "succeeded" && phase == "present"}, nil
}
