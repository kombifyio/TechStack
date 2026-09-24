package providercontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

const (
	maxProvisionDiscoveryCandidates = 64
	maxProvisionDiscoveryResources  = 512
	maxProvisionDiscoveryEvidence   = 1024
	maxProvisionResolutionRefBytes  = 2048
)

var (
	// ErrProvisionResolutionConflict identifies a replay whose request digest,
	// expected head, or expected resolution revision does not match custody.
	ErrProvisionResolutionConflict = errors.New("providercontrol: provision resolution conflict")
	// ErrProvisionResolutionEvidence identifies discovery or operator evidence
	// which cannot authorize a local custody decision.
	ErrProvisionResolutionEvidence = errors.New("providercontrol: provision resolution evidence is not verified")
)

// ProvisionResolutionOutcome is TechStack-local decision state. It is not a
// providerexecutor receipt phase and never authorizes another provider call.
type ProvisionResolutionOutcome string

const (
	ProvisionResolutionNoCandidateObserved           ProvisionResolutionOutcome = "no_candidate_observed"
	ProvisionResolutionAdoptedExactCandidate         ProvisionResolutionOutcome = "adopted_exact_candidate"
	ProvisionResolutionMultipleCandidatesQuarantined ProvisionResolutionOutcome = "multiple_candidates_quarantined"
)

// ProvisionCandidateGraph is one complete provider-resource candidate. A
// candidate is a graph, not one resource row. GraphDigest is derived after the
// providerexecutor contract has normalized and validated Resources.
type ProvisionCandidateGraph struct {
	GraphDigest string                             `json:"graph_digest"`
	Resources   []providerexecutor.ResourceBinding `json:"resources"`
}

// RecordProvisionDiscoveryRequest is produced by a read-only, certified
// adapter discovery path. Operator-facing transports must not deserialize it:
// candidate handles and counts are server-produced evidence.
type RecordProvisionDiscoveryRequest struct {
	TenantID                   string
	OperationID                string
	ExpectedHeadSequence       uint64
	ExpectedHeadReceiptDigest  string
	ExpectedResolutionRevision uint64
	RequestedBySubjectID       string
	IdempotencyKey             string
	ObservationRef             string
	ObservationDigest          string
	AttestationRef             string
	AttestationDigest          string
	CollectedAt                time.Time
	Candidates                 []ProvisionCandidateGraph
}

// ProvisionDiscoveryObservation is an immutable, secret-free provider
// discovery snapshot bound to one guarded accepted head.
type ProvisionDiscoveryObservation struct {
	TenantID                   string                    `json:"tenant_id"`
	OperationID                string                    `json:"operation_id"`
	ObservationID              string                    `json:"observation_id"`
	LeaseID                    string                    `json:"lease_id"`
	LeaseRevision              uint64                    `json:"lease_revision"`
	RuntimeServerID            string                    `json:"runtime_server_id"`
	ResourceGenerationID       string                    `json:"resource_generation_id"`
	HeadSequence               uint64                    `json:"head_sequence"`
	HeadReceiptDigest          string                    `json:"head_receipt_digest"`
	ObservedResolutionRevision uint64                    `json:"observed_resolution_revision"`
	AdapterManifestHash        string                    `json:"adapter_manifest_hash"`
	PreparedBinding            PreparedProvisionBinding  `json:"prepared_binding"`
	GuardedAt                  time.Time                 `json:"guarded_at"`
	RequestedBySubjectID       string                    `json:"requested_by_subject_id"`
	IdempotencyKey             string                    `json:"idempotency_key"`
	RequestDigest              string                    `json:"request_digest"`
	ObservationRef             string                    `json:"observation_ref"`
	ObservationDigest          string                    `json:"observation_digest"`
	AttestationRef             string                    `json:"attestation_ref"`
	AttestationDigest          string                    `json:"attestation_digest"`
	CollectedAt                time.Time                 `json:"collected_at"`
	Candidates                 []ProvisionCandidateGraph `json:"candidates"`
	SnapshotDigest             string                    `json:"snapshot_digest"`
	RecordedAt                 time.Time                 `json:"recorded_at"`
}

