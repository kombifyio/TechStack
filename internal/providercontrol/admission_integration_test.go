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
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestNativeAdmissionIntegrationCommitsOneReplayableAggregateWithoutProviderCall(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, executor := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()

	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("first AdmitProvision: %v", err)
	}
	if !created.Created || created.LeaseRevision != 1 || created.LeaseID != string(request.Lease.ID) ||
		created.RuntimeServerID != request.RuntimeServerID {
		t.Fatalf("created admission = %#v", created)
	}
	parsedGeneration, err := uuid.Parse(created.ResourceGenerationID)
	if err != nil || parsedGeneration == uuid.Nil || parsedGeneration.String() != created.ResourceGenerationID {
		t.Fatalf("resource generation = %q, want canonical UUID", created.ResourceGenerationID)
	}
	if created.Operation.Command.ResourceGenerationID != created.ResourceGenerationID ||
		created.Operation.Command.OperationID == "" || created.Operation.Head.Sequence != 1 {
		t.Fatalf("created provider operation = %#v", created.Operation)
	}
	if got := queueExecutorCallCount(executor); got != 0 {
		t.Fatalf("admission invoked provider adapter %d times", got)
	}

	assertNativeAdmissionAggregate(t, database, request, created)
	advanceNativeAdmissionLeaseHead(t, database, request)

	replayed, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("same-digest AdmitProvision replay: %v", err)
	}
	if replayed.Created || replayed.ResourceGenerationID != created.ResourceGenerationID ||
		replayed.Operation.Command.OperationID != created.Operation.Command.OperationID ||
		replayed.RequestDigest != created.RequestDigest {
		t.Fatalf("replayed admission = %#v, want exact existing custody", replayed)
	}
	if got := queueExecutorCallCount(executor); got != 0 {
		t.Fatalf("replay invoked provider adapter %d times", got)
	}

	conflicting := request
	conflicting.DesiredSpec = []byte(`{"region":"us","cpu":4,"ram":8}`)
	if _, err := admission.AdmitProvision(t.Context(), conflicting); !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatalf("different-digest replay error = %v, want ErrNativeAdmissionConflict", err)
	}
	assertNativeAdmissionCustodyCounts(t, database, request, created)
}

func TestNativeAdmissionIntegrationCreateSwitchBlocksFreshButNotReplay(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	policy := StaticProviderCreatePolicy{"ionos": {Enabled: true}}
	resolver := &nativeAdmissionIntegrationResolver{profile: testProfile("ionos-v1")}
	admission, _ := newNativeAdmissionIntegrationServiceWithResolverAndProviderCreates(
		t, database, resolver, staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 100)}, policy,
	)
	request := nativeProvisionAdmissionFixture()
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil || !created.Created {
		t.Fatalf("initial create: result=%#v err=%v", created, err)
	}
	policy["ionos"] = ProviderCreateDecision{Enabled: false, ReasonCode: ProviderCreateReasonKillSwitchDisabled}
	replayed, err := admission.AdmitProvision(t.Context(), request)
	if err != nil || replayed.Created || replayed.Operation.Command.OperationID != created.Operation.Command.OperationID {
		t.Fatalf("closed-switch replay: result=%#v err=%v", replayed, err)
	}
	fresh := nativeCapacityAdmissionRequest("switch-fresh", "stack-native-1")
	if _, err := admission.AdmitProvision(t.Context(), fresh); !errors.Is(err, ErrProviderCreateBlocked) {
		t.Fatalf("fresh create error = %v, want ErrProviderCreateBlocked", err)
	}
	assertNativeAdmissionCustodyCounts(t, database, request, created)
}

func TestNativeAdmissionIntegrationCrossProviderUnclassifiedCustodyBlocksFreshAdmission(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()

	withNativeAdmissionTenantWrite(t, database, request.TenantID, func(tx *sql.Tx) {
		if _, err := tx.ExecContext(t.Context(), `
			INSERT INTO servers (
				id, tenant_id, stack_id, owner_subject_id, provider_ref, name,
				lifecycle_state, desired_state, connection_state, health_state
			) VALUES (
				'legacy-unclassified-server', $1, $2, $3, $4, 'legacy unclassified runtime',
				'active', 'running', 'offline', 'unknown'
			)
		`, request.TenantID, request.Server.StackID, request.OwnerSubjectID, "centron"); err != nil {
			t.Fatalf("seed unclassified managed runtime server: %v", err)
		}
	})

	for _, attempt := range []struct {
		name string
		run  func() error
	}{
		{name: "read-only preflight", run: func() error { return admission.PreflightProvision(t.Context(), request) }},
		{name: "transactional admission", run: func() error {
			_, err := admission.AdmitProvision(t.Context(), request)
			return err
		}},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			err := attempt.run()
			if !errors.Is(err, ErrManagedRuntimeUnclassifiedCustody) {
				t.Fatalf("error = %v, want ErrManagedRuntimeUnclassifiedCustody", err)
			}
			var custody ManagedRuntimeUnclassifiedCustodyError
			if !errors.As(err, &custody) || custody.ProviderID != "ionos" || custody.StackID != request.Server.StackID {
				t.Fatalf("typed custody error = %#v, err=%v", custody, err)
			}
		})
	}
	assertNoNativeAdmissionAggregate(t, database, request)
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		for table, query := range map[string]string{
			"managed_runtime_server_slots":            `SELECT count(*) FROM managed_runtime_server_slots WHERE tenant_id = $1 AND slot_id = $2`,
			"managed_runtime_server_slot_generations": `SELECT count(*) FROM managed_runtime_server_slot_generations WHERE tenant_id = $1 AND slot_id = $2`,
		} {
			var count int
			if err := tx.QueryRowContext(t.Context(), query, request.TenantID, request.RuntimeSlotID).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if count != 0 {
				t.Fatalf("%s count = %d, want no fresh custody", table, count)
			}
		}
	})
}

