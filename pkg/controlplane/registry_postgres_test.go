package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreRegistryUsesTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 21, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO nodes")).
		WithArgs("node-1", "tenant-1", "instance-1", "stack-1", "", "media stack", "main", "online", "10.0.0.10", sqlmock.AnyArg()).
		WillReturnRows(nodeRows().AddRow(
			"node-1", "tenant-1", "instance-1", "stack-1", nil, "media stack", "main", "online", "10.0.0.10", `{"source":"stackkit_outputs"}`, now, now,
		))
	mock.ExpectCommit()

	node, err := store.UpsertNode(context.Background(), Node{
		ID:         "node-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		StackID:    "stack-1",
		Name:       "media stack",
		Role:       "main",
		Status:     "online",
		Address:    "10.0.0.10",
		Metadata:   map[string]any{"source": "stackkit_outputs"},
	})
	if err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if node.ID != "node-1" || node.Metadata["source"] != "stackkit_outputs" {
		t.Fatalf("unexpected node: %#v", node)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO services")).
		WithArgs("service-1", "tenant-1", "instance-1", "stack-1", "node-1", "jellyfin", "Jellyfin", "running", "stackkit_outputs", "https://jellyfin.example.test", "", sqlmock.AnyArg(), "managed").
		WillReturnRows(serviceRows().AddRow(
			"service-1", "tenant-1", "instance-1", "stack-1", "node-1", "jellyfin", "Jellyfin", "running", "stackkit_outputs", "https://jellyfin.example.test", nil, `{"port":8096}`, "managed", now, now,
		))
	mock.ExpectCommit()

	service, err := store.UpsertService(context.Background(), Service{
		ID:         "service-1",
		TenantID:   "tenant-1",
		InstanceID: "instance-1",
		StackID:    "stack-1",
		NodeID:     "node-1",
		ServiceKey: "jellyfin",
		Name:       "Jellyfin",
		Status:     "running",
		Source:     "stackkit_outputs",
		URL:        "https://jellyfin.example.test",
		Metadata:   map[string]any{"port": float64(8096)},
	})
	if err != nil {
		t.Fatalf("UpsertService: %v", err)
	}
	if service.ID != "service-1" || service.Metadata["port"] != float64(8096) {
		t.Fatalf("unexpected service: %#v", service)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")).
		WithArgs("tenant-1", "stack-1").
		WillReturnRows(nodeRows().AddRow(
			"node-1", "tenant-1", "instance-1", "stack-1", nil, "media stack", "main", "online", "10.0.0.10", `{"source":"stackkit_outputs"}`, now, now,
		))
	mock.ExpectCommit()

	nodes, err := store.ListNodesByStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != "node-1" {
		t.Fatalf("nodes = %#v, want node-1", nodes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func nodeRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "worker_id", "name", "role", "status", "address", "metadata_json", "created_at", "updated_at",
	})
}

func serviceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "node_id", "service_key", "name", "status", "source", "url", "migration_status", "metadata_json", "management_state", "created_at", "updated_at",
	})
}
