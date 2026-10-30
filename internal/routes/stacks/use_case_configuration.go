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

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/google/uuid"
)

const useCaseConfigurationCheckpointKey = "configuration_checkpoint"

type useCaseChangeCheckpoint struct {
	Removal          *jobs.WizardUseCaseRemovalBinding `json:"removal,omitempty"`
	BaseDigest       string                            `json:"base_digest"`
	CandidateSpec    map[string]any                    `json:"candidate_spec"`
	PreviousSpecHash string                            `json:"previous_spec_hash"`
	SpecHash         string                            `json:"spec_hash"`
	WorkloadRef      string                            `json:"workload_ref"`
	Requests         []jobs.StackKitLifecycleRequest   `json:"requests"`
}

func (h wizardRunHandlers) getUseCaseConfiguration(e *httpx.Event) error {
	stack, err := h.crud.findOwnedStoreStack(e, e.Request.PathValue("id"))
	if err != nil {
		return err
	}
	spec, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok || specv2.RequireCanonicalV2(spec) != nil {
		return configurationError(e, http.StatusConflict, "configuration_requires_native_spec", "This stack has no canonical native configuration")
	}
	catalog, err := jobs.UseCaseCatalogForStack(e.Request.Context(), stack.TenantID, stack.ID)
	if err != nil {
		return configurationError(e, http.StatusServiceUnavailable, "configuration_catalog_unavailable", "The release configuration catalog is unavailable")
	}
	reason := ""
	if stack.Status != "running" {
		reason = "configuration_busy"
	}
	view := configurationView(catalog, stack.ID, spec, reason)
	var selected string
	var selectedModule string
	for _, group := range view.UseCases {
		for _, workload := range group.Workloads {
			if workload.Eligible || catalog.RetainedRemovalCandidates && workload.RemovalEligible {
				selected = workload.WorkloadRef
				selectedModule = workload.ModuleRef
				break
			}
		}
		if selected != "" {
			break
		}
	}
	if selected == "" {
		return httpx.Success(e, http.StatusOK, view)
	}
	if h.crud.orch == nil {
		return configurationError(e, http.StatusServiceUnavailable, "configuration_native_inspection_unavailable", "The native configuration author is unavailable")
	}
	evidence, err := h.crud.orch.AuthorUseCaseConfiguration(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, stack.ID, stackkitcommand.UseCaseConfiguration{WorkloadSlug: selected, ModuleRef: selectedModule})
	if err != nil {
		return configurationError(e, http.StatusServiceUnavailable, "configuration_native_inspection_unavailable", "The installed native configuration could not be inspected")
	}
	if !sameConfigurationRelease(catalog.Release.Version, evidence.ReleaseVersion) {
		return configurationError(e, http.StatusServiceUnavailable, "configuration_catalog_release_mismatch", "Configuration descriptors do not match the pinned compiler")
	}
	if !reflect.DeepEqual(spec, evidence.PreviousSpec) {
		reason = "configuration_desired_runtime_mismatch"
	}
	view = configurationView(catalog, stack.ID, evidence.PreviousSpec, reason)
	view.ExpectedSpecHash = evidence.PreviousSpecHash
	return httpx.Success(e, http.StatusOK, view)
}

