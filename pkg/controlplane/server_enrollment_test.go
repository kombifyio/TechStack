package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMemoryStoreApplyServerEnrollmentBindsNodeAtomicallyAndPreservesExistingControlFields(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	command := serverEnrollmentTestCommand(now)
	result, err := store.ApplyServerEnrollment(context.Background(), command)
	if err != nil || result == nil || !result.Applied || result.Server.NodeID != "server-1" {
		t.Fatalf("ApplyServerEnrollment = %#v, %v", result, err)
	}
	node, err := store.GetNode(context.Background(), "tenant-1", "server-1")
	if err != nil || node.Status != "pending" || node.Role != "foundation" || node.WorkerID != "guard-1" {
		t.Fatalf("enrollment node = %#v, %v", node, err)
	}
	node.Name, node.Role, node.Status = "operator-name", "storage", "ready"
	store.nodes[node.ID] = *node
	command.Event.ExpectedRevision = result.Server.Revision
	command.Event.Generation = result.Server.Generation
	command.Event.ObservedAt = now.Add(time.Minute)
	command.Event.Runtime.ConnectionState = ""
	command.Event.Runtime.HealthState = ""
	if _, err := store.ApplyServerEnrollment(context.Background(), command); err != nil {
		t.Fatalf("repeat enrollment: %v", err)
	}
	preserved, _ := store.GetNode(context.Background(), "tenant-1", "server-1")
	if preserved.Name != "operator-name" || preserved.Role != "storage" || preserved.Status != "ready" {
		t.Fatalf("existing control-plane node was rewritten: %#v", preserved)
	}
}

func TestPostgresStoreApplyServerEnrollmentRollsBackNodeWhenServerInsertFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(`(?s)SELECT id, tenant_id.*FROM nodes WHERE id = \$1 FOR UPDATE`).
		WithArgs("server-1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO nodes`).
		WithArgs("server-1", "tenant-1", "instance-1", "stack-1", "guard-1", "runtime-1", "foundation", "", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT .* FROM servers.*FOR UPDATE`).
		WithArgs("tenant-1", "server-1").WillReturnRows(serverEventRuntimeRows())
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO servers (")).WillReturnError(errors.New("server insert failed"))
	mock.ExpectRollback()

	_, err = NewPostgresStore(db).ApplyServerEnrollment(context.Background(), serverEnrollmentTestCommand(now))
	if err == nil || err.Error() != "server insert failed" {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreEnrollmentPersistsAnUnassignedNodeAsNullStack(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 5, 20, 0, 0, 0, time.UTC)
	command := serverEnrollmentTestCommand(now)
	command.Node.StackID = ""
	command.Event.Runtime.StackID = ""

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	expectServerEventDatabaseTime(mock, now)
	mock.ExpectQuery(`(?s)SELECT id, tenant_id.*FROM nodes WHERE id = \$1 FOR UPDATE`).
		WithArgs("server-1").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`(?s)INSERT INTO nodes .*VALUES \(\$1, \$2, NULLIF\(\$3, ''\), NULLIF\(\$4, ''\), NULLIF\(\$5, ''\)`).
		WithArgs("server-1", "tenant-1", "instance-1", "", "guard-1", "runtime-1", "foundation", "", sqlmock.AnyArg()).
		WillReturnError(errors.New("stop after node insert contract"))
	mock.ExpectRollback()

	_, err = NewPostgresStore(db).ApplyServerEnrollment(context.Background(), command)
	if err == nil || err.Error() != "stop after node insert contract" {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func serverEnrollmentTestCommand(now time.Time) ServerEnrollment {
	return ServerEnrollment{
		Node: Node{ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", WorkerID: "guard-1", Name: "runtime-1", Role: "foundation", Status: "pending"},
		Event: ServerEvent{
			TenantID: "tenant-1", ServerID: "server-1", Generation: 1, Authority: ServerEventAuthorityControlPlane,
			Source: "pairing-redemption", SourceID: "worker-enrollment", ObservedAt: now,
			Runtime: ServerRuntime{
				ID: "server-1", TenantID: "tenant-1", InstanceID: "instance-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
				WorkerID: "guard-1", NodeID: "server-1", LeaseID: "lease-1", ProviderRef: "centron", Name: "runtime-1",
				LifecycleState: "enrolling", DesiredState: "running", ConnectionState: "pending", HealthState: "unknown",
			},
		},
	}
}
