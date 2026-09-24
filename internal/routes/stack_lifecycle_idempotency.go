package routes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/providercontrol"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

const (
	managedRuntimeIdempotencyHeader          = "Idempotency-Key"
	managedRuntimeIdempotencyScope           = "techstack/add-managed-runtime/v2"
	managedRuntimeIdempotencyIdentityVersion = "techstack/add-managed-runtime-identity/v2"
	managedRuntimeClientIntentVersion        = "techstack/add-managed-runtime-client-intent/v2"
	managedRuntimeEffectiveIntentVersion     = "techstack/add-managed-runtime-effective-intent/v2"
	managedRuntimeEffectiveReceiptVersion    = "techstack/add-managed-runtime-effective-receipt/v2"
	managedRuntimeCompletionSealKeyVersion   = "techstack/add-managed-runtime-completion-seal-key/v2"
	managedRuntimeCompletionReceiptVersion   = "techstack/add-managed-runtime-completion-receipt/v2"
	managedRuntimeCapacityDenialVersion      = "techstack/add-managed-runtime-capacity-denial/v1"
	managedRuntimeOperationName              = "add-managed-runtime"
)

var (
	errManagedRuntimeIdempotencyKeyInvalid = errors.New("managed runtime idempotency key is invalid")
	errManagedRuntimeIdempotencyConflict   = errors.New("managed runtime idempotency conflict")
	errManagedRuntimeReplayCorrupt         = errors.New("managed runtime replay state is incomplete")
)

// managedRuntimeClientIntent contains only normalized caller-selected fields.
// In particular, empty fields remain empty: mutable stack defaults must never
// be re-read to decide whether a previously persisted request is a replay.
type managedRuntimeClientIntent struct {
	Version           string   `json:"version"`
	RuntimeSlotKey    string   `json:"runtime_slot_key"`
	ProviderID        string   `json:"provider_id"`
	ProviderRegion    string   `json:"provider_region,omitempty"`
	NodeRole          string   `json:"node_role"`
	RuntimeOfferingID string   `json:"runtime_offering_id,omitempty"`
	StackKit          string   `json:"stackkit,omitempty"`
	Services          []string `json:"services"`
}

// managedRuntimeEffectiveIntent is the secret-free, immutable snapshot sent
// to the native manager. Replays use this snapshot rather than current stack
// defaults, which may legitimately have changed after the first request.
type managedRuntimeEffectiveIntent struct {
	Version               string   `json:"version"`
	TenantID              string   `json:"tenant_id"`
	OwnerSubjectID        string   `json:"owner_subject_id"`
	StackID               string   `json:"stack_id"`
	StackName             string   `json:"stack_name"`
	RuntimeSlotKey        string   `json:"runtime_slot_key"`
	RuntimeSlotGeneration uint64   `json:"runtime_slot_generation"`
	ProviderID            string   `json:"provider_id"`
	ProviderRegion        string   `json:"provider_region,omitempty"`
	NodeRole              string   `json:"node_role"`
	RuntimeOfferingID     string   `json:"runtime_offering_id"`
	StackKit              string   `json:"stackkit"`
	Services              []string `json:"services"`
}

type managedRuntimeIdempotency struct {
	JobID             string
	IdentityDigest    string
	ClientDigest      string
	completionSealKey [sha256.Size]byte
}

type managedRuntimeCapacityDenial struct {
	Version     string `json:"version"`
	ReasonCode  string `json:"reason_code"`
	ProviderID  string `json:"provider_id"`
	Limit       int    `json:"limit"`
	Held        int    `json:"held"`
	ReceiptHMAC string `json:"receipt_hmac_sha256"`
}

func readManagedRuntimeIdempotencyKey(request *http.Request) (string, error) {
	if request == nil {
		return "", errManagedRuntimeIdempotencyKeyInvalid
	}
	values := request.Header.Values(managedRuntimeIdempotencyHeader)
	if len(values) != 1 {
		return "", errManagedRuntimeIdempotencyKeyInvalid
	}
	return validateManagedRuntimeIdempotencyKey(values[0])
}

func validateManagedRuntimeIdempotencyKey(value string) (string, error) {
	// A comma can be introduced when an intermediary combines duplicate
	// headers. Rejecting it keeps "exactly one value" true end to end.
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value ||
		strings.Contains(value, ",") || !utf8.ValidString(value) {
		return "", errManagedRuntimeIdempotencyKeyInvalid
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errManagedRuntimeIdempotencyKeyInvalid
		}
	}
	return value, nil
}

func newManagedRuntimeIdempotency(
	tenantID, stackID, key string,
	request managedRuntimeServerRequest,
	generationValues ...uint64,
) (managedRuntimeIdempotency, error) {
	// The header remains mandatory as a transport retry correlation, but the
	// durable job identity is the product slot. Losing a browser key after a
	// 202 therefore cannot create a second server for the same slot.
	_ = key
	runtimeSlotKey := strings.ToLower(strings.TrimSpace(request.RuntimeSlotKey))
	if runtimeSlotKey == "" {
		runtimeSlotKey = jobruntime.PrimaryManagedRuntimeSlotKey
	}
	generation := uint64(1)
	if len(generationValues) > 0 {
		generation = generationValues[0]
	}
	if generation == 0 {
		return managedRuntimeIdempotency{}, fmt.Errorf("managed runtime slot generation is required")
	}
	generationKey := fmt.Sprintf("%d", generation)
	canonical := managedRuntimeClientIntent{
		Version:           managedRuntimeClientIntentVersion,
		RuntimeSlotKey:    runtimeSlotKey,
		ProviderID:        request.ProviderID,
		ProviderRegion:    canonicalManagedRuntimeRequestRegion(request),
		NodeRole:          managedRuntimeNodeRole(request.NodeRole),
		RuntimeOfferingID: strings.TrimSpace(request.RuntimeOfferingID),
		StackKit:          canonicalOptionalManagedRuntimeStackKit(request.StackKit),
		Services:          sortedManagedRuntimeServices(request.Services),
	}
	clientDigest, err := managedRuntimeJSONDigest(managedRuntimeClientIntentVersion, canonical)
	if err != nil {
		return managedRuntimeIdempotency{}, err
	}
	identity := managedRuntimeLengthEncodedDigest(
		managedRuntimeIdempotencyIdentityVersion,
		strings.TrimSpace(tenantID),
		strings.TrimSpace(stackID),
		managedRuntimeOperationName,
		canonical.RuntimeSlotKey,
		generationKey,
	)
	completionSealKey := managedRuntimeLengthEncodedDigest(
		managedRuntimeCompletionSealKeyVersion,
		strings.TrimSpace(tenantID),
		strings.TrimSpace(stackID),
		managedRuntimeOperationName,
		canonical.RuntimeSlotKey,
		generationKey,
	)
	return managedRuntimeIdempotency{
		JobID:             "add-server-" + hex.EncodeToString(identity[:16]),
		IdentityDigest:    "sha256:" + hex.EncodeToString(identity[:]),
		ClientDigest:      clientDigest,
		completionSealKey: completionSealKey,
	}, nil
}

