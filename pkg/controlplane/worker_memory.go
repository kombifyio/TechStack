package controlplane

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (s *MemoryStore) UpsertWorkerHeartbeat(_ context.Context, worker Worker) (*Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertWorkerHeartbeatLocked(worker), nil
}

func (s *MemoryStore) upsertWorkerHeartbeatLocked(worker Worker) *Worker {
	now := s.now()
	key := workerKey(worker.TenantID, worker.ID)
	if existing, ok := s.worker[key]; ok {
		worker.CreatedAt = existing.CreatedAt
		if existing.Approved {
			worker.Approved = true
			worker.ApprovedAt = cloneTime(existing.ApprovedAt)
			if worker.Status == "" || worker.Status == "pending" {
				worker.Status = existing.Status
			}
		}
		if existing.OwnerSubjectID != "" {
			worker.OwnerSubjectID = existing.OwnerSubjectID
		}
		worker.Resources = preserveWorkerCredentialResources(existing.Resources, worker.Resources)
	} else {
		worker.CreatedAt = now
	}
	if worker.LastSeenAt == nil {
		worker.LastSeenAt = &now
	}
	if worker.Status == "" {
		worker.Status = "pending"
	}
	worker.UpdatedAt = now
	worker.Tags = cloneMap(worker.Tags)
	worker.Capabilities = cloneMap(worker.Capabilities)
	worker.Resources = cloneMap(worker.Resources)
	s.worker[key] = worker
	return cloneWorker(worker)
}

func (s *MemoryStore) CompareAndSwapWorkerCredential(_ context.Context, command WorkerCredentialCAS) (*Worker, error) {
	prepared, err := normalizeWorkerCredentialCAS(command)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := workerKey(prepared.TenantID, prepared.WorkerID)
	worker, ok := s.worker[key]
	if !ok {
		return nil, ErrNotFound
	}
	current, err := WorkerCredentialStateFromWorker(worker)
	if err != nil || !workerCredentialStateEqual(current, prepared.Expected) {
		return nil, ErrConflict
	}
	worker.Resources = cloneMap(worker.Resources)
	if worker.Resources == nil {
		worker.Resources = map[string]any{}
	}
	for key, value := range workerCredentialResources(prepared.Next) {
		worker.Resources[key] = value
	}
	worker.UpdatedAt = s.now()
	s.worker[key] = worker
	return cloneWorker(worker), nil
}

func (s *MemoryStore) GetWorker(_ context.Context, tenantID, workerID string) (*Worker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	worker, ok := s.worker[workerKey(tenantID, workerID)]
	if !ok || worker.TenantID != tenantID || worker.ID != workerID {
		return nil, ErrNotFound
	}
	return cloneWorker(worker), nil
}

func (s *MemoryStore) ListWorkersByTenant(_ context.Context, tenantID string) ([]Worker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Worker, 0)
	for _, worker := range s.worker {
		if worker.TenantID == tenantID {
			out = append(out, *cloneWorker(worker))
		}
	}
	return out, nil
}

func (s *MemoryStore) ApproveWorker(_ context.Context, tenantID, workerID, ownerSubjectID string, approvedAt time.Time) (*Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := workerKey(tenantID, workerID)
	worker, ok := s.worker[key]
	if !ok || worker.TenantID != tenantID || worker.ID != workerID || worker.OwnerSubjectID != ownerSubjectID {
		return nil, ErrNotFound
	}
	worker.Approved = true
	worker.Status = "approved"
	worker.ApprovedAt = &approvedAt
	worker.UpdatedAt = approvedAt
	s.worker[key] = worker
	return cloneWorker(worker), nil
}

func (s *MemoryStore) UpsertPairingToken(_ context.Context, token PairingToken) (*PairingToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	key := pairingTokenKey(token.TenantID, token.TokenHash)
	if existingID, ok := s.tokenIDByTenantHashLocked(key); ok {
		token.ID = existingID
	}
	if existing, ok := s.tokens[token.ID]; ok {
		token.CreatedAt = existing.CreatedAt
	} else {
		token.CreatedAt = now
	}
	if token.Status == "" {
		token.Status = "active"
	}
	token.UpdatedAt = now
	token.Metadata = cloneMap(token.Metadata)
	s.tokens[token.ID] = token
	return clonePairingToken(token), nil
}

func (s *MemoryStore) ListPairingTokensByOwner(_ context.Context, tenantID, ownerSubjectID string) ([]PairingToken, error) {
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if tenantID == "" || ownerSubjectID == "" {
		return nil, ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PairingToken, 0)
	for _, token := range s.tokens {
		if token.TenantID == tenantID && token.OwnerSubjectID == ownerSubjectID {
			out = append(out, *clonePairingToken(token))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) GetPairingTokenByHash(_ context.Context, tenantID, tokenHash string) (*PairingToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, token := range s.tokens {
		if token.TokenHash != tokenHash {
			continue
		}
		if tenantID != "" && token.TenantID != tenantID {
			continue
		}
		return clonePairingToken(token), nil
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) ClaimPairingToken(_ context.Context, tenantID, tokenHash string, claimedAt time.Time) (*PairingToken, error) {
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tenantID == "" || tokenHash == "" || claimedAt.IsZero() {
		return nil, ErrNotFound
	}
	claimedAt = claimedAt.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, token := range s.tokens {
		if token.TenantID != tenantID || token.TokenHash != tokenHash {
			continue
		}
		if token.Status != "active" || token.UsedAt != nil || (token.ExpiresAt != nil && !token.ExpiresAt.After(claimedAt)) {
			return nil, ErrNotFound
		}
		usedAt := claimedAt
		token.Status = "used"
		token.UsedAt = &usedAt
		token.UpdatedAt = claimedAt
		s.tokens[id] = token
		return clonePairingToken(token), nil
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) ReleasePairingTokenClaim(_ context.Context, tenantID, tokenHash string) error {
	tenantID = strings.TrimSpace(tenantID)
	tokenHash = strings.TrimSpace(tokenHash)
	if tenantID == "" || tokenHash == "" {
		return ErrNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, token := range s.tokens {
		if token.TenantID != tenantID || token.TokenHash != tokenHash {
			continue
		}
		if token.Status != "used" {
			return ErrNotFound
		}
		token.Status = "active"
		token.UsedAt = nil
		token.UpdatedAt = time.Now().UTC()
		s.tokens[id] = token
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) RevokePairingToken(_ context.Context, tenantID, ownerSubjectID, tokenID string) error {
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	tokenID = strings.TrimSpace(tokenID)
	if tenantID == "" || ownerSubjectID == "" || tokenID == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.tokens[tokenID]
	if !ok || token.TenantID != tenantID || token.OwnerSubjectID != ownerSubjectID {
		return ErrNotFound
	}
	token.Status = "revoked"
	token.UpdatedAt = s.now()
	s.tokens[tokenID] = token
	return nil
}

func pairingTokenKey(tenantID, tokenHash string) string {
	return tenantID + "\x00" + tokenHash
}

func workerKey(tenantID, workerID string) string {
	return tenantID + "\x00" + workerID
}

func (s *MemoryStore) tokenIDByTenantHashLocked(key string) (string, bool) {
	for _, token := range s.tokens {
		if pairingTokenKey(token.TenantID, token.TokenHash) == key {
			return token.ID, true
		}
	}
	return "", false
}

var _ WorkerStore = (*MemoryStore)(nil)

var _ WorkerCredentialStore = (*MemoryStore)(nil)
