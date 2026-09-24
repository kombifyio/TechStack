package stacks

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
)

func (h wizardRunHandlers) resolveWalletSSHKey(ctx context.Context, tenantID, ownerID, label string) (string, error) {
	if h.cfg.Wallet == nil {
		return "", errors.New("wallet is not configured; store the SSH key before connecting remotely")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return "", errors.New("SSH key label is required")
	}
	items, err := h.cfg.Wallet.ListWalletItems(ctx, tenantID, "")
	if err != nil {
		return "", errors.New("could not load wallet credentials for SSH key lookup")
	}
	for _, item := range items {
		if !walletItemOwnedBy(item.Metadata, ownerID) {
			continue
		}
		name := strings.TrimSpace(walletMetadataString(item.Metadata["name"]))
		if !strings.EqualFold(name, label) {
			continue
		}
		itemKind := strings.TrimSpace(firstNonEmptyWalletString(
			walletMetadataString(item.Metadata["kind"]),
			item.ItemType,
		))
		if itemKind != "ssh_key" {
			continue
		}
		secret, revealErr := auth.DecryptIfNeeded(auth.GetEncryptor(), walletMetadataString(item.Metadata["secret"]))
		if revealErr != nil {
			return "", errors.New("could not decrypt the matching wallet credential")
		}
		secret = strings.TrimSpace(secret)
		if secret == "" {
			return "", errors.New("the matching wallet credential has no secret material")
		}
		return secret, nil
	}
	return "", errors.New("no wallet credential matched the SSH key label")
}

func walletItemOwnedBy(metadata map[string]any, ownerID string) bool {
	if metadata == nil {
		return false
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return false
	}
	for _, key := range []string{"owner_subject_id", "owner_id", "user_id"} {
		if strings.TrimSpace(walletMetadataString(metadata[key])) == ownerID {
			return true
		}
	}
	return false
}

func walletMetadataString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func firstNonEmptyWalletString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