func newManagedRuntimeEffectiveIntent(tenantID, ownerID string, stack managedRuntimeStackRef, request managedRuntimeServerRequest) managedRuntimeEffectiveIntent {
	providerRegion := managedRuntimeProviderRegion(request.ProviderID, request, stack)
	offeringID := firstNonEmptyString(
		request.RuntimeOfferingID,
		stack.field("runtime_offering_id"),
		string(monthlyruntime.DefaultOfferingID()),
	)
	runtimeSlotKey := strings.ToLower(strings.TrimSpace(request.RuntimeSlotKey))
	if runtimeSlotKey == "" {
		runtimeSlotKey = jobruntime.PrimaryManagedRuntimeSlotKey
	}
	return managedRuntimeEffectiveIntent{
		Version:               managedRuntimeEffectiveIntentVersion,
		TenantID:              strings.TrimSpace(tenantID),
		OwnerSubjectID:        strings.TrimSpace(ownerID),
		StackID:               strings.TrimSpace(stack.ID),
		StackName:             firstNonEmptyString(stack.Name, stack.ID),
		RuntimeSlotKey:        runtimeSlotKey,
		RuntimeSlotGeneration: 1,
		ProviderID:            request.ProviderID,
		ProviderRegion:        providerRegion,
		NodeRole:              managedRuntimeNodeRole(request.NodeRole),
		RuntimeOfferingID:     offeringID,
		StackKit: normalizeLifecycleStackKit(
			firstNonEmptyString(request.StackKit, stack.field("stackkit_catalog_ref"), registryCloudKit),
			registryCloudKit,
		),
		Services: sortedManagedRuntimeServices(request.Services),
	}
}

func managedRuntimeEffectiveIntentDigest(intent managedRuntimeEffectiveIntent) (string, error) {
	return managedRuntimeJSONDigest(managedRuntimeEffectiveIntentVersion, intent)
}

func managedRuntimeInitialJobResult(identity managedRuntimeIdempotency, effective managedRuntimeEffectiveIntent) (map[string]any, error) {
	effectiveDigest, err := managedRuntimeEffectiveIntentDigest(effective)
	if err != nil {
		return nil, err
	}
	effectiveMap, err := managedRuntimeStructMap(effective)
	if err != nil {
		return nil, err
	}
	result := managedRuntimePublicJobResult(effective)
	result["idempotency_scope"] = managedRuntimeIdempotencyScope
	result["idempotency_identity_sha256"] = identity.IdentityDigest
	result["client_request_sha256"] = identity.ClientDigest
	result["effective_intent_sha256"] = effectiveDigest
	result["owner_subject_id"] = effective.OwnerSubjectID
	result["effective_intent"] = effectiveMap
	result["effective_intent_hmac_sha256"] = managedRuntimeEffectiveIntentReceipt(identity, effectiveDigest, effective)
	return result, nil
}

func managedRuntimeReplayIntent(job *controlplane.Job, identity managedRuntimeIdempotency, ownerID string) (managedRuntimeEffectiveIntent, error) {
	storedScope, _ := jobResultString(job, "idempotency_scope")
	if job == nil || job.ID != identity.JobID || job.Type != managedRuntimeJobTypeUpdate ||
		storedScope != managedRuntimeIdempotencyScope {
		return managedRuntimeEffectiveIntent{}, errManagedRuntimeIdempotencyConflict
	}
	identityDigest, _ := job.Result["idempotency_identity_sha256"].(string)
	clientDigest, _ := job.Result["client_request_sha256"].(string)
	storedOwnerID, _ := job.Result["owner_subject_id"].(string)
	if identityDigest != identity.IdentityDigest || clientDigest != identity.ClientDigest || storedOwnerID != strings.TrimSpace(ownerID) {
		return managedRuntimeEffectiveIntent{}, errManagedRuntimeIdempotencyConflict
	}
	encoded, err := json.Marshal(job.Result["effective_intent"])
	if err != nil {
		return managedRuntimeEffectiveIntent{}, errManagedRuntimeReplayCorrupt
	}
	var effective managedRuntimeEffectiveIntent
	if err = json.Unmarshal(encoded, &effective); err != nil {
		return managedRuntimeEffectiveIntent{}, errManagedRuntimeReplayCorrupt
	}
	storedEffectiveDigest, _ := job.Result["effective_intent_sha256"].(string)
	actualEffectiveDigest, err := managedRuntimeEffectiveIntentDigest(effective)
	if err != nil || storedEffectiveDigest == "" || storedEffectiveDigest != actualEffectiveDigest ||
		effective.Version != managedRuntimeEffectiveIntentVersion || effective.TenantID != job.TenantID ||
		effective.StackID != job.StackID || effective.OwnerSubjectID != strings.TrimSpace(ownerID) ||
		!managedRuntimeEffectiveIntentReceiptValid(job, identity, effective) {
		return managedRuntimeEffectiveIntent{}, errManagedRuntimeReplayCorrupt
	}
	return effective, nil
}

func jobResultString(job *controlplane.Job, key string) (string, bool) {
	if job == nil || job.Result == nil {
		return "", false
	}
	value, ok := job.Result[key].(string)
	return value, ok
}

func managedRuntimeJobMatchesIdempotency(job *controlplane.Job, identity managedRuntimeIdempotency) bool {
	if job == nil || job.ID != identity.JobID || job.Type != managedRuntimeJobTypeUpdate {
		return false
	}
	storedScope, _ := jobResultString(job, "idempotency_scope")
	storedIdentity, _ := jobResultString(job, "idempotency_identity_sha256")
	storedClient, _ := jobResultString(job, "client_request_sha256")
	return storedScope == managedRuntimeIdempotencyScope && storedIdentity == identity.IdentityDigest &&
		storedClient == identity.ClientDigest
}