func (h wizardRunHandlers) configureUseCase(e *httpx.Event) error {
	stack, err := h.crud.findOwnedStoreStack(e, e.Request.PathValue("id"))
	if err != nil {
		return err
	}
	configuration, err := readUseCaseConfigurationRequest(e)
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
		return configurationError(e, http.StatusServiceUnavailable, "configuration_dispatch_unavailable", "Configuration dispatch is unavailable")
	}
	raw, _ := json.Marshal(struct {
		Operation, StackID string
		Configuration      stackkitcommand.UseCaseConfiguration
	}{"use_case_configure", stack.ID, configuration})
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	// Existing ledger authority wins before inspecting mutable desired state.
	prior, err := h.wizardRuns.GetWizardRunByKey(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, key)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return configurationError(e, http.StatusServiceUnavailable, "configuration_ledger_unavailable", "The original configuration acceptance is unavailable")
	}
	if prior != nil {
		if prior.RequestSHA256 != digest || prior.StackID != stack.ID {
			return configurationError(e, http.StatusConflict, "configuration_request_conflict", "This Idempotency-Key identifies another request")
		}
		if prior.Status == "completed" {
			response := cloneConfigurationResult(prior.Result)
			response["run_id"] = prior.ID
			response["idempotent_replay"] = true
			return httpx.Success(e, http.StatusAccepted, response)
		}
	}
	spec, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok || specv2.RequireCanonicalV2(spec) != nil {
		return configurationError(e, http.StatusConflict, "configuration_requires_native_spec", "This stack has no canonical native configuration")
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
		checkpoint, checkpointErr := loadUseCaseConfigurationCheckpoint(*prior, configuration.WorkloadSlug)
		if checkpointErr != nil {
			return configurationError(e, http.StatusConflict, "configuration_checkpoint_invalid", "The original configuration acceptance could not be recovered")
		}
		if checkpoint != nil {
			return h.dispatchUseCaseChange(e, stack, spec, *prior, checkpoint)
		}
	}
	if stack.Status != "running" {
		return configurationError(e, http.StatusConflict, "configuration_busy", "The stack is not ready for configuration")
	}
	catalog, err := jobs.UseCaseCatalogForStack(e.Request.Context(), stack.TenantID, stack.ID)
	if err != nil {
		return configurationError(e, http.StatusServiceUnavailable, "configuration_catalog_unavailable", "The release configuration catalog is unavailable")
	}
	selection, found := configurationSelection(configurationView(catalog, stack.ID, spec, ""), configuration.WorkloadSlug)
	if !found {
		return httpx.BadRequest(e, "Select an installed native catalog workload")
	}
	if err := validateCatalogConfiguration(configuration, selection); err != nil {
		return httpx.BadRequest(e, err.Error())
	}
	// Only the released catalog supplies this transport binding; it is not a
	// client-editable field and does not change the original request digest.
	configuration.ModuleRef = selection.ModuleRef
	run := controlplane.WizardRun{ID: uuid.NewString(), TenantID: stack.TenantID, OwnerSubjectID: stack.OwnerSubjectID, IdempotencyKey: key, RequestSHA256: digest, RunKind: "expansion", RequestedRunKind: "expansion", StackID: stack.ID, Status: "failed", ErrorReason: controlplane.WizardIntentProjectionPending, Intent: map[string]any{"operation": "use_case_configure", "workload_ref": configuration.WorkloadSlug}, Result: map[string]any{}}
	stored, err := h.wizardRuns.ReserveWizardRun(e.Request.Context(), run)
	if err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			return configurationError(e, http.StatusConflict, "configuration_request_conflict", "This Idempotency-Key identifies another request")
		}
		return configurationError(e, http.StatusServiceUnavailable, "configuration_ledger_unavailable", "The configuration acceptance could not be reserved")
	}
	run = *stored
	if run.Status == "completed" {
		response := cloneConfigurationResult(run.Result)
		response["run_id"] = run.ID
		response["idempotent_replay"] = true
		return httpx.Success(e, http.StatusAccepted, response)
	}
	checkpoint, err := loadUseCaseConfigurationCheckpoint(run, configuration.WorkloadSlug)
	if err != nil {
		return configurationError(e, http.StatusConflict, "configuration_checkpoint_invalid", "The original configuration acceptance could not be recovered")
	}
	if checkpoint == nil {
		evidence, err := h.crud.orch.AuthorUseCaseConfiguration(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, stack.ID, configuration)
		if err != nil {
			if errors.Is(err, jobs.ErrUseCaseConfigurationBaselineChanged) {
				return configurationError(e, http.StatusConflict, "configuration_baseline_changed", "The installed configuration changed; review it again")
			}
			return configurationError(e, http.StatusServiceUnavailable, "configuration_authoring_unavailable", "The pinned native compiler could not author this configuration")
		}
		if !sameConfigurationRelease(catalog.Release.Version, evidence.ReleaseVersion) {
			return configurationError(e, http.StatusServiceUnavailable, "configuration_catalog_release_mismatch", "Configuration descriptors do not match the pinned compiler")
		}
		if !reflect.DeepEqual(spec, evidence.PreviousSpec) {
			return configurationError(e, http.StatusConflict, "configuration_desired_runtime_mismatch", "The desired setup differs from the installed native baseline")
		}
		if err := stackkitcommand.ValidateUseCaseConfigurationModule(evidence, selection.ModuleRef); err != nil {
			return configurationError(e, http.StatusConflict, "configuration_candidate_scope_invalid", "The compiler candidate changed unrelated module intent")
		}
		requests, err := h.crud.orch.PrepareWizardExtend(e.Request.Context(), stack.TenantID, stack.OwnerSubjectID, stack.ID, run.ID, digest, evidence.PreviousSpec, evidence.CandidateSpec)
		if err != nil || len(requests) == 0 {
			return configurationError(e, http.StatusServiceUnavailable, "configuration_dispatch_unavailable", "The Advanced configuration operation could not be prepared")
		}
		for i := range requests {
			requests[i].WizardExtend.Configuration = &jobs.WizardUseCaseConfigurationBinding{WorkloadRef: configuration.WorkloadSlug, PreviousSpecHash: evidence.PreviousSpecHash, SpecHash: evidence.SpecHash}
		}
		checkpoint = &useCaseChangeCheckpoint{BaseDigest: wizardExtendSpecDigest(spec), CandidateSpec: evidence.CandidateSpec, PreviousSpecHash: evidence.PreviousSpecHash, SpecHash: evidence.SpecHash, WorkloadRef: configuration.WorkloadSlug, Requests: requests}
		checkpointJSON, _ := json.Marshal(checkpoint)
		var value map[string]any
		_ = json.Unmarshal(checkpointJSON, &value)
		run.Result = map[string]any{useCaseConfigurationCheckpointKey: value}
		saved, err := h.wizardRuns.UpsertWizardRun(e.Request.Context(), run)
		if err != nil {
			return configurationError(e, http.StatusServiceUnavailable, "configuration_checkpoint_unavailable", "The accepted configuration could not be recorded; retry with the same Idempotency-Key")
		}
		run = *saved
	}
	return h.dispatchUseCaseChange(e, stack, spec, run, checkpoint)
}

