package layoutadvice

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// The recommendation's execution id anchors its existing lifecycle group.
// Every node must carry the same accepted owner, candidate and wizard request;
// a completed member alone cannot settle a multi-node recommendation.
func (s *Service) wizardExtendGroupState(ctx context.Context, scope Scope, anchor *controlplane.Job) (bool, string) {
	raw := anchor.Payload["wizard_extend_binding"]
	if raw == nil {
		return false, ""
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return true, ""
	}
	var binding struct {
		JobIDs []string `json:"job_ids"`
	}
	if json.Unmarshal(encoded, &binding) != nil || len(binding.JobIDs) == 0 {
		return true, ""
	}
	expected, _ := anchor.Result["wizard_extend_receipt"].(map[string]any)
	if expected["tenant_id"] != scope.TenantID || expected["owner_id"] != scope.OwnerID || expected["stack_id"] != anchor.StackID || expected["group_job_ids"] != strings.Join(binding.JobIDs, ",") {
		return true, ""
	}
	allCompleted, failed := true, false
	for _, id := range binding.JobIDs {
		job, err := s.Jobs.GetJob(ctx, scope.TenantID, id)
		if err != nil || job == nil || job.StackID != anchor.StackID || job.Type != anchor.Type {
			return true, ""
		}
		receipt, _ := job.Result["wizard_extend_receipt"].(map[string]any)
		for _, key := range []string{"run_id", "request_digest", "candidate_digest", "release_version", "archive_sha256", "tenant_id", "owner_id", "stack_id", "stackkit_instance_id", "group_job_ids"} {
			if receipt[key] != expected[key] {
				return true, ""
			}
		}
		switch strings.ToLower(strings.TrimSpace(job.State)) {
		case "completed":
		case "failed", "cancelled", "canceled":
			allCompleted, failed = false, true
		default:
			allCompleted = false
		}
	}
	if failed {
		return true, "failed"
	}
	if allCompleted {
		return true, "completed"
	}
	return true, ""
}
