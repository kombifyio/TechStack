package controlplane

import (
	"context"
	"fmt"
)

// PrepareWizardPairing mirrors the Postgres contract in memory: the keyed run
// binds one placeholder job first, then the prepared result completes it. A
// repeat of the same key returns the already prepared job unchanged.
func (s *MemoryStore) PrepareWizardPairing(ctx context.Context, run WizardRun, requested UpsertJobRequest, prepare func(WorkerStore) (map[string]any, error)) (*Job, bool, error) {
	if s == nil || prepare == nil {
		return nil, false, fmt.Errorf("controlplane: pairing preparation is unavailable")
	}
	if err := validateWizardRun(run); err != nil {
		return nil, false, err
	}
	if run.IdempotencyKey == "" || run.NodeID == "" || requested.ID == "" || requested.TenantID != run.TenantID || requested.StackID != run.StackID || (requested.Type != "update" && requested.Type != "remote_enrollment") {
		return nil, false, fmt.Errorf("controlplane: matching keyed pairing operation required")
	}
	jobID, err := s.reservePairingJob(run, requested)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	s.mu.Unlock()
	if !ok || job.StackID != run.StackID || job.Type != requested.Type {
		return nil, false, ErrConflict
	}
	if job.Step != "pairing_prepare" {
		return cloneJob(job), false, nil
	}
	result, err := prepare(s)
	if err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job = s.jobs[jobID]
	job.State, job.Step, job.Progress, job.Message = "completed", "create_spec", 100, "Server registration prepared"
	if job.Type == "remote_enrollment" {
		job.State, job.Step, job.Progress, job.Message = "pending", "remote_ssh_connect", 5, "Connecting to your Node over SSH…"
	}
	job.Result = cloneMap(result)
	job.UpdatedAt = s.now()
	s.jobs[jobID] = job
	return cloneJob(job), true, nil
}

func (s *MemoryStore) reservePairingJob(run WizardRun, requested UpsertJobRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.wizardRunByKeyLocked(run.TenantID, run.OwnerSubjectID, run.IdempotencyKey)
	if existing == nil {
		stored, err := s.upsertWizardRunLocked(run)
		if err != nil {
			return "", err
		}
		existing = stored
	}
	// A reservation is written before the Stack exists, so it carries no
	// Stack/Node yet; the first preparation binds them.
	unbound := existing.StackID == "" && existing.NodeID == ""
	if existing.ID != run.ID || existing.RequestSHA256 != run.RequestSHA256 || (!unbound && (existing.StackID != run.StackID || existing.NodeID != run.NodeID)) {
		return "", ErrConflict
	}
	if existing.PairingJobID != "" {
		return existing.PairingJobID, nil
	}
	now := s.now()
	// Failed placeholder: recovery cannot execute it before preparation completes.
	s.jobs[requested.ID] = Job{
		ID: requested.ID, TenantID: run.TenantID, StackID: run.StackID, Type: requested.Type,
		State: "failed", Step: "pairing_prepare", Result: cloneMap(requested.Result), Payload: cloneMap(requested.Payload),
		ScheduledFor: now, CreatedAt: now, UpdatedAt: now,
	}
	bound := s.wizardRuns[existing.ID]
	bound.StackID, bound.NodeID = run.StackID, run.NodeID
	bound.PairingJobID = requested.ID
	bound.UpdatedAt = now
	s.wizardRuns[existing.ID] = bound
	return requested.ID, nil
}
