package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func TestIntegrationProviderReceiptFinalizationProjectsRuntimeServerAtomically(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()

	serverResource := providerexecutor.ResourceBinding{
		BindingID:     "server",
		Kind:          "ionos.server",
		NativeRef:     "/cloudapi/v6/datacenters/dc-123/servers/ionos-server-123",
		OwnershipHash: digest("owner-server"),
		Disposition:   providerexecutor.DispositionDelete,
		Observation:   providerexecutor.ObservationUnknown,
		Cleanup:       providerexecutor.CleanupPending,
	}
	publicIPResource := providerexecutor.ResourceBinding{
		BindingID: "public-ip", Kind: "ionos.public-ip", NativeRef: "ionos-ip://192.0.2.123",
		ParentBindingID: "server", OwnershipHash: digest("owner-public-ip"),
		Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	boundResources := []providerexecutor.ResourceBinding{serverResource, publicIPResource}
	presentResources := append([]providerexecutor.ResourceBinding(nil), boundResources...)
	for index := range presentResources {
		presentResources[index].Observation = providerexecutor.ObservationPresent
		presentResources[index].Cleanup = providerexecutor.CleanupRequired
	}
	executor.mu.Lock()
	executor.results = []providerexecutor.ExecutionResult{
		{
			Status:    providerexecutor.StatusPending,
			Phase:     providerexecutor.PhaseResourcesBound,
			Resources: boundResources,
		},
		{
			Status:    providerexecutor.StatusSucceeded,
			Phase:     providerexecutor.PhasePresent,
			Resources: presentResources,
		},
	}
	executor.mu.Unlock()

	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	record := created.Operation
	for _, wantPhase := range []providerexecutor.Phase{
		providerexecutor.PhaseAccepted,
		providerexecutor.PhaseResourcesBound,
		providerexecutor.PhasePresent,
	} {
		var advanced bool
		record, advanced, err = admission.coordinator.Advance(
			t.Context(), request.TenantID, record.Command.OperationID,
		)
		if err != nil {
			t.Fatalf("Advance to %s: %v", wantPhase, err)
		}
		if !advanced || record.Head.Phase != wantPhase {
			t.Fatalf("Advance phase = %s, advanced=%t, want %s", record.Head.Phase, advanced, wantPhase)
		}
	}

	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycleState, desiredState, connectionState, healthState, leaseID string
		var outcomeStatus, outcomeReason string
		var revision, generation, inventoryRevision int64
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lifecycle_state, desired_state, connection_state, health_state,
				revision, generation, inventory_revision, lease_id,
				last_outcome_json #>> '{status}', last_outcome_json #>> '{reason_code}'
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&lifecycleState, &desiredState, &connectionState, &healthState,
			&revision, &generation, &inventoryRevision, &leaseID, &outcomeStatus, &outcomeReason,
		); err != nil {
			t.Fatalf("load projected runtime server: %v", err)
		}
		if lifecycleState != "enrolling" || desiredState != "running" ||
			connectionState != "pending" || healthState != "unknown" ||
			revision != 2 || generation != 1 || inventoryRevision != 0 ||
			leaseID != created.LeaseID || outcomeStatus != "pending" ||
			outcomeReason != "awaiting_guard_heartbeat" {
			t.Fatalf(
				"runtime server = lifecycle=%s desired=%s connection=%s health=%s revision=%d generation=%d inventory=%d lease=%s outcome=%s/%s",
				lifecycleState, desiredState, connectionState, healthState,
				revision, generation, inventoryRevision, leaseID, outcomeStatus, outcomeReason,
			)
		}

		var bindingCount int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT count(*)
			FROM server_provider_resource_bindings
			WHERE tenant_id = $1 AND server_id = $2 AND server_generation = 1
				AND lease_id = $3 AND operation_id = $4 AND binding_id = 'server'
		`, request.TenantID, request.RuntimeServerID, created.LeaseID,
			record.Command.OperationID).Scan(&bindingCount); err != nil {
			t.Fatalf("count provider resource bindings: %v", err)
		}
		if bindingCount != 1 {
			t.Fatalf("provider server binding count = %d, want 1", bindingCount)
		}

		var transitionCount, outboxCount int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT count(*)
			FROM server_state_transitions
			WHERE tenant_id = $1 AND server_id = $2 AND dimension = 'lifecycle'
				AND from_state = 'planned' AND to_state = 'enrolling'
				AND reason_code = 'provider_present'
				AND source = 'provider-control-finalizer'
		`, request.TenantID, request.RuntimeServerID).Scan(&transitionCount); err != nil {
			t.Fatalf("count provider lifecycle transitions: %v", err)
		}
		if transitionCount != 1 {
			t.Fatalf("provider lifecycle transition count = %d, want 1", transitionCount)
		}
		if err := tx.QueryRowContext(t.Context(), `
			SELECT count(*)
			FROM server_registry_outbox
			WHERE tenant_id = $1 AND server_id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(&outboxCount); err != nil {
			t.Fatalf("count server registry outbox: %v", err)
		}
		if outboxCount != 2 {
			t.Fatalf("server registry outbox count = %d, want admission + provider finalization", outboxCount)
		}
	})

	replayed, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, record.Command.OperationID,
	)
	if err != nil || advanced || replayed.Head.ReceiptDigest != record.Head.ReceiptDigest {
		t.Fatalf("terminal replay = %#v, advanced=%t, err=%v", replayed.Head, advanced, err)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var receipts, bindings, transitions, outbox int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
				(SELECT count(*) FROM provider_operation_receipts
				 WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM server_provider_resource_bindings
				 WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM server_state_transitions
				 WHERE tenant_id = $1 AND server_id = $3 AND dimension = 'lifecycle'
				   AND source = 'provider-control-finalizer'),
				(SELECT count(*) FROM server_registry_outbox
				 WHERE tenant_id = $1 AND server_id = $3)
		`, request.TenantID, record.Command.OperationID, request.RuntimeServerID).Scan(
			&receipts,
			&bindings,
			&transitions,
			&outbox,
		); err != nil {
			t.Fatalf("load terminal replay counts: %v", err)
		}
		if receipts != 4 || bindings != 2 || transitions != 1 || outbox != 2 {
			t.Fatalf(
				"terminal replay counts = receipts=%d bindings=%d transitions=%d outbox=%d",
				receipts,
				bindings,
				transitions,
				outbox,
			)
		}
	})
}

func TestIntegrationProviderReceiptProjectionRollsBackEveryCustodyWrite(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "compute", NativeRef: "ionos-server-rollback",
		OwnershipHash: digest("owner-rollback"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	executor.mu.Lock()
	executor.results = []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		Resources: []providerexecutor.ResourceBinding{resource},
	}}
	executor.mu.Unlock()

	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v, advanced=%t, err=%v", accepted.Head, advanced, err)
	}

	injected := errors.New("injected runtime projection failure")
	admission.ledger.runtimeProjection = failingReceiptRuntimeProjector{
		delegate: admission.ledger.runtimeProjection,
		err:      injected,
	}
	if _, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	); !errors.Is(err, injected) || advanced {
		t.Fatalf("failed projection advanced=%t error=%v, want injected rollback", advanced, err)
	}

	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var phase string
		var receiptCount, resourceCount, bindingCount int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
				(SELECT phase FROM provider_operations
				 WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM provider_operation_receipts
				 WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM provider_operation_resources
				 WHERE tenant_id = $1 AND operation_id = $2),
				(SELECT count(*) FROM server_provider_resource_bindings
				 WHERE tenant_id = $1 AND operation_id = $2)
		`, request.TenantID, created.Operation.Command.OperationID).Scan(
			&phase,
			&receiptCount,
			&resourceCount,
			&bindingCount,
		); err != nil {
			t.Fatalf("load rolled-back projection: %v", err)
		}
		if phase != string(providerexecutor.PhaseAccepted) ||
			receiptCount != 2 || resourceCount != 0 || bindingCount != 0 {
			t.Fatalf(
				"rollback state = phase=%s receipts=%d resources=%d bindings=%d",
				phase,
				receiptCount,
				resourceCount,
				bindingCount,
			)
		}
	})
}

func TestIntegrationProviderReceiptCannotReviveNewerRuntimeServerGeneration(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	executor.mu.Lock()
	executor.results = []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
		Resources: []providerexecutor.ResourceBinding{{
			BindingID: "server", Kind: "compute", NativeRef: "ionos-server-stale",
			OwnershipHash: digest("owner-stale"), Disposition: providerexecutor.DispositionDelete,
			Observation: providerexecutor.ObservationPresent, Cleanup: providerexecutor.CleanupRequired,
		}},
	}}
	executor.mu.Unlock()

	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v, advanced=%t, err=%v", accepted.Head, advanced, err)
	}
	rebound, err := admission.servers.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: request.TenantID, ServerID: request.RuntimeServerID,
		ExpectedRevision: 1, Generation: 2,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    "generation-test", SourceID: "generation-test:2",
		ObservedAt: time.Now().UTC(),
		Runtime:    controlplane.ServerRuntime{ProviderRef: "centron"},
		Evidence:   map[string]any{"reason": "test_generation_fence"},
	})
	if err != nil || rebound == nil || !rebound.Applied || rebound.Server.Generation != 2 {
		t.Fatalf("rebind runtime server = %#v, err=%v", rebound, err)
	}

	if _, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	); !errors.Is(err, ErrLeaseFence) || advanced {
		t.Fatalf("stale generation advance=%t error=%v, want ErrLeaseFence", advanced, err)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle string
		var generation int64
		var resources, bindings int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lifecycle_state, generation,
				(SELECT count(*) FROM provider_operation_resources
				 WHERE tenant_id = $1 AND operation_id = $3),
				(SELECT count(*) FROM server_provider_resource_bindings
				 WHERE tenant_id = $1 AND operation_id = $3)
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID,
			created.Operation.Command.OperationID).Scan(
			&lifecycle,
			&generation,
			&resources,
			&bindings,
		); err != nil {
			t.Fatalf("load stale generation state: %v", err)
		}
		if lifecycle != string(serverregistry.LifecyclePlanned) ||
			generation != 2 || resources != 0 || bindings != 0 {
			t.Fatalf(
				"stale generation state = lifecycle=%s generation=%d resources=%d bindings=%d",
				lifecycle,
				generation,
				resources,
				bindings,
			)
		}
	})
}

func TestIntegrationProviderIdentityDriftFencesBeforeAdapterExecution(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin provider drift transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(
		t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID,
	); err != nil {
		t.Fatalf("set provider drift tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE servers
		SET provider_ref = 'centron'
		WHERE tenant_id = $1 AND id = $2
	`, request.TenantID, request.RuntimeServerID); err != nil {
		t.Fatalf("drift RuntimeServer provider identity: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit RuntimeServer provider drift: %v", err)
	}

	callsBefore := queueExecutorCallCount(executor)
	if _, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	); !errors.Is(err, ErrLeaseFence) || advanced {
		t.Fatalf("provider drift advance = advanced=%t err=%v, want ErrLeaseFence", advanced, err)
	}
	if calls := queueExecutorCallCount(executor); calls != callsBefore {
		t.Fatalf("provider adapter calls after identity drift = %d, want %d", calls, callsBefore)
	}
}

