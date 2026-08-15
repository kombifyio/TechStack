package clientpairing

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresConsumeUsesOneConditionalUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	consumedAt := testNow.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(tenantGUC, testTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE client_pairing_codes[\\s\\S]+consumed_at IS NULL[\\s\\S]+expires_at > \\$5[\\s\\S]+RETURNING").
		WithArgs(testTenant, hashCode("raw"), testInstance, testFingerprint, consumedAt).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "instance_id", "code_hash", "tls_fingerprint_sha256",
			"issued_by_subject_id", "issued_at", "expires_at", "consumed_at",
		}).AddRow("client_pair_1", testTenant, testInstance, hashCode("raw"), testFingerprint, "owner-1", testNow, testNow.Add(DefaultLifetime), consumedAt))
	mock.ExpectCommit()

	code, err := store.Consume(context.Background(), ConsumeRequest{
		TenantID: testTenant, CodeHash: hashCode("raw"), InstanceID: testInstance,
		TLSFingerprintSHA256: testFingerprint, Now: consumedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if code.ConsumedAt == nil || !code.ConsumedAt.Equal(consumedAt) {
		t.Fatalf("consumed code = %#v", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresConsumeDiagnosesReplayAfterAtomicUpdateLoses(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	codeHash := hashCode("raw")
	consumedAt := testNow.Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(tenantGUC, testTenant).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE client_pairing_codes").
		WithArgs(testTenant, codeHash, testInstance, testFingerprint, consumedAt).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id, tenant_id, instance_id, code_hash").
		WithArgs(testTenant, codeHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "instance_id", "code_hash", "tls_fingerprint_sha256",
			"issued_by_subject_id", "issued_at", "expires_at", "consumed_at",
		}).AddRow("client_pair_1", testTenant, testInstance, codeHash, testFingerprint, "owner-1", testNow, testNow.Add(DefaultLifetime), consumedAt))
	mock.ExpectRollback()

	_, err = store.Consume(context.Background(), ConsumeRequest{
		TenantID: testTenant, CodeHash: codeHash, InstanceID: testInstance,
		TLSFingerprintSHA256: testFingerprint, Now: consumedAt,
	})
	if !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
