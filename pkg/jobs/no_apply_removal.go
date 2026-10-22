package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

const (
	// NoApplyRemovalEvidenceResultField holds the admission-time proof that no
	// StackKits Apply ever ran for the stack being destroyed. Only the destroy
	// admission writes it.
	NoApplyRemovalEvidenceResultField = "no_apply_removal_evidence"
	// DestroyWorkspaceStateNoApplyRemoval records that the control plane held a
	// workspace but ran no StackKits removal, because nothing was ever applied.
	DestroyWorkspaceStateNoApplyRemoval = "no_apply_removal"

	// NoApplyRemovalBasisJobHistory is the proof class: every job of the stack
	// is either inert, never started, or failed at a position before Apply.
	NoApplyRemovalBasisJobHistory = "job_history_no_apply"

	noApplyRemovalEvidenceVersion = "techstack.no-apply-stack-removal/v1"

	// NoApplyJobScanLimit bounds the job history read to prove that no Apply
	// ran. A history that fills it cannot be proven complete and is refused.
	NoApplyJobScanLimit = 500
)

// ErrLocalTeardownNoAppliedWorkloads reports that a stack has no applied
// workload to build a node teardown authority from. It is what makes a removal
// without node teardown a candidate; the job history decides whether it is
// allowed.
var ErrLocalTeardownNoAppliedWorkloads = errors.New("local StackKits teardown authority has no applied workloads")

// NoApplyJobRecord names one job of the stack that the proof examined and what
// it showed. It is secret-free.
type NoApplyJobRecord struct {
	JobID  string `json:"job_id"`
	Type   string `json:"type"`
	State  string `json:"state"`
	Step   string `json:"step,omitempty"`
	Reason string `json:"reason"`
}

// NoApplyRemovalEvidence authorizes a destroy to proceed without node-side
// execution because the job history proves that StackKits Apply never ran.
type NoApplyRemovalEvidence struct {
	SchemaVersion  string             `json:"schema_version"`
	TechstackID    string             `json:"techstack_id"`
	TenantID       string             `json:"tenant_id"`
	OwnerID        string             `json:"owner_id"`
	Basis          string             `json:"basis"`
	ProvenAt       time.Time          `json:"proven_at"`
	SnapshotDigest string             `json:"snapshot_digest"`
	JobsExamined   int                `json:"jobs_examined"`
	Jobs           []NoApplyJobRecord `json:"jobs"`
}

// inertNoApplyJobTypes never run StackKits Apply on a node.
var inertNoApplyJobTypes = map[string]struct{}{
	string(JobTypeDestroy):          {},
	string(JobTypeDriftCheck):       {},
	string(JobTypeBackup):           {},
	string(JobTypeReconcileLease):   {},
	string(JobTypeRemoteEnrollment): {},
}

// preApplyRolloutSteps are the rollout tasks that all run before the typed
// Apply is dispatched. A rollout that failed at one of them cannot have applied.
var preApplyRolloutSteps = map[string]struct{}{
	StepValidate: {}, StepSaveConfig: {}, StepFindStackKit: {}, StepUnifyServices: {}, StepUnifyNetwork: {},
	StepUnifySecurity: {}, StepUnifyAuth: {}, StepCreateSpec: {}, StepValidateWorkers: {}, StepGenerateUnified: {},
	StepPersistUnified: {}, StepGenerateIaC: {}, StepCreateLease: {}, StepPrepareRollout: {}, StepRuntimeConnected: {},
	StepTelemetryHandshake: {}, StepSimulationGate: {}, StepPortAdmission: {}, StepStackKitPrepare: {},
	StepDockerReady: {}, StepOpenTofuReady: {}, StepTerramateReady: {}, StepTelemetryReady: {},
}