// ProvisionResolutionRequest contains no candidate count, outcome, handles,
// or resource graph. It can only select an already immutable observation.
type ProvisionResolutionRequest struct {
	TenantID                   string `json:"tenant_id"`
	OperationID                string `json:"operation_id"`
	ObservationID              string `json:"observation_id"`
	ObservationSnapshotDigest  string `json:"observation_snapshot_digest"`
	ExpectedHeadSequence       uint64 `json:"expected_head_sequence"`
	ExpectedHeadReceiptDigest  string `json:"expected_head_receipt_digest"`
	ExpectedResolutionRevision uint64 `json:"expected_resolution_revision"`
	IdempotencyKey             string `json:"idempotency_key"`
	OperatorSubjectID          string `json:"operator_subject_id"`
	OperatorAttestationRef     string `json:"operator_attestation_ref"`
	OperatorAttestationDigest  string `json:"operator_attestation_digest"`
	RequestDigest              string `json:"request_digest"`
}

// ProvisionResolutionDecision is an append-only local custody decision. Only
// the exact-one outcome may reference a new resources-bound receipt.
type ProvisionResolutionDecision struct {
	TenantID                  string                     `json:"tenant_id"`
	OperationID               string                     `json:"operation_id"`
	ResolutionRevision        uint64                     `json:"resolution_revision"`
	ObservationID             string                     `json:"observation_id"`
	ObservationSnapshotDigest string                     `json:"observation_snapshot_digest"`
	ExpectedHeadSequence      uint64                     `json:"expected_head_sequence"`
	ExpectedHeadReceiptDigest string                     `json:"expected_head_receipt_digest"`
	Outcome                   ProvisionResolutionOutcome `json:"outcome"`
	SelectedCandidateDigest   string                     `json:"selected_candidate_digest,omitempty"`
	OperatorSubjectID         string                     `json:"operator_subject_id"`
	OperatorAttestationRef    string                     `json:"operator_attestation_ref"`
	OperatorAttestationDigest string                     `json:"operator_attestation_digest"`
	IdempotencyKey            string                     `json:"idempotency_key"`
	RequestDigest             string                     `json:"request_digest"`
	DecisionDigest            string                     `json:"decision_digest"`
	ResultReceiptSequence     uint64                     `json:"result_receipt_sequence,omitempty"`
	ResultReceiptDigest       string                     `json:"result_receipt_digest,omitempty"`
	DecidedAt                 time.Time                  `json:"decided_at"`
}

// ProvisionResolutionVerifier verifies snapshot-level discovery evidence and
// operator attestations. providerexecutor evidence alone cannot represent an
// empty provider search.
type ProvisionResolutionVerifier interface {
	VerifyProvisionDiscovery(context.Context, OperationRecord, ProvisionDiscoveryObservation) error
	VerifyProvisionDecision(context.Context, OperationRecord, ProvisionDiscoveryObservation, ProvisionResolutionRequest) error
}

type provisionDiscoveryBinding struct {
	ObservationID        string
	LeaseID              string
	LeaseRevision        uint64
	RuntimeServerID      string
	ResourceGenerationID string
	HeadSequence         uint64
	HeadReceiptDigest    string
	AdapterManifestHash  string
	PreparedBinding      PreparedProvisionBinding
	GuardedAt            time.Time
	ResolutionRevision   uint64
}

