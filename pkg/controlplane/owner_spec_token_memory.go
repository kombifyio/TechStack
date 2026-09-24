package controlplane

import (
	"context"
	"fmt"
	"time"
)

func (s *MemoryStore) StoreOwnerSpecToken(_ context.Context, token OwnerSpecToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token.TokenHash == "" || token.TenantID == "" || token.StackID == "" || token.OwnerID == "" || token.ExpiresAt.IsZero() {
		return fmt.Errorf("controlplane: incomplete owner spec token")
	}
	s.ownerSpecTokens[token.TokenHash] = token
	return nil
}

func (s *MemoryStore) ConsumeOwnerSpecToken(_ context.Context, token OwnerSpecToken, consumedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.ownerSpecTokens[token.TokenHash]
	if !ok || stored.TenantID != token.TenantID || stored.StackID != token.StackID || stored.OwnerID != token.OwnerID || !consumedAt.Before(stored.ExpiresAt) {
		return ErrNotFound
	}
	delete(s.ownerSpecTokens, token.TokenHash)
	return nil
}