func TestNativeAdmissionIntegrationRestrictedRuntimeRoleCanCreateAndReplay(t *testing.T) {
	adminDatabase := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, adminDatabase)
	runtimeDatabase := openRestrictedNativeAdmissionRuntimeDB(t, adminDatabase)

	for _, table := range []string{
		"provider_credential_handles",
		"techstack_vm_leases",
		"runtime_lease_execution_authorities",
		"runtime_lease_idempotency_records",
		"managed_runtime_capacity_reservations",
		"managed_runtime_server_slots",
		"managed_runtime_server_slot_generations",
	} {
		for _, privilege := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
			var allowed bool
			if err := runtimeDatabase.QueryRowContext(t.Context(), `
				SELECT has_table_privilege(current_user, $1, $2)
			`, table, privilege).Scan(&allowed); err != nil {
				t.Fatalf("inspect restricted %s privilege on %s: %v", privilege, table, err)
			}
			if allowed {
				t.Fatalf("restricted runtime role unexpectedly has %s on %s", privilege, table)
			}
		}
	}

	profiles, err := NewPostgresCatalogExecutionProfileResolver(runtimeDatabase)
	if err != nil {
		t.Fatalf("create production catalog resolver over restricted role: %v", err)
	}
	admission, executor := newNativeAdmissionIntegrationServiceWithResolver(
		t, runtimeDatabase, profiles,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 100)},
	)
	request := nativeProvisionAdmissionFixture()
	request.Lease.Subject.ID = request.TenantID
	request.Lease.Metadata = map[string]string{runtimeOfferingMetadataKey: "monthly-runtime-standard"}
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("restricted role fresh admission: %v", err)
	}
	if !created.Created {
		t.Fatalf("restricted role fresh admission = %#v, want created", created)
	}
	replayed, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("restricted role replay admission: %v", err)
	}
	if replayed.Created || replayed.ResourceGenerationID != created.ResourceGenerationID ||
		replayed.Operation.Command.OperationID != created.Operation.Command.OperationID {
		t.Fatalf("restricted role replay = %#v, want exact existing custody", replayed)
	}
	if got := queueExecutorCallCount(executor); got != 0 {
		t.Fatalf("restricted role admission invoked provider adapter %d times", got)
	}
	accepted, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	)
	if err != nil || !advanced || accepted.Head.Phase != "accepted" {
		t.Fatalf("restricted role accepted transition = %#v, advanced=%t, err=%v", accepted, advanced, err)
	}
	if _, advanced, err := admission.coordinator.Advance(
		t.Context(), request.TenantID, created.Operation.Command.OperationID,
	); err != nil || !advanced {
		t.Fatalf("restricted role side-effect claim/append advanced=%t, err=%v", advanced, err)
	}
	if got := queueExecutorCallCount(executor); got != 1 {
		t.Fatalf("restricted role side-effect claim invoked provider adapter %d times, want 1", got)
	}
}

func TestNativeAdmissionIntegrationSerializesCrossReplicaReplay(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	capacity := &countingAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)}
	first, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)
	second, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)
	request := nativeProvisionAdmissionFixture()

	start := make(chan struct{})
	results := make(chan NativeProvisionAdmissionResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for _, admission := range []*NativeAdmission{first, second} {
		wait.Add(1)
		go func(service *NativeAdmission) {
			defer wait.Done()
			<-start
			result, err := service.AdmitProvision(t.Context(), request)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- result
		}(admission)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent AdmitProvision: %v", err)
	}

	var admissions []NativeProvisionAdmissionResult
	createdCount := 0
	for result := range results {
		admissions = append(admissions, result)
		if result.Created {
			createdCount++
		}
	}
	if len(admissions) != 2 || createdCount != 1 {
		t.Fatalf("concurrent results = %#v, want one create and one replay", admissions)
	}
	if admissions[0].ResourceGenerationID != admissions[1].ResourceGenerationID ||
		admissions[0].Operation.Command.OperationID != admissions[1].Operation.Command.OperationID {
		t.Fatalf("replicas received different custody: %#v", admissions)
	}
	if calls := capacity.calls.Load(); calls != 1 {
		t.Fatalf("capacity policy calls = %d, want one fresh decision before exact replay", calls)
	}
	assertNativeAdmissionTableCount(t, database, "managed_runtime_capacity_reservations", 1)
}

func TestNativeAdmissionIntegrationSerializesDistinctKeysAcrossStacksAtLimit(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionStacks(t, database, "stack-capacity-a", "stack-capacity-b")
	capacity := staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)}
	first, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)
	second, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)

	assertExactlyOneDistinctCapacityAdmission(t, database, first, second,
		nativeCapacityAdmissionRequest("a", "stack-capacity-a"),
		nativeCapacityAdmissionRequest("b", "stack-capacity-b"),
	)
}

func TestNativeAdmissionIntegrationPreflightIsReadOnlyAndChecksAuthoritativeCapacity(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, executor := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeCapacityAdmissionRequest("preflight-first", "")

	if err := admission.PreflightProvision(t.Context(), request); err != nil {
		t.Fatalf("empty-capacity PreflightProvision: %v", err)
	}
	assertNoNativeAdmissionAggregate(t, database, request)
	if got := queueExecutorCallCount(executor); got != 0 {
		t.Fatalf("preflight invoked provider adapter %d times", got)
	}

	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitProvision after successful preflight: %v", err)
	}
	assertNativeAdmissionAggregate(t, database, request, created)
	overLimit := nativeCapacityAdmissionRequest("preflight-over-limit", "")
	limitedAdmission, limitedExecutor := newNativeAdmissionIntegrationServiceWithCapacity(
		t, database, nil,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)},
	)
	if err := limitedAdmission.PreflightProvision(t.Context(), overLimit); !errors.Is(err, ErrManagedRuntimeCapacityExceeded) {
		t.Fatalf("capacity-full PreflightProvision error = %v, want ErrManagedRuntimeCapacityExceeded", err)
	}
	assertNoNativeAdmissionAggregate(t, database, overLimit)
	assertNativeAdmissionAggregate(t, database, request, created)
	if got := queueExecutorCallCount(executor); got != 0 {
		t.Fatalf("admission/preflight invoked provider adapter %d times", got)
	}
	if got := queueExecutorCallCount(limitedExecutor); got != 0 {
		t.Fatalf("capacity-denied preflight invoked provider adapter %d times", got)
	}
}

func TestNativeAdmissionIntegrationSerializesDistinctKeysOnSameStackAtLimit(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionStacks(t, database, "stack-capacity-shared")
	capacity := staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)}
	first, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)
	second, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)

	assertExactlyOneDistinctCapacityAdmission(t, database, first, second,
		nativeCapacityAdmissionRequest("same-a", "stack-capacity-shared"),
		nativeCapacityAdmissionRequest("same-b", "stack-capacity-shared"),
	)
}

func TestNativeAdmissionIntegrationCommittedUnknownHoldsCapacityAndReplays(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	capacity := staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)}
	admission, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)
	request := nativeCapacityAdmissionRequest("unknown", "")

	// Deliberately discard the first response, modeling a committed transaction
	// whose HTTP caller never observed the result.
	if _, err := admission.AdmitProvision(t.Context(), request); err != nil {
		t.Fatalf("commit admission: %v", err)
	}
	replayed, err := admission.AdmitProvision(t.Context(), request)
	if err != nil || replayed.Created {
		t.Fatalf("exact replay after lost response = %#v, err %v", replayed, err)
	}
	if _, err := admission.AdmitProvision(t.Context(), nativeCapacityAdmissionRequest("other", "")); !errors.Is(err, ErrManagedRuntimeCapacityExceeded) {
		t.Fatalf("distinct request after unknown commit error = %v, want capacity exceeded", err)
	}
	assertNativeAdmissionTableCount(t, database, "managed_runtime_capacity_reservations", 1)
}

