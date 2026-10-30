package controlplane

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// The in-memory GuardTenantHandoffStore mirrors migrations 169 and 170: every
// key a worker identity bound links that tenant until the tenant's confirmation
// for that key is consumed.

func (s *MemoryStore) ConfirmGuardTenantHandoff(_ context.Context, h GuardTenantHandoff) error {
	if strings.TrimSpace(h.TenantID) == "" || strings.TrimSpace(h.HandoffID) == "" || strings.TrimSpace(h.WorkerID) == "" ||
		h.ConfirmationSHA256 == "" || strings.TrimSpace(h.ConfirmedBy) == "" {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.guardHandoffs == nil {
		s.guardHandoffs = map[string]GuardTenantHandoff{}
	}
	key := workerKey(h.TenantID, h.HandoffID)
	if _, exists := s.guardHandoffs[key]; exists {
		return ErrConflict
	}
	s.guardHandoffs[key] = h
	return nil
}

func (s *MemoryStore) ConsumeGuardTenantHandoff(_ context.Context, c GuardTenantHandoffConsume) (*GuardTenantHandoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workerKey(c.TenantID, c.HandoffID)
	h, ok := s.guardHandoffs[key]
	if !ok {
		return nil, ErrConflict
	}
	record := guardbinding.HandoffConfirmationRecord{
		TenantID: h.TenantID, ConfirmationSHA256: h.ConfirmationSHA256, Disposition: h.Disposition,
		KeyThumbprint: h.KeyThumbprint, ExpiresAt: h.ExpiresAt, ReceiptDigest: h.ReceiptDigest, ReceiptedAt: h.ReceiptedAt,
	}
	if prior, used := s.guardHandoffUse[key]; used {
		record.ConsumerTenantID, record.ConsumerWorkerID, record.ConsumerKey = prior.ConsumerTenantID, prior.ConsumerWorkerID, prior.KeyThumbprint
	}
	repeat, ok := guardbinding.DecideHandoffConsumption(record, guardbinding.HandoffPresentation{
		ConfirmationSHA256: c.ConfirmationSHA256, Disposition: c.Disposition, ReceiptDigest: c.ReceiptDigest,
		ConsumerTenantID: c.ConsumerTenantID, ConsumerWorkerID: c.ConsumerWorkerID, KeyThumbprint: c.KeyThumbprint, Now: c.Now,
	})
	if !ok {
		return nil, ErrConflict
	}
	if !repeat {
		at := c.Now.UTC()
		h.ReceiptDigest, h.ReceiptEntries, h.ReceiptedAt = c.ReceiptDigest, c.ReceiptEntries, &at
		s.guardHandoffs[key] = h
		if s.guardHandoffUse == nil {
			s.guardHandoffUse = map[string]GuardTenantHandoffConsume{}
		}
		s.guardHandoffUse[key] = c
		if s.releasedKeyCustody == nil {
			s.releasedKeyCustody = map[string]bool{}
		}
		s.releasedKeyCustody[workerKey(h.TenantID, h.KeyThumbprint)] = true
	}
	out := h
	return &out, nil
}

func (s *MemoryStore) GuardKeyCustodyTenants(_ context.Context, thumbprint, tenantID string) ([]string, error) {
	if strings.TrimSpace(thumbprint) == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var tenants []string
	for _, identity := range s.workerIdentities {
		if identity.TenantID == tenantID || (identity.Thumbprint != thumbprint && identity.PreviousThumbprint != thumbprint) ||
			s.releasedKeyCustody[workerKey(identity.TenantID, thumbprint)] || slices.Contains(tenants, identity.TenantID) {
			continue
		}
		tenants = append(tenants, identity.TenantID)
	}
	slices.Sort(tenants)
	return tenants, nil
}

func (s *MemoryStore) VerifyGuardTenantHandoff(_ context.Context, tenantID, handoffID, confirmationSHA256, disposition, keyThumbprint string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.guardHandoffs[workerKey(tenantID, handoffID)]
	if !ok || h.ConfirmationSHA256 != confirmationSHA256 || h.Disposition != disposition ||
		!now.Before(h.ExpiresAt) || h.ReceiptedAt != nil || h.KeyThumbprint == "" || h.KeyThumbprint != keyThumbprint {
		return ErrConflict
	}
	return nil
}
