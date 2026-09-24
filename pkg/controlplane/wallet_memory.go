package controlplane

import (
	"context"
)

func (s *MemoryStore) UpsertWalletItem(_ context.Context, item WalletItem) (*WalletItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.wallet[item.ID]; ok {
		item.CreatedAt = existing.CreatedAt
	} else {
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
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.wallet[itemID]
	if !ok || item.TenantID != tenantID {
		return ErrNotFound
	}
	delete(s.wallet, itemID)
	return nil
}

var _ WalletStore = (*MemoryStore)(nil)