func TestNativeAdmissionIntegrationExplicitUnlimitedStillAuditsEveryGeneration(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	capacity := staticAdmissionCapacityPolicy{grant: CapacityGrant{
		ScopeKind: managedRuntimeCapacityScopeOwner, ScopeID: "owner-1",
		Mode: CapacityModeUnlimited, DecisionSource: CapacityDecisionSourceSelfHostManifest,
	}}
	admission, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, capacity)
	for _, suffix := range []string{"unlimited-a", "unlimited-b", "unlimited-c"} {
		if _, err := admission.AdmitProvision(t.Context(), nativeCapacityAdmissionRequest(suffix, "")); err != nil {
			t.Fatalf("unlimited admission %s: %v", suffix, err)
		}
	}
	assertNativeAdmissionTableCount(t, database, "managed_runtime_capacity_reservations", 3)
}

func TestNativeAdmissionIntegrationLimitedPolicyCountsPriorUnlimitedCustody(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	unlimited, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil,
		staticAdmissionCapacityPolicy{grant: CapacityGrant{
			ScopeKind: managedRuntimeCapacityScopeOwner, ScopeID: "owner-1",
			Mode: CapacityModeUnlimited, DecisionSource: CapacityDecisionSourceSelfHostManifest,
		}})
	if _, err := unlimited.AdmitProvision(t.Context(), nativeCapacityAdmissionRequest("prior-unlimited", "")); err != nil {
		t.Fatalf("unlimited admission: %v", err)
	}
	limited, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 1)})
	if _, err := limited.AdmitProvision(t.Context(), nativeCapacityAdmissionRequest("after-downgrade", "")); !errors.Is(err, ErrManagedRuntimeCapacityExceeded) {
		t.Fatalf("bounded admission after unlimited custody error = %v, want capacity exceeded", err)
	}
	assertNativeAdmissionTableCount(t, database, "managed_runtime_capacity_reservations", 1)
}

func TestNativeAdmissionIntegrationCapacityIsolatedByExplicitOwnerScope(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	policy := ownerScopedAdmissionCapacityPolicy{limit: 1}
	admission, _ := newNativeAdmissionIntegrationServiceWithCapacity(t, database, nil, policy)
	ownerOne := nativeCapacityAdmissionRequest("owner-one", "")
	ownerTwo := nativeCapacityAdmissionRequest("owner-two", "")
	ownerTwo.OwnerSubjectID = "owner-2"
	ownerTwo.Lease.Subject.ID = "owner-2"
	if _, err := admission.AdmitProvision(t.Context(), ownerOne); err != nil {
		t.Fatalf("owner one admission: %v", err)
	}
	if _, err := admission.AdmitProvision(t.Context(), ownerTwo); err != nil {
		t.Fatalf("owner two admission: %v", err)
	}
	assertNativeAdmissionTableCount(t, database, "managed_runtime_capacity_reservations", 2)
}

