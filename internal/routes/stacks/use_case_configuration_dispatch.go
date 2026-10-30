package stacks

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
)

func loadUseCaseConfigurationCheckpoint(run controlplane.WizardRun, workload string) (*useCaseChangeCheckpoint, error) {
	value := run.Result[useCaseConfigurationCheckpointKey]
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var checkpoint useCaseChangeCheckpoint
	if json.Unmarshal(raw, &checkpoint) != nil || checkpoint.Removal != nil || checkpoint.WorkloadRef != workload || checkpoint.BaseDigest == "" || len(checkpoint.Requests) == 0 || specv2.RequireCanonicalV2(checkpoint.CandidateSpec) != nil {
		return nil, controlplane.ErrConflict
	}
	for _, request := range checkpoint.Requests {
		if request.TenantID != run.TenantID || request.OwnerID != run.OwnerSubjectID || request.StackID != run.StackID || request.WizardExtend == nil || request.WizardExtend.RunID != run.ID || request.WizardExtend.RequestDigest != run.RequestSHA256 || request.WizardExtend.Configuration == nil || request.WizardExtend.Configuration.WorkloadRef != workload || request.WizardExtend.Configuration.PreviousSpecHash != checkpoint.PreviousSpecHash || request.WizardExtend.Configuration.SpecHash != checkpoint.SpecHash {
			return nil, controlplane.ErrConflict
		}
	}
	return &checkpoint, nil
}

func (h wizardRunHandlers) dispatchUseCaseChange(e *httpx.Event, stack *controlplane.Stack, current map[string]any, run controlplane.WizardRun, checkpoint *useCaseChangeCheckpoint) error {
	reasonPrefix := "configuration"
	if checkpoint.Removal != nil {
		reasonPrefix = "removal"
	}
	candidateDigest := wizardExtendSpecDigest(checkpoint.CandidateSpec)
	if wizardExtendSpecDigest(current) != candidateDigest {
		if wizardExtendSpecDigest(current) != checkpoint.BaseDigest {
			return configurationError(e, http.StatusConflict, reasonPrefix+"_baseline_changed", "The setup changed after this change was accepted")
		}
		// A queued original job proves this candidate was already saved. If
		// desired intent has since returned to its old digest, a later change
		// superseded this checkpoint. Never write that historical candidate
		// again, even when the final acceptance-ledger write originally failed.
		if h.crud.jobStore == nil {
			return configurationError(e, http.StatusServiceUnavailable, reasonPrefix+"_ledger_unavailable", "The original change jobs could not be inspected")
		}
		for _, request := range checkpoint.Requests {
			_, err := h.crud.jobStore.GetJob(e.Request.Context(), run.TenantID, jobs.WizardExtendJobID(request))
			if err == nil {
				return configurationError(e, http.StatusConflict, reasonPrefix+"_baseline_changed", "A later configuration superseded this accepted change")
			}
			if !errors.Is(err, controlplane.ErrNotFound) {
				return configurationError(e, http.StatusServiceUnavailable, reasonPrefix+"_ledger_unavailable", "The original change jobs could not be inspected")
			}
		}
		config := cloneConfigurationResult(stack.Config)
		config[stackConfigKeySpecV2] = checkpoint.CandidateSpec
		_, err := h.crud.stackStore.CompareAndSwapStackConfig(e.Request.Context(), controlplane.StackConfigCAS{TenantID: run.TenantID, StackID: stack.ID, ExpectedUpdatedAt: stack.UpdatedAt, Config: config})
		if errors.Is(err, controlplane.ErrConflict) {
			// A concurrent retry may have committed this exact accepted candidate.
			fresh, readErr := h.crud.stackStore.GetStack(e.Request.Context(), run.TenantID, stack.ID)
			if readErr != nil || fresh == nil || fresh.OwnerSubjectID != run.OwnerSubjectID {
				return configurationError(e, http.StatusConflict, reasonPrefix+"_baseline_changed", "The setup changed while the accepted change was saved")
			}
			spec, _ := fresh.Config[stackConfigKeySpecV2].(map[string]any)
			if wizardExtendSpecDigest(spec) != candidateDigest {
				return configurationError(e, http.StatusConflict, reasonPrefix+"_baseline_changed", "The setup changed while the accepted change was saved")
			}
		} else if err != nil {
			return configurationError(e, http.StatusServiceUnavailable, reasonPrefix+"_save_unavailable", "The accepted change could not be saved; retry with the same Idempotency-Key")
		}
	}
	jobIDs := make([]string, 0, len(checkpoint.Requests))
	for _, request := range checkpoint.Requests {
		id, err := h.crud.orch.EnqueueStackKitLifecycle(e.Request.Context(), request)
		if err != nil {
			return configurationError(e, http.StatusServiceUnavailable, reasonPrefix+"_dispatch_unavailable", "The accepted change could not be dispatched; retry with the same Idempotency-Key")
		}
		jobIDs = append(jobIDs, id)
	}
	response := map[string]any{"kit_deployment_id": stack.ID, "run_id": run.ID, "job_id": jobIDs[0], "job_ids": jobIDs, "state": wizardRunStateProvisioning, "operation": "use_case_configure", "workload_ref": checkpoint.WorkloadRef, "previous_spec_hash": checkpoint.PreviousSpecHash, "spec_hash": checkpoint.SpecHash, "accepted_at": time.Now().UTC()}
	if removal := checkpoint.Removal; removal != nil {
		response["operation"] = "use_case_remove"
		delete(response, "workload_ref")
		response["selector_kind"] = string(removal.Intent.Kind)
		response["selector_ref"] = removal.Intent.Ref
		response["removed_workload_refs"] = removal.Intent.WorkloadRefs()
		response["data_disposition"] = "retain"
	}

	// completed records HTTP acceptance. Existing all-node job receipts remain
	// the authority for runtime completion and for the active wizard notice.
	run.Status, run.ErrorReason, run.JobID, run.Result = "completed", "", jobIDs[0], response
	if _, err := h.wizardRuns.UpsertWizardRun(e.Request.Context(), run); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Change acceptance could not be recorded; retry with the same Idempotency-Key", nil)
	}
	return httpx.Success(e, http.StatusAccepted, response)
}