func TestIntegrationProviderReceiptCutoverBackfillsExactProviderAndRejectsLiveClaim(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin provider backfill setup: %v", err)
	}
	if _, err := tx.ExecContext(
		t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID,
	); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set provider backfill tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE servers
		SET provider_ref = NULL
		WHERE tenant_id = $1 AND id = $2
	`, request.TenantID, request.RuntimeServerID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("clear historical RuntimeServer provider: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit provider backfill setup: %v", err)
	}

	migrationPath := filepath.Join(
		"..", "..", "pkg", "db", "migrations",
		"035_provider_receipt_runtime_server_projection.sql",
	)
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read provider receipt projection migration: %v", err)
	}
	applyMigration := func() error {
		migrationTx, beginErr := database.BeginTx(t.Context(), nil)
		if beginErr != nil {
			return beginErr
		}
		if _, execErr := migrationTx.ExecContext(t.Context(), string(migration)); execErr != nil {
			_ = migrationTx.Rollback()
			return execErr
		}
		return migrationTx.Commit()
	}
	if err := applyMigration(); err != nil {
		t.Fatalf("reapply provider receipt migration for exact backfill: %v", err)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(assertTx *sql.Tx) {
		var providerRef string
		if err := assertTx.QueryRowContext(t.Context(), `
			SELECT provider_ref
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(&providerRef); err != nil {
			t.Fatalf("load backfilled RuntimeServer provider: %v", err)
		}
		if providerRef != "ionos" {
			t.Fatalf("backfilled RuntimeServer provider = %q, want ionos", providerRef)
		}
	})

	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}
	claim, err := admission.ledger.AcquireExecutionClaim(
		t.Context(),
		accepted.Command,
		accepted.Head,
		ExecutionClaimSideEffecting,
		"cutover-gate-worker",
		"cutover-gate-token",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim: %v", err)
	}
	t.Cleanup(func() {
		_ = admission.ledger.ReleaseExecutionClaim(context.Background(), claim)
	})
	if err := applyMigration(); err == nil ||
		!strings.Contains(err.Error(), "all unsettled side-effecting provider claims") {
		t.Fatalf("migration with live provider claim error = %v", err)
	}
}

func TestIntegrationExactInFlightClaimCanHeartbeatDuringTeardown(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}
	claim, err := admission.ledger.AcquireExecutionClaim(
		t.Context(),
		accepted.Command,
		accepted.Head,
		ExecutionClaimSideEffecting,
		"heartbeat-test-worker",
		"heartbeat-test-token",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("AcquireExecutionClaim: %v", err)
	}

	markNativeAdmissionServerDecommissioning(t, admission, database, request, 1)
	renewed, err := admission.ledger.RenewExecutionClaim(t.Context(), claim, 2*time.Minute)
	if err != nil {
		t.Fatalf("renew exact in-flight claim during teardown: %v", err)
	}
	if !renewed.ExpiresAt.After(claim.ExpiresAt) {
		t.Fatalf("renewed expiry = %s, want after %s", renewed.ExpiresAt, claim.ExpiresAt)
	}
	if err := admission.ledger.ReleaseExecutionClaim(t.Context(), renewed); err != nil {
		t.Fatalf("release exact in-flight claim during teardown: %v", err)
	}
}

func TestIntegrationUnsettledSideEffectHeadBlocksTeardownAndAllowsExactRecovery(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		release bool
	}{
		{name: "released_without_receipt", release: true},
		{name: "expired_without_receipt"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := openNativeAdmissionIntegrationDB(t)
			seedNativeAdmissionCredentialHandle(t, database)
			admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
			request := nativeProvisionAdmissionFixture()
			created, err := admission.AdmitProvision(t.Context(), request)
			if err != nil {
				t.Fatalf("AdmitProvision: %v", err)
			}
			accepted, advanced, err := admission.coordinator.Advance(
				t.Context(), request.TenantID, created.Operation.Command.OperationID,
			)
			if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
				t.Fatalf("accepted transition = %#v advanced=%t err=%v", accepted.Head, advanced, err)
			}
			claim, err := admission.ledger.AcquireExecutionClaim(
				t.Context(),
				accepted.Command,
				accepted.Head,
				ExecutionClaimSideEffecting,
				"unsettled-worker",
				"unsettled-token",
				time.Second,
			)
			if err != nil {
				t.Fatalf("AcquireExecutionClaim: %v", err)
			}
			if testCase.release {
				if err := admission.ledger.ReleaseExecutionClaim(t.Context(), claim); err != nil {
					t.Fatalf("release ambiguous side-effect claim: %v", err)
				}
			}

			markNativeAdmissionServerDecommissioning(t, admission, database, request, 1)
			if !testCase.release {
				wait := time.Until(claim.ExpiresAt) + 100*time.Millisecond
				if wait > 0 {
					time.Sleep(wait)
				}
			}
			if _, _, err := admission.coordinator.Start(t.Context(), StartRequest{
				TenantID: request.TenantID, LeaseID: created.LeaseID,
				LeaseRevision: created.LeaseRevision, RuntimeServerID: request.RuntimeServerID,
				ResourceGenerationID: created.ResourceGenerationID,
				Operation:            providerexecutor.OperationDecommission,
				IdempotencyKey:       "blocked-by-unsettled-" + testCase.name,
				LedgerRevision:       2,
				Targets: []providerexecutor.ResourceTarget{{
					BindingID: "unsettled-placeholder", Kind: "compute",
					NativeRef:     "ionos-unsettled-placeholder",
					OwnershipHash: digest("unsettled-placeholder"),
					Disposition:   providerexecutor.DispositionDelete,
				}},
				RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
			}); !errors.Is(err, ErrLeaseFence) {
				t.Fatalf("decommission with unsettled provider side effect error = %v, want ErrLeaseFence", err)
			}

			recovered, err := admission.ledger.AcquireExecutionClaim(
				t.Context(),
				accepted.Command,
				accepted.Head,
				ExecutionClaimSideEffecting,
				"recovery-worker",
				"recovery-token",
				time.Minute,
			)
			if err != nil {
				t.Fatalf("exact unsettled head recovery during teardown: %v", err)
			}
			if recovered.OperationID != claim.OperationID ||
				recovered.HeadReceiptDigest != claim.HeadReceiptDigest {
				t.Fatalf("recovered claim changed immutable head: got %#v want %#v", recovered, claim)
			}
		})
	}
}

func TestIntegrationInFlightProvisionReceiptProjectsAcrossGuardEnrollmentGeneration(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "compute", NativeRef: "/cloudapi/v6/datacenters/dc-in-flight/servers/server-in-flight",
		OwnershipHash: digest("owner-in-flight"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	publicIP := providerexecutor.ResourceBinding{
		BindingID: "public-ip", Kind: "ionos.public-ip", NativeRef: "ionos-ip://192.0.2.124",
		ParentBindingID: "server", OwnershipHash: digest("owner-in-flight-ip"),
		Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	executor := &blockingResultExecutor{
		entered: make(chan struct{}), release: make(chan struct{}),
		result: providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
			Resources: []providerexecutor.ResourceBinding{resource, publicIP},
		},
	}
	admission := newNativeAdmissionIntegrationServiceWithExecutor(t, database, executor, nil)
	request := nativeProvisionAdmissionFixture()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v, advanced=%t, err=%v", accepted.Head, advanced, err)
	}

	type advanceResult struct {
		record   OperationRecord
		advanced bool
		err      error
	}
	completed := make(chan advanceResult, 1)
	go func() {
		record, didAdvance, advanceErr := admission.coordinator.Advance(
			context.Background(), request.TenantID, created.Operation.Command.OperationID,
		)
		completed <- advanceResult{record: record, advanced: didAdvance, err: advanceErr}
	}()
	select {
	case <-executor.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider mutation did not enter")
	}

	enrollNativeAdmissionGuard(t, admission, request)
	close(executor.release)
	result := <-completed
	if result.err != nil || !result.advanced ||
		result.record.Head.Phase != providerexecutor.PhaseResourcesBound {
		t.Fatalf(
			"in-flight provider result = %#v advanced=%t err=%v",
			result.record.Head,
			result.advanced,
			result.err,
		)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, desired string
		var generation, bindingGeneration int64
		var bindingCount int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lifecycle_state, desired_state, generation,
				(SELECT count(*) FROM server_provider_resource_bindings
				 WHERE tenant_id = $1 AND operation_id = $3 AND binding_id = 'server'),
				(SELECT server_generation FROM server_provider_resource_bindings
				 WHERE tenant_id = $1 AND operation_id = $3 AND binding_id = 'server')
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID,
			created.Operation.Command.OperationID).Scan(
			&lifecycle,
			&desired,
			&generation,
			&bindingCount,
			&bindingGeneration,
		); err != nil {
			t.Fatalf("load in-flight teardown custody: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleEnrolling) ||
			desired != string(serverregistry.DesiredRunning) ||
			generation != 2 || bindingCount != 1 || bindingGeneration != 1 {
			t.Fatalf(
				"historical generation custody = lifecycle=%s desired=%s generation=%d bindings=%d binding_generation=%d",
				lifecycle,
				desired,
				generation,
				bindingCount,
				bindingGeneration,
			)
		}
	})

	present := resource
	present.Observation = providerexecutor.ObservationPresent
	present.Cleanup = providerexecutor.CleanupRequired
	presentIP := publicIP
	presentIP.Observation = providerexecutor.ObservationPresent
	presentIP.Cleanup = providerexecutor.CleanupRequired
	executor.mu.Lock()
	executor.result = providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
		Resources: []providerexecutor.ResourceBinding{present, presentIP},
	}
	executor.mu.Unlock()
	polled, changed, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !changed || polled.Head.Phase != providerexecutor.PhasePresent {
		t.Fatalf(
			"Guard-enrolled generation continuation = %#v changed=%t err=%v",
			polled.Head,
			changed,
			err,
		)
	}
	projected, err := admission.servers.GetServerRuntime(t.Context(), request.TenantID, request.RuntimeServerID)
	if err != nil {
		t.Fatalf("load projected Guard-enrolled runtime: %v", err)
	}
	if projected.Generation != 2 || projected.RuntimeTarget.ProviderTargetRef != "server-in-flight" ||
		projected.Metadata["runtime_public_ip"] != "192.0.2.124" {
		t.Fatalf("Guard-enrolled runtime target = %#v", projected)
	}

	markNativeAdmissionServerDecommissioning(t, admission, database, request, 2)
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, desired string
		var generation int64
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lifecycle_state, desired_state, generation
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&lifecycle,
			&desired,
			&generation,
		); err != nil {
			t.Fatalf("load historical generation teardown head: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleDecommissioning) ||
			desired != string(serverregistry.DesiredAbsent) || generation != 2 {
			t.Fatalf(
				"historical generation teardown head = lifecycle=%s desired=%s generation=%d",
				lifecycle,
				desired,
				generation,
			)
		}
	})
}

