package controlplane

import (
	"context"
	"strings"
)

func (s *MemoryStore) UpsertWalletItem(_ context.Context, item WalletItem) (*WalletItem, error) {
	return s.saveWalletItem(item, false, false)
}

func (s *MemoryStore) CreateWalletItem(_ context.Context, item WalletItem) (*WalletItem, error) {
	return s.saveWalletItem(item, true, false)
}

func (s *MemoryStore) UpdateWalletItem(_ context.Context, item WalletItem) (*WalletItem, error) {
	return s.saveWalletItem(item, false, true)
}

func (s *MemoryStore) saveWalletItem(item WalletItem, createOnly, updateOnly bool) (*WalletItem, error) {
	item, err := normalizeWalletIdentity(item)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.wallet[item.ID]; ok {
		if createOnly || !walletIdentityMatches(existing, item) {
			return nil, ErrConflict
		}
		if updateOnly && item.InstanceID != "" && item.InstanceID != existing.InstanceID {
			return nil, ErrConflict
		}
		item.InstanceID = existing.InstanceID
		item.CreatedAt = existing.CreatedAt
	} else {
		if updateOnly {
			return nil, ErrNotFound
		}
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	item.Metadata = cloneMap(item.Metadata)
	s.wallet[item.ID] = item
	return cloneWalletItem(item), nil
}

func (s *MemoryStore) GetWalletItem(_ context.Context, tenantID, itemID string) (*WalletItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, ok := s.wallet[itemID]
	if !ok || item.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneWalletItem(item), nil
}

func (s *MemoryStore) ListWalletItems(_ context.Context, tenantID, stackID string) ([]WalletItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]WalletItem, 0)
	for _, item := range s.wallet {
		if item.TenantID != tenantID {
			continue
		}
		if stackID != "" && item.StackID != stackID {
			continue
		}
		out = append(out, *cloneWalletItem(item))
	}
	return out, nil
}

func (s *MemoryStore) DeleteWalletItem(_ context.Context, tenantID, itemID string) error {
	return s.deleteWalletItem(tenantID, itemID, "", "", false)
}

func (s *MemoryStore) DeleteWalletItemForOwner(_ context.Context, tenantID, itemID, ownerID, itemType string) error {
	return s.deleteWalletItem(tenantID, itemID, ownerID, itemType, true)
}

func (s *MemoryStore) deleteWalletItem(tenantID, itemID, ownerID, itemType string, checkOwner bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.wallet[itemID]
	if !ok || item.TenantID != tenantID {
		return ErrNotFound
	}
	if checkOwner && (strings.TrimSpace(ownerID) == "" || WalletOwnerID(item.Metadata) != strings.TrimSpace(ownerID) || item.ItemType != itemType) {
		return ErrConflict
	}
	delete(s.wallet, itemID)
	return nil
}

var _ WalletStore = (*MemoryStore)(nil)