func sealProvisionDiscoveryObservation(
	ctx context.Context,
	record OperationRecord,
	binding provisionDiscoveryBinding,
	req RecordProvisionDiscoveryRequest,
	verifier ProvisionResolutionVerifier,
) (ProvisionDiscoveryObservation, error) {
	if err := validateProvisionDiscoveryObservationAdmission(record, binding, req, verifier); err != nil {
		return ProvisionDiscoveryObservation{}, err
	}
	candidates, err := sealProvisionCandidateGraphs(ctx, record, binding, req)
	if err != nil {
		return ProvisionDiscoveryObservation{}, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].GraphDigest < candidates[j].GraphDigest })

	observation := ProvisionDiscoveryObservation{
		TenantID: req.TenantID, OperationID: req.OperationID, ObservationID: binding.ObservationID,
		LeaseID: binding.LeaseID, LeaseRevision: binding.LeaseRevision,
		RuntimeServerID: binding.RuntimeServerID, ResourceGenerationID: binding.ResourceGenerationID,
		HeadSequence: binding.HeadSequence, HeadReceiptDigest: binding.HeadReceiptDigest,
		ObservedResolutionRevision: binding.ResolutionRevision,
		AdapterManifestHash:        binding.AdapterManifestHash, PreparedBinding: binding.PreparedBinding,
		GuardedAt: binding.GuardedAt, RequestedBySubjectID: strings.TrimSpace(req.RequestedBySubjectID),
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), ObservationRef: strings.TrimSpace(req.ObservationRef),
		ObservationDigest: strings.TrimSpace(req.ObservationDigest), AttestationRef: strings.TrimSpace(req.AttestationRef),
		AttestationDigest: strings.TrimSpace(req.AttestationDigest), CollectedAt: req.CollectedAt.UTC(), Candidates: candidates,
	}
	observation.RequestDigest = provisionDiscoveryRequestDigest(req)
	observation.SnapshotDigest = provisionDiscoverySnapshotDigest(observation)
	if err := verifier.VerifyProvisionDiscovery(ctx, record, observation); err != nil {
		return ProvisionDiscoveryObservation{}, provisionResolutionVerificationError(ctx, err)
	}
	return observation, nil
}

func validateProvisionDiscoveryObservationAdmission(
	record OperationRecord,
	binding provisionDiscoveryBinding,
	req RecordProvisionDiscoveryRequest,
	verifier ProvisionResolutionVerifier,
) error {
	if verifier == nil {
		return ErrEvidenceVerifier
	}
	if err := validateResolutionID("tenant_id", req.TenantID); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"operation_id": req.OperationID, "observation_id": binding.ObservationID,
		"requested_by_subject_id": req.RequestedBySubjectID, "idempotency_key": req.IdempotencyKey,
	} {
		if err := validateResolutionID(name, value); err != nil {
			return err
		}
	}
	if !provisionDiscoveryContextMatches(record, binding, req) {
		return ErrProvisionResolutionConflict
	}
	if err := validateProvisionResolutionHead(record); err != nil {
		return err
	}
	if err := validateProvisionResolutionGuard(record, binding); err != nil {
		return err
	}
	if !provisionDiscoveryBindingMatchesRecord(record, binding) {
		return ErrProvisionResolutionConflict
	}
	if err := validateSnapshotEnvelope(req.ObservationRef, req.ObservationDigest, req.AttestationRef, req.AttestationDigest); err != nil {
		return err
	}
	if !provisionDiscoveryCollectedAfterGuard(record, binding, req.CollectedAt) {
		return fmt.Errorf("%w: discovery must be collected after guarded dispatch", ErrProvisionResolutionEvidence)
	}
	if len(req.Candidates) > maxProvisionDiscoveryCandidates {
		return fmt.Errorf("%w: discovery has more than %d candidates", ErrInvalidRequest, maxProvisionDiscoveryCandidates)
	}
	if !provisionDiscoveryCandidatesMatchTerminalHead(record, req.Candidates) {
		return fmt.Errorf(
			"%w: terminal AMO failure settlement requires an exact zero-candidate observation",
			ErrProvisionResolutionEvidence,
		)
	}
	return nil
}

func provisionDiscoveryContextMatches(
	record OperationRecord,
	binding provisionDiscoveryBinding,
	req RecordProvisionDiscoveryRequest,
) bool {
	return req.TenantID == record.Command.TenantID && req.OperationID == record.Command.OperationID &&
		req.ExpectedHeadSequence == record.Head.Sequence && req.ExpectedHeadReceiptDigest == record.Head.ReceiptDigest &&
		req.ExpectedResolutionRevision == binding.ResolutionRevision
}

func provisionDiscoveryBindingMatchesRecord(record OperationRecord, binding provisionDiscoveryBinding) bool {
	return binding.LeaseID == record.Command.LeaseID && binding.LeaseRevision == record.Command.LeaseRevision &&
		binding.RuntimeServerID == record.Command.RuntimeServerID &&
		binding.ResourceGenerationID == record.Command.ResourceGenerationID &&
		binding.AdapterManifestHash == record.ExecutionProfile.AdapterManifestHash
}