func managedRuntimeEffectiveIntentReceipt(identity managedRuntimeIdempotency, effectiveDigest string, effective managedRuntimeEffectiveIntent) string {
	fields := []string{
		identity.JobID,
		identity.IdentityDigest,
		identity.ClientDigest,
		effectiveDigest,
		effective.Version,
		effective.TenantID,
		effective.OwnerSubjectID,
		effective.StackID,
		effective.StackName,
		effective.RuntimeSlotKey,
		effective.ProviderID,
		effective.ProviderRegion,
		effective.NodeRole,
		effective.RuntimeOfferingID,
		effective.StackKit,
	}
	fields = append(fields, effective.Services...)
	return managedRuntimeHMACDigest(identity.completionSealKey[:], managedRuntimeEffectiveReceiptVersion, fields...)
}

func managedRuntimeEffectiveIntentReceiptValid(job *controlplane.Job, identity managedRuntimeIdempotency, effective managedRuntimeEffectiveIntent) bool {
	if job == nil {
		return false
	}
	effectiveDigest, err := managedRuntimeEffectiveIntentDigest(effective)
	if err != nil {
		return false
	}
	stored, _ := jobResultString(job, "effective_intent_hmac_sha256")
	want := managedRuntimeEffectiveIntentReceipt(identity, effectiveDigest, effective)
	return managedRuntimeHMACEqual(stored, want)
}

func managedRuntimePublicJobResult(intent managedRuntimeEffectiveIntent) map[string]any {
	result := map[string]any{
		"creation_operation":       "add-server",
		"stack_id":                 intent.StackID,
		"runtime_slot_key":         intent.RuntimeSlotKey,
		"provider_id":              intent.ProviderID,
		"server_provisioning_mode": "kombify-cloud",
		"server_node_role":         intent.NodeRole,
		"stackkit_foundation":      intent.StackKit,
		"requested_services":       append([]string(nil), intent.Services...),
		"runtime_offering_id":      intent.RuntimeOfferingID,
	}
	if intent.ProviderRegion != "" {
		result["provider_region"] = intent.ProviderRegion
	}
	if intent.ProviderID == providercatalog.ProviderIONOS && intent.ProviderRegion != "" {
		result["ionos_datacenter"] = intent.ProviderRegion
	}
	return result
}

func (h stackLifecycleRouteHandlers) resumeManagedRuntimeServerJob(
	e *httpx.Event,
	stack managedRuntimeStackRef,
	job *controlplane.Job,
	idempotency managedRuntimeIdempotency,
	effective managedRuntimeEffectiveIntent,
	externalReplay bool,
) (*ManagedRuntimeExpansionResult, error) {
	if job == nil || job.ID == "" || job.TenantID == "" || job.StackID != stack.ID {
		return nil, writeManagedRuntimeIdempotencyError(e, stack.ID, "", errManagedRuntimeReplayCorrupt)
	}
	if !managedRuntimeJobMatchesIdempotency(job, idempotency) || !managedRuntimeJobMatchesEffectiveIntent(job, idempotency, effective) {
		return nil, writeManagedRuntimeIdempotencyError(e, stack.ID, job.ID, errManagedRuntimeReplayCorrupt)
	}

	switch strings.ToLower(strings.TrimSpace(job.State)) {
	case managedRuntimeJobStateCompleted:
		response, err := managedRuntimeResponseFromJob(job, true)
		if err != nil || !managedRuntimeResponseMatchesEffectiveIntent(response, effective) ||
			!managedRuntimeCompletionReceiptValid(job, idempotency) {
			return nil, writeManagedRuntimeIdempotencyError(e, stack.ID, job.ID, err)
		}
		return &response, nil
	case monthlyRuntimeEnrollmentStatusFailed:
		if denial, ok := managedRuntimeCapacityDenialFromJob(job, idempotency, effective); ok {
			return nil, writeManagedRuntimeCapacityDenial(e, stack.ID, job.ID, denial)
		}
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime request reached a terminal failure", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "managed_runtime_request_failed",
				managedRuntimeDetailsJobIDKey:      job.ID,
				managedRuntimeDetailsRetryableKey:  false,
				"stack_id":                         stack.ID,
			})
	case monthlyRuntimeEnrollmentStatusPending:
		// Pending proves that no native admission was attempted yet. Re-evaluate
		// mutable product authorization before claiming it; completed and running
		// jobs deliberately bypass this gate because they may already own provider
		// custody and can only be reconciled under the same operation identity.
		if !stackUsesManagedRuntime(stack) {
			return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
				"This stack is not configured for kombify Cloud managed runtimes", map[string]any{
					"stack_id": stack.ID,
				})
		}
		if decision := h.evaluateManagedRuntimeServerEntitlement(
			e.Request.Context(), effective.OwnerSubjectID,
			managedRuntimeRequestFromEffectiveIntent(effective),
		); decision.Denied {
			return nil, httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, decision.Message, decision.Details())
		}
		if h.managedLeases == nil {
			return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"Native managed runtime admission is not configured", map[string]any{
					managedRuntimeDetailsReasonCodeKey: "native_admission_unavailable",
					managedRuntimeDetailsJobIDKey:      job.ID,
					managedRuntimeDetailsRetryableKey:  true,
				})
		}
		if handled, err := h.preflightManagedRuntimeServer(e, effective, job.ID, job.ID); handled || err != nil {
			return nil, err
		}
		started, startErr := h.jobs.StartJob(e.Request.Context(), job.TenantID, job.ID, time.Now().UTC())
		if startErr == nil {
			job = started
			break
		}
		if errors.Is(startErr, controlplane.ErrStackExecutionBusy) {
			return nil, writeManagedRuntimeServerClaimError(e, stack.ID, job.ID, startErr)
		}
		current, reloadErr := h.jobs.GetJob(e.Request.Context(), job.TenantID, job.ID)
		if reloadErr == nil && current != nil && current.State != monthlyRuntimeEnrollmentStatusPending {
			return h.resumeManagedRuntimeServerJob(e, stack, current, idempotency, effective, true)
		}
		return nil, writeManagedRuntimeServerClaimError(e, stack.ID, job.ID, startErr)
	case "running":
		// A running replay is the expected crash-resume path. NativeAdmission
		// serializes and resolves this exact deterministic operation identity.
	default:
		return nil, writeManagedRuntimeIdempotencyError(e, stack.ID, job.ID, errManagedRuntimeReplayCorrupt)
	}

	admissionResult, admissionErr := h.admitManagedRuntimeServer(e, stack, effective, job)
	if admissionResult == nil || admissionErr != nil {
		return nil, admissionErr
	}

	return h.finalizeManagedRuntimeServerJob(e, stack, job, idempotency, effective, admissionResult, externalReplay)
}

