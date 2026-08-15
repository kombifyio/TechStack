package backupstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/auth"
)

const custodyTenantGUC = "app.tenant_id"

var (
	ErrCustodyNotFound = errors.New("managed backup custody not found")
	ErrInvalidCustody  = errors.New("invalid managed backup custody")
)

// Credentials are the control-plane-only credentials needed by the managed
// backup operations process. This value must never be logged or serialized
// into StackKits intent, artifacts, or Inventory.
type Credentials struct {
	TenantID           string
	StackID            string
	Bucket             string
	Endpoint           string
	TokenID            string
	AccessKeyID        string
	SecretAccessKey    string
	RepositoryPassword string
}

// CustodyEvidence contains opaque-reference source material. Durable custody
// establishes binding and target evidence only. AttestationEvidence remains
// empty until a separate real target read/write/delete verification succeeds.
// Callers may hash these values but must never project the bytes themselves.
type CustodyEvidence struct {
	BindingEvidence     []byte
	TargetEvidence      []byte
	AttestationEvidence []byte
	ObservedAt          time.Time
}

// PostgresCustodyStore durably holds per-stack credentials. An explicit
// encryptor is required: unlike optional UI-secret helpers, this repository
// never permits plaintext fallback.
type PostgresCustodyStore struct {
	db        *sql.DB
	encryptor *auth.SecretEncryptor
}

func NewPostgresCustodyStore(db *sql.DB, encryptor *auth.SecretEncryptor) (*PostgresCustodyStore, error) {
	if db == nil {
		return nil, fmt.Errorf("managed backup custody: database not configured")
	}
	if encryptor == nil {
		return nil, auth.ErrNoEncryptionKey
	}
	return &PostgresCustodyStore{db: db, encryptor: encryptor}, nil
}

// Upsert encrypts both secrets, persists them under tenant RLS, and verifies
// the row returned by PostgreSQL before producing custody evidence.
func (s *PostgresCustodyStore) Upsert(ctx context.Context, credentials Credentials) (CustodyEvidence, error) {
	credentials = normalizeCredentials(credentials)
	if err := validateCredentials(credentials); err != nil {
		return CustodyEvidence{}, err
	}
	if auth.IsEncrypted(credentials.SecretAccessKey) || auth.IsEncrypted(credentials.RepositoryPassword) {
		return CustodyEvidence{}, fmt.Errorf("%w: repository accepts plaintext inputs only", ErrInvalidCustody)
	}
	secretEncrypted, err := s.encryptor.Encrypt(credentials.SecretAccessKey)
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("encrypt managed backup access secret: %w", err)
	}
	passwordEncrypted, err := s.encryptor.Encrypt(credentials.RepositoryPassword)
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("encrypt managed backup repository password: %w", err)
	}

	var stored custodyRow
	err = s.withTenant(ctx, credentials.TenantID, func(tx *sql.Tx) error {
		return scanCustodyRow(tx.QueryRowContext(ctx, `
			INSERT INTO stack_backup_stores (
				tenant_id, stack_id, bucket, endpoint, token_id, access_key_id,
				secret_access_key_enc, kopia_repo_password_enc
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, stack_id) DO UPDATE SET
				bucket = EXCLUDED.bucket,
				endpoint = EXCLUDED.endpoint,
				token_id = EXCLUDED.token_id,
				access_key_id = EXCLUDED.access_key_id,
				secret_access_key_enc = EXCLUDED.secret_access_key_enc,
				kopia_repo_password_enc = EXCLUDED.kopia_repo_password_enc,
				custody_attestation_evidence = NULL,
				attested_at = NULL,
				updated_at = now()
			RETURNING tenant_id, stack_id, bucket, endpoint, token_id, access_key_id,
				secret_access_key_enc, kopia_repo_password_enc, updated_at,
				custody_attestation_evidence, attested_at
		`, credentials.TenantID, credentials.StackID, credentials.Bucket, credentials.Endpoint,
			credentials.TokenID, credentials.AccessKeyID, secretEncrypted, passwordEncrypted), &stored)
	})
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("persist managed backup custody: %w", err)
	}
	verified, err := s.decryptRow(stored)
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("verify persisted managed backup custody: %w", err)
	}
	if verified != credentials {
		return CustodyEvidence{}, errors.New("persisted managed backup custody does not match requested binding")
	}
	return evidenceFor(stored), nil
}

