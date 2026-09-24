package controlplane

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// OrphanProjectionCleanup is the exact compare-and-delete command used by the
// product cleanup boundary. It mutates control-plane projections only; provider
// resources and lifecycle queues are deliberately outside this store.
type OrphanProjectionCleanup struct {
	TenantID       string
	OwnerSubjectID string
	StaleBefore    time.Time
	AppliedAt      time.Time
	Stacks         []OrphanStackProjection
	Workers        []OrphanWorkerProjection
}

type OrphanStackProjection struct {
	ID        string
	Name      string
	UpdatedAt time.Time
}

type OrphanWorkerProjection struct {
	ID         string
	StackID    string
	Hostname   string
	LastSeenAt time.Time
	UpdatedAt  time.Time
}

// OrphanProjectionStore applies one previously planned projection cleanup as
// a transaction. Any identity or revision drift returns ErrConflict and leaves
// the complete command unapplied.
type OrphanProjectionStore interface {
	ApplyOrphanProjectionCleanup(ctx context.Context, command OrphanProjectionCleanup) error
}

func validateOrphanProjectionCleanup(command OrphanProjectionCleanup) error {
	if strings.TrimSpace(command.TenantID) == "" || strings.TrimSpace(command.OwnerSubjectID) == "" || command.StaleBefore.IsZero() || command.AppliedAt.IsZero() {
		return fmt.Errorf("controlplane: incomplete orphan projection cleanup scope")
	}
	for _, stack := range command.Stacks {
		if strings.TrimSpace(stack.ID) == "" || strings.TrimSpace(stack.Name) == "" || stack.UpdatedAt.IsZero() {
			return fmt.Errorf("controlplane: incomplete orphan stack projection")
		}
	}
	for _, worker := range command.Workers {
		if strings.TrimSpace(worker.ID) == "" || strings.TrimSpace(worker.Hostname) == "" || worker.LastSeenAt.IsZero() || worker.UpdatedAt.IsZero() {
			return fmt.Errorf("controlplane: incomplete orphan worker projection")
		}
	}
	return nil
}

func (s *MemoryStore) ApplyOrphanProjectionCleanup(_ context.Context, command OrphanProjectionCleanup) error {
	if err := validateOrphanProjectionCleanup(command); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	archiving := make(map[string]bool, len(command.Stacks))
	for _, expected := range command.Stacks {
		stack, ok := s.stacks[expected.ID]
		if !ok || stack.TenantID != command.TenantID || stack.OwnerSubjectID != command.OwnerSubjectID || stack.Name != expected.Name || stack.DeletedAt != nil || !stack.UpdatedAt.Equal(expected.UpdatedAt) {
			return fmt.Errorf("%w: orphan stack projection changed", ErrConflict)
		}
		archiving[expected.ID] = true
	}
	for _, expected := range command.Workers {
		worker, ok := s.worker[workerKey(command.TenantID, expected.ID)]
		if !ok || worker.OwnerSubjectID != command.OwnerSubjectID || worker.StackID != expected.StackID || worker.Hostname != expected.Hostname || worker.LastSeenAt == nil || !worker.LastSeenAt.Equal(expected.LastSeenAt) || !worker.UpdatedAt.Equal(expected.UpdatedAt) || !worker.LastSeenAt.Before(command.StaleBefore) {
			return fmt.Errorf("%w: orphan worker projection changed", ErrConflict)
		}
		if stack, ok := s.stacks[worker.StackID]; ok && stack.TenantID == command.TenantID && stack.DeletedAt == nil && !archiving[worker.StackID] {
			return fmt.Errorf("%w: orphan worker stack is active", ErrConflict)
		}
		for _, server := range s.servers {
			if server.TenantID == command.TenantID && server.WorkerID == worker.ID {
				return fmt.Errorf("%w: orphan worker still has a server projection", ErrConflict)
			}
		}
	}

	for _, expected := range command.Stacks {
		stack := s.stacks[expected.ID]
		appliedAt := command.AppliedAt.UTC()
		stack.DeletedAt = &appliedAt
		stack.UpdatedAt = appliedAt
		stack.Status = "stopped"
		s.stacks[expected.ID] = stack
	}
	for _, expected := range command.Workers {
		delete(s.worker, workerKey(command.TenantID, expected.ID))
		for id, node := range s.nodes {
			if node.TenantID == command.TenantID && node.WorkerID == expected.ID {
				node.WorkerID = ""
				s.nodes[id] = node
			}
		}
	}
	return nil
}

func (s *PostgresStore) ApplyOrphanProjectionCleanup(ctx context.Context, command OrphanProjectionCleanup) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	if err := validateOrphanProjectionCleanup(command); err != nil {
		return err
	}

	return s.withTenant(ctx, command.TenantID, func(tx *sql.Tx) error {
		for _, expected := range command.Stacks {
			result, err := tx.ExecContext(ctx, `
				UPDATE stacks
				SET deleted_at = $6, status = 'stopped', updated_at = $6
				WHERE tenant_id = $1 AND owner_subject_id = $2 AND id = $3
					AND name = $4 AND updated_at = $5 AND deleted_at IS NULL
			`, command.TenantID, command.OwnerSubjectID, expected.ID, expected.Name, expected.UpdatedAt, command.AppliedAt)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return fmt.Errorf("%w: orphan stack projection changed", ErrConflict)
			}
		}
		for _, expected := range command.Workers {
			result, err := tx.ExecContext(ctx, `
				DELETE FROM workers AS worker
				WHERE worker.tenant_id = $1 AND worker.owner_subject_id = $2 AND worker.id = $3
					AND COALESCE(worker.stack_id, '') = $4 AND worker.hostname = $5
					AND worker.last_seen_at = $6 AND worker.updated_at = $7
					AND worker.last_seen_at < $8
					AND NOT EXISTS (
						SELECT 1 FROM stacks AS stack
						WHERE stack.tenant_id = worker.tenant_id AND stack.id = worker.stack_id
							AND stack.deleted_at IS NULL
					)
			`, command.TenantID, command.OwnerSubjectID, expected.ID, expected.StackID, expected.Hostname, expected.LastSeenAt, expected.UpdatedAt, command.StaleBefore)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil || affected != 1 {
				return fmt.Errorf("%w: orphan worker projection changed", ErrConflict)
			}
		}
		return nil
	})
}

var _ OrphanProjectionStore = (*MemoryStore)(nil)
var _ OrphanProjectionStore = (*PostgresStore)(nil)