func (h stackLifecycleRouteHandlers) admitManagedRuntimeServer(
	e *httpx.Event,
	stack managedRuntimeStackRef,
	effective managedRuntimeEffectiveIntent,
	job *controlplane.Job,
) (*jobruntime.ManagedLeaseResult, error) {
	if h.managedLeases == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Native managed runtime admission is not configured", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "native_admission_unavailable",
				managedRuntimeDetailsJobIDKey:      job.ID,
				managedRuntimeDetailsRetryableKey:  true,
			})
	}
	result, err := h.managedLeases.CreateOrBindLease(e.Request.Context(), managedRuntimeLeaseRequest(effective, job.ID))
	if err != nil {
		return nil, writeManagedRuntimeAdmissionError(e, stack, effective, job, err)
	}
	if result == nil || strings.TrimSpace(result.LeaseID) == "" ||
		strings.TrimSpace(result.RuntimeSlotKey) != effective.RuntimeSlotKey ||
		strings.TrimSpace(result.RuntimeSlotID) == "" ||
		strings.TrimSpace(result.RuntimeServerID) == "" ||
		strings.TrimSpace(result.ResourceGenerationID) == "" ||
		strings.TrimSpace(result.OperationID) == "" ||
		strings.TrimSpace(result.Provider) != effective.ProviderID ||
		result.Phase != jobruntime.RuntimePhaseLeasePending {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Native managed runtime admission returned no durable correlations", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "native_admission_outcome_unconfirmed",
				managedRuntimeDetailsJobIDKey:      job.ID,
				managedRuntimeDetailsRetryableKey:  true,
				"stack_id":                         stack.ID,
			})
	}
	return result, nil
}

func writeManagedRuntimeAdmissionError(
	e *httpx.Event,
	stack managedRuntimeStackRef,
	effective managedRuntimeEffectiveIntent,
	job *controlplane.Job,
	admissionErr error,
) error {
	var capacityErr *providercontrol.ManagedRuntimeCapacityExceededError
	if errors.As(admissionErr, &capacityErr) {
		return writeManagedRuntimeAdmissionCapacityRecheckDenial(e, effective, job, capacityErr)
	}
	if errors.Is(admissionErr, providercontrol.ErrNativeAdmissionConflict) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Native managed runtime admission conflicts with the persisted operation", map[string]any{
				managedRuntimeDetailsReasonCodeKey: "native_admission_conflict",
				managedRuntimeDetailsJobIDKey:      job.ID,
				managedRuntimeDetailsRetryableKey:  false,
				"stack_id":                         stack.ID,
			})
	}
	if errors.Is(admissionErr, providercontrol.ErrManagedRuntimeCapacityPolicyUnavailable) ||
		errors.Is(admissionErr, monthlyruntime.ErrCapacityPolicyAuthorityUnavailable) {
		return writeManagedRuntimeAdmissionRecheckDenial(
			e, effective, job, "managed_runtime_capacity_policy_unavailable", true,
			"Managed server entitlement could not be verified",
			"The authoritative admission recheck could not verify the current account entitlement and managed-server budget.",
			[]string{"Refresh the account decision, then retry with the same Idempotency-Key.", "Contact kombify support if the account should include managed servers."},
		)
	}
	if errors.Is(admissionErr, providercontrol.ErrMutationActivationBlocked) {
		return writeManagedRuntimeAdmissionRecheckDenial(
			e, effective, job, "provider_control_not_activated", false,
			"Managed provider provisioning is not active",
			"The authoritative admission recheck found that this deployment's certified native provider path is not active.",
			[]string{"Retry with the same Idempotency-Key after native provider control is activated.", "Contact kombify support for activation status."},
		)
	}
	if errors.Is(admissionErr, providercontrol.ErrManagedRuntimeUnclassifiedCustody) {
		return writeManagedRuntimeAdmissionRecheckDenial(
			e, effective, job, "provider_custody_reconciliation_required", false,
			"Existing provider custody must be reconciled first",
			"This stack already has a managed lease or server without a verified runtime-slot binding. TechStack will not guess ownership or create another provider resource.",
			[]string{"Keep the provider create switch closed for this stack.", "Ask kombify support to reconcile the existing lease/server with immutable provider evidence."},
		)
	}
	var createBlocked providercontrol.ProviderCreateBlockedError
	if errors.As(admissionErr, &createBlocked) {
		return writeManagedRuntimeAdmissionRecheckDenial(
			e, effective, job, createBlocked.ReasonCode, false,
			"New managed servers are temporarily paused for this provider",
			"The provider-specific create safety switch is closed. Existing cleanup and reconciliation remain available.",
			[]string{"Retry with the same server slot after provider create is re-enabled.", "Contact kombify support with the job ID and provider."},
		)
	}
	// The handler cannot prove whether the native transaction committed. Keep
	// the running job as the only exact same-key reconciliation identity.
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
		"Native managed runtime admission has not been confirmed", map[string]any{
			managedRuntimeDetailsReasonCodeKey: "native_admission_outcome_unconfirmed",
			managedRuntimeDetailsJobIDKey:      job.ID,
			managedRuntimeDetailsRetryableKey:  true,
			"stack_id":                         stack.ID,
		})
}