func TestIntegrationLeaseOnlyTeardownFreezesGraphWithoutLateProvisionRevival(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("accepted transition = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "compute", NativeRef: "ionos-server-lease-only",
		OwnershipHash: digest("owner-lease-only"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	mutationClaim, err := admission.ledger.AcquireExecutionClaim(
		t.Context(), accepted.Command, accepted.Head, ExecutionClaimSideEffecting,
		"lease-only-create-worker", "lease-only-create-token", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire create claim: %v", err)
	}
	resourcesBound, err := providerexecutor.AssembleReceipt(
		t.Context(),
		providerexecutor.ExecutionRequest{Command: accepted.Command, Previous: accepted.Head},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
			Resources: []providerexecutor.ResourceBinding{resource},
		},
		time.Now().UTC(),
		nil,
	)
	if err != nil {
		t.Fatalf("assemble resources-bound receipt: %v", err)
	}
	if err := admission.ledger.AppendClaimedReceipt(
		t.Context(), accepted.Command, accepted.Head, resourcesBound, mutationClaim,
	); err != nil {
		t.Fatalf("append resources-bound receipt: %v", err)
	}
	pollClaim, err := admission.ledger.AcquireExecutionClaim(
		t.Context(), accepted.Command, resourcesBound, ExecutionClaimReadOnly,
		"lease-only-poll-worker", "lease-only-poll-token", time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire presence poll claim: %v", err)
	}

	before, err := admission.servers.GetServerRuntime(
		t.Context(), request.TenantID, request.RuntimeServerID,
	)
	if err != nil {
		t.Fatalf("load server before lease-only teardown: %v", err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin lease-only teardown: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(
		t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID,
	); err != nil {
		t.Fatalf("set lease-only teardown tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE techstack_vm_leases
		SET desired_state = 'absent',
		    cancelled_at = clock_timestamp(),
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND id = $2
	`, request.TenantID, created.LeaseID); err != nil {
		t.Fatalf("apply lease-only teardown: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit lease-only teardown: %v", err)
	}

	target := providerexecutor.ResourceTarget{
		BindingID: resource.BindingID, Kind: resource.Kind, NativeRef: resource.NativeRef,
		OwnershipHash: resource.OwnershipHash, Disposition: resource.Disposition,
	}
	if _, _, err := admission.coordinator.Start(t.Context(), StartRequest{
		TenantID: request.TenantID, LeaseID: created.LeaseID,
		LeaseRevision: created.LeaseRevision, RuntimeServerID: request.RuntimeServerID,
		ResourceGenerationID: created.ResourceGenerationID,
		Operation:            providerexecutor.OperationDecommission,
		IdempotencyKey:       "decommission-before-graph-freeze", LedgerRevision: 2,
		Targets:     []providerexecutor.ResourceTarget{target},
		RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
	}); !errors.Is(err, ErrCleanupCustody) {
		t.Fatalf("decommission before provision graph freeze error = %v, want ErrCleanupCustody", err)
	}

	present := resource
	present.Observation = providerexecutor.ObservationPresent
	present.Cleanup = providerexecutor.CleanupRequired
	presentReceipt, err := providerexecutor.AssembleReceipt(
		t.Context(),
		providerexecutor.ExecutionRequest{Command: accepted.Command, Previous: resourcesBound},
		providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent,
			Resources: []providerexecutor.ResourceBinding{present},
		},
		time.Now().UTC(),
		nil,
	)
	if err != nil {
		t.Fatalf("assemble late present receipt: %v", err)
	}
	if err := admission.ledger.AppendClaimedReceipt(
		t.Context(), accepted.Command, resourcesBound, presentReceipt, pollClaim,
	); err != nil {
		t.Fatalf("append late present receipt after lease-only teardown: %v", err)
	}
	after, err := admission.servers.GetServerRuntime(
		t.Context(), request.TenantID, request.RuntimeServerID,
	)
	if err != nil {
		t.Fatalf("load server after late present receipt: %v", err)
	}
	if after.Revision != before.Revision ||
		after.Generation != before.Generation ||
		after.LifecycleState != before.LifecycleState ||
		after.DesiredState != before.DesiredState {
		t.Fatalf("late present revived lease-only teardown server: before=%#v after=%#v", before, after)
	}
	if _, createdDecommission, err := admission.coordinator.Start(t.Context(), StartRequest{
		TenantID: request.TenantID, LeaseID: created.LeaseID,
		LeaseRevision: created.LeaseRevision, RuntimeServerID: request.RuntimeServerID,
		ResourceGenerationID: created.ResourceGenerationID,
		Operation:            providerexecutor.OperationDecommission,
		IdempotencyKey:       "decommission-after-graph-freeze", LedgerRevision: 2,
		Targets:     []providerexecutor.ResourceTarget{target},
		RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
	}); err != nil || !createdDecommission {
		t.Fatalf("decommission after provision graph freeze created=%t err=%v", createdDecommission, err)
	}
}

func TestIntegrationDefinitiveProviderAbsenceTombstonesAndReleasesCapacity(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, provisionExecutor := newNativeAdmissionIntegrationServiceWithCapacity(
		t,
		database,
		nil,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 2)},
	)
	request := nativeProvisionAdmissionFixture()
	initialSlot, err := admission.ResolveManagedRuntimeSlotGeneration(
		t.Context(), request.TenantID, request.Server.StackID, request.RuntimeSlotKey,
	)
	if err != nil || initialSlot.GenerationOrdinal != 1 || initialSlot.ExistingUnreleased ||
		initialSlot.RuntimeSlotID != request.RuntimeSlotID {
		t.Fatalf("initial runtime slot resolution = %+v err=%v", initialSlot, err)
	}
	resource := providerexecutor.ResourceBinding{
		BindingID: "server", Kind: "ionos.server", NativeRef: "ionos-server://datacenters/dc-1/servers/server-delete",
		OwnershipHash: digest("owner-delete"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	publicIP := providerexecutor.ResourceBinding{
		BindingID: "public-ip", Kind: "ionos.public-ip", NativeRef: "ionos-ip://203.0.113.10",
		OwnershipHash: digest("owner-public-ip"), Disposition: providerexecutor.DispositionDelete,
		Observation: providerexecutor.ObservationUnknown, Cleanup: providerexecutor.CleanupPending,
	}
	present := resource
	present.Observation = providerexecutor.ObservationPresent
	present.Cleanup = providerexecutor.CleanupRequired
	presentPublicIP := publicIP
	presentPublicIP.Observation = providerexecutor.ObservationPresent
	presentPublicIP.Cleanup = providerexecutor.CleanupRequired
	provisionExecutor.mu.Lock()
	provisionExecutor.results = []providerexecutor.ExecutionResult{
		{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound, Resources: []providerexecutor.ResourceBinding{resource, publicIP}},
		{Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhasePresent, Resources: []providerexecutor.ResourceBinding{present, presentPublicIP}},
	}
	provisionExecutor.mu.Unlock()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	occupiedSlot, err := admission.ResolveManagedRuntimeSlotGeneration(
		t.Context(), request.TenantID, request.Server.StackID, request.RuntimeSlotKey,
	)
	if err != nil || occupiedSlot.GenerationOrdinal != 1 || !occupiedSlot.ExistingUnreleased {
		t.Fatalf("occupied runtime slot resolution = %+v err=%v", occupiedSlot, err)
	}
	record := created.Operation
	for range 3 {
		record, _, err = admission.coordinator.Advance(
			t.Context(), request.TenantID, record.Command.OperationID,
		)
		if err != nil {
			t.Fatalf("advance provision: %v", err)
		}
		if record.Head.Phase == providerexecutor.PhasePresent {
			break
		}
	}
	if record.Head.Phase != providerexecutor.PhasePresent {
		t.Fatalf("provision terminal phase = %s, want present", record.Head.Phase)
	}
	blocked := sameRuntimeSlotGenerationRequest(request, "blocked", 2)
	if _, err := admission.AdmitProvision(t.Context(), blocked); !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatalf("unreleased same-slot admission error = %v, want ErrNativeAdmissionConflict", err)
	}
	withWritableNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE managed_runtime_server_slot_generations
			SET state = 'quarantined'
			WHERE tenant_id = $1
			  AND slot_id = $2
			  AND resource_generation_id = $3::uuid
		`, request.TenantID, request.RuntimeSlotID, created.ResourceGenerationID); err != nil {
			t.Fatalf("quarantine first slot generation: %v", err)
		}
	})
	quarantineBlocked := sameRuntimeSlotGenerationRequest(request, "quarantine-blocked", 2)
	if _, err := admission.AdmitProvision(t.Context(), quarantineBlocked); !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatalf("quarantined same-slot admission error = %v, want ErrNativeAdmissionConflict", err)
	}
	quarantinedSlot, err := admission.ResolveManagedRuntimeSlotGeneration(
		t.Context(), request.TenantID, request.Server.StackID, request.RuntimeSlotKey,
	)
	if err != nil || quarantinedSlot.GenerationOrdinal != 1 || !quarantinedSlot.ExistingUnreleased {
		t.Fatalf("quarantined runtime slot resolution = %+v err=%v", quarantinedSlot, err)
	}
	enrollNativeAdmissionGuard(t, admission, request)
	target := providerexecutor.ResourceTarget{
		BindingID: resource.BindingID, Kind: resource.Kind, NativeRef: resource.NativeRef,
		ParentBindingID: resource.ParentBindingID, OwnershipHash: resource.OwnershipHash,
		Disposition: resource.Disposition,
	}
	publicIPTarget := providerexecutor.ResourceTarget{
		BindingID: publicIP.BindingID, Kind: publicIP.Kind, NativeRef: publicIP.NativeRef,
		ParentBindingID: publicIP.ParentBindingID, OwnershipHash: publicIP.OwnershipHash,
		Disposition: publicIP.Disposition,
	}
	extraTarget := providerexecutor.ResourceTarget{
		BindingID: "unowned-extra", Kind: "volume", NativeRef: "ionos-volume-unowned",
		OwnershipHash: digest("unowned-extra"), Disposition: providerexecutor.DispositionDelete,
	}
	canonicalDesired, err := canonicalJSON(request.DesiredSpec)
	if err != nil {
		t.Fatalf("canonicalize reconcile desired spec: %v", err)
	}
	reconcileRequestedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, _, err := admission.coordinator.Start(t.Context(), StartRequest{
		TenantID: request.TenantID, LeaseID: created.LeaseID,
		LeaseRevision: created.LeaseRevision, RuntimeServerID: request.RuntimeServerID,
		ResourceGenerationID: created.ResourceGenerationID,
		Operation:            providerexecutor.OperationReconcile,
		IdempotencyKey:       "reconcile-unowned-extra", LedgerRevision: 2,
		DesiredSpec: &DesiredSpecRevision{
			TenantID: request.TenantID, LeaseID: created.LeaseID, Revision: 2,
			Ref:    "desired-spec://techstack/leases/lease-native-1/revisions/2",
			Digest: sha256Digest(canonicalDesired), Payload: canonicalDesired,
			CreatedAt: reconcileRequestedAt,
		},
		Targets:     []providerexecutor.ResourceTarget{target, publicIPTarget, extraTarget},
		RequestedAt: reconcileRequestedAt,
	}); !errors.Is(err, ErrCleanupCustody) {
		t.Fatalf("unowned reconcile target error = %v, want ErrCleanupCustody", err)
	}

	markNativeAdmissionServerDecommissioning(t, admission, database, request, 3)

	verifier := &acceptEvidenceVerifier{}
	absenceExecutor := &nativeFreshAbsenceExecutor{}
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", absenceExecutor); err != nil {
		t.Fatalf("register absence executor: %v", err)
	}
	ledger, err := NewPostgresLedger(database, verifier)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: &nativeAdmissionIntegrationResolver{profile: testProfile("ionos-v1")},
		Ledger:   ledger, ActivationGate: AllowGate{}, EvidenceVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if _, _, err := coordinator.Start(t.Context(), StartRequest{
		TenantID: request.TenantID, LeaseID: created.LeaseID,
		LeaseRevision: created.LeaseRevision, RuntimeServerID: request.RuntimeServerID,
		ResourceGenerationID: created.ResourceGenerationID,
		Operation:            providerexecutor.OperationDecommission,
		IdempotencyKey:       "decommission-unowned-extra", LedgerRevision: 2,
		Targets:     []providerexecutor.ResourceTarget{target, publicIPTarget, extraTarget},
		RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
	}); !errors.Is(err, ErrCleanupCustody) {
		t.Fatalf("unowned decommission target error = %v, want ErrCleanupCustody", err)
	}
	decommission, createdDecommission, err := coordinator.Start(t.Context(), StartRequest{
		TenantID: request.TenantID, LeaseID: created.LeaseID,
		LeaseRevision: created.LeaseRevision, RuntimeServerID: request.RuntimeServerID,
		ResourceGenerationID: created.ResourceGenerationID,
		Operation:            providerexecutor.OperationDecommission,
		IdempotencyKey:       "decommission-definitive", LedgerRevision: 2,
		Targets:     []providerexecutor.ResourceTarget{target, publicIPTarget},
		RequestedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil || !createdDecommission {
		t.Fatalf("start decommission = %#v created=%t err=%v", decommission, createdDecommission, err)
	}
	for step := range 4 {
		previousPhase := decommission.Head.Phase
		decommission, _, err = coordinator.Advance(
			t.Context(), request.TenantID, decommission.Command.OperationID,
		)
		if err != nil {
			t.Fatalf("advance decommission step %d from %s: %v", step+1, previousPhase, err)
		}
	}
	if decommission.Head.Status != providerexecutor.StatusSucceeded ||
		decommission.Head.Phase != providerexecutor.PhaseAbsent {
		t.Fatalf("decommission terminal head = %#v", decommission.Head)
	}
	if verifier.calls == 0 {
		t.Fatal("definitive absence evidence verifier was not called")
	}
	replayed, err := ledger.LoadOperation(
		t.Context(), request.TenantID, decommission.Command.OperationID,
	)
	if err != nil {
		t.Fatalf("load terminal decommission projection: %v", err)
	}
	if replayed.Head.ReceiptDigest != decommission.Head.ReceiptDigest ||
		replayed.RuntimeServerGeneration != decommission.RuntimeServerGeneration {
		t.Fatalf("terminal decommission replay mismatch: got %#v want %#v", replayed, decommission)
	}
	replayed, changed, err := coordinator.Advance(
		t.Context(), request.TenantID, decommission.Command.OperationID,
	)
	if err != nil || changed || replayed.Head.ReceiptDigest != decommission.Head.ReceiptDigest {
		t.Fatalf("terminal decommission advance replay = %#v changed=%t err=%v", replayed, changed, err)
	}

	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, desired string
		var decommissionedAt sql.NullTime
		var releases int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lifecycle_state, desired_state, decommissioned_at,
				(SELECT count(*) FROM managed_runtime_capacity_release_facts
				 WHERE tenant_id = $1 AND lease_id = $3
				   AND resource_generation_id = $4::uuid)
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID, created.LeaseID,
			created.ResourceGenerationID).Scan(
			&lifecycle,
			&desired,
			&decommissionedAt,
			&releases,
		); err != nil {
			t.Fatalf("load terminal decommission projection: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleDecommissioned) ||
			desired != string(serverregistry.DesiredAbsent) ||
			!decommissionedAt.Valid || releases != 1 {
			t.Fatalf(
				"terminal decommission = lifecycle=%s desired=%s tombstone=%v releases=%d",
				lifecycle,
				desired,
				decommissionedAt.Valid,
				releases,
			)
		}
	})

	releasedSlot, err := admission.ResolveManagedRuntimeSlotGeneration(
		t.Context(), request.TenantID, request.Server.StackID, request.RuntimeSlotKey,
	)
	if err != nil || releasedSlot.GenerationOrdinal != 2 || releasedSlot.ExistingUnreleased {
		t.Fatalf("released runtime slot resolution = %+v err=%v", releasedSlot, err)
	}
	next := sameRuntimeSlotGenerationRequest(request, "replacement", releasedSlot.GenerationOrdinal)
	if _, err := admission.AdmitProvision(t.Context(), next); err != nil {
		t.Fatalf("exactly released runtime slot did not admit replacement generation: %v", err)
	}
}

func sameRuntimeSlotGenerationRequest(
	previous NativeProvisionAdmissionRequest,
	suffix string,
	generation uint64,
) NativeProvisionAdmissionRequest {
	next := previous
	next.RuntimeSlotGeneration = generation
	next.Lease.ID = vmlease.LeaseID("lease-native-" + suffix)
	next.RuntimeServerID = "server-native-" + suffix
	next.IdempotencyKey = "native-admission-" + suffix
	next.DesiredSpecRef = "desired-spec://techstack/leases/lease-native-" + suffix + "/revisions/1"
	next.DesiredSpec = json.RawMessage(fmt.Sprintf(`{"replacement":%q}`, suffix))
	next.Server.Name = "Native Server " + suffix
	next.Server.Metadata = map[string]any{"replacement": suffix}
	return next
}

type failingReceiptRuntimeProjector struct {
	delegate providerReceiptRuntimeProjector
	err      error
}

func (p failingReceiptRuntimeProjector) ValidateMutationStartTx(
	ctx context.Context,
	tx *sql.Tx,
	command providerexecutor.Command,
	serverGeneration int64,
) error {
	return p.delegate.ValidateMutationStartTx(ctx, tx, command, serverGeneration)
}

func (p failingReceiptRuntimeProjector) PrepareTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	previous providerexecutor.Receipt,
	next providerexecutor.Receipt,
) (preparedProviderReceiptRuntimeProjection, error) {
	if _, err := p.delegate.PrepareTx(ctx, tx, record, previous, next); err != nil {
		return preparedProviderReceiptRuntimeProjection{}, err
	}
	return preparedProviderReceiptRuntimeProjection{}, p.err
}