func TestNativeAdmissionIntegrationReplayFailsClosedWhenReservationProjectionIsMissing(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeCapacityAdmissionRequest("missing-replay", "")
	if _, err := admission.AdmitProvision(t.Context(), request); err != nil {
		t.Fatalf("initial admission: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		ALTER TABLE managed_runtime_capacity_reservations
		DISABLE TRIGGER managed_runtime_capacity_reservations_reject_mutation
	`); err != nil {
		t.Fatalf("disable immutable trigger for corruption fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `
			ALTER TABLE managed_runtime_capacity_reservations
			ENABLE TRIGGER managed_runtime_capacity_reservations_reject_mutation
		`)
	})
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin corruption fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID); err != nil {
		t.Fatalf("set corruption tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		DELETE FROM managed_runtime_capacity_reservations
		WHERE tenant_id = $1 AND lease_id = $2
	`, request.TenantID, request.Lease.ID); err != nil {
		t.Fatalf("remove capacity projection fixture: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit corruption fixture: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		ALTER TABLE managed_runtime_capacity_reservations
		ENABLE TRIGGER managed_runtime_capacity_reservations_reject_mutation
	`); err != nil {
		t.Fatalf("restore immutable trigger: %v", err)
	}
	if _, err := admission.AdmitProvision(t.Context(), request); !errors.Is(err, ErrNativeAdmissionConflict) {
		t.Fatalf("replay without capacity projection error = %v, want conflict", err)
	}
}

func TestNativeAdmissionIntegrationReservationCannotBeReleasedByLocalStatusMutation(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeCapacityAdmissionRequest("immutable-hold", "")
	if _, err := admission.AdmitProvision(t.Context(), request); err != nil {
		t.Fatalf("initial admission: %v", err)
	}
	for name, statement := range map[string]string{
		"update": `UPDATE managed_runtime_capacity_reservations SET reservation_mode = 'quarantine' WHERE tenant_id = $1 AND lease_id = $2`,
		"delete": `DELETE FROM managed_runtime_capacity_reservations WHERE tenant_id = $1 AND lease_id = $2`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := database.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("begin mutation: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID); err != nil {
				t.Fatalf("set tenant: %v", err)
			}
			if _, err := tx.ExecContext(t.Context(), statement, request.TenantID, request.Lease.ID); err == nil ||
				!strings.Contains(err.Error(), "capacity reservations are immutable") {
				t.Fatalf("local %s error = %v, want immutable rejection", name, err)
			}
		})
	}
	assertNativeAdmissionTableCount(t, database, "managed_runtime_capacity_reservations", 1)
}

func TestNativeAdmissionIntegrationReconcileClaimCannotBorrowAnotherProviderReservation(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeCapacityAdmissionRequest("cross-provider-reconcile", "")
	created, err := admission.AdmitProvision(t.Context(), request)
	if err != nil {
		t.Fatalf("admit IONOS capacity reservation: %v", err)
	}

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin cross-provider reconcile claim: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID); err != nil {
		t.Fatalf("set cross-provider reconcile tenant: %v", err)
	}
	const (
		operationID   = "operation-cross-provider-reconcile"
		commandDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		receiptDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	)
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_operations (
			tenant_id, operation_id, lease_id, operation, idempotency_key,
			adapter_id, command_digest, command_json, ledger_revision,
			desired_spec_revision, provision_dispatch_mode, status, phase, head_sequence,
			head_receipt_digest, requested_at, created_at, updated_at
		)
		SELECT
			tenant_id, $3::text, lease_id, 'reconcile', $4::text,
			adapter_id, $5::text,
			jsonb_build_object(
				'schema_version', command_json->>'schema_version',
				'execution_authority', command_json->>'execution_authority',
				'execution_profile', command_json->'execution_profile',
				'runtime_server_generation', command_json->'runtime_server_generation',
				'command', (command_json->'command') || jsonb_build_object(
					'operation_id', $3::text,
					'operation', 'reconcile',
					'idempotency_key', $4::text,
					'provider_id', 'centron',
					'command_digest', $5::text
				)
			),
			ledger_revision, desired_spec_revision, provision_dispatch_mode,
			'pending', 'requested', 1, $6::text, requested_at, clock_timestamp(), clock_timestamp()
		FROM provider_operations
		WHERE tenant_id = $1 AND operation_id = $2
	`, request.TenantID, created.Operation.Command.OperationID, operationID,
		"cross-provider-reconcile", commandDigest, receiptDigest); err != nil {
		if !strings.Contains(err.Error(), "provider operation runtime server generation pin is stale") {
			t.Fatalf("cross-provider reconcile operation error = %v, want runtime server provider fence", err)
		}
		return
	}
	t.Fatal("cross-provider reconcile operation borrowed an IONOS RuntimeServer generation")
}

func TestNativeAdmissionIntegrationRollsBackAggregateWhenProfileAdmissionFails(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, _ := newNativeAdmissionIntegrationService(t, database, ErrProfileUnavailable)
	request := nativeProvisionAdmissionFixture()

	if _, err := admission.AdmitProvision(t.Context(), request); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("AdmitProvision error = %v, want ErrProfileUnavailable", err)
	}
	assertNoNativeAdmissionAggregate(t, database, request)
}

func TestNativeAdmissionIntegrationRejectsUnsupportedProviderAtomically(t *testing.T) {
	database := openNativeAdmissionIntegrationDB(t)
	admission, _ := newNativeAdmissionIntegrationService(t, database, nil)
	request := nativeProvisionAdmissionFixture()
	request.Lease.Resource.ProviderID = "historical-provider"

	if _, err := admission.AdmitProvision(t.Context(), request); !errors.Is(err, ErrManagedRuntimeCapacityPolicyUnavailable) {
		t.Fatalf("unsupported provider admission error = %v, want ErrManagedRuntimeCapacityPolicyUnavailable", err)
	}
	assertNoNativeAdmissionAggregate(t, database, request)
}

type countingAdmissionCapacityPolicy struct {
	grant CapacityGrant
	err   error
	calls atomic.Int32
}

type ownerScopedAdmissionCapacityPolicy struct{ limit int }

func (p ownerScopedAdmissionCapacityPolicy) ResolveCapacity(_ context.Context, request CapacityPolicyRequest) (CapacityGrant, error) {
	return ownerLimitedCapacityGrant(request.OwnerSubjectID, p.limit), nil
}

func (p *countingAdmissionCapacityPolicy) ResolveCapacity(context.Context, CapacityPolicyRequest) (CapacityGrant, error) {
	p.calls.Add(1)
	return p.grant, p.err
}

type capacityAdmissionOutcome struct {
	result NativeProvisionAdmissionResult
	err    error
}

func assertExactlyOneDistinctCapacityAdmission(
	t *testing.T,
	database *sql.DB,
	first, second *NativeAdmission,
	firstRequest, secondRequest NativeProvisionAdmissionRequest,
) {
	t.Helper()
	start := make(chan struct{})
	outcomes := make(chan capacityAdmissionOutcome, 2)
	var wait sync.WaitGroup
	for index, service := range []*NativeAdmission{first, second} {
		request := firstRequest
		if index == 1 {
			request = secondRequest
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := service.AdmitProvision(t.Context(), request)
			outcomes <- capacityAdmissionOutcome{result: result, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)
	created, denied := 0, 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil && outcome.result.Created:
			created++
		case errors.Is(outcome.err, ErrManagedRuntimeCapacityExceeded):
			denied++
		default:
			t.Fatalf("unexpected distinct capacity outcome = %#v", outcome)
		}
	}
	if created != 1 || denied != 1 {
		t.Fatalf("capacity outcomes = created %d denied %d, want one each", created, denied)
	}
	for table, want := range map[string]int{
		"managed_runtime_capacity_reservations": 1,
		"techstack_vm_leases":                   1,
		"provider_operations":                   1,
		"runtime_lease_idempotency_records":     1,
	} {
		assertNativeAdmissionTableCount(t, database, table, want)
	}
}

func nativeCapacityAdmissionRequest(suffix, stackID string) NativeProvisionAdmissionRequest {
	request := nativeProvisionAdmissionFixture()
	if strings.TrimSpace(stackID) == "" {
		stackID = request.Server.StackID
	}
	request.Lease.ID = vmlease.LeaseID("lease-capacity-" + suffix)
	request.RuntimeSlotKey = "slot-" + suffix
	request.RuntimeSlotID = DeriveManagedRuntimeSlotID(request.TenantID, stackID, request.RuntimeSlotKey)
	request.RuntimeServerID = "server-capacity-" + suffix
	request.IdempotencyKey = "capacity-" + suffix
	request.Server.Name = "capacity-" + suffix
	request.Server.StackID = stackID
	request.DesiredSpecRef = "desired-spec://techstack/leases/lease-capacity-" + suffix + "/revisions/1"
	request.DesiredSpec = json.RawMessage(fmt.Sprintf(`{"capacity_test":%q}`, suffix))
	return request
}

func seedNativeAdmissionStacks(t *testing.T, database *sql.DB, stackIDs ...string) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin stack seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, "tenant-1"); err != nil {
		t.Fatalf("set stack seed tenant: %v", err)
	}
	for _, stackID := range stackIDs {
		if _, err := tx.ExecContext(t.Context(), `
			INSERT INTO stacks (id, tenant_id, owner_subject_id, name, status)
			VALUES ($1, 'tenant-1', 'owner-1', $1, 'draft')
		`, stackID); err != nil {
			t.Fatalf("seed stack %s: %v", stackID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit stack seed: %v", err)
	}
}

func assertNativeAdmissionTableCount(t *testing.T, database *sql.DB, table string, want int) {
	t.Helper()
	var query string
	switch table {
	case "managed_runtime_capacity_reservations":
		query = `SELECT count(*) FROM managed_runtime_capacity_reservations WHERE tenant_id = $1`
	case "techstack_vm_leases":
		query = `SELECT count(*) FROM techstack_vm_leases WHERE tenant_id = $1`
	case "provider_operations":
		query = `SELECT count(*) FROM provider_operations WHERE tenant_id = $1`
	case "runtime_lease_idempotency_records":
		query = `SELECT count(*) FROM runtime_lease_idempotency_records WHERE tenant_id = $1`
	default:
		t.Fatalf("unsupported capacity assertion table %q", table)
	}
	withNativeAdmissionTenant(t, database, "tenant-1", func(tx *sql.Tx) {
		var count int
		if err := tx.QueryRowContext(t.Context(), query, "tenant-1").Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != want {
			t.Fatalf("%s count = %d, want %d", table, count, want)
		}
	})
}

func assertNoNativeAdmissionAggregate(t *testing.T, database *sql.DB, request NativeProvisionAdmissionRequest) {
	t.Helper()
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		for table, query := range map[string]string{
			"servers":                               `SELECT count(*) FROM servers WHERE tenant_id = $1 AND id = $2`,
			"techstack_vm_leases":                   `SELECT count(*) FROM techstack_vm_leases WHERE tenant_id = $1 AND id = $2`,
			"runtime_lease_execution_authorities":   `SELECT count(*) FROM runtime_lease_execution_authorities WHERE tenant_id = $1 AND lease_id = $2`,
			"provider_operations":                   `SELECT count(*) FROM provider_operations WHERE tenant_id = $1 AND lease_id = $2`,
			"runtime_lease_idempotency_records":     `SELECT count(*) FROM runtime_lease_idempotency_records WHERE tenant_id = $1 AND lease_id = $2`,
			"managed_runtime_capacity_reservations": `SELECT count(*) FROM managed_runtime_capacity_reservations WHERE tenant_id = $1 AND lease_id = $2`,
		} {
			identity := request.RuntimeServerID
			if table != "servers" {
				identity = string(request.Lease.ID)
			}
			var count int
			if err := tx.QueryRowContext(t.Context(), query, request.TenantID, identity).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if count != 0 {
				t.Fatalf("rollback retained %d rows in %s", count, table)
			}
		}
	})
}