func provisionDiscoveryCollectedAfterGuard(
	record OperationRecord,
	binding provisionDiscoveryBinding,
	collectedAt time.Time,
) bool {
	return !collectedAt.IsZero() && !collectedAt.Before(binding.GuardedAt) && !collectedAt.Before(record.Command.RequestedAt)
}

func provisionDiscoveryCandidatesMatchTerminalHead(
	record OperationRecord,
	candidates []ProvisionCandidateGraph,
) bool {
	return !isTerminalHandleFreeAMOProvisionHead(record) || len(candidates) == 0
}

func sealProvisionCandidateGraphs(
	ctx context.Context,
	record OperationRecord,
	binding provisionDiscoveryBinding,
	req RecordProvisionDiscoveryRequest,
) ([]ProvisionCandidateGraph, error) {
	candidates := make([]ProvisionCandidateGraph, len(req.Candidates))
	seen := make(map[string]struct{}, len(req.Candidates))
	totalResources := 0
	totalEvidence := 0
	for index, candidate := range req.Candidates {
		normalized, err := sealProvisionCandidateGraph(ctx, record, binding.GuardedAt, req.CollectedAt, candidate.Resources)
		if err != nil {
			return nil, err
		}
		if candidate.GraphDigest != "" && candidate.GraphDigest != normalized.GraphDigest {
			return nil, fmt.Errorf("%w: candidate graph digest mismatch", ErrProvisionResolutionEvidence)
		}
		if _, duplicate := seen[normalized.GraphDigest]; duplicate {
			return nil, fmt.Errorf("%w: duplicate candidate graph", ErrProvisionResolutionEvidence)
		}
		seen[normalized.GraphDigest] = struct{}{}
		candidates[index] = normalized
		totalResources += len(normalized.Resources)
		for _, resource := range normalized.Resources {
			totalEvidence += len(resource.Evidence)
		}
		if totalResources > maxProvisionDiscoveryResources || totalEvidence > maxProvisionDiscoveryEvidence {
			return nil, fmt.Errorf(
				"%w: discovery exceeds the bounded resource or evidence budget", ErrInvalidRequest,
			)
		}
	}
	return candidates, nil
}

func provisionResolutionVerificationError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	// A verifier may contain provider or attestation diagnostics. Keep those
	// details out of the application contract while retaining a stable class.
	return ErrProvisionResolutionEvidence
}

func sealProvisionCandidateGraph(
	ctx context.Context,
	record OperationRecord,
	guardedAt, collectedAt time.Time,
	resources []providerexecutor.ResourceBinding,
) (ProvisionCandidateGraph, error) {
	if len(resources) == 0 {
		return ProvisionCandidateGraph{}, fmt.Errorf("%w: a candidate graph cannot be empty", ErrProvisionResolutionEvidence)
	}
	sealed, err := providerexecutor.AssembleReceipt(ctx, providerexecutor.ExecutionRequest{
		Command: record.Command, Previous: record.Head,
	}, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound, Resources: resources,
	}, collectedAt, nil)
	if err != nil {
		return ProvisionCandidateGraph{}, err
	}
	for _, resource := range sealed.Resources {
		if resource.Observation != providerexecutor.ObservationPresent || len(resource.Evidence) == 0 {
			return ProvisionCandidateGraph{}, fmt.Errorf("%w: candidate resources require provider-native present evidence", ErrProvisionResolutionEvidence)
		}
		for _, evidence := range resource.Evidence {
			if !evidence.Definitive || evidence.Observation != providerexecutor.ObservationPresent ||
				(evidence.Source != providerexecutor.EvidenceSourceProviderAPI && evidence.Source != providerexecutor.EvidenceSourceProviderEvent) ||
				evidence.CollectedAt.Before(guardedAt) {
				return ProvisionCandidateGraph{}, fmt.Errorf("%w: candidate evidence is not a post-dispatch provider observation", ErrProvisionResolutionEvidence)
			}
		}
	}
	payload, err := json.Marshal(sealed.Resources)
	if err != nil {
		return ProvisionCandidateGraph{}, err
	}
	return ProvisionCandidateGraph{GraphDigest: prefixedSHA256("providercontrol/provision-candidate-graph/v1", payload), Resources: sealed.Resources}, nil
}

