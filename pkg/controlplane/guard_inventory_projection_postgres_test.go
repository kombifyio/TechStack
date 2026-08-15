package controlplane

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreApplyGuardInventoryProjectionCommitsOneTenantTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 7, 22, 5, 0, 0, 0, time.UTC)
	databaseNow := observedAt.Add(time.Second)
	command := guardInventoryPostgresTestCommand(observedAt, 1, 2)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, databaseNow)
	expectAcceptedGuardInventoryServerEvent(mock, observedAt, databaseNow)
	mock.ExpectExec(guardInventoryNodeUpsertPattern()).
		WithArgs(
			"server-1", "tenant-1", "instance-1", "stack-1", "guard-1", "runtime-1",
			"foundation", "healthy", "203.0.113.10", jsonInventoryRevisionArgument{revision: 1},
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectAcceptedGuardInventoryServiceEvent(mock, observedAt, databaseNow)
	mock.ExpectExec(guardInventoryRetainedUpdatePattern()).
		WithArgs("tenant-1", "stack-1", "server-1", "stackkits-inventory", jsonStringSliceArgument{values: []string{"service-1"}}, int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(guardInventoryPrunePattern()).
		WithArgs("tenant-1", "stack-1", "server-1", "stackkits-inventory", jsonStringSliceArgument{values: []string{"service-1"}}).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	result, err := store.ApplyGuardInventoryProjection(context.Background(), command)
	if err != nil {
		t.Fatalf("ApplyGuardInventoryProjection: %v", err)
	}
	if result == nil || result.ServerEvent == nil || !result.ServerEvent.Applied || result.Replayed ||
		result.ServerEvent.Inventory == nil || result.ServerEvent.Inventory.Revision != 1 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyGuardInventoryProjectionRollsBackServerOnServiceFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 7, 22, 5, 15, 0, 0, time.UTC)
	databaseNow := observedAt.Add(time.Second)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, databaseNow)
	expectAcceptedGuardInventoryServerEvent(mock, observedAt, databaseNow)
	mock.ExpectExec(guardInventoryNodeUpsertPattern()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceAggregateRows())
	mock.ExpectQuery(serviceAggregateInsertPattern()).
		WillReturnError(errors.New("service projection failed"))
	mock.ExpectRollback()

	_, err = store.ApplyGuardInventoryProjection(context.Background(), guardInventoryPostgresTestCommand(observedAt, 1, 2))
	if err == nil || err.Error() != "service projection failed" {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreApplyGuardInventoryProjectionVerifiesExactReplayDigest(t *testing.T) {
	for _, test := range []struct {
		name      string
		mismatch  bool
		wantError bool
	}{
		{name: "same projection replays"},
		{name: "same source position with changed projection conflicts", mismatch: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := NewPostgresStore(db)
			observedAt := time.Date(2026, 7, 22, 5, 30, 0, 0, time.UTC)
			databaseNow := observedAt.Add(time.Second)
			command := guardInventoryPostgresTestCommand(observedAt, 2, 2)
			prepared, err := prepareGuardInventoryProjection(command)
			if err != nil {
				t.Fatalf("prepare fixture: %v", err)
			}
			inventory := cloneGuardCanonicalMap(prepared.command.Event.Inventory.Inventory)
			if test.mismatch {
				deployment := inventory["deployment"].(map[string]any)
				envelope := deployment[guardProjectionEnvelopeKey].(map[string]any)
				envelope["digest"] = "sha256:" + strings.Repeat("0", 64)
			}
			inventoryJSON, err := json.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}

			mock.ExpectBegin()
			expectTenantGUC(mock, "tenant-1")
			expectServerEventDatabaseTime(mock, databaseNow)
			mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
				WithArgs("tenant-1", "server-1").
				WillReturnRows(guardInventoryCurrentServerRows(observedAt, 1, 2, 2))
			mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
				WithArgs("tenant-1", "server-1").
				WillReturnRows(guardInventoryCurrentServerRows(observedAt, 1, 2, 2))
			mock.ExpectQuery(`(?s)SELECT id, tenant_id, server_id, revision, source, observed_at,.*inventory_json::text, created_at.*FROM server_inventory_snapshots.*tenant_id = \$1.*server_id = \$2.*revision = \$3`).
				WithArgs("tenant-1", "server-1", int64(1)).
				WillReturnRows(serverEventInventoryRows().AddRow(
					int64(1), "tenant-1", "server-1", int64(1), "guard-inventory", observedAt,
					string(inventoryJSON), databaseNow,
				))
			if test.wantError {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}

			result, applyErr := store.ApplyGuardInventoryProjection(context.Background(), command)
			if test.wantError {
				if !errors.Is(applyErr, ErrConflict) {
					t.Fatalf("error = %v, want ErrConflict", applyErr)
				}
			} else {
				if applyErr != nil {
					t.Fatalf("ApplyGuardInventoryProjection: %v", applyErr)
				}
				if result == nil || result.ServerEvent == nil || result.ServerEvent.Applied || !result.Replayed ||
					result.ServerEvent.Inventory == nil || result.ServerEvent.Inventory.Revision != 1 {
					t.Fatalf("replay result = %#v", result)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPruneGuardInventoryServicesIsTenantSourceAndMigrationSafe(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(guardInventoryRetainedUpdatePattern()).
		WithArgs("tenant-1", "stack-1", "server-1", "stackkits-inventory", jsonStringSliceArgument{values: []string{}}, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(guardInventoryPrunePattern()).
		WithArgs("tenant-1", "stack-1", "server-1", "stackkits-inventory", jsonStringSliceArgument{values: []string{}}).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectRollback()

	if err := pruneGuardInventoryServicesTx(
		context.Background(), tx, "tenant-1", "stack-1", "server-1", "stackkits-inventory", nil, 7,
	); err != nil {
		t.Fatalf("pruneGuardInventoryServicesTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func guardInventoryPostgresTestCommand(observedAt time.Time, expectedRevision, sourceSequence int64) GuardInventoryProjection {
	return GuardInventoryProjection{
		Event: ServerEvent{
			TenantID: "tenant-1", ServerID: "server-1", ExpectedRevision: expectedRevision, Generation: 1,
			Authority: ServerEventAuthorityGuard, Source: "guard-inventory", SourceID: "guard-1",
			SourceEpoch: "epoch-a", SourceSequence: sourceSequence, ObservedAt: observedAt,
			ClearConnectionReason: true, ClearHealthReason: true,
			Evidence: map[string]any{"runtime_agent_id": "guard-1"},
			Runtime: ServerRuntime{
				ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
				OwnerSubjectID: "owner-1", WorkerID: "guard-1", NodeID: "server-1", LeaseID: "lease-1",
				ProviderRef: "ionos", Name: "runtime-1", ConnectionState: "connected", HealthState: "healthy",
				LastHeartbeatAt: &observedAt, Metadata: map[string]any{"runtime_agent_id": "guard-1"},
			},
			Inventory: &ServerInventoryEvent{
				Source: "guard-inventory",
				Inventory: map[string]any{
					"host":       map[string]any{"hostname": "runtime-1"},
					"services":   []any{map[string]any{"id": "service-1"}},
					"deployment": map[string]any{"stackkit": "cloud-kit"},
				},
			},
		},
		Node: Node{
			ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
			WorkerID: "guard-1", Name: "runtime-1", Role: "foundation", Status: "healthy",
			Address: "203.0.113.10", Metadata: map[string]any{"runtime_agent_id": "guard-1"},
		},
		Services: []GuardInventoryServiceProjection{{
			Legacy: Service{
				ID: "service-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
				NodeID: "server-1", ServiceKey: "vaultwarden", Name: "Vaultwarden", Status: "healthy",
				Source: "stackkits-inventory", URL: "https://vault.kombified.com",
				Metadata: map[string]any{"reported_service_id": "vault"},
			},
			Runtime: ServiceRuntime{
				ID: "service-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1",
				ServerID: "server-1", ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
				DesiredState: "running", ObservedState: "running", HealthState: "healthy", ObservedAt: &observedAt,
				StackKitVersion: "cloud-kit@1.0.0", Access: map[string]any{"mode": "direct", "url": "https://vault.kombified.com"},
				Capabilities: []string{"restart"}, Source: "stackkits-inventory",
				Metadata: map[string]any{"container_id": "container-1"},
			},
		}},
		ManifestObserved: true,
		ServiceSource:    "stackkits-inventory",
	}
}

func expectAcceptedGuardInventoryServerEvent(mock sqlmock.Sqlmock, observedAt, databaseNow time.Time) {
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(guardInventoryCurrentServerRows(observedAt.Add(-time.Minute), 0, 1, 1))
	mock.ExpectQuery(guardInventoryRetainedIDsPattern()).
		WithArgs("tenant-1", "stack-1", "server-1", "stackkits-inventory", jsonStringSliceArgument{values: []string{"service-1"}}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").
		WillReturnRows(guardInventoryCurrentServerRows(observedAt.Add(-time.Minute), 0, 1, 1))
	mock.ExpectQuery(`(?s)UPDATE servers SET.*revision = \$27.*RETURNING`).
		WillReturnRows(guardInventoryCurrentServerRows(observedAt, 1, 2, 2))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_inventory_snapshots (")).
		WillReturnRows(serverEventInventoryRows().AddRow(
			int64(1), "tenant-1", "server-1", int64(1), "guard-inventory", observedAt,
			`{"host":{"hostname":"runtime-1"}}`, databaseNow,
		))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO server_guard_source_epochs (")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO server_registry_outbox (")).
		WillReturnRows(serverEventOutboxRows().AddRow(
			int64(1), "tenant-1", "server-1", int64(2), int64(1), "guard",
			"guard-inventory", "server.aggregate.updated", `{"server_id":"server-1","aggregate_revision":2}`,
			observedAt, databaseNow, nil, 0,
		))
}

func guardInventoryCurrentServerRows(observedAt time.Time, inventoryRevision, revision, sourceSequence int64) *sqlmock.Rows {
	return serverEventRuntimeRows().AddRow(
		"server-1", "tenant-1", "instance-1", "stack-1", "owner-1", "guard-1", "server-1",
		"lease-1", "ionos", "unknown", nil, nil, nil, nil, nil, nil, nil, "runtime-1", "active", "running", "connected", "healthy",
		nil, observedAt, observedAt, inventoryRevision, revision, int64(1), "guard", "guard-1", "epoch-a", sourceSequence, observedAt,
		`[]`, `{}`, nil, nil, nil, nil, nil,
		observedAt, observedAt, observedAt, observedAt.Add(-time.Hour), observedAt,
	)
}

func guardInventoryPrunePattern() string {
	return `(?s)DELETE FROM services.*tenant_id = \$1.*stack_id = \$2.*source.*\$4.*server_id = \$3 OR \(server_id IS NULL AND node_id = \$3\).*migration_status.*NOT IN \('migrating', 'deploying', 'pending_verification', 'archived'\).*status.*NOT IN \('migrating', 'deploying', 'pending_verification', 'archived'\).*jsonb_array_elements_text\(\$5::jsonb\)`
}

func guardInventoryRetainedIDsPattern() string {
	return `(?s)SELECT id FROM services.*tenant_id = \$1.*stack_id = \$2.*server_id = \$3 OR \(server_id IS NULL AND node_id = \$3\).*source.*\$4.*migration_status.*'archived'.*jsonb_array_elements_text\(\$5::jsonb\).*FOR UPDATE`
}

func guardInventoryRetainedUpdatePattern() string {
	return `(?s)UPDATE services SET.*server_id = \$3.*url = NULL.*inventory_revision.*\$6::bigint.*access_json = CASE.*service_archived.*migration_in_progress.*tenant_id = \$1.*stack_id = \$2.*source.*\$4.*jsonb_array_elements_text\(\$5::jsonb\)`
}

func guardInventoryNodeUpsertPattern() string {
	return `(?s)INSERT INTO nodes.*ON CONFLICT \(id\) DO UPDATE SET\s+stack_id = COALESCE\(nodes.stack_id, EXCLUDED.stack_id\).*status = EXCLUDED.status.*WHERE nodes.tenant_id = EXCLUDED.tenant_id.*nodes.stack_id IS NULL OR nodes.stack_id = EXCLUDED.stack_id.*nodes.instance_id IS NULL OR nodes.instance_id = EXCLUDED.instance_id.*nodes.worker_id IS NULL OR nodes.worker_id = EXCLUDED.worker_id`
}

// expectAcceptedGuardInventoryServiceEvent asserts that the Guard service
// projection reaches the database only through the service aggregate boundary:
// one locked head read, one head write, and one transition row per dimension
// that changed. A created aggregate changes all four dimensions.
func expectAcceptedGuardInventoryServiceEvent(mock sqlmock.Sqlmock, observedAt, databaseNow time.Time) {
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceAggregateRows())
	mock.ExpectQuery(serviceAggregateInsertPattern()).
		WillReturnRows(serviceAggregateRows().AddRow(
			"service-1", "tenant-1", "instance-1", "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"vaultwarden",
			"default", "Vaultwarden", "running", "running", "healthy", "managed", observedAt,
			"cloud-kit@1.0.0", `{"mode":"direct","url":"https://vault.kombified.com"}`,
			`["restart"]`, "stackkits-inventory", `{"container_id":"container-1"}`,
			databaseNow, databaseNow, int64(1), "running", "", "server-1",
			"https://vault.kombified.com",
		))
	for _, dimension := range []struct{ name, to string }{
		{name: "desired", to: "running"},
		{name: "observed", to: "running"},
		{name: "health", to: "healthy"},
		{name: "management", to: "managed"},
	} {
		mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO service_state_transitions (")).
			WillReturnRows(serviceTransitionRows().AddRow(
				int64(1), "tenant-1", "service-1", dimension.name, nil, dimension.to,
				nil, "stackkits-inventory", observedAt, `{}`, databaseNow,
			))
	}
}

func serviceAggregateHeadPattern() string {
	return `(?s)SELECT .*FROM services\s+WHERE tenant_id = \$1 AND id = \$2\s+FOR UPDATE`
}

func serviceAggregateInsertPattern() string {
	return `(?s)INSERT INTO services \(.*ON CONFLICT \(id\) DO NOTHING.*RETURNING`
}

func serviceAggregateUpdatePattern() string {
	return `(?s)UPDATE services SET.*revision = \$30, management_state = \$31.*WHERE tenant_id = \$2 AND id = \$1 AND revision = \$32.*RETURNING`
}

func serviceAggregateRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "server_id",
		"target_kind", "provider_id", "managed_target_ref", "provider_receipt_ref",
		"sla_policy_ref", "backup_policy_ref", "placement_evidence_ref", "placement_observed_at",
		"service_key", "service_instance", "name", "desired_state", "observed_state", "health_state",
		"management_state", "observed_at", "stackkit_version", "access_json", "capabilities_json",
		"source", "metadata_json", "created_at", "updated_at",
		"revision", "status", "migration_status", "node_id", "url",
	})
}

func serviceTransitionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "service_id", "dimension", "from_state", "to_state",
		"reason_code", "source", "observed_at", "evidence_json", "created_at",
	})
}

type jsonInventoryRevisionArgument struct {
	revision int64
}

func (argument jsonInventoryRevisionArgument) Match(value driver.Value) bool {
	var payload []byte
	switch typed := value.(type) {
	case []byte:
		payload = typed
	case string:
		payload = []byte(typed)
	default:
		return false
	}
	var object map[string]any
	if json.Unmarshal(payload, &object) != nil {
		return false
	}
	revision, ok := object["inventory_revision"].(float64)
	return ok && int64(revision) == argument.revision
}

type jsonStringSliceArgument struct {
	values []string
}

func (argument jsonStringSliceArgument) Match(value driver.Value) bool {
	var payload []byte
	switch typed := value.(type) {
	case []byte:
		payload = typed
	case string:
		payload = []byte(typed)
	default:
		return false
	}
	var values []string
	if json.Unmarshal(payload, &values) != nil || len(values) != len(argument.values) {
		return false
	}
	for index := range values {
		if values[index] != argument.values[index] {
			return false
		}
	}
	return true
}
