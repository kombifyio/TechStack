package stacks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const wizardExtendCheckpointKey = "extend_checkpoint"

// The existing WizardRun ledger holds the accepted candidate before the first
// desired-state write. A retry never projects against its own partial result or
// applies a newer request under an earlier acceptance.
type wizardExtendCheckpoint struct {
	BaseDigest     string                          `json:"base_digest"`
	Projection     *specv2.Projection              `json:"projection"`
	Requests       []jobs.StackKitLifecycleRequest `json:"requests"`
	Recommendation string                          `json:"recommendation_id,omitempty"`
}

func (h wizardRunHandlers) runWizardRunningExtend(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack, base map[string]any) error {
	if h.crud.orch == nil {
		return wizardExtendUnavailable(e, "The lifecycle dispatcher is unavailable")
	}
	if locked, err := h.crud.lockedStackServices(e, run.tenantID, stack.ID, jobs.StackKitLifecycleAdvancedChangeSet); err != nil {
		return err
	} else if len(locked) > 0 {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Stack contains locked services", map[string]any{"reason": "service_locked", "locked_service_ids": locked})
	}
	if err := h.crud.refuseDuringServerMaintenance(e, run.tenantID, stack.ID, ""); err != nil {
		return err
	}
	checkpoint, err := loadWizardExtendCheckpoint(run)
	if err != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The original extension acceptance is unavailable", nil)
	}
	if checkpoint == nil {
		intent := run.request.Intent
		if metadata, ok := base["metadata"].(map[string]any); ok {
			if name, _ := metadata["name"].(string); name != "" {
				intent.Name = name
			}
		}
		projection, handled := h.projectAndValidate(e, base, intent, run.homelabID, h.layoutAcceptanceHook(e, run, stack.ID))
		if handled {
			return nil
		}
		run.captureGoalActivations(projection)
		requests, err := h.crud.orch.PrepareWizardExtend(e.Request.Context(), run.tenantID, run.ownerID, stack.ID, run.runID, run.requestHash, base, projection.Spec)
		if err != nil {
			return wizardExtendUnavailable(e, err.Error())
		}
		checkpoint = &wizardExtendCheckpoint{BaseDigest: wizardExtendSpecDigest(base), Projection: projection,
			Requests: requests, Recommendation: run.layoutRecommendationID}
		if handled := h.ensureWizardHomelab(e, run, stack.Name); handled {
			return nil
		}
		if err := h.saveWizardExtendCheckpoint(e, run, stack.ID, checkpoint); err != nil {
			return wizardExtendUnavailable(e, "The accepted extension could not be recorded; retry with the same Idempotency-Key")
		}
	} else {
		run.layoutRecommendationID = checkpoint.Recommendation
		run.captureGoalActivations(checkpoint.Projection)
		if handled := h.ensureWizardHomelab(e, run, stack.Name); handled {
			return nil
		}
	}
	currentDigest := wizardExtendSpecDigest(base)
	candidateDigest := wizardExtendSpecDigest(checkpoint.Projection.Spec)
	if currentDigest != candidateDigest {
		if currentDigest != checkpoint.BaseDigest {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The homelab changed after this extension was accepted", wizardRunDetails(
				"wizard_extend_config_changed", false, "Review the current setup", "The accepted extension was not applied to a different setup."))
		}
		config := make(map[string]any, len(stack.Config))
		for key, value := range stack.Config {
			config[key] = value
		}
		config[stackConfigKeySpecV2] = checkpoint.Projection.Spec
		_, err := h.crud.stackStore.CompareAndSwapStackConfig(e.Request.Context(), controlplane.StackConfigCAS{
			TenantID: run.tenantID, StackID: stack.ID, ExpectedUpdatedAt: stack.UpdatedAt, Config: config,
		})
		if errors.Is(err, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The homelab changed while the extension was being saved", nil)
		}
		if err != nil {
			return wizardExtendUnavailable(e, "The extended setup could not be saved; retry with the same Idempotency-Key")
		}
	}
	if err := h.persistWizardHomelabIntent(e.Request.Context(), run, stack.ID, checkpoint.Projection); err != nil {
		return wizardExtendUnavailable(e, "The extension decisions could not be saved; retry with the same Idempotency-Key")
	}
	jobIDs := make([]string, 0, len(checkpoint.Requests))
	for _, request := range checkpoint.Requests {
		jobID, err := h.crud.orch.EnqueueStackKitLifecycle(e.Request.Context(), request)
		if err != nil {
			return wizardExtendUnavailable(e, "The accepted extension could not be dispatched; retry with the same Idempotency-Key")
		}
		jobIDs = append(jobIDs, jobID)
		h.linkLayoutExecution(e, run, jobID)
	}
	state, primaryJobID := wizardRunStateAccepted, ""
	if len(jobIDs) > 0 {
		state, primaryJobID = wizardRunStateProvisioning, jobIDs[0]
	}
	data := map[string]any{
		"run_kind": run.effectiveKind, "requested_run_kind": run.request.Intent.RunKind, "coerced": run.coerced(),
		"homelab_id": run.homelab.ID, "kit_assignment_mode": wizardRunKindExtend,
		"kit_slug": wizardRunKitSlug(run.request, checkpoint.Projection), creationStackIDField: stack.ID,
		"node_id": checkpoint.Projection.NodeID, creationNameField: stack.Name, wizardRunStateField: state,
		"accepted_at": time.Now().UTC(), "job_ids": jobIDs,
	}
	if primaryJobID != "" {
		data[creationJobIDField] = primaryJobID
	}
	if run.layoutRecommendationID != "" {
		data[wizardRunLayoutRecommendationKey] = run.layoutRecommendationID
	}
	addWizardProjectionFields(data, checkpoint.Projection, h.cfg.ReleaseVersion)
	if err := h.recordWizardRun(e.Request.Context(), run, controlplane.WizardRun{StackID: stack.ID, NodeID: checkpoint.Projection.NodeID, JobID: primaryJobID}, data); err != nil {
		return wizardExtendUnavailable(e, "The extension outcome could not be recorded; retry with the same Idempotency-Key")
	}
	data["run_id"] = run.runID
	return httpx.Success(e, http.StatusAccepted, data)
}