func sealProvisionResolutionRequest(req ProvisionResolutionRequest) (ProvisionResolutionRequest, error) {
	for name, value := range map[string]string{
		"tenant_id": req.TenantID, "operation_id": req.OperationID, "observation_id": req.ObservationID,
		"idempotency_key": req.IdempotencyKey, "operator_subject_id": req.OperatorSubjectID,
	} {
		if err := validateResolutionID(name, value); err != nil {
			return ProvisionResolutionRequest{}, err
		}
	}
	if req.ExpectedHeadSequence == 0 || req.ExpectedHeadSequence > providerexecutor.MaxJSONSafeInteger {
		return ProvisionResolutionRequest{}, fmt.Errorf("%w: expected head sequence is invalid", ErrInvalidRequest)
	}
	if req.ExpectedResolutionRevision >= providerexecutor.MaxJSONSafeInteger {
		return ProvisionResolutionRequest{}, fmt.Errorf("%w: expected resolution revision is exhausted", ErrInvalidRequest)
	}
	for _, digest := range []string{req.ObservationSnapshotDigest, req.ExpectedHeadReceiptDigest, req.OperatorAttestationDigest} {
		if !executionProfileDigestPattern.MatchString(strings.TrimSpace(digest)) {
			return ProvisionResolutionRequest{}, fmt.Errorf("%w: provision resolution requires lowercase sha256 digests", ErrInvalidRequest)
		}
	}
	if err := validateResolutionRef("operator_attestation_ref", req.OperatorAttestationRef, "provider-attestation"); err != nil {
		return ProvisionResolutionRequest{}, err
	}
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.ObservationID = strings.TrimSpace(req.ObservationID)
	req.ObservationSnapshotDigest = strings.TrimSpace(req.ObservationSnapshotDigest)
	req.ExpectedHeadReceiptDigest = strings.TrimSpace(req.ExpectedHeadReceiptDigest)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.OperatorSubjectID = strings.TrimSpace(req.OperatorSubjectID)
	req.OperatorAttestationRef = strings.TrimSpace(req.OperatorAttestationRef)
	req.OperatorAttestationDigest = strings.TrimSpace(req.OperatorAttestationDigest)
	digest := provisionResolutionRequestDigest(req)
	if req.RequestDigest != "" && req.RequestDigest != digest {
		return ProvisionResolutionRequest{}, ErrProvisionResolutionConflict
	}
	req.RequestDigest = digest
	return req, nil
}

func deriveProvisionResolutionDecision(
	record OperationRecord,
	req ProvisionResolutionRequest,
	observation ProvisionDiscoveryObservation,
	decidedAt time.Time,
) (ProvisionResolutionDecision, error) {
	sealed, err := sealProvisionResolutionRequest(req)
	if err != nil {
		return ProvisionResolutionDecision{}, err
	}
	if !provisionResolutionSnapshotMatches(record, sealed, observation) {
		return ProvisionResolutionDecision{}, ErrProvisionResolutionConflict
	}
	decision := ProvisionResolutionDecision{
		TenantID: sealed.TenantID, OperationID: sealed.OperationID,
		ResolutionRevision: sealed.ExpectedResolutionRevision + 1,
		ObservationID:      observation.ObservationID, ObservationSnapshotDigest: observation.SnapshotDigest,
		ExpectedHeadSequence: sealed.ExpectedHeadSequence, ExpectedHeadReceiptDigest: sealed.ExpectedHeadReceiptDigest,
		OperatorSubjectID: sealed.OperatorSubjectID, OperatorAttestationRef: sealed.OperatorAttestationRef,
		OperatorAttestationDigest: sealed.OperatorAttestationDigest, IdempotencyKey: sealed.IdempotencyKey,
		RequestDigest: sealed.RequestDigest, DecidedAt: decidedAt.UTC(),
	}
	switch len(observation.Candidates) {
	case 0:
		decision.Outcome = ProvisionResolutionNoCandidateObserved
	case 1:
		decision.Outcome = ProvisionResolutionAdoptedExactCandidate
		decision.SelectedCandidateDigest = observation.Candidates[0].GraphDigest
	default:
		decision.Outcome = ProvisionResolutionMultipleCandidatesQuarantined
	}
	decision.DecisionDigest = provisionResolutionDecisionDigest(decision)
	return decision, nil
}

