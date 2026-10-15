package controlplane

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AuthRuntimeStore extends the existing auth authority with its remaining
// instance-scoped configuration and authenticated profile/session projections.
// It uses the existing auth_config, auth_sessions and membership schema.
type AuthRuntimeStore interface {
	EnsureAuthInstance(context.Context, string, string) error
	GetAuthConfig(context.Context, string, string) (*AuthConfig, error)
	UpsertAuthConfig(context.Context, AuthConfig) (*AuthConfig, error)
	SaveCloudLinkState(context.Context, CloudLinkState) error
	ConsumeCloudLinkState(context.Context, string, string, string, time.Time) (*CloudLinkState, error)
	GetCloudProfile(context.Context, string, string, string) (*CloudProfile, error)
	SetCloudLink(context.Context, string, string, string, *CloudIdentity) error
}

// Preserve the existing instance binding; a foreign tenant cannot adopt a
// process identity merely by supplying its id during auth configuration.
func (s *PostgresStore) EnsureAuthInstance(ctx context.Context, tenantID, instanceID string) error {
	if s == nil || s.db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(instanceID) == "" {
		return fmt.Errorf("controlplane: auth database, tenant and instance required")
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO techstack_instances (id,tenant_id,instance_key) VALUES ($1,$2,$1)
			ON CONFLICT (id) DO UPDATE SET last_seen_at=now()
			WHERE techstack_instances.tenant_id=EXCLUDED.tenant_id
				AND techstack_instances.instance_key=EXCLUDED.instance_key
		`, instanceID, tenantID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err == nil && changed != 1 {
			return ErrConflict
		}
		return err
	})
}

type CloudLinkState struct {
	TenantID, InstanceID, SubjectID, StateHash, VerifierCiphertext string
	ExpiresAt                                                      time.Time
}

type CloudIdentity struct {
	SubjectID     string `json:"subject_id"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	EmailVerified bool   `json:"email_verified"`
	LinkedAt      string `json:"linked_at"`
}

type CloudProfile struct {
	SubjectID, Email, DisplayName string
	ProviderKey                   string
	EmailVerified                 bool
	Link                          *CloudIdentity
}

func lowercaseSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (s *PostgresStore) GetAuthConfig(ctx context.Context, tenantID, instanceID string) (*AuthConfig, error) {
	if s == nil || s.db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(instanceID) == "" {
		return nil, fmt.Errorf("controlplane: auth database, tenant and instance required")
	}
	var out *AuthConfig
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var err error
		out, err = scanAuthConfig(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, mode, config_json::text, created_at, updated_at
			FROM auth_config WHERE tenant_id=$1 AND instance_id=$2
		`, tenantID, instanceID))
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

func (s *PostgresStore) SaveCloudLinkState(ctx context.Context, state CloudLinkState) error {
	if s == nil || s.db == nil || strings.TrimSpace(state.TenantID) == "" || strings.TrimSpace(state.InstanceID) == "" ||
		strings.TrimSpace(state.SubjectID) == "" || !lowercaseSHA256(state.StateHash) || strings.TrimSpace(state.VerifierCiphertext) == "" || state.ExpiresAt.IsZero() {
		return fmt.Errorf("controlplane: complete cloud-link state binding required")
	}
	metadata, err := json.Marshal(map[string]string{"instance_id": state.InstanceID, "purpose": "owner-link", "code_verifier": state.VerifierCiphertext})
	if err != nil {
		return err
	}
	return s.withTenant(ctx, state.TenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO auth_sessions (id, tenant_id, subject_id, provider_key, session_hash, expires_at, metadata_json)
			VALUES ($1,$2,$3,'cloud-link',$4,$5,$6::jsonb)
		`, "cloud-link:"+state.InstanceID+":"+state.StateHash, state.TenantID, state.SubjectID, state.StateHash, state.ExpiresAt.UTC(), metadata)
		return err
	})
}