func assertNativeAdmissionAggregate(
	t *testing.T,
	database *sql.DB,
	request NativeProvisionAdmissionRequest,
	result NativeProvisionAdmissionResult,
) {
	t.Helper()
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var serverRevision, serverGeneration int64
		var ownerID, leaseID, lifecycle, desired string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT revision, generation, owner_subject_id, lease_id, lifecycle_state, desired_state
			FROM servers WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.RuntimeServerID).Scan(
			&serverRevision, &serverGeneration, &ownerID, &leaseID, &lifecycle, &desired,
		); err != nil {
			t.Fatalf("load admitted server: %v", err)
		}
		if serverRevision != 1 || serverGeneration != 1 || ownerID != request.OwnerSubjectID ||
			leaseID != result.LeaseID || lifecycle != "planned" || desired != "running" {
			t.Fatalf("server aggregate = revision %d generation %d owner %q lease %q lifecycle %q desired %q",
				serverRevision, serverGeneration, ownerID, leaseID, lifecycle, desired)
		}

		var leaseRevision int64
		var leaseOwner, runtimeServerID, generationID string
		var validFrom, validUntil, renewedAt time.Time
		if err := tx.QueryRowContext(t.Context(), `
			SELECT lease_revision, owner_subject_id, server_id, resource_generation_id::text,
			       valid_from, valid_until, renewed_at
			FROM techstack_vm_leases WHERE tenant_id = $1 AND id = $2
		`, request.TenantID, request.Lease.ID).Scan(
			&leaseRevision, &leaseOwner, &runtimeServerID, &generationID,
			&validFrom, &validUntil, &renewedAt,
		); err != nil {
			t.Fatalf("load admitted lease: %v", err)
		}
		if leaseRevision != 1 || leaseOwner != request.OwnerSubjectID || runtimeServerID != request.RuntimeServerID ||
			generationID != result.ResourceGenerationID || !validFrom.Equal(renewedAt) ||
			!validUntil.Equal(validFrom.Add(30*24*time.Hour)) {
			t.Fatalf("lease projection = revision %d owner %q server %q generation %q validity [%s,%s] renewed %s",
				leaseRevision, leaseOwner, runtimeServerID, generationID, validFrom, validUntil, renewedAt)
		}
		if !result.Operation.Command.RequestedAt.Equal(validFrom) || !result.Operation.Head.IssuedAt.Equal(validFrom) {
			t.Fatalf("database-time custody differs: lease %s command %s receipt %s",
				validFrom, result.Operation.Command.RequestedAt, result.Operation.Head.IssuedAt)
		}

		var authority, operationID, digestHex string
		if err := tx.QueryRowContext(t.Context(), `
			SELECT authority.execution_authority, idempotency.operation_id,
			       encode(idempotency.request_digest, 'hex')
			FROM runtime_lease_execution_authorities AS authority
			JOIN runtime_lease_idempotency_records AS idempotency
			  ON idempotency.tenant_id = authority.tenant_id
			 AND idempotency.lease_id = authority.lease_id
			WHERE authority.tenant_id = $1 AND authority.lease_id = $2
		`, request.TenantID, request.Lease.ID).Scan(&authority, &operationID, &digestHex); err != nil {
			t.Fatalf("load native authority/idempotency: %v", err)
		}
		if authority != string(ExecutionAuthorityTechstackProviderControl) ||
			operationID != result.Operation.Command.OperationID || "sha256:"+digestHex != result.RequestDigest {
			t.Fatalf("authority/idempotency = %q %q sha256:%s", authority, operationID, digestHex)
		}

		var capacityOwner, capacityProvider, capacityGeneration, capacityOperation, capacityMode, policySource, policyDigest string
		var capacityLimit int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT owner_subject_id, provider_id, resource_generation_id::text, operation_id,
			       reservation_mode, capacity_limit, policy_source, policy_digest
			FROM managed_runtime_capacity_reservations
			WHERE tenant_id = $1 AND lease_id = $2
		`, request.TenantID, request.Lease.ID).Scan(
			&capacityOwner, &capacityProvider, &capacityGeneration, &capacityOperation,
			&capacityMode, &capacityLimit, &policySource, &policyDigest,
		); err != nil {
			t.Fatalf("load managed runtime capacity reservation: %v", err)
		}
		if capacityOwner != request.OwnerSubjectID || capacityProvider != "ionos" ||
			capacityGeneration != result.ResourceGenerationID ||
			capacityOperation != result.Operation.Command.OperationID || capacityMode != string(CapacityModeLimited) ||
			capacityLimit != 100 || policySource != CapacityDecisionSourceSignedRuntimeBudget ||
			!validCapacityPolicyDigest(policyDigest) {
			t.Fatalf("capacity reservation = owner %q provider %q generation %q operation %q mode %q limit %d source %q digest %q",
				capacityOwner, capacityProvider, capacityGeneration, capacityOperation,
				capacityMode, capacityLimit, policySource, policyDigest)
		}

		var specCount, operationCount, receiptCount, registryOutboxCount int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
			  (SELECT count(*) FROM provider_desired_spec_revisions WHERE tenant_id = $1 AND lease_id = $2),
			  (SELECT count(*) FROM provider_operations WHERE tenant_id = $1 AND lease_id = $2),
			  (SELECT count(*) FROM provider_operation_receipts WHERE tenant_id = $1 AND operation_id = $3),
			  (SELECT count(*) FROM server_registry_outbox WHERE tenant_id = $1 AND server_id = $4)
		`, request.TenantID, request.Lease.ID, result.Operation.Command.OperationID, request.RuntimeServerID).Scan(
			&specCount, &operationCount, &receiptCount, &registryOutboxCount,
		); err != nil {
			t.Fatalf("count native admission custody: %v", err)
		}
		if specCount != 1 || operationCount != 1 || receiptCount != 1 || registryOutboxCount != 1 {
			t.Fatalf("custody counts = spec %d operation %d receipt %d registry-outbox %d",
				specCount, operationCount, receiptCount, registryOutboxCount)
		}
	})
}

