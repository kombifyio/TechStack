package homeassistant

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/kombifyio/techstack/pkg/auth"
)

// Journal keeps native dispatch intent separate from its response. A process
// restart can reconcile a claimed request, but can never blindly submit it
// again. Recovery passwords are separately encrypted, never workflow payloads.
type Journal struct {
	DB        *sql.DB
	Encryptor *auth.SecretEncryptor
	TenantID  string
}

func (j *Journal) valid() error {
	if j == nil || j.DB == nil || j.Encryptor == nil || j.TenantID == "" {
		return errors.New("Home Assistant durable encrypted operation custody unavailable")
	}
	return nil
}
func (j *Journal) Claim(ctx context.Context, key string) (bool, map[string]any, error) {
	if err := j.valid(); err != nil {
		return false, nil, err
	}
	if key == "" {
		return false, nil, errors.New("operation key required")
	}
	result, err := j.DB.ExecContext(ctx, `INSERT INTO home_assistant_operations (tenant_id,operation_key) VALUES ($1,$2) ON CONFLICT DO NOTHING`, j.TenantID, key)
	if err != nil {
		return false, nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, nil, err
	}
	var raw []byte
	if err := j.DB.QueryRowContext(ctx, `SELECT receipt FROM home_assistant_operations WHERE tenant_id=$1 AND operation_key=$2`, j.TenantID, key).Scan(&raw); err != nil {
		return false, nil, err
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return false, nil, err
	}
	return n == 1, receipt, nil
}
func (j *Journal) Save(ctx context.Context, key string, receipt map[string]any) error {
	if err := j.valid(); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	result, err := j.DB.ExecContext(ctx, `UPDATE home_assistant_operations SET receipt=$3::jsonb,updated_at=now() WHERE tenant_id=$1 AND operation_key=$2`, j.TenantID, key, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("operation claim missing")
	}
	return nil
}
func (j *Journal) RecoveryKey(ctx context.Context, key string) (string, error) {
	if err := j.valid(); err != nil {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	encrypted, err := j.Encryptor.Encrypt(base64.RawURLEncoding.EncodeToString(secret))
	if err != nil {
		return "", err
	}
	_, err = j.DB.ExecContext(ctx, `INSERT INTO home_assistant_recovery_keys (tenant_id,operation_key,password_enc) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, j.TenantID, key, encrypted)
	if err != nil {
		return "", err
	}
	return j.ReadRecoveryKey(ctx, key)
}

func (j *Journal) ReadRecoveryKey(ctx context.Context, key string) (string, error) {
	if err := j.valid(); err != nil {
		return "", err
	}
	var stored string
	err := j.DB.QueryRowContext(ctx, `SELECT password_enc FROM home_assistant_recovery_keys WHERE tenant_id=$1 AND operation_key=$2`, j.TenantID, key).Scan(&stored)
	if err != nil {
		return "", err
	}
	return j.Encryptor.Decrypt(stored)
}
