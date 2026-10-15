package controlplane

import "strings"

// WalletOwnerID accepts legacy owner aliases only when they identify one owner.
// Missing, conflicting and non-string identities never confer ownership.
func WalletOwnerID(metadata map[string]any) string {
	owner := ""
	for _, key := range []string{"owner_id", "owner_subject_id", "user_id"} {
		value := metadata[key]
		if value == nil {
			continue
		}
		alias, ok := value.(string)
		if !ok {
			return ""
		}
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if owner != "" && alias != owner {
			return ""
		}
		owner = alias
	}
	return owner
}

func normalizeWalletIdentity(item WalletItem) (WalletItem, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.TenantID = strings.TrimSpace(item.TenantID)
	for _, key := range []string{"kind", "item_type"} {
		if value := item.Metadata[key]; value != nil {
			if _, ok := value.(string); !ok {
				return WalletItem{}, ErrConflict
			}
		}
	}
	kind, _ := item.Metadata["kind"].(string)
	itemType, _ := item.Metadata["item_type"].(string)
	item.ItemType = strings.TrimSpace(firstNonEmpty(item.ItemType, kind, itemType, "other"))
	for _, alias := range []string{kind, itemType} {
		if strings.TrimSpace(alias) != "" && strings.TrimSpace(alias) != item.ItemType {
			return WalletItem{}, ErrConflict
		}
	}
	owner := WalletOwnerID(item.Metadata)
	if item.ID == "" || item.TenantID == "" || owner == "" {
		return WalletItem{}, ErrConflict
	}
	item.Metadata = cloneMap(item.Metadata)
	item.Metadata["owner_id"] = owner
	item.Metadata["id"] = item.ID
	item.Metadata["kind"] = item.ItemType
	return item, nil
}

func walletIdentityMatches(existing, proposed WalletItem) bool {
	owner := WalletOwnerID(existing.Metadata)
	return owner != "" && owner == WalletOwnerID(proposed.Metadata) &&
		existing.TenantID == proposed.TenantID &&
		strings.TrimSpace(firstNonEmpty(existing.ItemType, "other")) == proposed.ItemType
}
