package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	SubstrateServerMetadata   = "substrate_server_id"
	SubstrateRevisionMetadata = "substrate_binding_revision"
	SubstrateGuestMetadata    = "substrate_guest_id"
)

// SubstrateBinding is an owner-authorized placement declaration. ConfigJSON
// contains resource and image pins, never credentials; the Guard holds those.
type SubstrateBinding struct {
	ServerID   string          `json:"server_id"`
	WorkerID   string          `json:"worker_id"`
	Node       string          `json:"node"`
	Revision   int64           `json:"revision"`
	ConfigJSON json.RawMessage `json:"config"`
}

// VerifyCustomerGuest checks the immutable creation receipt, not caller-supplied
// correlation fields or a mutable server projection.
func (a *NativeAdmission) VerifyCustomerGuest(ctx context.Context, tenant, owner, stack, lease, server, operation string) error {
	if a == nil || a.db == nil {
		return ErrInvalidRequest
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenant); err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id JOIN servers s ON s.tenant_id=g.tenant_id AND s.id=g.server_id WHERE g.tenant_id=$1 AND l.owner_subject_id=$2 AND s.stack_id=$3 AND g.lease_id=$4 AND g.server_id=$5 AND g.operation_id=$6)`, tenant, owner, stack, lease, server, operation).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrNativeAdmissionConflict
	}
	return nil
}

func requireSubstrateBindingTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID string, lease vmlease.Lease) (SubstrateBinding, error) {
	return readSubstrateBindingTx(ctx, tx, tenantID, ownerID, lease, true)
}

func readSubstrateBindingTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID string, lease vmlease.Lease, lock bool) (SubstrateBinding, error) {
	var binding SubstrateBinding
	if lease.CustodyClass != vmlease.CustodyCustomerSubstrate || lease.Resource.ProviderID != "proxmox" || lease.BillingMode != vmlease.BillingModeLocal || lease.LifecycleClass != vmlease.LifecycleClassOneTime || lease.RecreatePolicy != vmlease.RecreatePolicyNever {
		return binding, fmt.Errorf("%w: invalid customer substrate custody", ErrInvalidRequest)
	}
	revision, err := strconv.ParseInt(lease.Metadata[SubstrateRevisionMetadata], 10, 64)
	guestID, idErr := strconv.Atoi(lease.Metadata[SubstrateGuestMetadata])
	serverID := lease.Metadata[SubstrateServerMetadata]
	if err != nil || idErr != nil || revision < 1 || serverID == "" || lease.Resource.EngineVMID != serverID+"/"+strconv.Itoa(guestID) {
		return binding, fmt.Errorf("%w: exact substrate revision and reserved guest identity required", ErrInvalidRequest)
	}
	if lock {
		var locked bool
		if err := tx.QueryRowContext(ctx, `SELECT substrate_lock_binding($1,$2,$3)`, tenantID, serverID, revision).Scan(&locked); err != nil {
			return binding, err
		} else if !locked {
			return binding, ErrClaimCredentialDenied
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT b.server_id,b.worker_id,b.node,b.revision,b.config_json
 FROM substrate_bindings b JOIN servers s ON s.tenant_id=b.tenant_id AND s.id=b.server_id
 WHERE b.tenant_id=$1 AND b.server_id=$2 AND b.revision=$3 AND b.enabled
 AND s.owner_subject_id=$4 AND s.worker_id=b.worker_id
 AND s.metadata_json->>'server_node_role'='substrate'
 AND s.connection_state='connected' AND s.lifecycle_state='active'
 AND s.last_heartbeat_at > clock_timestamp()-interval '2 minutes'
 AND $5 BETWEEN (b.config_json->>'min_guest_id')::int AND (b.config_json->>'max_guest_id')::int`, tenantID, serverID, revision, ownerID, guestID).Scan(&binding.ServerID, &binding.WorkerID, &binding.Node, &binding.Revision, &binding.ConfigJSON)
	if err != nil {
		return binding, fmt.Errorf("%w: authorized substrate Guard is unavailable", ErrClaimCredentialDenied)
	}
	return binding, nil
}

