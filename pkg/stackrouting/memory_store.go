package stackrouting

import (
	"context"
	"strings"
	"sync"
	"time"
)

type memoryIdempotencyRecord struct {
	RequestHash string
	State       DesiredState
}

// MemoryStore is the deterministic store used by application-service and
// route tests. Production uses PostgresStore.
type MemoryStore struct {
	mu          sync.Mutex
	now         func() time.Time
	states      map[string]DesiredState
	idempotency map[string]memoryIdempotencyRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:         time.Now,
		states:      map[string]DesiredState{},
		idempotency: map[string]memoryIdempotencyRecord{},
	}
}

func (s *MemoryStore) Get(_ context.Context, tenantID, stackID string) (*DesiredState, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[routingKey(tenantID, stackID)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneDesiredState(&state), nil
}

func (s *MemoryStore) Put(_ context.Context, req PutRequest) (*PutResult, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idemKey := idempotencyKey(req.TenantID, req.OwnerSubjectID, req.IdempotencyKey)
	if replay, ok := s.idempotency[idemKey]; ok {
		if replay.RequestHash != req.RequestHash {
			return nil, ErrIdempotencyConflict
		}
		return &PutResult{State: cloneDesiredState(&replay.State), Replay: true}, nil
	}

	key := routingKey(req.TenantID, req.StackID)
	current, exists := s.states[key]
	currentRevision := int64(0)
	if exists {
		currentRevision = current.Revision
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != currentRevision {
		return nil, ErrRevisionConflict
	}

	next := req.DesiredState
	if exists && current.RolloutStatus == RolloutPending && !sameDesiredRouting(current, next) {
		return nil, ErrRolloutInProgress
	}
	if exists && sameDesiredRouting(current, next) {
		next = current
	} else {
		next.Revision = currentRevision + 1
		now := s.now().UTC()
		if exists {
			next.CreatedAt = current.CreatedAt
		} else {
			next.CreatedAt = now
		}
		next.UpdatedAt = now
		s.states[key] = next
	}
	s.idempotency[idemKey] = memoryIdempotencyRecord{RequestHash: req.RequestHash, State: next}
	return &PutResult{State: cloneDesiredState(&next)}, nil
}

func (s *MemoryStore) MarkRolloutFinished(_ context.Context, tenantID, stackID string, expectedRevision int64, jobID, terminalStatus, reasonCode string) (*DesiredState, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	jobID, terminalStatus, reasonCode, err := normalizeFinishedRollout(jobID, terminalStatus, reasonCode)
	if err != nil {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := routingKey(tenantID, stackID)
	state, ok := s.states[key]
	if !ok {
		return nil, ErrNotFound
	}
	next, changed, err := finishMemoryRollout(state, expectedRevision, jobID, terminalStatus, reasonCode, s.now)
	if err != nil {
		return nil, err
	}
	if !changed {
		return cloneDesiredState(&state), nil
	}
	s.states[key] = next
	s.updateIdempotencyReceipts(tenantID, stackID, expectedRevision, next)
	return cloneDesiredState(&next), nil
}

func normalizeFinishedRollout(jobID, terminalStatus, reasonCode string) (string, string, string, error) {
	jobID = strings.TrimSpace(jobID)
	terminalStatus = strings.TrimSpace(terminalStatus)
	reasonCode = strings.TrimSpace(reasonCode)
	if jobID == "" || (terminalStatus != RolloutCompleted && terminalStatus != RolloutFailed) {
		return "", "", "", ErrInvalid
	}
	if terminalStatus == RolloutCompleted {
		reasonCode = ""
	}
	return jobID, terminalStatus, reasonCode, nil
}

func finishMemoryRollout(state DesiredState, expectedRevision int64, jobID, terminalStatus, reasonCode string, now func() time.Time) (DesiredState, bool, error) {
	if state.Revision != expectedRevision {
		return DesiredState{}, false, ErrRevisionConflict
	}
	if state.RolloutJobID == jobID && state.RolloutStatus == terminalStatus {
		return state, false, nil
	}
	if !canFinishMemoryRollout(state, jobID) {
		return DesiredState{}, false, ErrRevisionConflict
	}
	state.RolloutStatus = terminalStatus
	state.RolloutJobID = jobID
	state.ReasonCode = reasonCode
	state.UpdatedAt = now().UTC()
	return state, true, nil
}

func canFinishMemoryRollout(state DesiredState, jobID string) bool {
	if state.RolloutStatus == RolloutPending {
		return state.RolloutJobID == jobID
	}
	return state.RolloutStatus == RolloutNotRequested && state.RolloutJobID == ""
}

func (s *MemoryStore) MarkRolloutDispatched(_ context.Context, tenantID, stackID string, expectedRevision int64, jobID string) (*DesiredState, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := routingKey(tenantID, stackID)
	state, ok := s.states[key]
	if !ok {
		return nil, ErrNotFound
	}
	if state.Revision != expectedRevision {
		return nil, ErrRevisionConflict
	}
	if state.RolloutJobID == jobID && (state.RolloutStatus == RolloutPending || state.RolloutStatus == RolloutCompleted || state.RolloutStatus == RolloutFailed) {
		return cloneDesiredState(&state), nil
	}
	if state.RolloutStatus != RolloutNotRequested && state.RolloutStatus != RolloutFailed {
		return nil, ErrRevisionConflict
	}
	state.RolloutStatus = RolloutPending
	state.RolloutJobID = jobID
	state.ReasonCode = ""
	state.UpdatedAt = s.now().UTC()
	s.states[key] = state
	s.updateIdempotencyReceipts(tenantID, stackID, expectedRevision, state)
	return cloneDesiredState(&state), nil
}

func (s *MemoryStore) updateIdempotencyReceipts(tenantID, stackID string, revision int64, state DesiredState) {
	for key, receipt := range s.idempotency {
		if receipt.State.TenantID == tenantID && receipt.State.StackID == stackID && receipt.State.Revision == revision {
			receipt.State = state
			s.idempotency[key] = receipt
		}
	}
}

func routingKey(tenantID, stackID string) string {
	return tenantID + "\x00" + stackID
}

func idempotencyKey(tenantID, ownerID, key string) string {
	return tenantID + "\x00" + ownerID + "\x00" + key
}

func sameDesiredRouting(left, right DesiredState) bool {
	return left.TenantID == right.TenantID &&
		left.StackID == right.StackID &&
		left.OwnerSubjectID == right.OwnerSubjectID &&
		left.ServerID == right.ServerID &&
		left.LeaseID == right.LeaseID &&
		left.Mode == right.Mode &&
		left.Domain == right.Domain &&
		left.Provenance == right.Provenance
}

var _ Store = (*MemoryStore)(nil)