func (p failingReceiptRuntimeProjector) ApplyTx(
	context.Context,
	*sql.Tx,
	preparedProviderReceiptRuntimeProjection,
) error {
	return p.err
}

func TestIntegrationResourceFreeNoDispatchTeardownIsAtomicAndReleasesCapacity(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor := newNativeAdmissionIntegrationServiceWithCapacity(
		t,
		database,
		nil,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)},
	)
	request := nativeCapacityAdmissionRequest("resource-free-a", "")
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}

	withWritableNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent',
			    cancelled_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, string(request.Lease.ID)); err != nil {
			t.Fatalf("cancel resource-free lease: %v", err)
		}
		// The destroy path records decommission intent before it discovers that
		// the provision never committed a provider resource. That intentional
		// lifecycle event advances the RuntimeServer generation while the sealed
		// provision operation stays pinned to the generation it admitted.
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE servers
			SET revision = revision + 1,
			    generation = generation + 1,
			    desired_state = 'absent',
			    lifecycle_state = 'decommissioning',
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID); err != nil {
			t.Fatalf("project resource-free decommission intent: %v", err)
		}
	})

	finalized, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || finalized.AutomationState != OperationAutomationComplete ||
		finalized.Head.ReceiptDigest != created.Operation.Head.ReceiptDigest {
		t.Fatalf(
			"resource-free finalization = %#v advanced=%t err=%v",
			finalized,
			advanced,
			err,
		)
	}
	if calls := queueExecutorCallCount(executor); calls != 0 {
		t.Fatalf("resource-free teardown invoked provider adapter %d times", calls)
	}

	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, desired, authority, releaseAuthority string
		var decommissionedAt sql.NullTime
		var outcomeCleared bool
		var runnable, held int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT server.lifecycle_state, server.desired_state, server.decommissioned_at,
			       server.last_outcome_json IS NULL,
			       terminalization.authority, release.release_authority,
			       (SELECT count(*) FROM provider_control_runnable_tenants
			        WHERE tenant_id = $1),
			       (
			           SELECT count(*)
			           FROM managed_runtime_capacity_reservations AS reservation
			           WHERE reservation.tenant_id = $1
			             AND reservation.owner_subject_id = 'owner-1'
			             AND NOT EXISTS (
			                 SELECT 1
			                 FROM managed_runtime_capacity_release_facts AS released
			                 WHERE released.tenant_id = reservation.tenant_id
			                   AND released.lease_id = reservation.lease_id
			                   AND released.resource_generation_id =
			                       reservation.resource_generation_id
			             )
			       )
			FROM servers AS server
			JOIN provider_operation_resource_free_terminalizations AS terminalization
			  ON terminalization.tenant_id = server.tenant_id
			 AND terminalization.server_id = server.id
			JOIN managed_runtime_capacity_release_facts AS release
			  ON release.tenant_id = terminalization.tenant_id
			 AND release.lease_id = terminalization.lease_id
			 AND release.resource_generation_id =
			     terminalization.resource_generation_id
			WHERE server.tenant_id = $1 AND server.id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&lifecycle,
			&desired,
			&decommissionedAt,
			&outcomeCleared,
			&authority,
			&releaseAuthority,
			&runnable,
			&held,
		); err != nil {
			t.Fatalf("load resource-free teardown proof: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleDecommissioned) ||
			desired != string(serverregistry.DesiredAbsent) ||
			!decommissionedAt.Valid ||
			!outcomeCleared ||
			authority != resourceFreeNoDispatchAuthority ||
			releaseAuthority != resourceFreeCapacityReleaseAuthority ||
			runnable != 0 ||
			held != 0 {
			t.Fatalf(
				"resource-free proof = lifecycle=%s desired=%s tombstone=%t outcome_cleared=%t authority=%s release=%s runnable=%d held=%d",
				lifecycle,
				desired,
				decommissionedAt.Valid,
				outcomeCleared,
				authority,
				releaseAuthority,
				runnable,
				held,
			)
		}
	})

	replayed, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || advanced || replayed.AutomationState != OperationAutomationComplete {
		t.Fatalf("resource-free replay = %#v advanced=%t err=%v", replayed, advanced, err)
	}
	if _, err := admission.AdmitProvision(
		t.Context(),
		nativeCapacityAdmissionRequest("resource-free-b", ""),
	); err != nil {
		t.Fatalf("capacity was not reusable after resource-free teardown: %v", err)
	}
}