func advanceNativeAdmissionLeaseHead(
	t *testing.T,
	database *sql.DB,
	request NativeProvisionAdmissionRequest,
) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin lease-head advance: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, setTenantErr := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, request.TenantID); setTenantErr != nil {
		t.Fatalf("set lease-head advance tenant context: %v", setTenantErr)
	}
	result, err := tx.ExecContext(t.Context(), `
		UPDATE techstack_vm_leases
		SET lease_revision = 2, desired_state = 'stopped', renewed_at = clock_timestamp(),
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND id = $2
	`, request.TenantID, request.Lease.ID)
	if err != nil {
		t.Fatalf("advance admitted lease head: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		t.Fatalf("advanced lease rows = %d, error = %v", rows, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit lease-head advance: %v", err)
	}
}

func assertNativeAdmissionCustodyCounts(
	t *testing.T,
	database *sql.DB,
	request NativeProvisionAdmissionRequest,
	result NativeProvisionAdmissionResult,
) {
	t.Helper()
	withNativeAdmissionTenant(t, database, request.TenantID, func(tx *sql.Tx) {
		var serverCount, leaseCount, authorityCount, idempotencyCount, operationCount, receiptCount, capacityCount int
		if err := tx.QueryRowContext(t.Context(), `
			SELECT
			  (SELECT count(*) FROM servers WHERE tenant_id = $1 AND id = $2),
			  (SELECT count(*) FROM techstack_vm_leases WHERE tenant_id = $1 AND id = $3),
			  (SELECT count(*) FROM runtime_lease_execution_authorities WHERE tenant_id = $1 AND lease_id = $3),
			  (SELECT count(*) FROM runtime_lease_idempotency_records WHERE tenant_id = $1 AND lease_id = $3),
			  (SELECT count(*) FROM provider_operations WHERE tenant_id = $1 AND operation_id = $4),
			  (SELECT count(*) FROM provider_operation_receipts WHERE tenant_id = $1 AND operation_id = $4),
			  (SELECT count(*) FROM managed_runtime_capacity_reservations WHERE tenant_id = $1 AND lease_id = $3)
		`, request.TenantID, request.RuntimeServerID, request.Lease.ID, result.Operation.Command.OperationID).Scan(
			&serverCount, &leaseCount, &authorityCount, &idempotencyCount, &operationCount, &receiptCount, &capacityCount,
		); err != nil {
			t.Fatalf("count native admission aggregate: %v", err)
		}
		if serverCount != 1 || leaseCount != 1 || authorityCount != 1 || idempotencyCount != 1 ||
			operationCount != 1 || receiptCount != 1 || capacityCount != 1 {
			t.Fatalf("aggregate counts = server %d lease %d authority %d idempotency %d operation %d receipt %d capacity %d",
				serverCount, leaseCount, authorityCount, idempotencyCount, operationCount, receiptCount, capacityCount)
		}
	})
}

func newNativeAdmissionIntegrationService(
	t *testing.T,
	database *sql.DB,
	resolverErr error,
) (*NativeAdmission, *queueExecutor) {
	return newNativeAdmissionIntegrationServiceWithCapacity(
		t, database, resolverErr,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 100)},
	)
}

func newNativeAdmissionIntegrationServiceWithCapacity(
	t *testing.T,
	database *sql.DB,
	resolverErr error,
	capacity CapacityPolicyResolver,
) (*NativeAdmission, *queueExecutor) {
	t.Helper()
	resolver := &nativeAdmissionIntegrationResolver{profile: testProfile("ionos-v1"), err: resolverErr}
	return newNativeAdmissionIntegrationServiceWithResolver(t, database, resolver, capacity)
}

func newNativeAdmissionIntegrationServiceWithResolver(
	t *testing.T,
	database *sql.DB,
	resolver ExecutionProfileResolver,
	capacity CapacityPolicyResolver,
) (*NativeAdmission, *queueExecutor) {
	return newNativeAdmissionIntegrationServiceWithResolverAndProviderCreates(
		t, database, resolver, capacity, AllowProviderCreate{},
	)
}

func newNativeAdmissionIntegrationServiceWithResolverAndProviderCreates(
	t *testing.T,
	database *sql.DB,
	resolver ExecutionProfileResolver,
	capacity CapacityPolicyResolver,
	providerCreates ProviderCreateAuthorizer,
) (*NativeAdmission, *queueExecutor) {
	t.Helper()
	executor := &queueExecutor{}
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", executor); err != nil {
		t.Fatalf("register native integration adapter: %v", err)
	}
	ledger, err := NewPostgresLedger(database, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	gate := AllowGate{}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Registry: registry, Profiles: resolver, Ledger: ledger, ActivationGate: gate,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	admission, err := NewNativeAdmission(NativeAdmissionConfig{
		Database: database, Coordinator: coordinator, ActivationGate: gate,
		ProviderCreates: providerCreates,
		CapacityPolicy:  capacity,
	})
	if err != nil {
		t.Fatalf("NewNativeAdmission: %v", err)
	}
	return admission, executor
}

func seedNativeAdmissionCredentialHandle(t *testing.T, database *sql.DB) {
	seedNativeAdmissionCredentialHandleForProvider(t, database, "ionos")
}

