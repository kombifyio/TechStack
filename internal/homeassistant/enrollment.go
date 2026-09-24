package homeassistant

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
)

// EnrollmentStore persists only already authenticated, canonically validated
// runtime requests. Pending capture cannot mutate HA or accept credentials.
type EnrollmentStore struct {
	DB                  *sql.DB
	Encryptor           *auth.SecretEncryptor
	VerifyFreshEndpoint func(context.Context, string, string, string, string, string) error
}
type PendingOwner struct {
	BindingRef      string `json:"binding_ref"`
	StackID         string `json:"stack_id"`
	Origin          string `json:"origin"`
	ManagementScope string `json:"management_scope"`
	Bound           bool   `json:"bound"`
}
type AttachOwner struct {
	LifecycleGrants           Grants           `json:"lifecycle_grants"`
	DependencyAssessmentRef   string           `json:"dependency_assessment_ref"`
	RecoveryBindingRef        string           `json:"recovery_binding_ref"`
	RecoveryBackupOperationID string           `json:"recovery_backup_operation_id"`
	StackID                   string           `json:"stack_id"`
	BindingRef                string           `json:"binding_ref"`
	URL                       string           `json:"url"`
	Token                     string           `json:"token"`
	LeaseID                   string           `json:"lease_id,omitempty"`
	ConfigureGranted          bool             `json:"configure_granted"`
	Settings                  BaselineSettings `json:"settings"`
}

type attachedOwnerSettings struct {
	Baseline                  BaselineSettings `json:"baseline"`
	Grants                    Grants           `json:"grants"`
	DependencyAssessmentRef   string           `json:"dependency_assessment_ref"`
	RecoveryBindingRef        string           `json:"recovery_binding_ref"`
	RecoveryBackupOperationID string           `json:"recovery_backup_operation_id"`
}