func TestIntegrationRestrictedRuntimeResourceFreeNoDispatchTeardownDoesNotReadAdminEvidence(t *testing.T) {
	adminDatabase := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, adminDatabase)
	runtimeDatabase := openRestrictedNativeAdmissionRuntimeDB(t, adminDatabase)
	profiles, err := NewPostgresCatalogExecutionProfileResolver(runtimeDatabase)
	if err != nil {
		t.Fatalf("create restricted runtime catalog resolver: %v", err)
	}
	admission, executor := newNativeAdmissionIntegrationServiceWithResolver(
		t,
		runtimeDatabase,
		profiles,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)},
	)
	request := nativeCapacityAdmissionRequest("restricted-resource-free", "")
	request.Lease.Subject.ID = request.TenantID
	request.Lease.Metadata = map[string]string{
		runtimeOfferingMetadataKey: "monthly-runtime-standard",
	}
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("restricted runtime AdmitProvision: %v", err)
	}

	withWritableNativeAdmissionTenant(t, adminDatabase, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent',
			    cancelled_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, created.LeaseID); err != nil {
			t.Fatalf("cancel restricted runtime lease: %v", err)
		}
	})

	finalized, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || !advanced ||
		finalized.AutomationState != OperationAutomationComplete {
		t.Fatalf(
			"restricted runtime resource-free finalization = %#v advanced=%t err=%v",
			finalized,
			advanced,
			err,
		)
	}
	if calls := queueExecutorCallCount(executor); calls != 0 {
		t.Fatalf("restricted runtime resource-free teardown invoked provider adapter %d times", calls)
	}
	withNativeAdmissionTenant(t, adminDatabase, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, authority string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT server.lifecycle_state, terminalization.authority
			FROM servers AS server
			JOIN provider_operation_resource_free_terminalizations AS terminalization
			  ON terminalization.tenant_id = server.tenant_id
			 AND terminalization.server_id = server.id
			WHERE server.tenant_id = $1
			  AND server.id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&lifecycle,
			&authority,
		); err != nil {
			t.Fatalf("load restricted runtime resource-free proof: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleDecommissioned) ||
			authority != resourceFreeNoDispatchAuthority {
			t.Fatalf(
				"restricted runtime resource-free proof = lifecycle=%s authority=%s",
				lifecycle,
				authority,
			)
		}
	})
}

func TestIntegrationResourceFreeFactCannotCommitWithoutTombstoneAndRelease(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeCapacityAdmissionRequest("resource-free-raw", "")
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	withWritableNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent',
			    cancelled_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, created.LeaseID); err != nil {
			t.Fatalf("cancel raw resource-free lease: %v", err)
		}
	})

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin raw terminalization: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(
		t.Context(),
		`SELECT set_config($1, $2, true)`,
		tenantContextKey,
		request.TenantID,
	); err != nil {
		t.Fatalf("set raw terminalization tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_operation_resource_free_terminalizations (
			tenant_id, operation_id, lease_id, lease_revision,
			server_id, server_generation, resource_generation_id,
			head_sequence, head_receipt_digest, authority, terminalized_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::uuid,$8,$9,$10,clock_timestamp())
	`, request.TenantID, created.Operation.Command.OperationID, created.LeaseID,
		int64(created.LeaseRevision), request.RuntimeServerID,
		created.Operation.RuntimeServerGeneration, created.ResourceGenerationID,
		int64(created.Operation.Head.Sequence), created.Operation.Head.ReceiptDigest,
		resourceFreeNoDispatchAuthority); err != nil {
		t.Fatalf("insert otherwise-valid raw terminalization fact: %v", err)
	}
	commitErr := tx.Commit()
	if commitErr == nil || !strings.Contains(
		commitErr.Error(),
		"must atomically include its exact RuntimeServer tombstone and capacity release",
	) {
		t.Fatalf("raw terminalization commit error = %v, want atomic proof rejection", commitErr)
	}

	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var facts, releases int
		var lifecycle string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
			    (SELECT count(*) FROM provider_operation_resource_free_terminalizations
			     WHERE tenant_id = $1 AND operation_id = $2),
			    (SELECT count(*) FROM managed_runtime_capacity_release_facts
			     WHERE tenant_id = $1 AND lease_id = $3),
			    (SELECT lifecycle_state FROM servers
			     WHERE tenant_id = $1 AND id = $4)
		`, request.TenantID, created.Operation.Command.OperationID,
			created.LeaseID, request.RuntimeServerID).Scan(
			&facts,
			&releases,
			&lifecycle,
		); err != nil {
			t.Fatalf("load rolled-back raw terminalization: %v", err)
		}
		if facts != 0 || releases != 0 ||
			lifecycle != string(serverregistry.LifecyclePlanned) {
			t.Fatalf(
				"raw terminalization rollback = facts=%d releases=%d lifecycle=%s",
				facts,
				releases,
				lifecycle,
			)
		}
	})
}

