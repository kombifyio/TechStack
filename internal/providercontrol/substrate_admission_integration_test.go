package providercontrol

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/jackc/pgx/v5"
)

type substrateAdmissionExecutor struct{ queueExecutor }

func (*substrateAdmissionExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{AdapterManifestHash: substrate.AdapterManifestHash,
		Mode: CrashRecoveryProviderCorrelation, PerHeadInvocationKey: true,
		ProviderPersistedCorrelation: true, UniqueCorrelation: true, RecoveryByCorrelation: true}
}

// The provider-control public admission boundary must enforce customer custody
// transactionally, including when executed by its restricted PostgreSQL role.
func TestSubstrateAdmissionPreservesCustomerCustodyAcrossRetry(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	withNativeAdmissionTenantWrite(t, database, "tenant-1", func(tx *sql.Tx) {
		for _, statement := range []string{
			`INSERT INTO workers(id,tenant_id,hostname,owner_subject_id,type,provider) VALUES('pve-guard','tenant-1','pve','owner-1','substrate','proxmox')`,
			`INSERT INTO servers(id,tenant_id,stack_id,owner_subject_id,worker_id,name,lifecycle_state,desired_state,connection_state,health_state,last_heartbeat_at,metadata_json) VALUES('pve','tenant-1','stack-native-1','owner-1','pve-guard','Proxmox','active','running','connected','healthy',clock_timestamp(),'{"server_node_role":"substrate"}')`,
			`INSERT INTO substrate_bindings(tenant_id,server_id,worker_id,node,revision,enabled,config_json) VALUES('tenant-1','pve','pve-guard','pve',1,true,'{"min_guest_id":8000,"max_guest_id":8999}')`,
		} {
			if _, err := tx.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
	})
	runtimeDB := openRestrictedNativeAdmissionRuntimeDB(t, database)
	var role, schema string
	if err := runtimeDB.QueryRowContext(t.Context(), `SELECT current_user,current_schema()`).Scan(&role, &schema); err != nil {
		t.Fatal(err)
	}
	// Production admission already has INSERT for secret-free credential handles.
	if _, err := database.ExecContext(t.Context(), "GRANT INSERT ON "+pgx.Identifier{schema, "provider_credential_handles"}.Sanitize()+" TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	executor := &substrateAdmissionExecutor{}
	if err := registry.Register(substrate.AdapterID, executor); err != nil {
		t.Fatal(err)
	}
	profiles, err := NewPostgresCatalogExecutionProfileResolver(runtimeDB)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := NewPostgresLedger(runtimeDB, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := AllowGate{}
	coordinator, err := NewCoordinator(CoordinatorConfig{Registry: registry, Profiles: profiles, Ledger: ledger, ActivationGate: gate})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := NewNativeAdmission(NativeAdmissionConfig{Database: runtimeDB, Coordinator: coordinator, ActivationGate: gate, ProviderCreates: StaticProviderCreatePolicy{}, CapacityPolicy: staticAdmissionCapacityPolicy{err: errors.New("commercial capacity unavailable")}})
	if err != nil {
		t.Fatal(err)
	}
	request := nativeProvisionAdmissionFixture()
	request.Lease.CustodyClass = vmlease.CustodyCustomerSubstrate
	request.Lease.Subject.ID = request.TenantID
	request.Lease.Resource = vmlease.ResourceRef{ProviderID: "proxmox", EngineVMID: "pve/8000"}
	request.Lease.BillingMode = vmlease.BillingModeLocal
	request.Lease.LifecycleClass = vmlease.LifecycleClassOneTime
	request.Lease.RecreatePolicy = vmlease.RecreatePolicyNever
	request.Lease.Metadata = map[string]string{runtimeOfferingMetadataKey: "ubuntu-24.04", SubstrateServerMetadata: "pve", SubstrateRevisionMetadata: "1", SubstrateGuestMetadata: "8000"}
	// The restricted database boundary must hold the owner's grant stable until
	// admission commits without granting the runtime permission to edit it.
	lockedTx, err := runtimeDB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lockedTx.Rollback()
	if _, err := lockedTx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id','tenant-1',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := requireSubstrateBindingTx(t.Context(), lockedTx, request.TenantID, request.OwnerSubjectID, request.Lease); err != nil {
		t.Fatal(err)
	}
	competing, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := competing.ExecContext(t.Context(), `SELECT revision FROM substrate_bindings WHERE tenant_id='tenant-1' AND server_id='pve' FOR UPDATE NOWAIT`); err == nil {
		competing.Rollback()
		t.Fatal("binding update crossed an uncommitted guest admission")
	}
	competing.Rollback()
	if err := lockedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Operation.ExecutionProfile.CredentialMode != CredentialModeWorkerHeld {
		t.Fatalf("customer admission did not retain worker custody: %+v", created)
	}
	if err := admission.VerifyCustomerGuest(t.Context(), request.TenantID, request.OwnerSubjectID, request.Server.StackID, created.LeaseID, created.RuntimeServerID, created.Operation.Command.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := admission.VerifyCustomerGuest(t.Context(), request.TenantID, request.OwnerSubjectID, request.Server.StackID, created.LeaseID, created.RuntimeServerID, "unrelated-operation"); !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatal("queue accepted substituted guest operation")
	}
	replayed, err := admission.AdmitProvision(t.Context(), request)
	if err != nil || replayed.Created || replayed.Operation.Command.CommandDigest != created.Operation.Command.CommandDigest {
		t.Fatalf("replay lost original operation: %+v %v", replayed, err)
	}
	if executor.calls != 0 {
		t.Fatal("admission performed provider mutation")
	}
	duplicate := request
	duplicate.RuntimeSlotKey = "second"
	duplicate.RuntimeSlotID = DeriveManagedRuntimeSlotID(request.TenantID, request.Server.StackID, "second")
	duplicate.RuntimeServerID = "server-second"
	duplicate.Lease.ID = "lease-second"
	duplicate.IdempotencyKey = "second-admission"
	duplicate.DesiredSpecRef = "desired-spec://techstack/leases/lease-second/revisions/1"
	if _, err := admission.AdmitProvision(t.Context(), duplicate); !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatal("reserved guest number was lent to another lease")
	}
	assertNoNativeAdmissionAggregate(t, database, duplicate)
	if _, advanced, err := coordinator.Advance(t.Context(), request.TenantID, created.Operation.Command.OperationID); err != nil || !advanced {
		t.Fatalf("accept guest operation: advanced=%v err=%v", advanced, err)
	}
	withNativeAdmissionTenantWrite(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `UPDATE servers SET connection_state='offline' WHERE id='pve'`); err != nil {
			t.Fatal(err)
		}
	})
	if err := admission.PreflightProvision(t.Context(), request); !errors.Is(err, ErrClaimCredentialDenied) {
		t.Fatalf("offline Guard admitted: %v", err)
	}
	if _, err := admission.AdmitProvision(t.Context(), request); err != nil {
		t.Fatalf("offline retry cannot retrieve original custody: %v", err)
	}
	if _, advanced, err := coordinator.Advance(t.Context(), request.TenantID, created.Operation.Command.OperationID); err == nil && advanced {
		t.Fatal("offline Guard executed a guest operation")
	}
	if executor.calls != 0 {
		t.Fatal("offline execution reached the provider")
	}
	withNativeAdmissionTenantWrite(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `UPDATE servers SET connection_state='connected',last_heartbeat_at=clock_timestamp() WHERE id='pve'`); err != nil {
			t.Fatal(err)
		}
	})
	if _, advanced, err := coordinator.Advance(t.Context(), request.TenantID, created.Operation.Command.OperationID); err != nil || !advanced {
		t.Fatalf("restricted role guest execution claim: advanced=%v err=%v", advanced, err)
	}
	if executor.calls != 1 {
		t.Fatal("authorized guest execution did not reach its adapter")
	}
}
