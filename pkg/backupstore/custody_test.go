package backupstore

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/auth"
)

func TestCustodyRequiresEncryptionKey(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewPostgresCustodyStore(db, nil); !errors.Is(err, auth.ErrNoEncryptionKey) {
		t.Fatalf("expected fail-closed encryption error, got %v", err)
	}
}

func TestCustodyUpsertEncryptsAndVerifiesReturnedRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	encryptor := testEncryptor(t)
	returnedSecret, _ := encryptor.Encrypt("s3-secret")
	returnedPassword, _ := encryptor.Encrypt("repo-password")
	observedAt := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(custodyTenantGUC, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO stack_backup_stores").
		WithArgs("tenant-a", "stack-a", "skbk-stack-a", "https://acct.r2.cloudflarestorage.com",
			"token-a", "access-a", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(custodyRows().AddRow("tenant-a", "stack-a", "skbk-stack-a", "https://acct.r2.cloudflarestorage.com", "token-a", "access-a", returnedSecret, returnedPassword, observedAt, nil, nil))
	mock.ExpectCommit()

	store, err := NewPostgresCustodyStore(db, encryptor)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.Upsert(context.Background(), testCredentials())
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ObservedAt != observedAt || len(evidence.BindingEvidence) == 0 || len(evidence.TargetEvidence) == 0 {
		t.Fatalf("incomplete evidence: %#v", evidence)
	}
	if len(evidence.AttestationEvidence) != 0 {
		t.Fatal("durable custody must not masquerade as operational target attestation")
	}
	for _, evidencePart := range [][]byte{evidence.BindingEvidence, evidence.TargetEvidence, evidence.AttestationEvidence} {
		for _, forbidden := range []string{"bucket", "token", "secret", "account"} {
			if strings.Contains(strings.ToLower(string(evidencePart)), forbidden) {
				t.Fatalf("evidence leaked %q", forbidden)
			}
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCustodyGetDecryptsTenantScopedRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	encryptor := testEncryptor(t)
	secret, _ := encryptor.Encrypt("s3-secret")
	password, _ := encryptor.Encrypt("repo-password")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(custodyTenantGUC, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT tenant_id, stack_id, bucket").WithArgs("tenant-a", "stack-a").
		WillReturnRows(custodyRows().AddRow("tenant-a", "stack-a", "skbk-stack-a", "https://acct.r2.cloudflarestorage.com", "token-a", "access-a", secret, password, time.Now(), nil, nil))
	mock.ExpectCommit()
	store, _ := NewPostgresCustodyStore(db, encryptor)
	got, err := store.Get(context.Background(), "tenant-a", "stack-a")
	if err != nil {
		t.Fatal(err)
	}
	if got != testCredentials() {
		t.Fatalf("credentials mismatch: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCustodyRejectsPlaintextReturnedByDatabase(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).WithArgs(custodyTenantGUC, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT tenant_id, stack_id, bucket").WithArgs("tenant-a", "stack-a").
		WillReturnRows(custodyRows().AddRow("tenant-a", "stack-a", "skbk-stack-a", "https://acct.r2.cloudflarestorage.com", "token-a", "access-a", "plaintext", "plaintext", time.Now(), nil, nil))
	mock.ExpectCommit()
	store, _ := NewPostgresCustodyStore(db, testEncryptor(t))
	if _, err := store.Get(context.Background(), "tenant-a", "stack-a"); err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("expected plaintext rejection, got %v", err)
	}
}

func TestCustodyRejectsEndpointPathsAndEncryptedInputs(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresCustodyStore(db, testEncryptor(t))
	withPath := testCredentials()
	withPath.Endpoint += "/bucket"
	if _, err := store.Upsert(context.Background(), withPath); !errors.Is(err, ErrInvalidCustody) {
		t.Fatalf("endpoint path was accepted: %v", err)
	}
	encryptedInput := testCredentials()
	encryptedInput.SecretAccessKey = auth.EncryptedPrefix + "not-ciphertext"
	if _, err := store.Upsert(context.Background(), encryptedInput); !errors.Is(err, ErrInvalidCustody) {
		t.Fatalf("pre-encrypted input was accepted: %v", err)
	}
}

func TestCustodyRecordsAttestationOnlyForCurrentGeneration(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	encryptor := testEncryptor(t)
	secret, _ := encryptor.Encrypt("s3-secret")
	password, _ := encryptor.Encrypt("repo-password")
	updatedAt := time.Now().UTC().Add(-time.Minute)
	attestedAt := updatedAt.Add(30 * time.Second)
	row := custodyRow{TenantID: "tenant-a", StackID: "stack-a", Bucket: "skbk-stack-a", Endpoint: "https://acct.r2.cloudflarestorage.com",
		TokenID: "token-a", AccessKeyID: "access-a", SecretAccessKeyEncrypted: secret, RepositoryPasswordEncrypted: password, UpdatedAt: updatedAt}
	verified := evidenceFor(row)
	verified.AttestationEvidence = []byte("sha256:" + strings.Repeat("a", 64))
	verified.ObservedAt = attestedAt

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).WithArgs(custodyTenantGUC, "tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT tenant_id, stack_id, bucket.*FOR UPDATE").WithArgs("tenant-a", "stack-a").
		WillReturnRows(custodyRows().AddRow(row.TenantID, row.StackID, row.Bucket, row.Endpoint, row.TokenID, row.AccessKeyID, secret, password, updatedAt, nil, nil))
	mock.ExpectQuery("UPDATE stack_backup_stores").WithArgs("tenant-a", "stack-a", string(verified.AttestationEvidence), attestedAt).
		WillReturnRows(custodyRows().AddRow(row.TenantID, row.StackID, row.Bucket, row.Endpoint, row.TokenID, row.AccessKeyID, secret, password, updatedAt, string(verified.AttestationEvidence), attestedAt))
	mock.ExpectCommit()
	store, _ := NewPostgresCustodyStore(db, encryptor)
	recorded, err := store.RecordAttestation(context.Background(), "tenant-a", "stack-a", verified)
	if err != nil {
		t.Fatal(err)
	}
	if string(recorded.AttestationEvidence) != string(verified.AttestationEvidence) || !recorded.ObservedAt.Equal(attestedAt) {
		t.Fatalf("recorded evidence = %#v", recorded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func custodyRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"tenant_id", "stack_id", "bucket", "endpoint", "token_id", "access_key_id", "secret_access_key_enc", "kopia_repo_password_enc", "updated_at", "custody_attestation_evidence", "attested_at"})
}

func testEncryptor(t *testing.T) *auth.SecretEncryptor {
	t.Helper()
	encryptor, err := auth.NewSecretEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return encryptor
}

func testCredentials() Credentials {
	return Credentials{TenantID: "tenant-a", StackID: "stack-a", Bucket: "skbk-stack-a",
		Endpoint: "https://acct.r2.cloudflarestorage.com", TokenID: "token-a", AccessKeyID: "access-a",
		SecretAccessKey: "s3-secret", RepositoryPassword: "repo-password"}
}
