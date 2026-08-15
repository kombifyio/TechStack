package controlplane

import "context"

func (s *MemoryStore) ApplyGuardInventorySatellites(_ context.Context, command GuardInventorySatelliteProjection) (*GuardInventorySatelliteResult, error) {
	prepared, err := normalizeGuardInventorySatelliteProjection(command)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	head, ok := s.servers[prepared.ServerID]
	if !ok || !guardInventorySatelliteHeadMatches(head, prepared) || head.LifecycleState == "decommissioning" || head.LifecycleState == "decommissioned" {
		return &GuardInventorySatelliteResult{Applied: false}, nil
	}

	worker := prepared.Worker
	now := s.now()
	workerIdentity := workerKey(prepared.TenantID, worker.ID)
	if existing, exists := s.worker[workerIdentity]; exists {
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
	s.worker[workerIdentity] = worker

	ril := prepared.RILServer
	if existing, exists := s.rilSrv[ril.ID]; exists {
		ril.CreatedAt = existing.CreatedAt
	} else {
		ril.CreatedAt = now
	}
	if ril.Status == "" {
		ril.Status = "unknown"
	}
	ril.UpdatedAt = now
	ril.Health = cloneMap(ril.Health)
	ril.Inventory = cloneMap(ril.Inventory)
	ril.LastSeenAt = cloneTime(ril.LastSeenAt)
	s.rilSrv[ril.ID] = ril

	return &GuardInventorySatelliteResult{Worker: cloneWorker(worker), Applied: true}, nil
}