func (h stackLifecycleRouteHandlers) finalizeManagedRuntimeServerJob(
	e *httpx.Event,
	stack managedRuntimeStackRef,
	job *controlplane.Job,
	idempotency managedRuntimeIdempotency,
	effective managedRuntimeEffectiveIntent,
	admissionResult *jobruntime.ManagedLeaseResult,
	externalReplay bool,
) (*ManagedRuntimeExpansionResult, error) {
	completedResult := cloneLifecycleMap(job.Result)
	for key, value := range managedRuntimePublicJobResult(effective) {
		completedResult[key] = value
	}
	completedResult["lease_id"] = admissionResult.LeaseID
	completedResult["runtime_slot_key"] = admissionResult.RuntimeSlotKey
	completedResult["runtime_slot_id"] = admissionResult.RuntimeSlotID
	completedResult["runtime_server_id"] = admissionResult.RuntimeServerID
	completedResult["resource_generation_id"] = admissionResult.ResourceGenerationID
	completedResult["operation_id"] = admissionResult.OperationID
	completedResult["idempotent_replay"] = admissionResult.IdempotentReplay
	completedResult["provider_id"] = admissionResult.Provider
	completedResult["runtime_phase"] = string(admissionResult.Phase)
	completedResult["desired_state"] = admissionResult.DesiredState
	completedResult["billing_mode"] = admissionResult.BillingMode
	completedResult["enrollment_status"] = monthlyRuntimeEnrollmentStatusPending
	completedResult["managed_runtime_addition"] = true
	completedResult["completion_receipt_hmac_sha256"] = managedRuntimeCompletionReceipt(job, completedResult, idempotency)

	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(e.Request.Context()), managedRuntimeJobStoreWriteTimeout)
	defer cancel()
	completed, finishErr := h.jobs.CompleteJob(finishCtx, job.TenantID, job.ID, completedResult, time.Now().UTC())
	if finishErr == nil {
		return managedRuntimeCompletedJobResponse(e, stack.ID, job.ID, completed, idempotency, effective, externalReplay || admissionResult.IdempotentReplay)
	}
	if current, reloadErr := h.jobs.GetJob(e.Request.Context(), job.TenantID, job.ID); reloadErr == nil && current.State == managedRuntimeJobStateCompleted {
		return managedRuntimeCompletedJobResponse(e, stack.ID, job.ID, current, idempotency, effective, true)
	}
	// Native admission is known to be durable but the job completion result is
	// uncertain. Return its exact correlations and require the same key.
	response := managedRuntimeResponseFromAdmission(job.ID, effective, admissionResult, externalReplay || admissionResult.IdempotentReplay)
	response.Warnings = []string{"Native admission was accepted, but job finalization is pending; retry with the same Idempotency-Key"}
	return &response, nil
}

func managedRuntimeCompletedJobResponse(
	e *httpx.Event,
	stackID string,
	jobID string,
	job *controlplane.Job,
	idempotency managedRuntimeIdempotency,
	effective managedRuntimeEffectiveIntent,
	replay bool,
) (*ManagedRuntimeExpansionResult, error) {
	response, responseErr := managedRuntimeResponseFromJob(job, replay)
	if responseErr != nil || !managedRuntimeResponseMatchesEffectiveIntent(response, effective) ||
		!managedRuntimeCompletionReceiptValid(job, idempotency) {
		return nil, writeManagedRuntimeIdempotencyError(e, stackID, jobID, responseErr)
	}
	return &response, nil
}

func writeManagedRuntimeAdmissionCapacityRecheckDenial(
	e *httpx.Event,
	effective managedRuntimeEffectiveIntent,
	job *controlplane.Job,
	capacity *providercontrol.ManagedRuntimeCapacityExceededError,
) error {
	if job == nil || capacity == nil || capacity.Limit <= 0 || capacity.Held < capacity.Limit {
		return writeManagedRuntimeIdempotencyError(e, effective.StackID, "", errManagedRuntimeReplayCorrupt)
	}
	if strings.TrimSpace(capacity.TenantID) != effective.TenantID ||
		strings.TrimSpace(capacity.OwnerSubjectID) != effective.OwnerSubjectID {
		return writeManagedRuntimeIdempotencyError(e, effective.StackID, job.ID, errManagedRuntimeReplayCorrupt)
	}
	details := monthlyruntime.ManagedRuntimeCapacityReservationDenialDetails(
		effective.ProviderID, capacity.Limit, capacity.Held,
	)
	details[managedRuntimeDetailsJobIDKey] = job.ID
	details["stack_id"] = effective.StackID
	details["job_state"] = "running"
	details["retry_scope"] = "exact_same_idempotency_key"
	details["attempt_outcome"] = "rejected_before_native_write"
	if guidance, ok := details["user_guidance"].(map[string]any); ok {
		guidance["body"] = "This admission attempt found that all managed-server slots were reserved and returned before its own native write. The existing running job remains the only reconciliation identity because another concurrent same-key attempt may still be completing."
		steps, _ := guidance["next_steps"].([]string)
		guidance["next_steps"] = append(
			[]string{"Retry this running operation with the same Idempotency-Key; do not create a replacement operation."},
			steps...,
		)
	}
	return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
		"This account already holds the maximum number of managed server slots", details)
}

func managedRuntimeCapacityDenialFromJob(
	job *controlplane.Job,
	idempotency managedRuntimeIdempotency,
	effective managedRuntimeEffectiveIntent,
) (managedRuntimeCapacityDenial, bool) {
	if job == nil || strings.ToLower(strings.TrimSpace(job.State)) != monthlyRuntimeEnrollmentStatusFailed ||
		strings.TrimSpace(job.ErrorDetails) == "" {
		return managedRuntimeCapacityDenial{}, false
	}
	var denial managedRuntimeCapacityDenial
	if err := json.Unmarshal([]byte(job.ErrorDetails), &denial); err != nil ||
		denial.Version != managedRuntimeCapacityDenialVersion ||
		denial.ReasonCode != monthlyruntime.ReasonMaxServersReached ||
		denial.ProviderID != effective.ProviderID || denial.Limit <= 0 || denial.Held < denial.Limit {
		return managedRuntimeCapacityDenial{}, false
	}
	want := managedRuntimeCapacityDenialReceipt(job, denial, idempotency, effective)
	return denial, managedRuntimeHMACEqual(denial.ReceiptHMAC, want)
}

func managedRuntimeCapacityDenialReceipt(
	job *controlplane.Job,
	denial managedRuntimeCapacityDenial,
	idempotency managedRuntimeIdempotency,
	effective managedRuntimeEffectiveIntent,
) string {
	if job == nil {
		return ""
	}
	effectiveDigest, _ := managedRuntimeEffectiveIntentDigest(effective)
	return managedRuntimeHMACDigest(
		idempotency.completionSealKey[:], managedRuntimeCapacityDenialVersion,
		job.ID, job.TenantID, job.StackID, effectiveDigest, denial.ReasonCode,
		denial.ProviderID, fmt.Sprintf("%d", denial.Limit), fmt.Sprintf("%d", denial.Held),
	)
}