func provisionResolutionSnapshotMatches(
	record OperationRecord,
	sealed ProvisionResolutionRequest,
	observation ProvisionDiscoveryObservation,
) bool {
	if sealed.TenantID != observation.TenantID || sealed.OperationID != observation.OperationID ||
		sealed.ObservationID != observation.ObservationID || sealed.ObservationSnapshotDigest != observation.SnapshotDigest ||
		sealed.ExpectedResolutionRevision != observation.ObservedResolutionRevision {
		return false
	}
	if sealed.ExpectedHeadSequence != record.Head.Sequence || sealed.ExpectedHeadReceiptDigest != record.Head.ReceiptDigest {
		return false
	}
	if isTerminalHandleFreeAMOProvisionHead(record) {
		return len(observation.Candidates) == 0 && observation.HeadSequence+1 == record.Head.Sequence &&
			record.Head.PreviousReceiptDigest == observation.HeadReceiptDigest
	}
	return sealed.ExpectedHeadSequence == observation.HeadSequence && sealed.ExpectedHeadReceiptDigest == observation.HeadReceiptDigest
}

func validateProvisionResolutionHead(record OperationRecord) error {
	if record.ExecutionAuthority != ExecutionAuthorityTechstackProviderControl ||
		record.Command.Operation != providerexecutor.OperationProvision ||
		record.ProvisionDispatch != ProvisionDispatchAtMostOnceManualReconcile ||
		len(record.Head.Resources) != 0 {
		return ErrProvisionManualReconcile
	}
	if record.Head.Status == providerexecutor.StatusPending &&
		record.Head.Phase == providerexecutor.PhaseAccepted &&
		record.AutomationState == OperationAutomationManualReconcileRequired {
		return nil
	}
	if isTerminalHandleFreeAMOProvisionHead(record) {
		return nil
	}
	return ErrProvisionManualReconcile
}

func isTerminalHandleFreeAMOProvisionHead(record OperationRecord) bool {
	return record.ExecutionAuthority == ExecutionAuthorityTechstackProviderControl &&
		record.Command.Operation == providerexecutor.OperationProvision &&
		record.ProvisionDispatch == ProvisionDispatchAtMostOnceManualReconcile &&
		record.Head.Status == providerexecutor.StatusFailed &&
		record.Head.Phase == providerexecutor.PhaseFailed &&
		len(record.Head.Resources) == 0 &&
		record.AutomationState == OperationAutomationComplete
}

func validateProvisionResolutionGuard(record OperationRecord, binding provisionDiscoveryBinding) error {
	if binding.HeadSequence == 0 ||
		binding.HeadSequence > providerexecutor.MaxJSONSafeInteger ||
		binding.ResolutionRevision >= providerexecutor.MaxJSONSafeInteger ||
		!executionProfileDigestPattern.MatchString(binding.HeadReceiptDigest) {
		return ErrProvisionResolutionConflict
	}
	if isTerminalHandleFreeAMOProvisionHead(record) {
		if binding.HeadSequence+1 != record.Head.Sequence ||
			record.Head.PreviousReceiptDigest != binding.HeadReceiptDigest {
			return ErrProvisionResolutionConflict
		}
		return nil
	}
	if binding.HeadSequence != record.Head.Sequence ||
		binding.HeadReceiptDigest != record.Head.ReceiptDigest {
		return ErrProvisionResolutionConflict
	}
	return nil
}

func validateSnapshotEnvelope(observationRef, observationDigest, attestationRef, attestationDigest string) error {
	if err := validateResolutionRef("observation_ref", observationRef, "provider-evidence"); err != nil {
		return err
	}
	if err := validateResolutionRef("attestation_ref", attestationRef, "provider-attestation"); err != nil {
		return err
	}
	if !executionProfileDigestPattern.MatchString(strings.TrimSpace(observationDigest)) ||
		!executionProfileDigestPattern.MatchString(strings.TrimSpace(attestationDigest)) {
		return fmt.Errorf("%w: discovery evidence requires lowercase sha256 digests", ErrInvalidRequest)
	}
	return nil
}