func (s *EnrollmentStore) transaction(ctx context.Context, tenant string) (*sql.Tx, error) {
	if s == nil || s.DB == nil || s.Encryptor == nil || tenant == "" {
		return nil, ErrUnauthorized
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenant); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func (s *EnrollmentStore) Resolve(ctx context.Context, input managedstackkit.OperationsRequest, origin, scope string) (*OwnerBinding, error) {
	if input.TenantID == "" || input.StackID == "" || input.RuntimeAgentID == "" {
		return nil, ErrUnauthorized
	}
	if err := input.Request.Validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(input.TenantID + "\x00" + input.StackID + "\x00" + input.RuntimeAgentID + "\x00" + input.Request.PlanHash + "\x00" + input.Request.RuntimeTargets[0].InstanceRef))
	ref := "ha-owner-" + hex.EncodeToString(sum[:])
	raw, err := json.Marshal(input.Request)
	if err != nil {
		return nil, err
	}
	tx, err := s.transaction(ctx, input.TenantID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO home_assistant_owner_bindings(tenant_id,binding_ref,stack_id,runtime_agent_id,plan_hash,origin,management_scope,request_json)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8::jsonb WHERE EXISTS(SELECT 1 FROM stacks WHERE tenant_id=$1 AND id=$3) AND EXISTS(SELECT 1 FROM workers WHERE tenant_id=$1 AND id=$4 AND approved AND status<>'rejected') AND EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND stack_id=$3 AND worker_id=$4) ON CONFLICT DO NOTHING`, input.TenantID, ref, input.StackID, input.RuntimeAgentID, input.Request.PlanHash, origin, scope, raw)
	if err != nil {
		return nil, err
	}
	var storedRaw, settingsRaw []byte
	var endpoint, encrypted, leaseID sql.NullString
	var configure bool
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT b.request_json,b.settings_json,b.endpoint,b.token_enc,b.configure_granted,b.lease_id FROM home_assistant_owner_bindings b WHERE b.tenant_id=$1 AND b.binding_ref=$2 AND b.origin=$3 AND b.management_scope=$4 AND EXISTS(SELECT 1 FROM workers w WHERE w.tenant_id=b.tenant_id AND w.id=b.runtime_agent_id AND w.approved AND w.status<>'rejected') AND EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=b.tenant_id AND n.stack_id=b.stack_id AND n.worker_id=b.runtime_agent_id)`, input.TenantID, ref, origin, scope).Scan(&storedRaw, &settingsRaw, &endpoint, &encrypted, &configure, &leaseID)
	if err != nil {
		return nil, err
	}
	var retained managedstackkit.OperationsRequest
	retained.TenantID = input.TenantID
	retained.StackID = input.StackID
	retained.RuntimeAgentID = input.RuntimeAgentID
	if json.Unmarshal(storedRaw, &retained.Request) != nil || retained.Request.RequestDigest != input.Request.RequestDigest {
		return nil, ErrUnauthorized
	}
	if origin == "new" {
		if err = tx.QueryRowContext(ctx, `SELECT owner_subject_id FROM stacks WHERE tenant_id=$1 AND id=$2`, input.TenantID, input.StackID).Scan(&owner); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if !encrypted.Valid {
		return nil, errors.New("Home Assistant owner enrollment awaits explicit authenticated instance attachment")
	}
	if origin == "new" {
		if !leaseID.Valid || s.VerifyFreshEndpoint == nil {
			return nil, ErrUnauthorized
		}
		if err = s.VerifyFreshEndpoint(ctx, input.TenantID, owner, input.StackID, leaseID.String, endpoint.String); err != nil {
			return nil, err
		}
	}
	token, err := s.Encryptor.Decrypt(encrypted.String)
	if err != nil {
		return nil, err
	}
	core, err := NewLocalClient(ctx, endpoint.String, token)
	if err != nil {
		return nil, err
	}
	var settings attachedOwnerSettings
	if json.Unmarshal(settingsRaw, &settings) != nil {
		return nil, ErrUnauthorized
	}
	custodyRef := ref
	if origin == "new" {
		if !leaseID.Valid || leaseID.String == "" {
			return nil, ErrUnauthorized
		}
		custodyRef = "ha-lease/" + leaseID.String
	}
	// Plan generations change sealed targets, never the instance's configuration
	// ownership journal. Rebinding must retain intervening user customizations.
	instance := &InstanceBinding{BindingRef: custodyRef, Origin: origin, ManagementScope: scope, Core: core, Settings: settings.Baseline, ConfigureGranted: configure, Custody: &Journal{DB: s.DB, Encryptor: s.Encryptor, TenantID: input.TenantID}, DependencyAssessmentRef: settings.DependencyAssessmentRef, RecoveryBackupOperationID: settings.RecoveryBackupOperationID}
	if scope == "managed" {
		instance.Supervisor, err = NewSupervisorViaCore(ctx, endpoint.String, token, settings.Grants)
		if err != nil {
			return nil, err
		}
		if settings.Grants.Backup || settings.Grants.Restore || settings.Grants.Update {
			instance.Archives, err = s.archive(ctx, input.TenantID, input.StackID)
			if err != nil {
				return nil, err
			}
		}
	}
	return &OwnerBinding{RecoveryBindingRef: settings.RecoveryBindingRef, TenantID: input.TenantID, StackID: input.StackID, RuntimeAgentID: input.RuntimeAgentID, Target: input.Request.RuntimeTargets[0], Health: input.Request.HealthTargets, Instance: instance}, nil
}

func (s *EnrollmentStore) archive(ctx context.Context, tenant, stack string) (ArchiveStore, error) {
	custody, err := backupstore.NewPostgresCustodyStore(s.DB, s.Encryptor)
	if err != nil {
		return nil, err
	}
	credentials, err := custody.Get(ctx, tenant, stack)
	if err != nil {
		return nil, err
	}
	return backupstore.NewHomeAssistantArchive(ctx, credentials)
}

