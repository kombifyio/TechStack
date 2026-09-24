package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (s *MemoryStore) CreateStack(_ context.Context, req CreateStackRequest) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.stacks[req.ID]; exists {
		return nil, ErrConflict
	}
	if existing := s.activeStackByNameLocked(req.TenantID, req.OwnerSubjectID, req.Name); existing != nil {
		return nil, ErrConflict
	}
	stackKitInstanceID := strings.TrimSpace(req.StackKitInstanceID)
	if stackKitInstanceID != "" && strings.TrimSpace(req.HomelabID) != "" {
		if existing := s.activeStackByStackKitInstanceLocked(req.TenantID, req.HomelabID, stackKitInstanceID, ""); existing != nil {
			return nil, ErrStackKitInstanceConflict
		}
	}

	now := s.now()
	stack := Stack{
		ID:                 req.ID,
		TenantID:           req.TenantID,
		InstanceID:         req.InstanceID,
		OwnerSubjectID:     req.OwnerSubjectID,
		HomelabID:          strings.TrimSpace(req.HomelabID),
		StackKitInstanceID: stackKitInstanceID,
		Name:               req.Name,
		Description:        req.Description,
		Mode:               firstNonEmpty(req.Mode, "easy"),
		Status:             firstNonEmpty(req.Status, "draft"),
		Config:             cloneMap(req.Config),
		Services:           cloneSliceOfMaps(req.Services),
		RuntimeSummary:     map[string]any{},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	s.stacks[stack.ID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) GetStack(_ context.Context, tenantID, stackID string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	return cloneStack(stack), nil
}

// GetStackIncludingDeleted returns one exact tenant-scoped stack for durable
// receipt authorization. Product inventory must continue to use GetStack so
// archived stacks remain invisible there.
func (s *MemoryStore) GetStackIncludingDeleted(_ context.Context, tenantID, stackID string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneStack(stack), nil
}

func (s *MemoryStore) GetActiveStackByName(_ context.Context, tenantID, ownerSubjectID, name string) (*Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stack := s.activeStackByNameLocked(tenantID, ownerSubjectID, name)
	if stack == nil {
		return nil, ErrNotFound
	}
	return cloneStack(*stack), nil
}

func (s *MemoryStore) ListStacksByTenant(_ context.Context, tenantID string) ([]Stack, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Stack, 0)
	for _, stack := range s.stacks {
		if stack.TenantID == tenantID && stack.DeletedAt == nil {
			out = append(out, *cloneStack(stack))
		}
	}
	return out, nil
}

func (s *MemoryStore) SoftDeleteStack(_ context.Context, tenantID, stackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return ErrNotFound
	}
	now := s.now()
	stack.DeletedAt = &now
	stack.UpdatedAt = now
	stack.Status = "stopped"
	s.stacks[stackID] = stack
	return nil
}

func (s *MemoryStore) UpdateStackRuntime(_ context.Context, tenantID, stackID string, runtime RuntimeUpdate) (*Stack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if runtime.Status != "" {
		stack.Status = runtime.Status
	}
	stack.RuntimeSummary = cloneMap(runtime.RuntimeSummary)
	stack.DriftStatus = runtime.DriftStatus
	stack.DriftCheckedAt = cloneTime(runtime.DriftCheckedAt)
	stack.UpdatedAt = s.now()
	s.stacks[stackID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) CompareAndSwapStackConfig(_ context.Context, command StackConfigCAS) (*Stack, error) {
	prepared, err := normalizeStackConfigCAS(command)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[prepared.StackID]
	if !ok || stack.TenantID != prepared.TenantID || stack.DeletedAt != nil {
		return nil, ErrConflict
	}
	if !stack.UpdatedAt.Equal(prepared.ExpectedUpdatedAt) {
		return nil, ErrConflict
	}
	stack.Config = cloneMap(prepared.Config)
	stack.UpdatedAt = s.now()
	if !stack.UpdatedAt.After(prepared.ExpectedUpdatedAt) {
		stack.UpdatedAt = prepared.ExpectedUpdatedAt.Add(time.Nanosecond)
	}
	s.stacks[stack.ID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) SetStackHomelab(_ context.Context, tenantID, stackID, homelabID string) (*Stack, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	homelabID = strings.TrimSpace(homelabID)
	if homelabID == "" {
		return nil, fmt.Errorf("controlplane: homelab id required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stack, ok := s.stacks[stackID]
	if !ok || stack.TenantID != tenantID || stack.DeletedAt != nil {
		return nil, ErrNotFound
	}
	if stack.StackKitInstanceID != "" {
		if existing := s.activeStackByStackKitInstanceLocked(tenantID, homelabID, stack.StackKitInstanceID, stack.ID); existing != nil {
			return nil, ErrStackKitInstanceConflict
		}
	}
	stack.HomelabID = homelabID
	stack.UpdatedAt = s.now()
	s.stacks[stackID] = stack
	return cloneStack(stack), nil
}

func (s *MemoryStore) activeStackByNameLocked(tenantID, ownerSubjectID, name string) *Stack {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, stack := range s.stacks {
		if stack.DeletedAt == nil &&
			stack.TenantID == tenantID &&
			stack.OwnerSubjectID == ownerSubjectID &&
			strings.ToLower(strings.TrimSpace(stack.Name)) == needle {
			found := stack
			return &found
		}
	}
	return nil
}

func (s *MemoryStore) activeStackByStackKitInstanceLocked(tenantID, homelabID, stackKitInstanceID, excludingStackID string) *Stack {
	homelabID = strings.TrimSpace(homelabID)
	stackKitInstanceID = strings.TrimSpace(stackKitInstanceID)
	for _, stack := range s.stacks {
		if stack.DeletedAt == nil && stack.ID != excludingStackID && stack.TenantID == tenantID &&
			stack.HomelabID == homelabID && stack.StackKitInstanceID == stackKitInstanceID {
			found := stack
			return &found
		}
	}
	return nil
}

var _ StackStore = (*MemoryStore)(nil)
