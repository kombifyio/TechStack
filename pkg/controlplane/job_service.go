package controlplane

import "context"

// ServiceJobLister applies the recorded service target before the bounded
// storage window, so unrelated newer jobs cannot hide a service's activity.
type ServiceJobLister interface {
	ListJobsByService(context.Context, string, string, string, int) ([]Job, error)
}

func (s *MemoryStore) ListJobsByService(_ context.Context, tenantID, stackID, serviceID string, limit int) ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Job, 0)
	for _, job := range s.jobs {
		if job.TenantID != tenantID || (stackID != "" && job.StackID != stackID) {
			continue
		}
		receipt, _ := job.Result["service_action_receipt"].(map[string]any)
		if memoryJobResultString(job.Result["service_id"]) == serviceID || memoryJobResultString(receipt["service_id"]) == serviceID {
			out = append(out, *cloneJob(job))
		}
	}
	return limitJobs(out, limit), nil
}

func (s *PostgresStore) ListJobsByService(ctx context.Context, tenantID, stackID, serviceID string, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.listJobs(ctx, tenantID, `
		SELECT id, tenant_id, instance_id, stack_id, type, state, priority,
			progress, step, message, error, error_details, logs_json::text,
			result_json::text, scheduled_for, started_at, completed_at,
			created_at, updated_at
		FROM jobs
		WHERE tenant_id = $1 AND ($2 = '' OR stack_id = $2)
			AND (result_json->>'service_id' = $3 OR result_json->'service_action_receipt'->>'service_id' = $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, tenantID, stackID, serviceID, limit)
}