func loadWizardExtendCheckpoint(run *wizardRunState) (*wizardExtendCheckpoint, error) {
	if run.priorFailed == nil || run.priorFailed.Result[wizardExtendCheckpointKey] == nil {
		return nil, nil
	}
	if run.priorFailed.RequestSHA256 != run.requestHash {
		return nil, controlplane.ErrConflict
	}
	raw, err := json.Marshal(run.priorFailed.Result[wizardExtendCheckpointKey])
	if err != nil {
		return nil, err
	}
	var checkpoint wizardExtendCheckpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return nil, err
	}
	if checkpoint.Projection == nil || checkpoint.BaseDigest == "" || specv2.RequireCanonicalV2(checkpoint.Projection.Spec) != nil {
		return nil, controlplane.ErrConflict
	}
	return &checkpoint, nil
}

func (h wizardRunHandlers) saveWizardExtendCheckpoint(e *httpx.Event, run *wizardRunState, stackID string, checkpoint *wizardExtendCheckpoint) error {
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return h.writeWizardRunLedger(e.Request.Context(), run, controlplane.WizardRun{StackID: stackID}, "failed", "wizard_extend_pending",
		map[string]any{wizardExtendCheckpointKey: value})
}

func wizardExtendSpecDigest(spec map[string]any) string {
	raw, _ := json.Marshal(spec)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func wizardExtendUnavailable(e *httpx.Event, message string) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, message, wizardRunDetails(
		"wizard_extend_dispatch_unavailable", true, "Retry this extension", "Keep this attempt's Idempotency-Key so the original setup and operations are reused."))
}

// An extension has one real job per affected node. The active notice follows
// an unresolved job until every node has a terminal outcome, and never treats
// an unavailable or foreign group member as completed.
func (h wizardRunHandlers) wizardRunOperationJob(ctx context.Context, tenantID string, run *controlplane.WizardRun) (*controlplane.Job, error) {
	primary := h.wizardRunJob(ctx, tenantID, run.JobID)
	raw, err := json.Marshal(run.Result["job_ids"])
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil || len(ids) == 0 {
		return primary, nil
	}
	var live, failed *controlplane.Job
	for _, id := range ids {
		job := h.wizardRunJob(ctx, tenantID, id)
		if job == nil || job.StackID != run.StackID {
			return nil, controlplane.ErrNotFound
		}
		receipt, _ := job.Result[jobs.WizardExtendReceiptKey].(map[string]any)
		if receipt["run_id"] != run.ID || receipt["tenant_id"] != tenantID || receipt["owner_id"] != run.OwnerSubjectID || receipt["stack_id"] != run.StackID || receipt["request_digest"] != run.RequestSHA256 {
			return nil, controlplane.ErrConflict
		}
		if wizardRunJobLive(job.State) && live == nil {
			live = job
		} else if job.State != string(jobs.JobStateCompleted) && failed == nil {
			failed = job
		}
	}
	if live != nil {
		return live, nil
	}
	if failed != nil {
		return failed, nil
	}
	return primary, nil
}