func seedNativeAdmissionCredentialHandleForProvider(t *testing.T, database *sql.DB, providerID string) {
	t.Helper()
	handle := testCredentialHandle()
	handle.ProviderID = providerID
	custodyHash, err := credentialCustodyHash("tenant-1", handle)
	if err != nil {
		t.Fatalf("hash native admission credential custody: %v", err)
	}
	connectionHash, err := credentialConnectionHash("tenant-1", handle)
	if err != nil {
		t.Fatalf("hash native admission provider connection: %v", err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin native admission credential seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, "tenant-1"); err != nil {
		t.Fatalf("set native admission credential tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_credential_handles (
			tenant_id, handle_id, handle_version, provider_id, credential_mode,
			subject_kind, subject_id, grant_id, credential_scope,
			custody_ref, connection_ref, custody_hash, connection_hash,
			valid_from, valid_until
		) VALUES (
			'tenant-1', $1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, clock_timestamp() - interval '1 hour',
			clock_timestamp() + interval '24 hours'
		)
	`, handle.HandleID, handle.HandleVersion, handle.ProviderID, handle.CredentialMode,
		handle.SubjectKind, handle.SubjectID, handle.GrantID, handle.Scope,
		handle.CustodyRef, handle.ConnectionRef, custodyHash, connectionHash); err != nil {
		t.Fatalf("seed native admission credential handle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit native admission credential handle: %v", err)
	}
}

type nativeAdmissionIntegrationResolver struct {
	profile ExecutionProfile
	err     error
	calls   atomic.Int32
}

func (r *nativeAdmissionIntegrationResolver) ResolveExecutionProfile(
	context.Context,
	ProfileRequest,
) (ExecutionProfile, error) {
	return r.profile, r.err
}

func (r *nativeAdmissionIntegrationResolver) ResolveExecutionProfileTx(
	ctx context.Context,
	tx *sql.Tx,
	request ProfileRequest,
) (ExecutionProfile, error) {
	r.calls.Add(1)
	if r.err != nil {
		return ExecutionProfile{}, r.err
	}
	var providerID, runtimeServerID, generationID string
	var leaseRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT provider_id, lease_revision, server_id, resource_generation_id::text
		FROM techstack_vm_leases
		WHERE tenant_id = $1 AND id = $2
	`, request.TenantID, request.LeaseID).Scan(
		&providerID, &leaseRevision, &runtimeServerID, &generationID,
	); err != nil {
		return ExecutionProfile{}, fmt.Errorf("integration profile resolver cannot see admitted lease: %w", err)
	}
	if providerID != r.profile.ProviderID || leaseRevision != int64(request.LeaseRevision) ||
		runtimeServerID != request.RuntimeServerID || generationID != request.ResourceGenerationID {
		return ExecutionProfile{}, fmt.Errorf("%w: integration profile resolver observed mismatched lease custody", ErrProfileUnavailable)
	}
	return r.profile, nil
}

func queueExecutorCallCount(executor *queueExecutor) int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}

func withNativeAdmissionTenant(t *testing.T, database *sql.DB, tenantID string, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin tenant assertion transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, tenantID); err != nil {
		t.Fatalf("set tenant assertion context: %v", err)
	}
	fn(tx)
}

func withNativeAdmissionTenantWrite(t *testing.T, database *sql.DB, tenantID string, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin native admission tenant write: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, tenantID); err != nil {
		t.Fatalf("scope native admission tenant write: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit native admission tenant write: %v", err)
	}
}

func withWritableNativeAdmissionTenant(t *testing.T, database *sql.DB, tenantID string, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin writable tenant integration transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, tenantID); err != nil {
		t.Fatalf("set writable tenant integration context: %v", err)
	}
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit writable tenant integration transaction: %v", err)
	}
}

func openNativeAdmissionIntegrationDB(t *testing.T) *sql.DB {
	return openNativeAdmissionIntegrationDBWithDispatch(t, ProvisionDispatchNativeIdempotency)
}

func openNativeAdmissionIntegrationDBWithDispatch(
	t *testing.T,
	provisionDispatchMode ProvisionDispatchMode,
) *sql.DB {
	return openNativeAdmissionIntegrationDBWithProfile(
		t,
		provisionDispatchMode,
		"ionos",
		"ionos-v1",
		"ionos-managed-pvm-monthly",
	)
}

func openNativeAdmissionIntegrationDBWithProfile(
	t *testing.T,
	provisionDispatchMode ProvisionDispatchMode,
	providerID string,
	adapterID string,
	runtimeProfileID string,
) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping native admission PostgreSQL integration test")
	}

	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse native admission integration DSN: %v", err)
	}
	adminDatabase := stdlib.OpenDB(*adminConfig)
	if pingErr := adminDatabase.PingContext(t.Context()); pingErr != nil {
		_ = adminDatabase.Close()
		t.Fatalf("ping native admission integration PostgreSQL: %v", pingErr)
	}
	schema := fmt.Sprintf("native_admission_test_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, createSchemaErr := adminDatabase.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); createSchemaErr != nil {
		_ = adminDatabase.Close()
		t.Fatalf("create native admission integration schema: %v", createSchemaErr)
	}

	scopedConfig := adminConfig.Copy()
	scopedConfig.RuntimeParams = make(map[string]string, len(adminConfig.RuntimeParams)+1)
	for key, value := range adminConfig.RuntimeParams {
		scopedConfig.RuntimeParams[key] = value
	}
	scopedConfig.RuntimeParams["search_path"] = schema + ",public"
	database := stdlib.OpenDB(*scopedConfig)
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	if pingErr := database.PingContext(t.Context()); pingErr != nil {
		_ = database.Close()
		_, _ = adminDatabase.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = adminDatabase.Close()
		t.Fatalf("open native admission integration database: %v", pingErr)
	}
	t.Cleanup(func() {
		_ = database.Close()
		_, _ = adminDatabase.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = adminDatabase.Close()
	})
	applyNativeAdmissionMigrations(t, database)
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin native admission tenant seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config($1, $2, true)`, tenantContextKey, "tenant-1"); err != nil {
		t.Fatalf("set native admission seed tenant context: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status)
		VALUES ('tenant-1', 'Native Admission Test', 'saas', 'active')
	`); err != nil {
		t.Fatalf("seed native admission tenant: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO stacks (id, tenant_id, owner_subject_id, name, status)
		VALUES ('stack-native-1', 'tenant-1', 'owner-1', 'Native Admission Stack', 'draft')
	`); err != nil {
		t.Fatalf("seed native admission stack: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-2026-07-21', 'draft')
	`); err != nil {
		t.Fatalf("seed native admission provider catalog version: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot, adapter_manifest_hash,
			provision_dispatch_mode
		) VALUES (
			'catalog-2026-07-21', $1, $2, 'managed',
			$3, 'monthly-runtime-standard',
			true, 'pause', true, '{}'::jsonb, $4, $5
		)
	`, providerID, adapterID, runtimeProfileID, digest("test-adapter-manifest"), provisionDispatchMode); err != nil {
		t.Fatalf("seed native admission provider catalog profile: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
		UPDATE provider_catalog_versions
		SET status = 'retired', retired_at = clock_timestamp()
		WHERE status = 'active';

		UPDATE provider_catalog_versions
		SET status = 'active', activated_at = clock_timestamp()
		WHERE catalog_version = 'catalog-2026-07-21'
	`); err != nil {
		t.Fatalf("activate native admission provider catalog: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit native admission tenant seed: %v", err)
	}
	return database
}