func writeManagedRuntimeCapacityDenial(
	e *httpx.Event,
	stackID, jobID string,
	denial managedRuntimeCapacityDenial,
) error {
	details := monthlyruntime.ManagedRuntimeCapacityReservationDenialDetails(denial.ProviderID, denial.Limit, denial.Held)
	details[managedRuntimeDetailsJobIDKey] = jobID
	details["stack_id"] = stackID
	return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
		"This account already holds the maximum number of managed server slots", details)
}

func managedRuntimeRequestFromEffectiveIntent(effective managedRuntimeEffectiveIntent) managedRuntimeServerRequest {
	return managedRuntimeServerRequest{
		NodeRole:          effective.NodeRole,
		RuntimeOfferingID: effective.RuntimeOfferingID,
		ProviderID:        effective.ProviderID,
		ProviderRegion:    effective.ProviderRegion,
		IONOSDatacenter:   effective.ProviderRegion,
		StackKit:          effective.StackKit,
		RuntimeSlotKey:    effective.RuntimeSlotKey,
		Services:          append([]string(nil), effective.Services...),
	}
}

func managedRuntimeLeaseRequest(effective managedRuntimeEffectiveIntent, operationKey string) jobruntime.ManagedLeaseRequest {
	return jobruntime.ManagedLeaseRequest{
		StackID:               effective.StackID,
		StackName:             effective.StackName,
		StackKit:              effective.StackKit,
		TenantID:              effective.TenantID,
		OwnerID:               effective.OwnerSubjectID,
		Provider:              effective.ProviderID,
		OperationKey:          strings.TrimSpace(operationKey),
		RuntimeSlotKey:        effective.RuntimeSlotKey,
		RuntimeSlotGeneration: effective.RuntimeSlotGeneration,
		NodeRole:              effective.NodeRole,
		Services:              append([]string(nil), effective.Services...),
		Metadata: map[string]string{
			"runtime_offering_id": effective.RuntimeOfferingID,
			"provider_region":     effective.ProviderRegion,
		},
	}
}

func writeManagedRuntimeAdmissionRecheckDenial(
	e *httpx.Event,
	effective managedRuntimeEffectiveIntent,
	job *controlplane.Job,
	reasonCode string,
	retryable bool,
	title string,
	body string,
	nextSteps []string,
) error {
	jobID := ""
	if job != nil {
		jobID = strings.TrimSpace(job.ID)
	}
	details := managedRuntimePreflightDetails(
		effective, "", reasonCode, retryable, title, body, nextSteps,
	)
	if guidance, ok := details["user_guidance"].(map[string]any); ok {
		guidance["body"] = strings.TrimSpace(body + " This attempt returned before its own native write. The existing running job remains the only reconciliation identity because another concurrent same-key attempt may still be completing. Retry only with the same Idempotency-Key.")
		guidance["next_steps"] = append(
			[]string{"Retry this running operation with the same Idempotency-Key; do not create a replacement operation."},
			nextSteps...,
		)
	}
	if jobID != "" {
		details[managedRuntimeDetailsJobIDKey] = jobID
	}
	details["job_state"] = "running"
	details["retry_scope"] = "exact_same_idempotency_key"
	details["attempt_outcome"] = "rejected_before_native_write"
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
		title, details)
}

func (h stackLifecycleRouteHandlers) preflightManagedRuntimeServer(
	e *httpx.Event,
	effective managedRuntimeEffectiveIntent,
	operationKey string,
	persistedJobID string,
) (bool, error) {
	preflight, ok := h.managedLeases.(jobruntime.ManagedLeaseAdmissionPreflighter)
	if !ok || preflight == nil {
		return true, writeManagedRuntimePreflightUnavailable(
			e, effective, persistedJobID, "native_admission_preflight_unavailable", true,
			"Managed server availability could not be verified",
			"This deployment does not have the required native admission preflight.",
			[]string{"Retry after the TechStack runtime has been updated.", "Contact kombify support if this state persists."},
		)
	}
	err := preflight.PreflightCreateOrBindLease(e.Request.Context(), managedRuntimeLeaseRequest(effective, operationKey))
	if err == nil {
		return false, nil
	}
	var capacity *providercontrol.ManagedRuntimeCapacityExceededError
	if errors.As(err, &capacity) && capacity != nil &&
		strings.TrimSpace(capacity.TenantID) == effective.TenantID &&
		strings.TrimSpace(capacity.OwnerSubjectID) == effective.OwnerSubjectID &&
		capacity.Limit > 0 && capacity.Held >= capacity.Limit {
		details := monthlyruntime.ManagedRuntimeCapacityReservationDenialDetails(
			effective.ProviderID, capacity.Limit, capacity.Held,
		)
		details["stack_id"] = effective.StackID
		appendManagedRuntimePreflightState(details, persistedJobID)
		return true, httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"This account already holds the maximum number of managed server slots", details)
	}
	if errors.Is(err, providercontrol.ErrManagedRuntimeCapacityPolicyUnavailable) ||
		errors.Is(err, monthlyruntime.ErrCapacityPolicyAuthorityUnavailable) {
		return true, writeManagedRuntimePreflightUnavailable(
			e, effective, persistedJobID, "managed_runtime_capacity_policy_unavailable", true,
			"Managed server entitlement could not be verified",
			"The current account entitlement and managed-server budget could not be verified.",
			[]string{"Retry after the account decision has refreshed.", "Contact kombify support if the account should include managed servers."},
		)
	}
	if errors.Is(err, providercontrol.ErrMutationActivationBlocked) {
		return true, writeManagedRuntimePreflightUnavailable(
			e, effective, persistedJobID, "provider_control_not_activated", false,
			"Managed provider provisioning is not active",
			"This TechStack deployment has not activated its certified native provider path.",
			[]string{"Use a deployment with certified provider control enabled.", "Contact kombify support for activation status."},
		)
	}
	if errors.Is(err, providercontrol.ErrManagedRuntimeUnclassifiedCustody) {
		return true, writeManagedRuntimePreflightUnavailable(
			e, effective, persistedJobID, "provider_custody_reconciliation_required", false,
			"Existing provider custody must be reconciled first",
			"This stack already has a managed lease or server without a verified runtime-slot binding. TechStack will not guess ownership or create another provider resource.",
			[]string{"Do not create a replacement server.", "Ask kombify support to reconcile the existing lease/server with immutable provider evidence."},
		)
	}
	var createBlocked providercontrol.ProviderCreateBlockedError
	if errors.As(err, &createBlocked) {
		return true, writeManagedRuntimePreflightUnavailable(
			e, effective, persistedJobID, createBlocked.ReasonCode, false,
			"New managed servers are temporarily paused for this provider",
			"The provider-specific create safety switch is closed. Existing cleanup and reconciliation remain available.",
			[]string{"Retry with the same server slot after provider create is re-enabled.", "Contact kombify support with the provider and stack ID."},
		)
	}
	if errors.Is(err, providercontrol.ErrNativeAdmissionConflict) {
		details := managedRuntimePreflightDetails(
			effective, persistedJobID, "native_admission_conflict", false,
			"Managed server request conflicts with an existing operation",
			"The same operation identity is already bound to different managed-server intent.",
			[]string{"Retry with the original request body and Idempotency-Key.", "Use a new Idempotency-Key only for genuinely new intent."},
		)
		return true, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Native managed runtime admission conflicts with the persisted operation", details)
	}
	return true, writeManagedRuntimePreflightUnavailable(
		e, effective, persistedJobID, "native_admission_preflight_unavailable", true,
		"Managed server availability could not be verified",
		"The native admission preflight did not return a conclusive result.",
		[]string{"Retry the request.", "Contact kombify support if this state persists."},
	)
}