// preApplyTypedOperations are the typed StackKits commands of a rollout that run
// before Apply. A rollout whose recorded failure names one of them stopped
// before Apply was dispatched.
var preApplyTypedOperations = map[string]struct{}{
	StackKitLifecycleAdvancedTrustImport: {}, StackKitLifecycleInit: {}, StackKitLifecycleAddressBind: {},
	StackKitLifecycleGenerate: {}, StackKitLifecyclePlan: {}, originCertificateStep: {},
	stackKitStepPrepareSourceGeneration: {},
}

// NewNoApplyRemovalEvidence proves from the job history of one stack that no
// StackKits Apply ever succeeded, was dispatched or is still running, and binds
// the proof to the exact port-claim snapshot the destroy releases. It fails
// closed: a job whose position cannot be shown to precede Apply, a job still in
// flight, a history that fills the scan limit, or any recorded Apply output
// refuses the removal with the reason.
func NewNoApplyRemovalEvidence(techstackID, tenantID, ownerID string, snapshot portinventory.TeardownSnapshot, history []controlplane.Job, now time.Time) (NoApplyRemovalEvidence, error) {
	if len(history) >= NoApplyJobScanLimit {
		return NoApplyRemovalEvidence{}, fmt.Errorf("the job history of the stack is too long to prove that no Apply ran (%d jobs read)", len(history))
	}
	records := make([]NoApplyJobRecord, 0, len(history))
	for index := range history {
		record, err := classifyNoApplyJob(&history[index])
		if err != nil {
			return NoApplyRemovalEvidence{}, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].JobID < records[j].JobID })
	evidence := NoApplyRemovalEvidence{
		SchemaVersion: noApplyRemovalEvidenceVersion,
		TechstackID:   strings.TrimSpace(techstackID), TenantID: strings.TrimSpace(tenantID), OwnerID: strings.TrimSpace(ownerID),
		Basis: NoApplyRemovalBasisJobHistory, ProvenAt: now.UTC(), SnapshotDigest: snapshot.SnapshotDigest,
		JobsExamined: len(history), Jobs: records,
	}
	if err := evidence.Validate(); err != nil {
		return NoApplyRemovalEvidence{}, err
	}
	return evidence, nil
}

func classifyNoApplyJob(job *controlplane.Job) (NoApplyJobRecord, error) {
	record := NoApplyJobRecord{JobID: job.ID, Type: job.Type, State: strings.ToLower(strings.TrimSpace(job.State)), Step: strings.TrimSpace(job.Step)}
	refuse := func(reason string) (NoApplyJobRecord, error) {
		return NoApplyJobRecord{}, fmt.Errorf("job %s (%s, %s) %s", job.ID, job.Type, record.State, reason)
	}
	if _, inert := inertNoApplyJobTypes[strings.TrimSpace(job.Type)]; inert {
		record.Reason = "inert_job_type"
		return record, nil
	}
	if applied, evidence := recordedApplyEvidence(job.Result); applied {
		return refuse("recorded " + evidence + "; an Apply may have succeeded")
	}
	switch record.State {
	case string(JobStateFailed), string(JobStateCancelled), "cancelled":
	case string(JobStateCompleted):
		// A completed provision only prepared the stack's specification; a
		// completed host-acting job is not shown to have left the host alone.
		if strings.TrimSpace(job.Type) == string(JobTypeProvision) {
			record.Reason = "completed_without_apply_output"
			return record, nil
		}
		return refuse("completed without an applied workload record, so it cannot be shown to have left the host alone")
	default:
		return refuse("is still in flight")
	}
	if job.StartedAt == nil || job.StartedAt.IsZero() {
		record.Reason = "never_started"
		return record, nil
	}
	if operation, recorded := recordedTypedFailureOperation(job.Result); recorded {
		if _, before := preApplyTypedOperations[operation]; !before {
			return refuse(fmt.Sprintf("failed at typed operation %q, which is not shown to precede Apply", operation))
		}
		record.Reason = "failed_before_apply_at_" + operation
		return record, nil
	}
	if _, before := preApplyRolloutSteps[record.Step]; before {
		record.Reason = "failed_before_apply_at_" + record.Step
		return record, nil
	}
	return refuse(fmt.Sprintf("failed at step %q, which is not shown to precede Apply", record.Step))
}