func openRestrictedNativeAdmissionRuntimeDB(t *testing.T, adminDatabase *sql.DB) *sql.DB {
	t.Helper()
	var schema string
	if err := adminDatabase.QueryRowContext(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatalf("load native admission integration schema: %v", err)
	}
	var superuser, createRole bool
	if err := adminDatabase.QueryRowContext(t.Context(), `
		SELECT rolsuper, rolcreaterole
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&superuser, &createRole); err != nil {
		t.Fatalf("inspect native admission integration role authority: %v", err)
	}
	if !superuser && !createRole {
		t.Skip("integration PostgreSQL role cannot create a restricted runtime role")
	}

	role := "native_admission_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := "native_admission_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := pgx.Identifier{role}.Sanitize()
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDatabase.ExecContext(t.Context(), fmt.Sprintf(
		"CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS",
		quotedRole, password,
	)); err != nil {
		t.Fatalf("create restricted native admission role: %v", err)
	}

	var runtimeDatabase *sql.DB
	t.Cleanup(func() {
		if runtimeDatabase != nil {
			_ = runtimeDatabase.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminDatabase.ExecContext(cleanupCtx, "DROP OWNED BY "+quotedRole)
		_, _ = adminDatabase.ExecContext(cleanupCtx, "DROP ROLE IF EXISTS "+quotedRole)
	})
	// #nosec G202 -- both identifiers are locally generated and quoted with pgx.Identifier.
	if _, err := adminDatabase.ExecContext(t.Context(),
		"GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatalf("grant restricted integration schema usage: %v", err)
	}
	grants := []struct {
		table      string
		privileges string
	}{
		{"provider_catalog_versions", "SELECT"},
		{"provider_catalog_profiles", "SELECT"},
		{"provider_credential_handles", "SELECT"},
		{"substrate_bindings", "SELECT"},
		{"substrate_guest_leases", "SELECT,INSERT"},
		{"servers", "SELECT,INSERT,UPDATE"},
		{"server_state_transitions", "SELECT,INSERT"},
		{"server_registry_outbox", "SELECT,INSERT"},
		{"techstack_vm_leases", "SELECT,INSERT"},
		{"runtime_lease_execution_authorities", "SELECT,INSERT"},
		{"runtime_lease_idempotency_records", "SELECT,INSERT"},
		{"managed_runtime_capacity_reservations", "SELECT,INSERT"},
		{"managed_runtime_server_slots", "SELECT,INSERT"},
		{"managed_runtime_server_slot_generations", "SELECT,INSERT"},
		{"managed_runtime_capacity_release_facts", "SELECT,INSERT"},
		{"managed_runtime_capacity_quarantine_retirements", "SELECT"},
		{"provider_desired_spec_revisions", "SELECT,INSERT,UPDATE"},
		{"provider_operations", "SELECT,INSERT,UPDATE"},
		{"provider_operation_receipts", "SELECT,INSERT"},
		{"provider_operation_resources", "SELECT,INSERT,UPDATE"},
		{"provider_operation_evidence", "SELECT,INSERT,UPDATE"},
		{"provider_operation_execution_claims", "SELECT,INSERT,UPDATE"},
		{"provider_provision_dispatch_guards", "SELECT,INSERT"},
		{"provider_provision_discovery_observations", "SELECT,INSERT"},
		{"provider_provision_resolution_decisions", "SELECT,INSERT"},
		{"server_provider_resource_bindings", "SELECT,INSERT"},
		{"provider_operation_resource_free_terminalizations", "SELECT,INSERT"},
	}
	for _, grant := range grants {
		// #nosec G202 -- table names are constants and the role identifier is locally generated and quoted.
		statement := "GRANT " + grant.privileges + " ON " +
			pgx.Identifier{schema, grant.table}.Sanitize() + " TO " + quotedRole
		if _, err := adminDatabase.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("grant restricted runtime access on %s: %v", grant.table, err)
		}
	}
	for _, sequence := range []string{"server_state_transitions_id_seq", "server_registry_outbox_id_seq"} {
		// #nosec G202 -- sequence names are constants and the role identifier is locally generated and quoted.
		statement := "GRANT USAGE ON SEQUENCE " +
			pgx.Identifier{schema, sequence}.Sanitize() + " TO " + quotedRole
		if _, err := adminDatabase.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("grant restricted runtime sequence %s: %v", sequence, err)
		}
	}
	for _, function := range []string{
		"substrate_guest_custody_valid(text,text,text,text)",
		"substrate_lock_binding(text,text,bigint)",
		"provider_control_lock_runtime_lease_projection(text)",
		"provider_control_count_unsettled_generation_dispatch_guards(text,uuid)",
	} {
		// #nosec G202 -- function and role identifiers are fixed or locally generated and quoted.
		if _, err := adminDatabase.ExecContext(
			t.Context(),
			"GRANT EXECUTE ON FUNCTION "+quotedSchema+"."+function+" TO "+quotedRole,
		); err != nil {
			t.Fatalf("grant restricted runtime function %s: %v", function, err)
		}
	}

	config, err := pgx.ParseConfig(strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL")))
	if err != nil {
		t.Fatalf("parse restricted native admission DSN: %v", err)
	}
	config.User = role
	config.Password = password
	runtimeParams := make(map[string]string, len(config.RuntimeParams)+1)
	for key, value := range config.RuntimeParams {
		runtimeParams[key] = value
	}
	config.RuntimeParams = runtimeParams
	config.RuntimeParams["search_path"] = schema + ",public"
	runtimeDatabase = stdlib.OpenDB(*config)
	runtimeDatabase.SetMaxOpenConns(4)
	runtimeDatabase.SetMaxIdleConns(4)
	if err := runtimeDatabase.PingContext(t.Context()); err != nil {
		t.Fatalf("ping restricted native admission role: %v", err)
	}
	return runtimeDatabase
}

func applyNativeAdmissionMigrations(t *testing.T, database *sql.DB) {
	t.Helper()
	migrationDir := filepath.Join("..", "..", "pkg", "db", "migrations")
	entries, err := os.ReadDir(migrationDir)
	if err != nil {
		t.Fatalf("read native admission migrations: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(migrationDir, entry.Name()))
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}
		tx, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("begin migration %s: %v", entry.Name(), err)
		}
		if _, err := tx.ExecContext(t.Context(), string(payload)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration %s: %v", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration %s: %v", entry.Name(), err)
		}
	}
}
