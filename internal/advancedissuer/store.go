package advancedissuer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/db"
)

// EnvIssuerKey overrides the stored installation key with an unpadded
// standard-Base64 32-byte Ed25519 seed. It exists for tests and local
// development only and is refused unless TECHSTACK_ENV names one of them.
const EnvIssuerKey = "TECHSTACK_ADVANCED_ISSUER_KEY"

// envOverrideIssuerID names the issuer of an env-provided test key.
const envOverrideIssuerID = "techstack.env-override"

// ErrTrustBindingMissing reports that a deployment's host has not imported
// this installation's trust bundle yet.
var ErrTrustBindingMissing = errors.New("advanced trust binding is not recorded for this deployment")

// PostgresStore keeps the installation key encrypted at rest with the
// TECHSTACK_ENCRYPTION_KEY secret encryptor and records issuances and trust
// bindings under tenant RLS.
type PostgresStore struct {
	db        *db.DB
	encryptor *auth.SecretEncryptor
}

func NewPostgresStore(database *db.DB, encryptor *auth.SecretEncryptor) (*PostgresStore, error) {
	if database == nil || database.DB == nil {
		return nil, errors.New("advanced issuer store requires a database")
	}
	if encryptor == nil {
		return nil, auth.ErrNoEncryptionKey
	}
	return &PostgresStore{db: database, encryptor: encryptor}, nil
}

// LoadOrCreateKey returns the installation issuer, creating it exactly once.
// Concurrent first starts converge on the single row that won the insert.
func (store *PostgresStore) LoadOrCreateKey(ctx context.Context) (string, ed25519.PrivateKey, error) {
	if issuerID, key, err := store.loadKey(ctx); err == nil {
		return issuerID, key, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("generate advanced issuer key: %w", err)
	}
	encrypted, err := store.encryptor.Encrypt(base64.RawStdEncoding.EncodeToString(privateKey.Seed()))
	if err != nil {
		return "", nil, fmt.Errorf("encrypt advanced issuer key: %w", err)
	}
	issuerID := "techstack." + uuid.NewString()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO advanced_issuer_keys (singleton, issuer_id, key_id, public_key, private_key_enc)
		VALUES (true, $1, $2, $3, $4)
		ON CONFLICT (singleton) DO NOTHING`,
		issuerID, KeyID(publicKey), base64.RawStdEncoding.EncodeToString(publicKey), encrypted,
	); err != nil {
		return "", nil, fmt.Errorf("store advanced issuer key: %w", err)
	}
	return store.loadKey(ctx)
}

func (store *PostgresStore) loadKey(ctx context.Context) (string, ed25519.PrivateKey, error) {
	var issuerID, keyID, encrypted string
	err := store.db.QueryRowContext(ctx, `
		SELECT issuer_id, key_id, private_key_enc FROM advanced_issuer_keys WHERE singleton`,
	).Scan(&issuerID, &keyID, &encrypted)
	if err != nil {
		return "", nil, err
	}
	seedText, err := store.encryptor.Decrypt(encrypted)
	if err != nil {
		return "", nil, fmt.Errorf("decrypt advanced issuer key: %w", err)
	}
	key, err := keyFromSeed(seedText)
	if err != nil {
		return "", nil, err
	}
	if KeyID(key.Public().(ed25519.PublicKey)) != keyID {
		return "", nil, errors.New("stored advanced issuer key does not match its key id")
	}
	return issuerID, key, nil
}

func (store *PostgresStore) RecordIssuance(ctx context.Context, issuance Issuance) error {
	return store.db.WithTenant(ctx, issuance.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO advanced_capability_issuances (
				tenant_id, capability_id, stack_id, stackkit_stack_id, owner_ref,
				operations, issuer_id, key_id, issued_at, expires_at
			) VALUES ($1, $2, $3, $4, $5, $6::text[], $7, $8, $9, $10)`,
			issuance.TenantID, issuance.CapabilityID, issuance.DeploymentID, issuance.StackID, issuance.OwnerRef,
			"{"+strings.Join(issuance.Operations, ",")+"}", issuance.IssuerID, issuance.KeyID, issuance.IssuedAt, issuance.ExpiresAt,
		)
		return err
	})
}

func (store *PostgresStore) RecordTrustBinding(ctx context.Context, binding TrustBinding) error {
	return store.db.WithTenant(ctx, binding.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO advanced_trust_bindings (
				tenant_id, stack_id, stackkit_stack_id, owner_ref, bundle_sha256,
				issuer_id, key_id, imported_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, stack_id) DO UPDATE SET
				stackkit_stack_id = EXCLUDED.stackkit_stack_id,
				owner_ref = EXCLUDED.owner_ref,
				bundle_sha256 = EXCLUDED.bundle_sha256,
				issuer_id = EXCLUDED.issuer_id,
				key_id = EXCLUDED.key_id,
				imported_at = EXCLUDED.imported_at,
				updated_at = now()`,
			binding.TenantID, binding.DeploymentID, binding.StackID, binding.OwnerRef, binding.BundleSHA256,
			binding.IssuerID, binding.KeyID, binding.ImportedAt,
		)
		return err
	})
}

func (store *PostgresStore) LookupTrustBinding(ctx context.Context, tenantID, deploymentID string) (TrustBinding, error) {
	binding := TrustBinding{TenantID: tenantID, DeploymentID: deploymentID}
	err := store.db.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT stackkit_stack_id, owner_ref, bundle_sha256, issuer_id, key_id, imported_at
			FROM advanced_trust_bindings WHERE tenant_id = $1 AND stack_id = $2`,
			tenantID, deploymentID,
		).Scan(&binding.StackID, &binding.OwnerRef, &binding.BundleSHA256, &binding.IssuerID, &binding.KeyID, &binding.ImportedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return TrustBinding{}, ErrTrustBindingMissing
	}
	return binding, err
}

// Open composes the installation issuer from the environment and the
// control-plane database. The env override is honored only in development
// and test runtimes; production keys live only in encrypted storage.
func Open(ctx context.Context, database *db.DB, encryptor *auth.SecretEncryptor) (*Issuer, error) {
	store, err := NewPostgresStore(database, encryptor)
	if err != nil {
		return nil, err
	}
	if override := strings.TrimSpace(os.Getenv(EnvIssuerKey)); override != "" {
		if !envOverrideAllowed(os.Getenv("TECHSTACK_ENV")) {
			return nil, fmt.Errorf("%s is for tests and local development only", EnvIssuerKey)
		}
		key, seedErr := keyFromSeed(override)
		if seedErr != nil {
			return nil, fmt.Errorf("%s: %w", EnvIssuerKey, seedErr)
		}
		return New(Config{IssuerID: envOverrideIssuerID, PrivateKey: key, Recorder: store})
	}
	issuerID, key, err := store.LoadOrCreateKey(ctx)
	if err != nil {
		return nil, err
	}
	return New(Config{IssuerID: issuerID, PrivateKey: key, Recorder: store, Now: time.Now})
}

func envOverrideAllowed(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "development", "dev", "local", "test":
		return true
	default:
		return false
	}
}

func keyFromSeed(text string) (ed25519.PrivateKey, error) {
	seed, err := base64.RawStdEncoding.Strict().DecodeString(strings.TrimSpace(text))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("advanced issuer key must be an unpadded standard-Base64 32-byte Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
