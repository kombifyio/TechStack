package stacks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/google/uuid"
)

const useCaseRemovalCheckpointKey = "removal_checkpoint"

type useCaseRemovalRequest struct {
	Kind             stackkitcommand.UseCaseRemovalKind `json:"selector_kind"`
	Ref              string                             `json:"selector_ref"`
	ExpectedSpecHash string                             `json:"expected_spec_hash"`
}

func readUseCaseRemovalRequest(e *httpx.Event) (useCaseRemovalRequest, error) {
	var request useCaseRemovalRequest
	raw, err := io.ReadAll(io.LimitReader(e.Request.Body, stackkitcommand.MaxUseCaseConfigurationBytes+1))
	if err != nil || len(raw) > stackkitcommand.MaxUseCaseConfigurationBytes {
		return request, fmt.Errorf("removal request is not bounded")
	}
	if err := decodeStrictJSONBody(bytes.NewReader(raw), &request); err != nil {
		return request, fmt.Errorf("invalid removal request")
	}
	if (request.Kind != stackkitcommand.UseCaseRemovalWorkload && request.Kind != stackkitcommand.UseCaseRemovalGroup) || request.Ref == "" || request.Ref != strings.TrimSpace(request.Ref) || len(request.Ref) > 128 || !stackkitcommand.NativeSpecHashValid(request.ExpectedSpecHash) {
		return request, fmt.Errorf("select one installed workload or catalog use case and its native baseline")
	}
	return request, nil
}

// Membership and module ownership come only from the exact released catalog.
// The native resolver remains the authority for dependencies and retirement.
func selectedUseCaseRemoval(catalog stackkitrelease.UseCaseCatalog, spec map[string]any, request useCaseRemovalRequest) (stackkitcommand.UseCaseRemoval, error) {
	removal := stackkitcommand.UseCaseRemoval{Kind: request.Kind, Ref: request.Ref, ExpectedSpecHash: request.ExpectedSpecHash}
	installed := configuredCatalogWorkloads(catalog, spec)
	selected := map[string]bool{}
	retiredModules := map[string]bool{}
	for _, member := range installed {
		matches := request.Kind == stackkitcommand.UseCaseRemovalWorkload && member.workload == request.Ref || request.Kind == stackkitcommand.UseCaseRemovalGroup && member.useCaseID == request.Ref
		if !matches {
			continue
		}
		if len(member.alternative.Modules) != 1 {
			return removal, fmt.Errorf("selected workload lacks one exact native catalog module")
		}
		module := member.alternative.Modules[0].ID
		removal.Members = append(removal.Members, stackkitcommand.UseCaseRemovalMember{WorkloadRef: member.workload, ModuleRef: module})
		selected[member.workload], retiredModules[module] = true, true
	}
	if err := removal.Validate(); err != nil {
		return removal, err
	}
	for _, member := range installed {
		if selected[member.workload] {
			continue
		}
		for _, module := range member.alternative.Modules {
			if retiredModules[module.ID] {
				return removal, fmt.Errorf("a retained workload still owns the selected module")
			}
		}
	}
	return removal, nil
}

func loadUseCaseRemovalCheckpoint(run controlplane.WizardRun, request useCaseRemovalRequest) (*useCaseChangeCheckpoint, error) {
	value := run.Result[useCaseRemovalCheckpointKey]
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var checkpoint useCaseChangeCheckpoint
	if json.Unmarshal(raw, &checkpoint) != nil || checkpoint.Removal == nil || checkpoint.BaseDigest == "" || len(checkpoint.Requests) == 0 || specv2.RequireCanonicalV2(checkpoint.CandidateSpec) != nil {
		return nil, controlplane.ErrConflict
	}
	removal := checkpoint.Removal
	if removal.Intent.Kind != request.Kind || removal.Intent.Ref != request.Ref || removal.Intent.ExpectedSpecHash != request.ExpectedSpecHash || removal.Intent.Validate() != nil || checkpoint.PreviousSpecHash != removal.Intent.ExpectedSpecHash || checkpoint.SpecHash != removal.SpecHash {
		return nil, controlplane.ErrConflict
	}
	for _, job := range checkpoint.Requests {
		binding := job.WizardExtend
		if job.TenantID != run.TenantID || job.OwnerID != run.OwnerSubjectID || job.StackID != run.StackID || binding == nil || binding.RunID != run.ID || binding.RequestDigest != run.RequestSHA256 || binding.Configuration != nil || !reflect.DeepEqual(binding.Removal, removal) {
			return nil, controlplane.ErrConflict
		}
	}
	return &checkpoint, nil
}