func (s *PostgresCustodyStore) Get(ctx context.Context, tenantID, stackID string) (Credentials, error) {
	stored, err := s.loadRow(ctx, tenantID, stackID)
	if err != nil {
		return Credentials{}, err
	}
	return s.decryptRow(stored)
}

// Evidence returns opaque-reference material only after the stored ciphertext
// has been successfully decrypted and structurally verified.
func (s *PostgresCustodyStore) Evidence(ctx context.Context, tenantID, stackID string) (CustodyEvidence, error) {
	stored, err := s.loadRow(ctx, tenantID, stackID)
	if err != nil {
		return CustodyEvidence{}, err
	}
	if _, err := s.decryptRow(stored); err != nil {
		return CustodyEvidence{}, fmt.Errorf("verify managed backup custody: %w", err)
	}
	return evidenceFor(stored), nil
}

func (s *PostgresCustodyStore) loadRow(ctx context.Context, tenantID, stackID string) (custodyRow, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return custodyRow{}, fmt.Errorf("%w: tenant id and stack id are required", ErrInvalidCustody)
	}
	var stored custodyRow
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := scanCustodyRow(tx.QueryRowContext(ctx, `
			SELECT tenant_id, stack_id, bucket, endpoint, token_id, access_key_id,
				secret_access_key_enc, kopia_repo_password_enc, updated_at,
				custody_attestation_evidence, attested_at
			FROM stack_backup_stores
			WHERE tenant_id = $1 AND stack_id = $2
		`, tenantID, stackID), &stored)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCustodyNotFound
		}
		return err
	})
	if err != nil {
		return custodyRow{}, fmt.Errorf("load managed backup custody: %w", err)
	}
	return stored, nil
}

// RecordAttestation binds one real target-verification result to the current
// custody generation. It refuses stale or mismatched binding/target evidence.
func (s *PostgresCustodyStore) RecordAttestation(ctx context.Context, tenantID, stackID string, verified CustodyEvidence) (CustodyEvidence, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" || !validEvidenceDigest(verified.AttestationEvidence) || verified.ObservedAt.IsZero() {
		return CustodyEvidence{}, fmt.Errorf("%w: complete verified evidence is required", ErrInvalidCustody)
	}
	var stored custodyRow
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := scanCustodyRow(tx.QueryRowContext(ctx, `
			SELECT tenant_id, stack_id, bucket, endpoint, token_id, access_key_id,
				secret_access_key_enc, kopia_repo_password_enc, updated_at,
				custody_attestation_evidence, attested_at
			FROM stack_backup_stores
			WHERE tenant_id = $1 AND stack_id = $2
			FOR UPDATE
		`, tenantID, stackID), &stored); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCustodyNotFound
			}
			return err
		}
		base := evidenceFor(stored)
		if !bytes.Equal(verified.BindingEvidence, base.BindingEvidence) || !bytes.Equal(verified.TargetEvidence, base.TargetEvidence) ||
			verified.ObservedAt.Before(stored.UpdatedAt) || verified.ObservedAt.After(time.Now().UTC().Add(5*time.Minute)) {
			return errors.New("managed backup attestation does not match the current custody generation")
		}
		return scanCustodyRow(tx.QueryRowContext(ctx, `
			UPDATE stack_backup_stores
			SET custody_attestation_evidence = $3, attested_at = $4
			WHERE tenant_id = $1 AND stack_id = $2
			RETURNING tenant_id, stack_id, bucket, endpoint, token_id, access_key_id,
				secret_access_key_enc, kopia_repo_password_enc, updated_at,
				custody_attestation_evidence, attested_at
		`, tenantID, stackID, string(verified.AttestationEvidence), verified.ObservedAt.UTC()), &stored)
	})
	if err != nil {
		return CustodyEvidence{}, fmt.Errorf("record managed backup custody attestation: %w", err)
	}
	if _, err := s.decryptRow(stored); err != nil {
		return CustodyEvidence{}, fmt.Errorf("verify attested managed backup custody: %w", err)
	}
	return evidenceFor(stored), nil
}