func writeManagedRuntimePreflightUnavailable(
	e *httpx.Event,
	effective managedRuntimeEffectiveIntent,
	persistedJobID string,
	reasonCode string,
	retryable bool,
	title string,
	body string,
	nextSteps []string,
) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, title,
		managedRuntimePreflightDetails(effective, persistedJobID, reasonCode, retryable, title, body, nextSteps))
}

func managedRuntimePreflightDetails(
	effective managedRuntimeEffectiveIntent,
	persistedJobID string,
	reasonCode string,
	retryable bool,
	title string,
	body string,
	nextSteps []string,
) map[string]any {
	details := map[string]any{
		"error_code":        "managed_runtime_preflight_unavailable",
		"reason_code":       reasonCode,
		"capability":        monthlyruntime.ManagedRuntimeCapability,
		"provider_id":       effective.ProviderID,
		"required_features": monthlyruntime.RequiredFeatureKeysForProvider(effective.ProviderID),
		"missing_features":  []string{},
		"retryable":         retryable,
		"stack_id":          effective.StackID,
		"user_guidance": map[string]any{
			"title":      title,
			"body":       body,
			"next_steps": append([]string(nil), nextSteps...),
		},
		"remediation": "Restore the request-bound entitlement decision and certified native provider-control availability before retrying.",
		"support_context": map[string]any{
			"feature_source": "Stripe/FGA/Flagship entitlement chain",
			"cost_bearing":   true,
		},
	}
	appendManagedRuntimePreflightState(details, persistedJobID)
	return details
}

func appendManagedRuntimePreflightState(details map[string]any, persistedJobID string) {
	if details == nil {
		return
	}
	persistedJobID = strings.TrimSpace(persistedJobID)
	workflowState := "No job was queued."
	if persistedJobID != "" {
		details[managedRuntimeDetailsJobIDKey] = persistedJobID
		workflowState = "The existing job remains pending and no native admission was written; retry with the same Idempotency-Key."
	}
	guidance, _ := details["user_guidance"].(map[string]any)
	if guidance == nil {
		return
	}
	body, _ := guidance["body"].(string)
	guidance["body"] = strings.TrimSpace(body + " " + workflowState)
	steps, _ := guidance["next_steps"].([]string)
	if persistedJobID != "" {
		steps = append([]string{"Retry this pending operation with the same Idempotency-Key; do not create a replacement operation."}, steps...)
	}
	guidance["next_steps"] = steps
}

func managedRuntimeJobMatchesEffectiveIntent(job *controlplane.Job, identity managedRuntimeIdempotency, effective managedRuntimeEffectiveIntent) bool {
	if job == nil || effective.Version != managedRuntimeEffectiveIntentVersion ||
		job.TenantID != effective.TenantID || job.StackID != effective.StackID {
		return false
	}
	digest, err := managedRuntimeEffectiveIntentDigest(effective)
	if err != nil {
		return false
	}
	storedDigest, _ := jobResultString(job, "effective_intent_sha256")
	storedOwner, _ := jobResultString(job, "owner_subject_id")
	return storedDigest == digest && storedOwner == effective.OwnerSubjectID &&
		managedRuntimeEffectiveIntentReceiptValid(job, identity, effective)
}

func managedRuntimeCompletionReceipt(job *controlplane.Job, result map[string]any, identity managedRuntimeIdempotency) string {
	if job == nil {
		return ""
	}
	value := func(key string) string {
		text, _ := result[key].(string)
		return text
	}
	return managedRuntimeHMACDigest(
		identity.completionSealKey[:],
		managedRuntimeCompletionReceiptVersion,
		job.ID,
		job.TenantID,
		job.StackID,
		identity.IdentityDigest,
		identity.ClientDigest,
		value("effective_intent_sha256"),
		value("effective_intent_hmac_sha256"),
		value("lease_id"),
		value("runtime_slot_key"),
		value("runtime_slot_id"),
		value("runtime_server_id"),
		value("resource_generation_id"),
		value("operation_id"),
		value("provider_id"),
		value("provider_region"),
		value("ionos_datacenter"),
		value("server_node_role"),
		value("runtime_offering_id"),
		value("runtime_phase"),
		value("enrollment_status"),
		value("desired_state"),
		value("billing_mode"),
	)
}

func managedRuntimeCompletionReceiptValid(job *controlplane.Job, identity managedRuntimeIdempotency) bool {
	if job == nil || job.Result == nil {
		return false
	}
	stored, _ := jobResultString(job, "completion_receipt_hmac_sha256")
	want := managedRuntimeCompletionReceipt(job, job.Result, identity)
	return managedRuntimeHMACEqual(stored, want)
}

