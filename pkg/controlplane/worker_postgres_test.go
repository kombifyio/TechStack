package controlplane

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreWorkerAndPairingTokenUseTenantScope(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 5, 28, 20, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO pairing_tokens")).
		WithArgs("pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "active", expiresAt, nil, sqlmock.AnyArg()).
		WillReturnRows(pairingTokenRows().AddRow(
			"pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "active", expiresAt, nil, `{"scope":"worker"}`, now, now,
		))
	mock.ExpectCommit()

	token, err := store.UpsertPairingToken(context.Background(), PairingToken{
		ID:             "pair-1",
		TenantID:       "tenant-1",
		InstanceID:     "instance-1",
		StackID:        "stack-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "worker setup",
		TokenHash:      "hash-1",
		Status:         "active",
		ExpiresAt:      &expiresAt,
		Metadata:       map[string]any{"scope": "worker"},
	})
	if err != nil {
		t.Fatalf("UpsertPairingToken: %v", err)
	}
	if token.ID != "pair-1" || token.Metadata["scope"] != "worker" {
		t.Fatalf("unexpected pairing token: %#v", token)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, tenant_id")+".*"+regexp.QuoteMeta("FROM pairing_tokens")+".*"+regexp.QuoteMeta("WHERE tenant_id = $1 AND token_hash = $2")).
		WithArgs("tenant-1", "hash-1").
		WillReturnRows(pairingTokenRows().AddRow(
			"pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "active", expiresAt, nil, `{"scope":"worker"}`, now, now,
		))
	mock.ExpectCommit()

	resolvedToken, err := store.GetPairingTokenByHash(context.Background(), "tenant-1", "hash-1")
	if err != nil {
		t.Fatalf("GetPairingTokenByHash: %v", err)
	}
	if resolvedToken.ID != "pair-1" || resolvedToken.TenantID != "tenant-1" {
		t.Fatalf("unexpected resolved pairing token: %#v", resolvedToken)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO workers")).
		WithArgs("worker-1", "tenant-1", "instance-1", "stack-1", "agent-a", "10.0.0.10", "linux", "amd64", "hash-1", "pending", false, nil, sqlmock.AnyArg(), 4, 8192, 100, "", false, true, "25.0", "agent", "self-hosted", sqlmock.AnyArg(), "auth0|user-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(workerRows().AddRow(
			"worker-1", "tenant-1", "instance-1", "stack-1", "agent-a", "10.0.0.10", "linux", "amd64", "hash-1", "pending", false, nil, now, 4, 8192, 100, "", false, true, "25.0", "agent", "self-hosted", `{"zone":"lab"}`, "auth0|user-1", `{"shell":true}`, `{"cpu":"4"}`, now, now,
		))
	mock.ExpectCommit()

	worker, err := store.UpsertWorkerHeartbeat(context.Background(), Worker{
		ID:             "worker-1",
		TenantID:       "tenant-1",
		InstanceID:     "instance-1",
		StackID:        "stack-1",
		Hostname:       "agent-a",
		IP:             "10.0.0.10",
		OS:             "linux",
		Arch:           "amd64",
		TokenHash:      "hash-1",
		Status:         "pending",
		CPUCores:       4,
		RAMMB:          8192,
		DiskGB:         100,
		HasHWTranscode: true,
		DockerVersion:  "25.0",
		Type:           "agent",
		Provider:       "self-hosted",
		Tags:           map[string]any{"zone": "lab"},
		OwnerSubjectID: "auth0|user-1",
		Capabilities:   map[string]any{"shell": true},
		Resources:      map[string]any{"cpu": "4"},
	})
	if err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if worker.ID != "worker-1" || worker.StackID != "stack-1" || worker.Tags["zone"] != "lab" {
		t.Fatalf("unexpected worker: %#v", worker)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE workers")).
		WithArgs("tenant-1", "worker-1", "auth0|user-1", now).
		WillReturnRows(workerRows().AddRow(
			"worker-1", "tenant-1", "instance-1", "stack-1", "agent-a", "10.0.0.10", "linux", "amd64", "hash-1", "approved", true, now, now, 4, 8192, 100, "", false, true, "25.0", "agent", "self-hosted", `{"zone":"lab"}`, "auth0|user-1", `{"shell":true}`, `{"cpu":"4"}`, now, now,
		))
	mock.ExpectCommit()

	worker, err = store.ApproveWorker(context.Background(), "tenant-1", "worker-1", "auth0|user-1", now)
	if err != nil {
		t.Fatalf("ApproveWorker: %v", err)
	}
	if !worker.Approved || worker.Status != "approved" {
		t.Fatalf("worker was not approved: %#v", worker)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPostgresStoreClaimPairingTokenUsesAtomicEligibilityUpdate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	claimedAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	expiresAt := claimedAt.Add(time.Hour)
	claimQuery := regexp.QuoteMeta("UPDATE pairing_tokens") + ".*" +
		regexp.QuoteMeta("SET status = 'used', used_at = $3, updated_at = $3") + ".*" +
		regexp.QuoteMeta("AND status = 'active' AND used_at IS NULL") + ".*" +
		regexp.QuoteMeta("AND (expires_at IS NULL OR expires_at > $3)") + ".*" +
		regexp.QuoteMeta("RETURNING")

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(claimQuery).
		WithArgs("tenant-1", "hash-1", claimedAt).
		WillReturnRows(pairingTokenRows().AddRow(
			"pair-1", "tenant-1", "instance-1", "stack-1", "auth0|user-1", "worker setup", "hash-1", "used", expiresAt, claimedAt, `{}`, claimedAt, claimedAt,
		))
	mock.ExpectCommit()

	claimed, err := store.ClaimPairingToken(context.Background(), "tenant-1", "hash-1", claimedAt)
	if err != nil || claimed.Status != "used" || claimed.UsedAt == nil || !claimed.UsedAt.Equal(claimedAt) {
		t.Fatalf("ClaimPairingToken = %#v, %v", claimed, err)
	}

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-1")
	mock.ExpectQuery(claimQuery).
		WithArgs("tenant-1", "hash-1", claimedAt).
		WillReturnRows(pairingTokenRows())
	mock.ExpectRollback()
	if _, err := store.ClaimPairingToken(context.Background(), "tenant-1", "hash-1", claimedAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ClaimPairingToken error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func workerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "hostname", "ip", "os", "arch", "token_hash", "status",
		"approved", "approved_at", "last_seen_at", "cpu_cores", "ram_mb", "disk_gb", "gpu", "has_nvme",
		"has_hw_transcode", "docker_version", "type", "provider", "tags_json", "owner_subject_id",
		"capabilities_json", "resources_json", "created_at", "updated_at",
	})
}

func pairingTokenRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "owner_subject_id", "name", "token_hash", "status", "expires_at", "used_at", "metadata_json", "created_at", "updated_at",
	})
}