func TestIntegrationNoCandidateDecisionBecomesRunnableAndFinalizesWithoutRedispatch(t *testing.T) {
	database := openNativeAdmissionIntegrationDBWithDispatch(
		t,
		ProvisionDispatchAtMostOnceManualReconcile,
	)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor, ledger := newNativeAdmissionIntegrationAMOService(
		t,
		database,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)},
	)
	request := nativeCapacityAdmissionRequest("resource-free-no-candidate", "")
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("advance AMO accepted = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}
	manual, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || advanced ||
		manual.AutomationState != OperationAutomationManualReconcileRequired {
		t.Fatalf("AMO guarded head = %#v advanced=%t err=%v", manual, advanced, err)
	}

	binding := provisionDiscoveryBinding{
		LeaseID:              manual.Command.LeaseID,
		LeaseRevision:        manual.Command.LeaseRevision,
		RuntimeServerID:      manual.Command.RuntimeServerID,
		ResourceGenerationID: manual.Command.ResourceGenerationID,
		HeadSequence:         manual.Head.Sequence,
		HeadReceiptDigest:    manual.Head.ReceiptDigest,
		AdapterManifestHash:  manual.ExecutionProfile.AdapterManifestHash,
		PreparedBinding:      preparedBindingForCommandHead(t, manual.Command, manual.Head),
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if err := tx.QueryRowContext(t.Context(), `
			SELECT guarded_at
			FROM provider_provision_dispatch_guards
			WHERE tenant_id = $1 AND operation_id = $2
		`, request.TenantID, manual.Command.OperationID).Scan(&binding.GuardedAt); err != nil {
			t.Fatalf("load AMO guard: %v", err)
		}
	})
	resolution, err := NewPostgresProvisionResolutionStore(
		ledger,
		allowProvisionResolutionVerifier{},
	)
	if err != nil {
		t.Fatalf("NewPostgresProvisionResolutionStore: %v", err)
	}
	discoveryRequest := provisionDiscoveryRequest(manual, binding, 0)
	discoveryRequest.CollectedAt = integrationDBTime(t, database)
	observation, observationCreated, err := resolution.RecordProvisionDiscovery(
		t.Context(),
		discoveryRequest,
	)
	if err != nil || !observationCreated {
		t.Fatalf("record zero-candidate observation created=%t err=%v", observationCreated, err)
	}
	decision, current, decisionCreated, err := resolution.CommitProvisionResolution(
		t.Context(),
		mustBuildProvisionResolutionRequest(t, manual, observation, "resource-free-no-candidate-decision"),
	)
	if err != nil || !decisionCreated ||
		decision.Outcome != ProvisionResolutionNoCandidateObserved ||
		current.Head.ReceiptDigest != manual.Head.ReceiptDigest {
		t.Fatalf(
			"zero-candidate decision = %#v current=%#v created=%t err=%v",
			decision,
			current,
			decisionCreated,
			err,
		)
	}

	withWritableNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent',
			    cancelled_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, created.LeaseID); err != nil {
			t.Fatalf("cancel no-candidate lease: %v", err)
		}
	})
	runnable, err := ledger.ListRunnableOperations(t.Context(), request.TenantID, 10)
	if err != nil || len(runnable) != 1 ||
		runnable[0].OperationID != manual.Command.OperationID {
		t.Fatalf("no-candidate runnable projection = %#v err=%v", runnable, err)
	}

	finalized, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		manual.Command.OperationID,
	)
	if err != nil || !advanced || finalized.AutomationState != OperationAutomationComplete {
		t.Fatalf("no-candidate finalization = %#v advanced=%t err=%v", finalized, advanced, err)
	}
	executor.mu.Lock()
	dispatchCalls := executor.dispatchCalls
	executor.mu.Unlock()
	if dispatchCalls != 1 {
		t.Fatalf("no-candidate finalization dispatch calls = %d, want original one", dispatchCalls)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var authority, releaseAuthority, lifecycle string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT terminalization.authority, release.release_authority, server.lifecycle_state
			FROM provider_operation_resource_free_terminalizations AS terminalization
			JOIN managed_runtime_capacity_release_facts AS release
			  ON release.tenant_id = terminalization.tenant_id
			 AND release.lease_id = terminalization.lease_id
			 AND release.resource_generation_id = terminalization.resource_generation_id
			JOIN servers AS server
			  ON server.tenant_id = terminalization.tenant_id
			 AND server.id = terminalization.server_id
			WHERE terminalization.tenant_id = $1
			  AND terminalization.operation_id = $2
		`, request.TenantID, manual.Command.OperationID).Scan(
			&authority,
			&releaseAuthority,
			&lifecycle,
		); err != nil {
			t.Fatalf("load no-candidate terminal proof: %v", err)
		}
		if authority != resourceFreeNoCandidateAuthority ||
			releaseAuthority != resourceFreeCapacityReleaseAuthority ||
			lifecycle != string(serverregistry.LifecycleDecommissioned) {
			t.Fatalf(
				"no-candidate proof = authority=%s release=%s lifecycle=%s",
				authority,
				releaseAuthority,
				lifecycle,
			)
		}
	})
	if _, err := admission.AdmitProvision(
		t.Context(),
		nativeCapacityAdmissionRequest("resource-free-after-no-candidate", ""),
	); err != nil {
		t.Fatalf("capacity was not reusable after no-candidate teardown: %v", err)
	}
}

func TestIntegrationFailedAMOZeroCandidateDecisionAtomicallyReleasesCapacity(t *testing.T) {
	database := openNativeAdmissionIntegrationDBWithDispatch(
		t,
		ProvisionDispatchAtMostOnceManualReconcile,
	)
	seedNativeAdmissionCredentialHandle(t, database)
	admission, executor, ledger := newNativeAdmissionIntegrationAMOService(
		t,
		database,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)},
	)
	executor.mu.Lock()
	executor.dispatchResults = []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusFailed,
		Phase:  providerexecutor.PhaseFailed,
		Reason: &providerexecutor.Reason{
			Code:      providerexecutor.ReasonCodeProviderTimeout,
			Retryable: true,
		},
	}}
	executor.mu.Unlock()

	request := nativeCapacityAdmissionRequest("failed-amo-zero-candidate", "")
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("advance AMO accepted = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}
	failed, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || !advanced ||
		failed.Head.Status != providerexecutor.StatusFailed ||
		failed.Head.Phase != providerexecutor.PhaseFailed ||
		len(failed.Head.Resources) != 0 {
		t.Fatalf("advance AMO failed = %#v advanced=%t err=%v", failed.Head, advanced, err)
	}

	binding := provisionDiscoveryBinding{
		LeaseID:              failed.Command.LeaseID,
		LeaseRevision:        failed.Command.LeaseRevision,
		RuntimeServerID:      failed.Command.RuntimeServerID,
		ResourceGenerationID: failed.Command.ResourceGenerationID,
		AdapterManifestHash:  failed.ExecutionProfile.AdapterManifestHash,
		PreparedBinding:      preparedBindingForCommandHead(t, failed.Command, accepted.Head),
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if err := tx.QueryRowContext(t.Context(), `
			SELECT head_sequence, head_receipt_digest, guarded_at
			FROM provider_provision_dispatch_guards
			WHERE tenant_id = $1 AND operation_id = $2
		`, request.TenantID, failed.Command.OperationID).Scan(
			&binding.HeadSequence,
			&binding.HeadReceiptDigest,
			&binding.GuardedAt,
		); err != nil {
			t.Fatalf("load failed AMO guard: %v", err)
		}
	})
	if binding.HeadSequence+1 != failed.Head.Sequence ||
		binding.HeadReceiptDigest != failed.Head.PreviousReceiptDigest {
		t.Fatalf(
			"failed AMO predecessor = guard %d/%s failed %d/%s",
			binding.HeadSequence,
			binding.HeadReceiptDigest,
			failed.Head.Sequence,
			failed.Head.PreviousReceiptDigest,
		)
	}

	resolution, err := NewPostgresProvisionResolutionStore(
		ledger,
		allowProvisionResolutionVerifier{},
	)
	if err != nil {
		t.Fatalf("NewPostgresProvisionResolutionStore: %v", err)
	}
	nonzeroRequest := provisionDiscoveryRequest(failed, binding, 1)
	nonzeroRequest.IdempotencyKey = "failed-amo-nonzero-candidate"
	nonzeroRequest.CollectedAt = integrationDBTime(t, database)
	if _, _, err := resolution.RecordProvisionDiscovery(
		t.Context(),
		nonzeroRequest,
	); !errors.Is(err, ErrProvisionResolutionEvidence) {
		t.Fatalf(
			"failed AMO nonzero-candidate observation error = %v, want evidence rejection",
			err,
		)
	}
	preConsumptionRequest := provisionDiscoveryRequest(failed, binding, 0)
	preConsumptionRequest.IdempotencyKey = "failed-amo-pre-consumption"
	preConsumptionRequest.CollectedAt = binding.GuardedAt
	if _, _, err := resolution.RecordProvisionDiscovery(
		t.Context(),
		preConsumptionRequest,
	); err == nil {
		t.Fatal("failed AMO pre-consumption observation unexpectedly committed")
	}
	discoveryRequest := provisionDiscoveryRequest(failed, binding, 0)
	discoveryRequest.CollectedAt = integrationDBTime(t, database)
	observation, observationCreated, err := resolution.RecordProvisionDiscovery(
		t.Context(),
		discoveryRequest,
	)
	if err != nil || !observationCreated {
		t.Fatalf(
			"record failed AMO zero-candidate observation created=%t err=%v",
			observationCreated,
			err,
		)
	}
	if observation.HeadSequence != accepted.Head.Sequence ||
		observation.HeadReceiptDigest != accepted.Head.ReceiptDigest {
		t.Fatalf(
			"failed AMO observation head = %d/%s, want guarded %d/%s",
			observation.HeadSequence,
			observation.HeadReceiptDigest,
			accepted.Head.Sequence,
			accepted.Head.ReceiptDigest,
		)
	}
	decisionRequest := mustBuildProvisionResolutionRequest(t, failed, observation, "failed-amo-zero-candidate-decision")
	if _, _, _, err := resolution.CommitProvisionResolution(
		t.Context(),
		decisionRequest,
	); err == nil {
		t.Fatal("failed AMO decision before teardown unexpectedly committed")
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var decisions, terminalizations, releases int
		var lifecycle string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
			    (SELECT count(*) FROM provider_provision_resolution_decisions
			     WHERE tenant_id = $1 AND operation_id = $2),
			    (SELECT count(*) FROM provider_operation_resource_free_terminalizations
			     WHERE tenant_id = $1 AND operation_id = $2),
			    (SELECT count(*) FROM managed_runtime_capacity_release_facts
			     WHERE tenant_id = $1 AND release_operation_id = $2),
			    (SELECT lifecycle_state FROM servers
			     WHERE tenant_id = $1 AND id = $3)
		`, request.TenantID, failed.Command.OperationID, request.RuntimeServerID).Scan(
			&decisions,
			&terminalizations,
			&releases,
			&lifecycle,
		); err != nil {
			t.Fatalf("load pre-teardown failed AMO rollback: %v", err)
		}
		if decisions != 0 || terminalizations != 0 || releases != 0 ||
			lifecycle != string(serverregistry.LifecycleFailed) {
			t.Fatalf(
				"pre-teardown failed AMO rollback = decisions=%d terminalizations=%d releases=%d lifecycle=%s",
				decisions,
				terminalizations,
				releases,
				lifecycle,
			)
		}
	})

	withWritableNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent',
			    cancelled_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, created.LeaseID); err != nil {
			t.Fatalf("cancel failed AMO lease: %v", err)
		}
		// An independently completed decommission can advance and tombstone the
		// RuntimeServer before the exact no-candidate decision settles the failed
		// provision. Settlement must retain that tombstone while binding its live
		// forward generation to the immutable provision head and provider proof.
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE servers
			SET revision = revision + 1,
			    generation = generation + 1,
			    desired_state = 'absent',
			    lifecycle_state = 'decommissioned',
			    decommissioned_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID); err != nil {
			t.Fatalf("project failed AMO decommission intent: %v", err)
		}
	})
	decision, current, decisionCreated, err := resolution.CommitProvisionResolution(
		t.Context(),
		decisionRequest,
	)
	if err != nil || !decisionCreated ||
		decision.Outcome != ProvisionResolutionNoCandidateObserved ||
		current.Head.ReceiptDigest != failed.Head.ReceiptDigest {
		t.Fatalf(
			"failed AMO settlement = decision=%#v current=%#v created=%t err=%v",
			decision,
			current,
			decisionCreated,
			err,
		)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, desired, authority, releaseAuthority string
		var decommissionedAt sql.NullTime
		if err := tx.QueryRowContext(t.Context(), `
			SELECT server.lifecycle_state, server.desired_state,
			       server.decommissioned_at, terminalization.authority,
			       release.release_authority
			FROM servers AS server
			JOIN provider_operation_resource_free_terminalizations AS terminalization
			  ON terminalization.tenant_id = server.tenant_id
			 AND terminalization.server_id = server.id
			JOIN managed_runtime_capacity_release_facts AS release
			  ON release.tenant_id = terminalization.tenant_id
			 AND release.release_operation_id = terminalization.operation_id
			WHERE server.tenant_id = $1 AND server.id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&lifecycle,
			&desired,
			&decommissionedAt,
			&authority,
			&releaseAuthority,
		); err != nil {
			t.Fatalf("load failed AMO settlement proof: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleDecommissioned) ||
			desired != string(serverregistry.DesiredAbsent) ||
			!decommissionedAt.Valid ||
			authority != resourceFreeNoCandidateAuthority ||
			releaseAuthority != resourceFreeCapacityReleaseAuthority {
			t.Fatalf(
				"failed AMO settlement proof = lifecycle=%s desired=%s tombstone=%t authority=%s release=%s",
				lifecycle,
				desired,
				decommissionedAt.Valid,
				authority,
				releaseAuthority,
			)
		}
	})
	replayedDecision, _, replayCreated, err := resolution.CommitProvisionResolution(
		t.Context(),
		decisionRequest,
	)
	if err != nil || replayCreated ||
		replayedDecision.DecisionDigest != decision.DecisionDigest {
		t.Fatalf(
			"failed AMO decision replay = %#v created=%t err=%v",
			replayedDecision,
			replayCreated,
			err,
		)
	}
	executor.mu.Lock()
	dispatchCalls := executor.dispatchCalls
	executor.mu.Unlock()
	if dispatchCalls != 1 {
		t.Fatalf("failed AMO settlement dispatch calls = %d, want one", dispatchCalls)
	}
	if _, err := admission.AdmitProvision(
		t.Context(),
		nativeCapacityAdmissionRequest("after-failed-amo-settlement", ""),
	); err != nil {
		t.Fatalf("capacity was not reusable after failed AMO settlement: %v", err)
	}
}

func TestIntegrationCancelDuringInflightAMOFailureSettlesWithoutServerRevival(t *testing.T) {
	database := openNativeAdmissionIntegrationDBWithDispatch(
		t,
		ProvisionDispatchAtMostOnceManualReconcile,
	)
	seedNativeAdmissionCredentialHandle(t, database)
	executor := &atMostOnceExecutor{
		dispatchResults: []providerexecutor.ExecutionResult{{
			Status: providerexecutor.StatusFailed,
			Phase:  providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{
				Code:      providerexecutor.ReasonCodeProviderTimeout,
				Retryable: true,
			},
		}},
		dispatchEntered: make(chan struct{}),
		dispatchRelease: make(chan struct{}),
	}
	admission, ledger := newNativeAdmissionIntegrationAMOServiceWithExecutor(
		t,
		database,
		executor,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)},
	)
	request := nativeCapacityAdmissionRequest("cancel-inflight-amo", "")
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision: %v", err)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(),
		request.TenantID,
		created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != providerexecutor.PhaseAccepted {
		t.Fatalf("advance AMO accepted = %#v advanced=%t err=%v", accepted.Head, advanced, err)
	}

	type advanceResult struct {
		record   OperationRecord
		advanced bool
		err      error
	}
	resultCh := make(chan advanceResult, 1)
	go func() {
		record, didAdvance, advanceErr := admission.coordinator.Advance(
			t.Context(),
			request.TenantID,
			created.Operation.Command.OperationID,
		)
		resultCh <- advanceResult{
			record:   record,
			advanced: didAdvance,
			err:      advanceErr,
		}
	}()
	select {
	case <-executor.dispatchEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("AMO provider call did not enter")
	}
	withWritableNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			UPDATE techstack_vm_leases
			SET desired_state = 'absent',
			    cancelled_at = clock_timestamp(),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, created.LeaseID); err != nil {
			t.Fatalf("cancel in-flight AMO lease: %v", err)
		}
	})
	close(executor.dispatchRelease)
	var dispatch advanceResult
	select {
	case dispatch = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("AMO provider result did not settle")
	}
	if dispatch.err != nil || !dispatch.advanced ||
		dispatch.record.Head.Status != providerexecutor.StatusFailed ||
		dispatch.record.Head.Phase != providerexecutor.PhaseFailed ||
		len(dispatch.record.Head.Resources) != 0 {
		t.Fatalf(
			"in-flight AMO failed result = %#v advanced=%t err=%v",
			dispatch.record.Head,
			dispatch.advanced,
			dispatch.err,
		)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lifecycle_state
			FROM servers
			WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(&lifecycle); err != nil {
			t.Fatalf("load canceled in-flight RuntimeServer: %v", err)
		}
		if lifecycle != string(serverregistry.LifecyclePlanned) {
			t.Fatalf("canceled in-flight RuntimeServer lifecycle = %s, want planned without late failure revival", lifecycle)
		}
	})

	failed := dispatch.record
	binding := provisionDiscoveryBinding{
		LeaseID:              failed.Command.LeaseID,
		LeaseRevision:        failed.Command.LeaseRevision,
		RuntimeServerID:      failed.Command.RuntimeServerID,
		ResourceGenerationID: failed.Command.ResourceGenerationID,
		AdapterManifestHash:  failed.ExecutionProfile.AdapterManifestHash,
		PreparedBinding:      preparedBindingForCommandHead(t, failed.Command, accepted.Head),
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		if err := tx.QueryRowContext(t.Context(), `
			SELECT head_sequence, head_receipt_digest, guarded_at
			FROM provider_provision_dispatch_guards
			WHERE tenant_id = $1 AND operation_id = $2
		`, request.TenantID, failed.Command.OperationID).Scan(
			&binding.HeadSequence,
			&binding.HeadReceiptDigest,
			&binding.GuardedAt,
		); err != nil {
			t.Fatalf("load canceled in-flight AMO guard: %v", err)
		}
	})
	resolution, err := NewPostgresProvisionResolutionStore(
		ledger,
		allowProvisionResolutionVerifier{},
	)
	if err != nil {
		t.Fatalf("NewPostgresProvisionResolutionStore: %v", err)
	}
	discoveryRequest := provisionDiscoveryRequest(failed, binding, 0)
	discoveryRequest.IdempotencyKey = "cancel-inflight-amo-discovery"
	discoveryRequest.CollectedAt = integrationDBTime(t, database)
	observation, observationCreated, err := resolution.RecordProvisionDiscovery(
		t.Context(),
		discoveryRequest,
	)
	if err != nil || !observationCreated {
		t.Fatalf(
			"record canceled in-flight zero-candidate observation created=%t err=%v",
			observationCreated,
			err,
		)
	}
	decision, current, decisionCreated, err := resolution.CommitProvisionResolution(
		t.Context(),
		mustBuildProvisionResolutionRequest(t, failed, observation, "cancel-inflight-amo-decision"),
	)
	if err != nil || !decisionCreated ||
		decision.Outcome != ProvisionResolutionNoCandidateObserved ||
		current.Head.ReceiptDigest != failed.Head.ReceiptDigest {
		t.Fatalf(
			"canceled in-flight AMO settlement = decision=%#v current=%#v created=%t err=%v",
			decision,
			current,
			decisionCreated,
			err,
		)
	}
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var lifecycle, desired string
		var held int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT server.lifecycle_state, server.desired_state,
			       (
			           SELECT count(*)
			           FROM managed_runtime_capacity_reservations AS reservation
			           WHERE reservation.tenant_id = $1
			             AND reservation.owner_subject_id = 'owner-1'
			             AND NOT EXISTS (
			                 SELECT 1
			                 FROM managed_runtime_capacity_release_facts AS release
			                 WHERE release.tenant_id = reservation.tenant_id
			                   AND release.lease_id = reservation.lease_id
			                   AND release.resource_generation_id =
			                       reservation.resource_generation_id
			             )
			       )
			FROM servers AS server
			WHERE server.tenant_id = $1 AND server.id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&lifecycle,
			&desired,
			&held,
		); err != nil {
			t.Fatalf("load canceled in-flight AMO settlement: %v", err)
		}
		if lifecycle != string(serverregistry.LifecycleDecommissioned) ||
			desired != string(serverregistry.DesiredAbsent) ||
			held != 0 {
			t.Fatalf(
				"canceled in-flight AMO settlement = lifecycle=%s desired=%s held=%d",
				lifecycle,
				desired,
				held,
			)
		}
	})
	executor.mu.Lock()
	dispatchCalls := executor.dispatchCalls
	executor.mu.Unlock()
	if dispatchCalls != 1 {
		t.Fatalf("canceled in-flight AMO dispatch calls = %d, want one", dispatchCalls)
	}
	if _, err := admission.AdmitProvision(
		t.Context(),
		nativeCapacityAdmissionRequest("after-cancel-inflight-amo", ""),
	); err != nil {
		t.Fatalf("capacity was not reusable after canceled in-flight AMO settlement: %v", err)
	}
}

type blockingResultExecutor struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	result  providerexecutor.ExecutionResult
	calls   int
}

type nativeFreshAbsenceExecutor struct{ calls int }

func (*nativeFreshAbsenceExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{
		AdapterManifestHash: digest("test-adapter-manifest"),
		Mode:                CrashRecoveryNativeIdempotency, PerHeadInvocationKey: true,
	}
}

func (e *nativeFreshAbsenceExecutor) ExecuteCrashRecoverableReadOnly(
	ctx context.Context,
	invocation AdapterInvocation,
) providerexecutor.ExecutionResult {
	return e.execute(ctx, invocation.Request)
}

func (e *nativeFreshAbsenceExecutor) ExecuteCrashRecoverableMutation(
	ctx context.Context,
	invocation AdapterInvocation,
) providerexecutor.ExecutionResult {
	return e.execute(ctx, invocation.Request)
}

func (e *nativeFreshAbsenceExecutor) execute(
	ctx context.Context,
	request providerexecutor.ExecutionRequest,
) providerexecutor.ExecutionResult {
	if err := ctx.Err(); err != nil {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTimeout, Retryable: true},
		}
	}
	e.calls++
	switch e.calls {
	case 1:
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted}
	case 2:
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAbsencePending}
	default:
		resources := make([]providerexecutor.ResourceBinding, 0, len(request.Command.Targets))
		for _, target := range request.Command.Targets {
			evidence := providerexecutor.Evidence{
				Ref:    "provider-evidence://ionos/operations/delete-fresh/" + target.BindingID,
				Digest: digest("absence-evidence-" + target.BindingID), Source: providerexecutor.EvidenceSourceProviderAPI,
				OperationID: request.Command.OperationID, LeaseRevision: request.Command.LeaseRevision,
				RuntimeServerID: request.Command.RuntimeServerID, ProviderID: request.Command.ProviderID,
				CapabilitySnapshotHash: request.Command.CapabilitySnapshotHash,
				BindingID:              target.BindingID, NativeRefHash: providerexecutor.ComputeNativeRefHash(target.NativeRef),
				ConnectionHash: request.Command.ConnectionHash, ExecutionProfileHash: request.Command.ExecutionProfileHash,
				ResourceGraphHash: request.Command.ResourceGraphHash,
				SubjectHash:       providerexecutor.ComputeEvidenceSubjectHash(request.Command, target, providerexecutor.ObservationAbsent),
				Observation:       providerexecutor.ObservationAbsent, Definitive: true,
				AttestationRef:    "provider-attestation://ionos/operations/delete-fresh/" + target.BindingID,
				AttestationDigest: digest("absence-attestation-" + target.BindingID),
				CollectedAt:       request.Previous.PhaseEnteredAt.Add(time.Millisecond),
			}
			resources = append(resources, providerexecutor.ResourceBinding{
				BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef,
				ParentBindingID: target.ParentBindingID, OwnershipHash: target.OwnershipHash,
				Disposition: target.Disposition, Observation: providerexecutor.ObservationAbsent,
				Cleanup: providerexecutor.CleanupComplete, Evidence: []providerexecutor.Evidence{evidence},
			})
		}
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent, Resources: resources,
		}
	}
}

func (*blockingResultExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{
		AdapterManifestHash: digest("test-adapter-manifest"),
		Mode:                CrashRecoveryNativeIdempotency, PerHeadInvocationKey: true,
	}
}

func (e *blockingResultExecutor) ExecuteCrashRecoverableReadOnly(
	ctx context.Context,
	_ AdapterInvocation,
) providerexecutor.ExecutionResult {
	return e.execute(ctx)
}

func (e *blockingResultExecutor) ExecuteCrashRecoverableMutation(
	ctx context.Context,
	_ AdapterInvocation,
) providerexecutor.ExecutionResult {
	return e.execute(ctx)
}

func (e *blockingResultExecutor) execute(ctx context.Context) providerexecutor.ExecutionResult {
	e.mu.Lock()
	e.calls++
	if e.calls == 1 {
		close(e.entered)
	}
	e.mu.Unlock()
	select {
	case <-e.release:
		e.mu.Lock()
		result := e.result
		e.mu.Unlock()
		return result
	case <-ctx.Done():
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{
				Code: providerexecutor.ReasonCodeProviderTimeout, Retryable: true,
			},
		}
	}
}

func newNativeAdmissionIntegrationServiceWithExecutor(
	t *testing.T,
	database *sql.DB,
	executor CrashRecoverableExecutor,
	verifier providerexecutor.EvidenceVerifier,
) *NativeAdmission {
	t.Helper()
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", executor); err != nil {
		t.Fatalf("register native integration executor: %v", err)
	}
	ledger, err := NewPostgresLedger(database, verifier)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	gate := AllowGate{}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: &nativeAdmissionIntegrationResolver{profile: testProfile("ionos-v1")},
		Ledger:   ledger, ActivationGate: gate, EvidenceVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	admission, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate,
		ProviderCreates: AllowProviderCreate{},
		CapacityPolicy: staticAdmissionCapacityPolicy{
			grant: ownerLimitedCapacityGrant("owner-1", 100),
		},
	})
	if err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}
	return admission
}

func newNativeAdmissionIntegrationAMOService(
	t *testing.T,
	database *sql.DB,
	capacity CapacityPolicyResolver,
) (*NativeAdmission, *atMostOnceExecutor, *PostgresLedger) {
	t.Helper()
	executor := &atMostOnceExecutor{}
	admission, ledger := newNativeAdmissionIntegrationAMOServiceWithExecutor(
		t,
		database,
		executor,
		capacity,
	)
	return admission, executor, ledger
}

func newNativeAdmissionIntegrationAMOServiceWithExecutor(
	t *testing.T,
	database *sql.DB,
	executor *atMostOnceExecutor,
	capacity CapacityPolicyResolver,
) (*NativeAdmission, *PostgresLedger) {
	t.Helper()
	registry := NewRegistry()
	if err := registry.RegisterAtMostOnceProvision("ionos-v1", executor); err != nil {
		t.Fatalf("register native AMO integration executor: %v", err)
	}
	ledger, err := NewPostgresLedger(database, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchAtMostOnceManualReconcile
	resolver := &nativeAdmissionIntegrationResolver{profile: profile}
	gate := AllowGate{}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: resolver,
		Ledger:   ledger, ActivationGate: gate,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	admission, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate,
		ProviderCreates: AllowProviderCreate{},
		CapacityPolicy:  capacity,
	})
	if err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}
	return admission, ledger
}

func markNativeAdmissionServerDecommissioning(
	t *testing.T,
	admission *NativeAdmission,
	database *sql.DB,
	request NativeProvisionAdmissionRequest,
	expectedRevision int64,
) {
	t.Helper()
	current, err := admission.servers.GetServerRuntime(
		t.Context(),
		request.TenantID,
		request.RuntimeServerID,
	)
	if err != nil {
		t.Fatalf("load runtime server before decommission: %v", err)
	}
	if current.Revision != expectedRevision {
		t.Fatalf(
			"runtime server revision before decommission = %d, want %d",
			current.Revision,
			expectedRevision,
		)
	}
	observedAt := time.Now().UTC()
	result, err := admission.servers.ApplyServerEvent(t.Context(), controlplane.ServerEvent{
		TenantID: request.TenantID, ServerID: request.RuntimeServerID,
		ExpectedRevision: expectedRevision, Generation: current.Generation,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    "decommission-test", SourceID: "decommission-test:" + request.RuntimeServerID,
		ObservedAt: observedAt,
		Runtime: controlplane.ServerRuntime{
			LifecycleState:      string(serverregistry.LifecycleDecommissioning),
			LifecycleReasonCode: "decommission_requested",
			DesiredState:        string(serverregistry.DesiredAbsent),
			DesiredReasonCode:   "user_requested_absence",
		},
		Evidence: map[string]any{"reason": "integration_decommission"},
	})
	if err != nil || result == nil || !result.Applied {
		t.Fatalf("mark runtime server decommissioning = %#v, err=%v", result, err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin lease teardown transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(
		t.Context(),
		`SELECT set_config($1, $2, true)`,
		tenantContextKey,
		request.TenantID,
	); err != nil {
		t.Fatalf("set lease teardown tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE techstack_vm_leases
		SET desired_state = 'absent',
		    cancelled_at = clock_timestamp(),
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND id = $2
	`, request.TenantID, string(request.Lease.ID)); err != nil {
		t.Fatalf("mark native lease absent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit native lease teardown: %v", err)
	}
}