func managedRuntimeResponseFromAdmission(jobID string, effective managedRuntimeEffectiveIntent, admission *jobruntime.ManagedLeaseResult, replay bool) ManagedRuntimeExpansionResult {
	return ManagedRuntimeExpansionResult{
		KitDeploymentID: effective.StackID, JobID: jobID,
		RuntimeSlotKey: admission.RuntimeSlotKey, RuntimeSlotID: admission.RuntimeSlotID,
		LeaseID: admission.LeaseID, RuntimeServerID: admission.RuntimeServerID,
		ResourceGenerationID: admission.ResourceGenerationID, OperationID: admission.OperationID,
		ProviderID: admission.Provider, ProviderRegion: effective.ProviderRegion,
		IONOSDatacenter: effective.ProviderRegion, NodeRole: effective.NodeRole,
		RuntimeOfferingID: effective.RuntimeOfferingID,
		EnrollmentStatus:  monthlyRuntimeEnrollmentStatusPending,
		RuntimePhase:      string(admission.Phase), IdempotentReplay: replay,
		Message: "Native managed runtime admission accepted; provisioning is pending",
	}
}

func managedRuntimeResponseFromJob(job *controlplane.Job, replay bool) (ManagedRuntimeExpansionResult, error) {
	if job == nil {
		return ManagedRuntimeExpansionResult{}, errManagedRuntimeReplayCorrupt
	}
	stringValue := func(key string) string {
		value, _ := job.Result[key].(string)
		return strings.TrimSpace(value)
	}
	response := ManagedRuntimeExpansionResult{
		KitDeploymentID: stringValue("stack_id"), JobID: job.ID,
		RuntimeSlotKey: stringValue("runtime_slot_key"), RuntimeSlotID: stringValue("runtime_slot_id"),
		LeaseID: stringValue("lease_id"), RuntimeServerID: stringValue("runtime_server_id"),
		ResourceGenerationID: stringValue("resource_generation_id"), OperationID: stringValue("operation_id"),
		ProviderID: stringValue("provider_id"), ProviderRegion: stringValue("provider_region"),
		IONOSDatacenter: stringValue("ionos_datacenter"), NodeRole: stringValue("server_node_role"),
		RuntimeOfferingID: stringValue("runtime_offering_id"), EnrollmentStatus: stringValue("enrollment_status"),
		RuntimePhase: stringValue("runtime_phase"), IdempotentReplay: replay,
		Message:  "Native managed runtime admission accepted; provisioning is pending",
		Warnings: managedRuntimeResultStrings(job.Result["warnings"]),
	}
	if response.KitDeploymentID == "" || response.JobID == "" || response.RuntimeSlotKey == "" || response.RuntimeSlotID == "" ||
		response.LeaseID == "" || response.RuntimeServerID == "" ||
		response.ResourceGenerationID == "" || response.OperationID == "" || response.ProviderID == "" ||
		response.NodeRole == "" || response.RuntimeOfferingID == "" || response.EnrollmentStatus == "" || response.RuntimePhase == "" {
		return ManagedRuntimeExpansionResult{}, errManagedRuntimeReplayCorrupt
	}
	return response, nil
}

func managedRuntimeResponseMatchesEffectiveIntent(response ManagedRuntimeExpansionResult, effective managedRuntimeEffectiveIntent) bool {
	return response.KitDeploymentID == effective.StackID && response.RuntimeSlotKey == effective.RuntimeSlotKey &&
		response.ProviderID == effective.ProviderID &&
		response.ProviderRegion == effective.ProviderRegion && response.NodeRole == effective.NodeRole &&
		response.RuntimeOfferingID == effective.RuntimeOfferingID
}

func managedRuntimeResultStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func writeManagedRuntimeIdempotencyError(e *httpx.Event, stackID, jobID string, err error) error {
	details := map[string]any{
		"stack_id":                        stackID,
		managedRuntimeDetailsRetryableKey: false,
	}
	if jobID != "" {
		details[managedRuntimeDetailsJobIDKey] = jobID
	}
	if errors.Is(err, errManagedRuntimeIdempotencyConflict) {
		details[managedRuntimeDetailsReasonCodeKey] = "idempotency_conflict"
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Idempotency-Key was already used for a different Add-Server request", details)
	}
	details[managedRuntimeDetailsReasonCodeKey] = "idempotency_state_invalid"
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
		"Managed runtime replay state is incomplete and requires reconciliation", details)
}

func canonicalManagedRuntimeRequestRegion(request managedRuntimeServerRequest) string {
	if request.ProviderID != providercatalog.ProviderIONOS {
		return ""
	}
	return strings.TrimSpace(firstNonEmptyString(request.IONOSDatacenter, request.ProviderRegion))
}

func canonicalOptionalManagedRuntimeStackKit(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return normalizeLifecycleStackKit(value, registryCloudKit)
}

func sortedManagedRuntimeServices(values []string) []string {
	services := normalizedServiceKeys(values)
	sort.Strings(services)
	if services == nil {
		return []string{}
	}
	return services
}

func managedRuntimeStructMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func managedRuntimeJSONDigest(domain string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode managed runtime idempotency intent: %w", err)
	}
	digest := managedRuntimeLengthEncodedDigest(domain, string(encoded))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func managedRuntimeLengthEncodedDigest(domain string, fields ...string) [sha256.Size]byte {
	hash := sha256.New()
	writeManagedRuntimeDigestField(hash, domain)
	for _, field := range fields {
		writeManagedRuntimeDigestField(hash, field)
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func managedRuntimeHMACDigest(key []byte, domain string, fields ...string) string {
	mac := hmac.New(sha256.New, key)
	writeManagedRuntimeDigestField(mac, domain)
	for _, field := range fields {
		writeManagedRuntimeDigestField(mac, field)
	}
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func managedRuntimeHMACEqual(left, right string) bool {
	const prefix = "hmac-sha256:"
	if !strings.HasPrefix(left, prefix) || !strings.HasPrefix(right, prefix) {
		return false
	}
	leftBytes, leftErr := hex.DecodeString(strings.TrimPrefix(left, prefix))
	rightBytes, rightErr := hex.DecodeString(strings.TrimPrefix(right, prefix))
	return leftErr == nil && rightErr == nil && len(leftBytes) == sha256.Size && len(rightBytes) == sha256.Size &&
		hmac.Equal(leftBytes, rightBytes)
}

type managedRuntimeDigestWriter interface {
	Write([]byte) (int, error)
}

func writeManagedRuntimeDigestField(writer managedRuntimeDigestWriter, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}