func validateResolutionRef(name, value, scheme string) error {
	value = strings.TrimSpace(value)
	if len(value) > maxProvisionResolutionRefBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidRequest, name, maxProvisionResolutionRefBytes)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != scheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: %s must be an opaque %s reference", ErrInvalidRequest, name, scheme)
	}
	return nil
}

func validateResolutionID(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 {
		return fmt.Errorf("%w: %s is required and must not exceed 256 bytes", ErrInvalidRequest, name)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("%w: %s contains control characters", ErrInvalidRequest, name)
		}
	}
	return nil
}

func provisionDiscoveryRequestDigest(req RecordProvisionDiscoveryRequest) string {
	payload, _ := json.Marshal(struct {
		TenantID, OperationID, ObservationID, HeadDigest, SubjectID, IdempotencyKey string
		HeadSequence, ResolutionRevision                                            uint64
		ObservationRef, ObservationDigest, AttestationRef, AttestationDigest        string
		CollectedAt                                                                 time.Time
		Candidates                                                                  []ProvisionCandidateGraph
	}{strings.TrimSpace(req.TenantID), strings.TrimSpace(req.OperationID), "", strings.TrimSpace(req.ExpectedHeadReceiptDigest),
		strings.TrimSpace(req.RequestedBySubjectID), strings.TrimSpace(req.IdempotencyKey), req.ExpectedHeadSequence,
		req.ExpectedResolutionRevision, strings.TrimSpace(req.ObservationRef), strings.TrimSpace(req.ObservationDigest),
		strings.TrimSpace(req.AttestationRef), strings.TrimSpace(req.AttestationDigest), req.CollectedAt.UTC(), req.Candidates})
	return prefixedSHA256("providercontrol/provision-discovery-request/v1", payload)
}

func provisionDiscoverySnapshotDigest(observation ProvisionDiscoveryObservation) string {
	payload, _ := json.Marshal(struct {
		RequestDigest, LeaseID                                     string
		LeaseRevision                                              uint64
		RuntimeServerID, ResourceGenerationID, AdapterManifestHash string
		PreparedBinding                                            PreparedProvisionBinding
		GuardedAt                                                  time.Time
	}{observation.RequestDigest, observation.LeaseID, observation.LeaseRevision, observation.RuntimeServerID,
		observation.ResourceGenerationID, observation.AdapterManifestHash, observation.PreparedBinding, observation.GuardedAt})
	return prefixedSHA256("providercontrol/provision-discovery-snapshot/v1", payload)
}

func provisionResolutionRequestDigest(req ProvisionResolutionRequest) string {
	req.RequestDigest = ""
	payload, _ := json.Marshal(req)
	return prefixedSHA256("providercontrol/provision-resolution-request/v1", payload)
}

func provisionResolutionDecisionDigest(decision ProvisionResolutionDecision) string {
	payload, _ := json.Marshal(struct {
		TenantID, OperationID, ObservationID, ObservationSnapshotDigest               string
		ResolutionRevision, ExpectedHeadSequence, ResultReceiptSequence               uint64
		ExpectedHeadReceiptDigest                                                     string
		Outcome                                                                       ProvisionResolutionOutcome
		SelectedCandidateDigest, OperatorSubjectID, OperatorAttestationRef            string
		OperatorAttestationDigest, IdempotencyKey, RequestDigest, ResultReceiptDigest string
	}{
		decision.TenantID, decision.OperationID, decision.ObservationID, decision.ObservationSnapshotDigest,
		decision.ResolutionRevision, decision.ExpectedHeadSequence, decision.ResultReceiptSequence,
		decision.ExpectedHeadReceiptDigest, decision.Outcome, decision.SelectedCandidateDigest,
		decision.OperatorSubjectID, decision.OperatorAttestationRef, decision.OperatorAttestationDigest,
		decision.IdempotencyKey, decision.RequestDigest, decision.ResultReceiptDigest,
	})
	return prefixedSHA256("providercontrol/provision-resolution-decision/v1", payload)
}

func prefixedSHA256(domain string, payload []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(payload)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