// Owned loads the retained request; the lifecycle caller cannot supply a target.
func (s *EnrollmentStore) Owned(ctx context.Context, tenant, owner, ref string) (*OwnerBinding, error) {
	tx, err := s.transaction(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var input managedstackkit.OperationsRequest
	input.TenantID = tenant
	var raw []byte
	var origin, scope string
	err = tx.QueryRowContext(ctx, `SELECT b.stack_id,b.runtime_agent_id,b.request_json,b.origin,b.management_scope FROM home_assistant_owner_bindings b JOIN stacks s ON s.tenant_id=b.tenant_id AND s.id=b.stack_id WHERE b.tenant_id=$1 AND b.binding_ref=$2 AND s.owner_subject_id=$3 AND EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=b.tenant_id AND n.stack_id=b.stack_id AND n.worker_id=b.runtime_agent_id) AND EXISTS(SELECT 1 FROM workers w WHERE w.tenant_id=b.tenant_id AND w.id=b.runtime_agent_id AND w.approved AND w.status<>'rejected')`, tenant, ref, owner).Scan(&input.StackID, &input.RuntimeAgentID, &raw, &origin, &scope)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if json.Unmarshal(raw, &input.Request) != nil {
		return nil, ErrUnauthorized
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	binding, err := s.Resolve(ctx, input, origin, scope)
	if err == nil {
		binding.OwnerID = owner
	}
	return binding, err
}

func (s *EnrollmentStore) List(ctx context.Context, tenant, owner, stack string) ([]PendingOwner, error) {
	tx, err := s.transaction(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT b.binding_ref,b.stack_id,b.origin,b.management_scope,b.token_enc IS NOT NULL FROM home_assistant_owner_bindings b JOIN stacks s ON s.tenant_id=b.tenant_id AND s.id=b.stack_id WHERE b.tenant_id=$1 AND b.stack_id=$2 AND s.owner_subject_id=$3 ORDER BY b.created_at`, tenant, stack, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingOwner{}
	for rows.Next() {
		var p PendingOwner
		if err = rows.Scan(&p.BindingRef, &p.StackID, &p.Origin, &p.ManagementScope, &p.Bound); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *EnrollmentStore) Attach(ctx context.Context, tenant, owner string, req AttachOwner) error {
	if _, err := req.Settings.values(); err != nil {
		return err
	}
	tx, err := s.transaction(ctx, tenant)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var origin string
	var bound bool
	err = tx.QueryRowContext(ctx, `SELECT b.origin,b.token_enc IS NOT NULL FROM home_assistant_owner_bindings b JOIN stacks s ON s.tenant_id=b.tenant_id AND s.id=b.stack_id WHERE b.tenant_id=$1 AND b.binding_ref=$2 AND b.stack_id=$3 AND s.owner_subject_id=$4 AND EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=b.tenant_id AND n.stack_id=b.stack_id AND n.worker_id=b.runtime_agent_id) AND EXISTS(SELECT 1 FROM workers w WHERE w.tenant_id=b.tenant_id AND w.id=b.runtime_agent_id AND w.approved AND w.status<>'rejected') FOR UPDATE OF b`, tenant, req.BindingRef, req.StackID, owner).Scan(&origin, &bound)
	if err != nil {
		return ErrUnauthorized
	}
	if bound {
		return errors.New("existing attachment must not be overwritten")
	}
	if req.LifecycleGrants.Backup || req.LifecycleGrants.Restore || req.LifecycleGrants.Update {
		if origin == "existing" || req.DependencyAssessmentRef == "" {
			return ErrUnauthorized
		}
		if _, err = s.archive(ctx, tenant, req.StackID); err != nil {
			return err
		}
	}
	if origin == "new" {
		var granted bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id WHERE g.tenant_id=$1 AND g.lease_id=$2 AND l.owner_subject_id=$3 AND l.lease_json #>> '{metadata,stack_id}'=$4 AND l.lease_json #>> '{metadata,runtime_offering_id}'='haos')`, tenant, req.LeaseID, owner, req.StackID).Scan(&granted)
		if err != nil {
			return err
		}
		if !granted {
			return ErrUnauthorized
		}
		if s.VerifyFreshEndpoint == nil {
			return errors.New("fresh HAOS endpoint requires native guest identity verification")
		}
		if err = s.VerifyFreshEndpoint(ctx, tenant, owner, req.StackID, req.LeaseID, req.URL); err != nil {
			return err
		}
	} else if req.ConfigureGranted {
		return ErrUnauthorized
	}
	core, err := NewLocalClient(ctx, req.URL, req.Token)
	if err != nil {
		return err
	}
	defer core.Close()
	if _, err = core.Observe(ctx); err != nil {
		return err
	}
	if origin == "new" {
		supervisor, e := NewSupervisorViaCore(ctx, req.URL, req.Token, Grants{})
		if e != nil {
			return e
		}
		defer supervisor.Close()
		if _, err = supervisor.Observe(ctx); err != nil {
			return err
		}
	}
	encrypted, err := s.Encryptor.Encrypt(req.Token)
	if err != nil {
		return err
	}
	settings, err := json.Marshal(attachedOwnerSettings{Baseline: req.Settings, Grants: req.LifecycleGrants, DependencyAssessmentRef: req.DependencyAssessmentRef, RecoveryBindingRef: req.RecoveryBindingRef, RecoveryBackupOperationID: req.RecoveryBackupOperationID})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE home_assistant_owner_bindings SET endpoint=$3,token_enc=$4,settings_json=$5::jsonb,configure_granted=$6,lease_id=NULLIF($7,'') WHERE tenant_id=$1 AND binding_ref=$2`, tenant, req.BindingRef, core.Endpoint(), encrypted, settings, req.ConfigureGranted, req.LeaseID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
