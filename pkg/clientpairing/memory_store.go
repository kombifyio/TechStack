package clientpairing

import (
	"context"
	"sync"
)

// MemoryStore is a deterministic, concurrency-safe implementation used by
// contract tests and local component harnesses. Production wiring uses
// PostgresStore.
type MemoryStore struct {
	mu    sync.Mutex
	codes map[string]Code
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{codes: make(map[string]Code)}
}

func (s *MemoryStore) Create(_ context.Context, code Code) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryKey(code.TenantID, code.CodeHash)
	if _, exists := s.codes[key]; exists {
		return ErrConflict
	}
	s.codes[key] = cloneCode(code)
	return nil
}

func (s *MemoryStore) Consume(_ context.Context, req ConsumeRequest) (*Code, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryKey(req.TenantID, req.CodeHash)
	record, exists := s.codes[key]
	if !exists {
		return nil, ErrInvalidCode
	}
	if record.InstanceID != req.InstanceID || record.TLSFingerprintSHA256 != req.TLSFingerprintSHA256 {
		return nil, ErrBindingMismatch
	}
	if record.ConsumedAt != nil {
		return nil, ErrAlreadyConsumed
	}
	if !record.ExpiresAt.After(req.Now) {
		return nil, ErrExpired
	}
	consumedAt := req.Now.UTC()
	record.ConsumedAt = &consumedAt
	s.codes[key] = record
	out := cloneCode(record)
	return &out, nil
}

func memoryKey(tenantID, codeHash string) string {
	return tenantID + "\x00" + codeHash
}

func cloneCode(code Code) Code {
	if code.ConsumedAt != nil {
		consumedAt := *code.ConsumedAt
		code.ConsumedAt = &consumedAt
	}
	return code
}