func enrollNativeAdmissionGuard(
	t *testing.T,
	admission *NativeAdmission,
	request NativeProvisionAdmissionRequest,
) {
	t.Helper()
	current, err := admission.servers.GetServerRuntime(
		t.Context(),
		request.TenantID,
		request.RuntimeServerID,
	)
	if err != nil {
		t.Fatalf("load runtime server before Guard enrollment: %v", err)
	}
	seedTx, err := admission.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin Guard enrollment seed: %v", err)
	}
	defer func() { _ = seedTx.Rollback() }()
	if _, err := seedTx.ExecContext(
		t.Context(),
		`SELECT set_config($1, $2, true)`,
		tenantContextKey,
		request.TenantID,
	); err != nil {
		t.Fatalf("set Guard enrollment tenant: %v", err)
	}
	if _, err := seedTx.ExecContext(t.Context(), `
			INSERT INTO stacks (
				id, tenant_id, owner_subject_id, name, status
			) VALUES (
				'stack-native-1', $1, 'owner-1', 'Native Stack', 'draft'
			)
			ON CONFLICT (id) DO NOTHING
		`, request.TenantID); err != nil {
		t.Fatalf("seed Guard enrollment stack: %v", err)
	}
	if _, err := seedTx.ExecContext(t.Context(), `
			INSERT INTO workers (
				id, tenant_id, hostname, status, approved, owner_subject_id, stack_id
			) VALUES (
				'guard-native-1', $1, 'native-runtime', 'pending', false,
				'owner-1', 'stack-native-1'
			)
			ON CONFLICT (tenant_id, id) DO NOTHING
		`, request.TenantID); err != nil {
		t.Fatalf("seed Guard enrollment worker: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit Guard enrollment seed: %v", err)
	}
	observedAt := time.Now().UTC()
	result, err := admission.servers.ApplyServerEnrollment(
		t.Context(),
		controlplane.ServerEnrollment{
			Node: controlplane.Node{
				ID: request.RuntimeServerID, TenantID: request.TenantID,
				StackID:  "stack-native-1",
				WorkerID: "guard-native-1", Name: "native-runtime",
				Role: "foundation", Status: "pending",
			},
			Event: controlplane.ServerEvent{
				TenantID: request.TenantID, ServerID: request.RuntimeServerID,
				ExpectedRevision: current.Revision, Generation: current.Generation + 1,
				Authority: controlplane.ServerEventAuthorityControlPlane,
				Source:    "pairing-redemption", SourceID: "guard-enrollment-test",
				ObservedAt: observedAt,
				Runtime: controlplane.ServerRuntime{
					StackID: "stack-native-1", WorkerID: "guard-native-1",
					NodeID:         request.RuntimeServerID,
					LifecycleState: string(serverregistry.LifecycleEnrolling),
				},
			},
		},
	)
	if err != nil || result == nil || !result.Applied {
		t.Fatalf("Guard enrollment generation bump = %#v, err=%v", result, err)
	}
	if result.Server.Generation != current.Generation+1 {
		t.Fatalf(
			"Guard enrollment generation = %d, want %d",
			result.Server.Generation,
			current.Generation+1,
		)
	}
}
