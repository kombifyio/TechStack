package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreUpsertServiceRuntimePersistsSeparatedState(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	service := ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
		ServiceKey: "vaultwarden", ServiceInstance: "default", Name: "Vaultwarden",
		DesiredState: "running", ObservedState: "running", HealthState: "healthy", ObservedAt: &now,
		StackKitVersion: "basement-kit@1.2.3", Access: map[string]any{"mode": "relay", "url": "https://vault.owner.kombify.me", "route_id": "route-1"},
		Capabilities: []string{"restart"}, Source: "stackkits-inventory", Metadata: map[string]any{"container_id": "container-1"},
	}
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(serviceAggregateHeadPattern()).
		WithArgs("tenant-1", "service-1").
		WillReturnRows(serviceAggregateRows())
	// Status is the derived observed projection, never the health value.
	mock.ExpectQuery(serviceAggregateInsertPattern()).
		WithArgs(
			"service-1", "tenant-1", "", "stack-1", "", "server-1",
			"server", "", "", "", "", "", "", nil,
			"vaultwarden", "default",
			"Vaultwarden", "running", "stackkits-inventory", "https://vault.owner.kombify.me", "",
			sqlmock.AnyArg(), "running", "running", "healthy", now, "basement-kit@1.2.3",
			sqlmock.AnyArg(), sqlmock.AnyArg(), int64(1), "managed",
			"unlocked", "", "", nil,
		).
		WillReturnRows(serviceAggregateRows().AddRow(
			"service-1", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"vaultwarden", "default", "Vaultwarden",
			"running", "running", "healthy", "managed", "unlocked", nil, nil, nil, now, "basement-kit@1.2.3",
			`{"mode":"relay","route_id":"route-1","url":"https://vault.owner.kombify.me"}`, `["restart"]`,
			"stackkits-inventory", `{"container_id":"container-1"}`, now, now,
			int64(1), "running", "", "", "https://vault.owner.kombify.me",
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
				nil, "stackkits-inventory", now, `{}`, now,
			))
	}
	mock.ExpectCommit()

	got, err := store.UpsertServiceRuntime(context.Background(), service)
	if err != nil {
		t.Fatalf("UpsertServiceRuntime: %v", err)
	}
	if got.HealthState != "healthy" || got.Access["mode"] != "relay" || len(got.Capabilities) != 1 {
		t.Fatalf("unexpected service runtime: %#v", got)
	}
	if got.ObservedState != "running" {
		t.Fatalf("observed state was conflated with health: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreGetServiceRuntimeFallsBackToUnknownLegacyProjection(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("FROM services WHERE tenant_id = $1 AND id = $2")+`.*target_kind = 'managed_workload'`).
		WithArgs("tenant-1", "legacy-service-1").
		WillReturnRows(serviceRuntimeRows())
	mock.ExpectQuery(regexp.QuoteMeta("FROM services legacy")).
		WithArgs("tenant-1", "legacy-service-1").
		WillReturnRows(serviceRuntimeRows().AddRow(
			"legacy-service-1", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"vaultwarden", "default", "Vaultwarden",
			"running", "unknown", "unknown", "observed", "unlocked", nil, nil, nil, nil, nil,
			`{"mode":"unavailable","reason_code":"legacy_backfill_requires_observation"}`, `[]`,
			legacyServiceRuntimeBackfillSource, `{"backfill":true,"legacy_source":"legacy-registry","legacy_status":"healthy"}`, now, now,
		))
	mock.ExpectCommit()

	got, err := store.GetServiceRuntime(context.Background(), "tenant-1", "legacy-service-1")
	if err != nil {
		t.Fatalf("GetServiceRuntime: %v", err)
	}
	if got.ServerID != "server-1" || got.HealthState != legacyServiceRuntimeUnknown || got.ObservedAt != nil || got.Access[legacyServiceAccessModeKey] != legacyServiceRuntimeUnavailable {
		t.Fatalf("unsafe legacy projection: %#v", got)
	}
	if got.Metadata["backfill"] != true || got.Source != legacyServiceRuntimeBackfillSource {
		t.Fatalf("missing backfill provenance: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreListServiceRuntimesCombinesMeasuredAndSafeBackfill(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("FROM services")+".*"+regexp.QuoteMeta("WHERE tenant_id = $1")+`.*target_kind = 'managed_workload'`).
		WithArgs("tenant-1", "stack-1", "").
		WillReturnRows(serviceRuntimeRows().AddRow(
			"service-measured", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"z-service", "default", "Measured",
			legacyServiceDesiredRunning, legacyServiceDesiredRunning, "healthy", "managed",
			"unlocked", nil, nil, nil, now, "basement-kit@1.2.3",
			`{"mode":"direct","url":"https://service.example.test"}`, `[]`, "stackkits-inventory", `{}`, now, now,
		))
	mock.ExpectQuery(regexp.QuoteMeta("FROM services legacy")).
		WithArgs("tenant-1", "stack-1", "").
		WillReturnRows(serviceRuntimeRows().AddRow(
			"legacy-service", "tenant-1", nil, "stack-1", "server-1", "server",
			nil, nil, nil, nil, nil, nil, nil,
			"a-service", "default", "Backfilled",
			legacyServiceDesiredRunning, legacyServiceRuntimeUnknown, legacyServiceRuntimeUnknown, "observed",
			"unlocked", nil, nil, nil, nil, nil,
			`{"mode":"unavailable","reason_code":"legacy_backfill_requires_observation"}`, `[]`,
			legacyServiceRuntimeBackfillSource, `{"backfill":true}`, now, now,
		))
	mock.ExpectCommit()

	rows, err := store.ListServiceRuntimes(context.Background(), "tenant-1", "stack-1", "")
	if err != nil {
		t.Fatalf("ListServiceRuntimes: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "legacy-service" || rows[1].ID != "service-measured" {
		t.Fatalf("combined rows = %#v", rows)
	}
	if rows[0].HealthState != legacyServiceRuntimeUnknown || rows[0].Access[legacyServiceAccessModeKey] != legacyServiceRuntimeUnavailable {
		t.Fatalf("unsafe backfill row: %#v", rows[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func serviceRuntimeRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "server_id",
		"target_kind", "provider_id", "managed_target_ref", "provider_receipt_ref",
		"sla_policy_ref", "backup_policy_ref", "placement_evidence_ref", "placement_observed_at",
		"service_key", "service_instance", "name",
		"desired_state", "observed_state", "health_state", "management_state",
		"mutation_lock_state", "mutation_lock_reason_code", "mutation_lock_actor", "mutation_lock_changed_at",
		"observed_at", "stackkit_version", "access_json", "capabilities_json",
		"source", "metadata_json", "created_at", "updated_at",
	})
}