func readUseCaseConfigurationRequest(e *httpx.Event) (stackkitcommand.UseCaseConfiguration, error) {
	var body struct {
		ExpectedSpecHash   string                     `json:"expected_spec_hash"`
		ComputeProfile     string                     `json:"compute_profile,omitempty"`
		StorageProfile     string                     `json:"storage_profile,omitempty"`
		AcceleratorProfile string                     `json:"accelerator_profile,omitempty"`
		Settings           map[string]json.RawMessage `json:"settings,omitempty"`
		SecretRefs         map[string]string          `json:"secret_refs,omitempty"`
	}
	raw, err := io.ReadAll(io.LimitReader(e.Request.Body, stackkitcommand.MaxUseCaseConfigurationBytes+1))
	if err != nil || len(raw) > stackkitcommand.MaxUseCaseConfigurationBytes {
		return stackkitcommand.UseCaseConfiguration{}, fmt.Errorf("configuration body is not bounded")
	}
	if err := decodeStrictJSONBody(bytes.NewReader(raw), &body); err != nil {
		return stackkitcommand.UseCaseConfiguration{}, fmt.Errorf("invalid configuration body")
	}
	c := stackkitcommand.UseCaseConfiguration{WorkloadSlug: e.Request.PathValue("workload"), ExpectedSpecHash: body.ExpectedSpecHash, ComputeProfile: body.ComputeProfile, StorageProfile: body.StorageProfile, AcceleratorProfile: body.AcceleratorProfile, Settings: body.Settings, SecretRefs: body.SecretRefs}
	encoded, _ := json.Marshal(c)
	c, err = stackkitcommand.DecodeUseCaseConfiguration(encoded)
	if err != nil {
		return c, err
	}
	if c.ExpectedSpecHash == "" || c.ReadOnly() {
		return c, fmt.Errorf("configuration requires the native expected spec hash and a selected change")
	}
	return c, nil
}
func sameConfigurationRelease(catalog, pin string) bool {
	return strings.TrimPrefix(catalog, "v") == strings.TrimPrefix(pin, "v")
}
func configurationError(e *httpx.Event, status int, reason, message string) error {
	code := ksapi.ErrCodeUnavailable
	if status == http.StatusConflict {
		code = ksapi.ErrCodeConflict
	}
	return httpx.Error(e, status, code, message, map[string]any{"reason_code": reason})
}
func cloneConfigurationResult(source map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range source {
		out[key] = value
	}
	return out
}