// recordedApplyEvidence reports whether a job result carries any sign of an
// Apply that ran: applied workloads or Apply output, or a rollout result that is
// not pending.
func recordedApplyEvidence(result map[string]any) (bool, string) {
	outputs := mapFromInterface(result[metadataKeyStackKitOutputs])
	if apply := mapFromInterface(outputs["apply"]); len(apply) > 0 {
		return true, "StackKits Apply output"
	}
	proof := mapFromInterface(result["e2e_proof"])
	if state := strings.ToLower(stringFromInterface(proof["rollout_result"])); state != "" && state != string(JobStatePending) {
		return true, "a rollout result of " + state
	}
	if rollout := mapFromInterface(mapFromInterface(result["runtime_proof"])["rollout"]); len(rollout) > 0 {
		return true, "a rollout runtime proof"
	}
	return false, ""
}

func recordedTypedFailureOperation(result map[string]any) (string, bool) {
	failure := mapFromInterface(result["typed_stackkit_rollout"])
	if len(failure) == 0 {
		return "", false
	}
	return stringFromInterface(failure["operation"]), true
}

// Validate checks the evidence is well-formed.
func (evidence NoApplyRemovalEvidence) Validate() error {
	for _, value := range []string{evidence.TechstackID, evidence.TenantID, evidence.OwnerID, evidence.SnapshotDigest} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("no-Apply removal evidence identity is incomplete")
		}
	}
	if evidence.SchemaVersion != noApplyRemovalEvidenceVersion || evidence.Basis != NoApplyRemovalBasisJobHistory ||
		evidence.ProvenAt.IsZero() || evidence.JobsExamined < len(evidence.Jobs) {
		return errors.New("no-Apply removal evidence is invalid")
	}
	return nil
}

// DecodeNoApplyRemovalEvidence restores the typed evidence from the JSON-compatible
// shape stored in a durable job result.
func DecodeNoApplyRemovalEvidence(value any) (NoApplyRemovalEvidence, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return NoApplyRemovalEvidence{}, fmt.Errorf("encode no-Apply removal evidence: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence NoApplyRemovalEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return NoApplyRemovalEvidence{}, fmt.Errorf("decode no-Apply removal evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return NoApplyRemovalEvidence{}, errors.New("no-Apply removal evidence contains trailing data")
	}
	if err := evidence.Validate(); err != nil {
		return NoApplyRemovalEvidence{}, err
	}
	return evidence, nil
}

// boundNoApplyRemovalEvidence returns the destroy job's no-Apply evidence when
// it carries any. It must name this stack, tenant and owner and the exact
// port-claim snapshot the destroy releases.
func boundNoApplyRemovalEvidence(job *Job, snapshot portinventory.TeardownSnapshot) (NoApplyRemovalEvidence, bool, error) {
	if job == nil {
		return NoApplyRemovalEvidence{}, false, errors.New("no-Apply removal job is missing")
	}
	raw, exists := job.Snapshot().Result[NoApplyRemovalEvidenceResultField]
	if !exists {
		return NoApplyRemovalEvidence{}, false, nil
	}
	evidence, err := DecodeNoApplyRemovalEvidence(raw)
	if err != nil {
		return NoApplyRemovalEvidence{}, false, err
	}
	if evidence.TechstackID != job.TargetID || evidence.TenantID != payloadString(job.Payload, tenantIDField) ||
		evidence.OwnerID != payloadString(job.Payload, "owner_id") || evidence.SnapshotDigest != snapshot.SnapshotDigest {
		return NoApplyRemovalEvidence{}, false, errors.New("no-Apply removal evidence does not match the destroy job")
	}
	return evidence, true, nil
}