func (h wizardRunHandlers) removeUseCase(e *httpx.Event) error {
	stack, err := h.crud.findOwnedStoreStack(e, e.Request.PathValue("id"))
	if err != nil {
		return err
	}
	request, err := readUseCaseRemovalRequest(e)
	if err != nil {
		return httpx.BadRequest(e, err.Error())
	}
	if len(e.Request.Header.Values("Idempotency-Key")) != 1 {
		return httpx.BadRequest(e, "Exactly one Idempotency-Key is required")
	}
	key := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		return httpx.BadRequest(e, "A bounded Idempotency-Key is required")
	}
	if h.wizardRuns == nil || h.crud.orch == nil {
		return configurationError(e, http.StatusServiceUnavailable, "removal_dispatch_unavailable", "Removal dispatch is unavailable")
	}
	raw, _ := json.Marshal(struct {
		Operation, StackID string
		Request            useCaseRemovalRequest
	}{"use_case_remove", stack.ID, request})
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	prior, err := h.wizardRuns.GetWizardRunByKey(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, key)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return configurationError(e, http.StatusServiceUnavailable, "removal_ledger_unavailable", "The original removal acceptance is unavailable")
	}
	if prior != nil {
		if prior.RequestSHA256 != digest || prior.StackID != stack.ID {
			return configurationError(e, http.StatusConflict, "removal_request_conflict", "This Idempotency-Key identifies another request")
		}
		if prior.Status == "completed" {
			response := cloneConfigurationResult(prior.Result)
			response["run_id"], response["idempotent_replay"] = prior.ID, true
			return httpx.Success(e, http.StatusAccepted, response)
		}
	}
	spec, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok || specv2.RequireCanonicalV2(spec) != nil {
		return configurationError(e, http.StatusConflict, "removal_requires_native_spec", "This stack has no canonical native configuration")
	}
	if locked, err := h.crud.lockedStackServices(e, stack.TenantID, stack.ID, jobs.StackKitLifecycleAdvancedChangeSet); err != nil {
		return err
	} else if len(locked) > 0 {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Stack contains locked services", map[string]any{"reason_code": "service_locked", "locked_service_ids": locked})
	}
	if err := h.crud.refuseDuringServerMaintenance(e, stack.TenantID, stack.ID, ""); err != nil {
		return err
	}
	if prior != nil {
		checkpoint, err := loadUseCaseRemovalCheckpoint(*prior, request)
		if err != nil {
			return configurationError(e, http.StatusConflict, "removal_checkpoint_invalid", "The original removal acceptance could not be recovered")
		}
		if checkpoint != nil {
			return h.dispatchUseCaseChange(e, stack, spec, *prior, checkpoint)
		}
	}
	if stack.Status != "running" {
		return configurationError(e, http.StatusConflict, "removal_busy", "The stack is not ready for removal")
	}
	catalog, err := jobs.UseCaseCatalogForStack(e.Request.Context(), stack.TenantID, stack.ID)
	if err != nil || !catalog.RetainedRemovalCandidates {
		return configurationError(e, http.StatusServiceUnavailable, "removal_native_contract_unavailable", "The pinned release does not support retained-data removal candidates")
	}
	removal, err := selectedUseCaseRemoval(catalog, spec, request)
	if err != nil {
		return httpx.BadRequest(e, err.Error())
	}
	run := controlplane.WizardRun{ID: uuid.NewString(), TenantID: stack.TenantID, OwnerSubjectID: stack.OwnerSubjectID, IdempotencyKey: key, RequestSHA256: digest, RunKind: "expansion", RequestedRunKind: "expansion", StackID: stack.ID, Status: "failed", ErrorReason: controlplane.WizardIntentProjectionPending, Intent: map[string]any{"operation": "use_case_remove", "selector_kind": string(request.Kind), "selector_ref": request.Ref}, Result: map[string]any{}}
	stored, err := h.wizardRuns.ReserveWizardRun(e.Request.Context(), run)
	if err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			return configurationError(e, http.StatusConflict, "removal_request_conflict", "This Idempotency-Key identifies another request")
		}
		return configurationError(e, http.StatusServiceUnavailable, "removal_ledger_unavailable", "Removal acceptance could not be reserved")
	}
	run = *stored
	if run.Status == "completed" {
		response := cloneConfigurationResult(run.Result)
		response["run_id"], response["idempotent_replay"] = run.ID, true
		return httpx.Success(e, http.StatusAccepted, response)
	}
	checkpoint, err := loadUseCaseRemovalCheckpoint(run, request)
	if err != nil {
		return configurationError(e, http.StatusConflict, "removal_checkpoint_invalid", "The original removal acceptance could not be recovered")
	}
	if checkpoint == nil {
		evidence, err := h.crud.orch.AuthorUseCaseRemoval(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, stack.ID, removal)
		if err != nil {
			if errors.Is(err, jobs.ErrNativeIntentBaselineChanged) {
				return configurationError(e, http.StatusConflict, "removal_baseline_changed", "The installed configuration changed; review removal again")
			}
			return configurationError(e, http.StatusServiceUnavailable, "removal_authoring_unavailable", "The pinned native compiler could not author this removal")
		}
		if !sameConfigurationRelease(catalog.Release.Version, evidence.ReleaseVersion) {
			return configurationError(e, http.StatusServiceUnavailable, "removal_catalog_release_mismatch", "Removal membership does not match the pinned compiler")
		}
		if !reflect.DeepEqual(spec, evidence.PreviousSpec) {
			return configurationError(e, http.StatusConflict, "removal_desired_runtime_mismatch", "Desired intent differs from the installed native baseline")
		}
		requests, err := h.crud.orch.PrepareWizardUseCaseRemoval(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, stack.ID, run.ID, digest, evidence.PreviousSpec, evidence.CandidateSpec, removal)
		if err != nil || len(requests) == 0 {
			return configurationError(e, http.StatusServiceUnavailable, "removal_dispatch_unavailable", "The Advanced removal group could not be prepared")
		}
		binding := &jobs.WizardUseCaseRemovalBinding{Intent: removal, SpecHash: evidence.SpecHash}
		for i := range requests {
			requests[i].WizardExtend.Removal = binding
		}
		checkpoint = &useCaseChangeCheckpoint{BaseDigest: wizardExtendSpecDigest(spec), CandidateSpec: evidence.CandidateSpec, PreviousSpecHash: evidence.PreviousSpecHash, SpecHash: evidence.SpecHash, Requests: requests, Removal: binding}
		checkpointJSON, _ := json.Marshal(checkpoint)
		var value map[string]any
		_ = json.Unmarshal(checkpointJSON, &value)
		run.Result = map[string]any{useCaseRemovalCheckpointKey: value}
		saved, err := h.wizardRuns.UpsertWizardRun(e.Request.Context(), run)
		if err != nil {
			return configurationError(e, http.StatusServiceUnavailable, "removal_checkpoint_unavailable", "The accepted removal could not be recorded; retry with the same Idempotency-Key")
		}
		run = *saved
	}
	return h.dispatchUseCaseChange(e, stack, spec, run, checkpoint)
}
