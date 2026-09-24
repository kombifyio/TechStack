package systemwallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/logger"
)

type Credential struct {
	Role     string
	Name     string
	Username string
	Secret   string
	Notes    string
}

func ValidateCustody(mode config.DeploymentMode, log *logger.Logger) error {
	if auth.GetEncryptor() != nil {
		return nil
	}
	if mode.IsSaaS() {
		return fmt.Errorf("wallet encryption is required in SaaS mode: set %s to a 32-byte key", auth.EncryptionKeyEnvVar)
	}
	if log != nil {
		log.Warn("wallet_encryption_disabled",
			"message", "Wallet secret writes will fail closed until TECHSTACK_ENCRYPTION_KEY is set",
			"recommendation", "Set TECHSTACK_ENCRYPTION_KEY to a 32-byte key; plaintext secrets are never stored",
		)
	}
	return nil
}

func UpsertCredential(ctx context.Context, store controlplane.WalletStore, tenantID, ownerID string, credential Credential) error {
	tenantID = strings.TrimSpace(tenantID)
	ownerID = strings.TrimSpace(ownerID)
	role := strings.TrimSpace(credential.Role)
	if store == nil || tenantID == "" || ownerID == "" || role == "" {
		return fmt.Errorf("canonical wallet identity is incomplete")
	}
	secret, err := auth.EncryptIfNeeded(auth.GetEncryptor(), credential.Secret)
	if err != nil {
		return fmt.Errorf("encrypt recovery credential: %w", err)
	}
	serviceID := ServiceID(role)
	item := controlplane.WalletItem{
		ID:          ItemID(tenantID, ownerID, role),
		TenantID:    tenantID,
		ItemType:    "password",
		Provider:    "system_account",
		ExternalRef: serviceID,
		Metadata: map[string]any{
			"owner_id": ownerID, "service_id": serviceID, "name": credential.Name,
			"kind": "password", "username": credential.Username, "secret": secret,
			"has_secret": true, "item_class": "recovery", "source_type": "system_account",
			"source_ref": serviceID, "access_mode": "reveal", "revealable": true,
			"notes": credential.Notes, "auto_generated": true,
		},
	}
	item.Metadata["id"] = item.ID
	_, err = store.UpsertWalletItem(ctx, item)
	return err
}

func ItemID(tenantID, ownerID, role string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + ownerID + "\x00" + role))
	return "wallet_" + hex.EncodeToString(sum[:])[:24]
}

func ServiceID(role string) string {
	return "system:" + strings.TrimSpace(role)
}