// The guarded update consumes the nonce exactly once, including callbacks
// whose subsequent provider exchange fails. Tenant, instance and expiry are
// verified in the same statement that returns the sealed PKCE verifier.
func (s *PostgresStore) ConsumeCloudLinkState(ctx context.Context, tenantID, instanceID, stateHash string, now time.Time) (*CloudLinkState, error) {
	if s == nil || s.db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(instanceID) == "" || !lowercaseSHA256(stateHash) || now.IsZero() {
		return nil, fmt.Errorf("controlplane: complete cloud-link consumption binding required")
	}
	out := &CloudLinkState{TenantID: tenantID, InstanceID: instanceID, StateHash: stateHash}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			UPDATE auth_sessions SET revoked_at=$4, updated_at=$4
			WHERE tenant_id=$1 AND provider_key='cloud-link' AND session_hash=$3
				AND metadata_json->>'instance_id'=$2 AND metadata_json->>'purpose'='owner-link'
				AND revoked_at IS NULL AND expires_at>$4
			RETURNING subject_id, metadata_json->>'code_verifier', expires_at
		`, tenantID, instanceID, stateHash, now.UTC()).Scan(&out.SubjectID, &out.VerifierCiphertext, &out.ExpiresAt)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

func (s *PostgresStore) GetCloudProfile(ctx context.Context, tenantID, instanceID, subjectID string) (*CloudProfile, error) {
	if s == nil || s.db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(instanceID) == "" || strings.TrimSpace(subjectID) == "" {
		return nil, fmt.Errorf("controlplane: auth database, tenant, instance and subject required")
	}
	out := &CloudProfile{SubjectID: subjectID}
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var metadata string
		err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(u.primary_email,''), COALESCE(u.display_name,''), COALESCE(m.provider_key,''), u.metadata_json::text
			FROM techstack_users u JOIN techstack_memberships m ON m.user_id=u.id
			WHERE m.tenant_id=$1 AND m.subject_id=$2 AND m.user_id=$2 AND m.status='active'
		`, tenantID, subjectID).Scan(&out.Email, &out.DisplayName, &out.ProviderKey, &metadata)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var fields struct {
			EmailVerified bool                      `json:"email_verified"`
			Links         map[string]*CloudIdentity `json:"cloud_links"`
		}
		if err := json.Unmarshal([]byte(metadata), &fields); err != nil {
			return err
		}
		out.EmailVerified, out.Link = fields.EmailVerified, fields.Links[tenantID+":"+instanceID]
		return nil
	})
	return out, err
}

func (s *PostgresStore) SetCloudLink(ctx context.Context, tenantID, instanceID, subjectID string, link *CloudIdentity) error {
	if s == nil || s.db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(instanceID) == "" || strings.TrimSpace(subjectID) == "" {
		return fmt.Errorf("controlplane: auth database, tenant, instance and subject required")
	}
	if link != nil && (strings.TrimSpace(link.SubjectID) == "" || strings.TrimSpace(link.Email) == "" || !link.EmailVerified) {
		return fmt.Errorf("controlplane: verified cloud identity required")
	}
	encoded, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE techstack_users u SET metadata_json=jsonb_set(u.metadata_json,'{cloud_links}',
				CASE WHEN $4::jsonb='null'::jsonb THEN COALESCE(u.metadata_json->'cloud_links','{}'::jsonb)-$3::text
				ELSE COALESCE(u.metadata_json->'cloud_links','{}'::jsonb)||jsonb_build_object($3::text,$4::jsonb) END), updated_at=now()
			FROM techstack_memberships m WHERE m.user_id=u.id AND m.user_id=$2 AND m.tenant_id=$1 AND m.subject_id=$2 AND m.status='active'
		`, tenantID, subjectID, tenantID+":"+instanceID, encoded)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err == nil && changed == 0 {
			return ErrNotFound
		}
		return err
	})
}