type custodyRow struct {
	TenantID, StackID, Bucket, Endpoint, TokenID, AccessKeyID string
	SecretAccessKeyEncrypted, RepositoryPasswordEncrypted     string
	UpdatedAt                                                 time.Time
	AttestationEvidence                                       sql.NullString
	AttestedAt                                                sql.NullTime
}

type rowScanner interface{ Scan(...any) error }

func scanCustodyRow(row rowScanner, destination *custodyRow) error {
	return row.Scan(&destination.TenantID, &destination.StackID, &destination.Bucket,
		&destination.Endpoint, &destination.TokenID, &destination.AccessKeyID,
		&destination.SecretAccessKeyEncrypted, &destination.RepositoryPasswordEncrypted,
		&destination.UpdatedAt, &destination.AttestationEvidence, &destination.AttestedAt)
}

func (s *PostgresCustodyStore) decryptRow(row custodyRow) (Credentials, error) {
	if !auth.IsEncrypted(row.SecretAccessKeyEncrypted) || !auth.IsEncrypted(row.RepositoryPasswordEncrypted) {
		return Credentials{}, errors.New("managed backup custody contains plaintext or unsupported ciphertext")
	}
	secret, err := s.encryptor.Decrypt(row.SecretAccessKeyEncrypted)
	if err != nil {
		return Credentials{}, err
	}
	password, err := s.encryptor.Decrypt(row.RepositoryPasswordEncrypted)
	if err != nil {
		return Credentials{}, err
	}
	credentials := Credentials{TenantID: row.TenantID, StackID: row.StackID, Bucket: row.Bucket,
		Endpoint: row.Endpoint, TokenID: row.TokenID, AccessKeyID: row.AccessKeyID,
		SecretAccessKey: secret, RepositoryPassword: password}
	if err := validateCredentials(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func (s *PostgresCustodyStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", custodyTenantGUC, tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func normalizeCredentials(value Credentials) Credentials {
	value.TenantID = strings.TrimSpace(value.TenantID)
	value.StackID = strings.TrimSpace(value.StackID)
	value.Bucket = strings.TrimSpace(value.Bucket)
	value.Endpoint = strings.TrimSpace(value.Endpoint)
	value.TokenID = strings.TrimSpace(value.TokenID)
	value.AccessKeyID = strings.TrimSpace(value.AccessKeyID)
	return value
}

func validateCredentials(value Credentials) error {
	if value.TenantID == "" || value.StackID == "" || value.Bucket == "" || value.TokenID == "" ||
		value.AccessKeyID == "" || value.SecretAccessKey == "" || value.RepositoryPassword == "" {
		return fmt.Errorf("%w: all identity and credential fields are required", ErrInvalidCustody)
	}
	parsed, err := url.Parse(value.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: endpoint must be an authority-only HTTPS URL", ErrInvalidCustody)
	}
	return nil
}

func evidenceFor(row custodyRow) CustodyEvidence {
	digest := func(parts ...string) []byte {
		sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		return []byte(fmt.Sprintf("sha256:%x", sum))
	}
	evidence := CustodyEvidence{
		BindingEvidence: digest(row.TenantID, row.StackID, row.UpdatedAt.UTC().Format(time.RFC3339Nano)),
		TargetEvidence:  digest(row.Bucket, row.Endpoint, row.TokenID, row.AccessKeyID, row.SecretAccessKeyEncrypted, row.RepositoryPasswordEncrypted),
		ObservedAt:      row.UpdatedAt.UTC(),
	}
	if row.AttestationEvidence.Valid && row.AttestedAt.Valid && validEvidenceDigest([]byte(row.AttestationEvidence.String)) {
		evidence.AttestationEvidence = []byte(row.AttestationEvidence.String)
		evidence.ObservedAt = row.AttestedAt.Time.UTC()
	}
	return evidence
}

func validEvidenceDigest(value []byte) bool {
	text := string(value)
	if len(text) != len("sha256:")+64 || !strings.HasPrefix(text, "sha256:") {
		return false
	}
	for _, character := range strings.TrimPrefix(text, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