func (a *NativeAdmission) preflightSubstrate(ctx context.Context, request NativeProvisionAdmissionRequest) error {
	if err := a.activation.Require(ctx); err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config($1,$2,true)`, tenantContextKey, request.TenantID); err != nil {
		return err
	}
	if _, err = readSubstrateBindingTx(ctx, tx, request.TenantID, request.OwnerSubjectID, request.Lease, false); err != nil {
		return err
	}
	return tx.Commit()
}

func substrateCredentialHandle(lease vmlease.Lease, binding SubstrateBinding) credentialHandleRow {
	// Per-lease custody prevents one guest from borrowing another's grant.
	identity := "substrate/" + string(lease.ID)
	return credentialHandleRow{HandleID: identity, HandleVersion: binding.Revision, ProviderID: "proxmox", CredentialMode: CredentialModeWorkerHeld,
		SubjectKind: string(lease.Subject.Kind), SubjectID: lease.Subject.ID, GrantID: identity, Scope: "owned-guest",
		CustodyRef: "custody://" + identity, ConnectionRef: "provider-connection://substrate/" + binding.WorkerID + "/" + string(lease.ID),
		ValidFrom: lease.ValidFrom, ValidUntil: lease.ValidUntil}
}

func ensureSubstrateCredentialAuthorityTx(ctx context.Context, tx *sql.Tx, tenantID, ownerID string, lease vmlease.Lease) error {
	binding, err := requireSubstrateBindingTx(ctx, tx, tenantID, ownerID, lease)
	if err != nil {
		return err
	}
	handle := substrateCredentialHandle(lease, binding)
	if !validCredentialHandleIdentity(handle) {
		return ErrClaimCredentialDenied
	}
	custodyHash, err := credentialCustodyHash(tenantID, handle)
	if err != nil {
		return err
	}
	connectionHash, err := credentialConnectionHash(tenantID, handle)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_credential_handles
 (tenant_id,handle_id,handle_version,provider_id,credential_mode,subject_kind,subject_id,grant_id,credential_scope,custody_ref,connection_ref,custody_hash,connection_hash,valid_from,valid_until)
 VALUES ($1,$2,$3,'proxmox','worker_held',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		tenantID, handle.HandleID, handle.HandleVersion, handle.SubjectKind, handle.SubjectID, handle.GrantID, handle.Scope, handle.CustodyRef, handle.ConnectionRef, custodyHash, connectionHash, handle.ValidFrom, handle.ValidUntil)
	return err
}

func resolveSubstrateExecutionProfileTx(ctx context.Context, tx *sql.Tx, request ProfileRequest, selection leaseProfileSelection) (ExecutionProfile, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT lease_json FROM techstack_vm_leases WHERE tenant_id=$1 AND id=$2 AND lease_revision=$3`, request.TenantID, request.LeaseID, request.LeaseRevision).Scan(&raw); err != nil {
		return ExecutionProfile{}, err
	}
	var lease vmlease.Lease
	if json.Unmarshal(raw, &lease) != nil {
		return ExecutionProfile{}, ErrProfileUnavailable
	}
	binding, err := readSubstrateBindingTx(ctx, tx, request.TenantID, selection.OwnerSubjectID, lease, false)
	if err != nil {
		return ExecutionProfile{}, err
	}
	handle := substrateCredentialHandle(lease, binding)
	if err = tx.QueryRowContext(ctx, `SELECT custody_hash,connection_hash FROM provider_credential_handles WHERE tenant_id=$1 AND handle_id=$2 AND handle_version=$3 AND revoked_at IS NULL`, request.TenantID, handle.HandleID, handle.HandleVersion).Scan(&handle.PersistedCustodyHash, &handle.PersistedConnectionHash); err != nil {
		return ExecutionProfile{}, ErrProfileUnavailable
	}
	row := catalogProfileRow{CatalogVersion: "substrate-v1", ProviderID: "proxmox", AdapterID: "proxmox-substrate-v1", CredentialMode: CredentialModeWorkerHeld,
		RuntimeProfileID: "customer-substrate-v1", OfferingID: selection.OfferingID, CanPause: true, StopEffect: "pause", CanRecreate: false,
		AdapterManifestHash: "sha256:d0d36d0ce8eca8c262240fb4e3d08603acdb9fd2ebd0182ef1584b9a4c701876", ProvisionDispatchMode: ProvisionDispatchProviderCorrelation, CapabilitySnapshot: binding.ConfigJSON}
	profile, err := assembleCatalogExecutionProfile(request.TenantID, row, handle)
	if err != nil {
		return ExecutionProfile{}, err
	}
	_, _, err = normalizeExecutionProfile(profile)
	return profile, err
}

func insertSubstrateGuestCustodyTx(ctx context.Context, tx *sql.Tx, request NativeProvisionAdmissionRequest, result NativeProvisionAdmissionResult) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO substrate_guest_leases
 (tenant_id,lease_id,server_id,substrate_server_id,guest_id,slot_id,resource_generation_id,operation_id,request_digest)
 VALUES ($1,$2,$3,$4,$5,$6,$7::uuid,$8,$9)`, request.TenantID, result.LeaseID, result.RuntimeServerID, request.Lease.Metadata[SubstrateServerMetadata], request.Lease.Metadata[SubstrateGuestMetadata], request.RuntimeSlotID, result.ResourceGenerationID, result.Operation.Command.OperationID, result.RequestDigest)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: guest identity is already reserved", ErrNativeAdmissionConflict)
	}
	return err
}

func validateSubstrateGuestReplayTx(ctx context.Context, tx *sql.Tx, request NativeProvisionAdmissionRequest, result NativeProvisionAdmissionResult) error {
	var digest, serverID, operationID string
	err := tx.QueryRowContext(ctx, `SELECT request_digest,server_id,operation_id FROM substrate_guest_leases WHERE tenant_id=$1 AND lease_id=$2 AND slot_id=$3 AND resource_generation_id=$4::uuid`, request.TenantID, result.LeaseID, request.RuntimeSlotID, result.ResourceGenerationID).Scan(&digest, &serverID, &operationID)
	if err != nil || digest != result.RequestDigest || serverID != result.RuntimeServerID || operationID != result.Operation.Command.OperationID {
		return ErrNativeAdmissionConflict
	}
	return nil
}

func authorizeSubstrateClaimTx(ctx context.Context, tx *sql.Tx, record OperationRecord) error {
	var raw []byte
	var ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT lease_json,owner_subject_id FROM techstack_vm_leases WHERE tenant_id=$1 AND id=$2 AND lease_revision=$3`, record.Command.TenantID, record.Command.LeaseID, record.Command.LeaseRevision).Scan(&raw, &ownerID); err != nil {
		return ErrClaimCredentialDenied
	}
	var lease vmlease.Lease
	if json.Unmarshal(raw, &lease) != nil {
		return ErrClaimCredentialDenied
	}
	binding, err := requireSubstrateBindingTx(ctx, tx, record.Command.TenantID, ownerID, lease)
	if err != nil || record.Command.ConnectionRef != "provider-connection://substrate/"+binding.WorkerID+"/"+string(lease.ID) || record.Command.CustodyRef != "custody://substrate/"+string(lease.ID) {
		return ErrClaimCredentialDenied
	}
	return nil
}
