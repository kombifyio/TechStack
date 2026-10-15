package jobs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/unifier"
)

// RolloutRetryCandidateSHA256Field is the existing exact recovery receipt's
// native candidate digest, shared by dispatch and its execution boundary.
const RolloutRetryCandidateSHA256Field = "rollout_retry_candidate_sha256"

// RolloutRetryCurrentIntentField carries the validated failed Apply's node
// intent binding so recovery can obtain that document's normalized CAS hash.
const RolloutRetryCurrentIntentField = "rollout_retry_current_intent"

// RolloutRetryCurrentSpecHashField is a sealed caller CAS precondition, never
// authorization. StackKits compares it with the actual node intent at INIT.
const RolloutRetryCurrentSpecHashField = "rollout_retry_current_spec_hash"

func ValidRolloutRetryCurrentSpecHash(value string) bool {
	return stackKitSnapshotAnchorPattern.MatchString(value)
}

func bindRolloutRetryCurrentIntent(job *Job, request *StackKitLifecycleRequest, release, leaseID, serverID string) error {
	value, present := job.Payload[RolloutRetryCurrentIntentField]
	if !present {
		if _, hasHash := job.Payload[RolloutRetryCurrentSpecHashField]; hasHash {
			return fmt.Errorf("native retry CAS precondition has no admitted intent lineage")
		}
		return nil
	}
	prior := mapFromInterface(value)
	// The failed Apply recorded the controller's executed document. A split
	// prefix binding executes its bound document, not the node's init file.
	executedSpecPath := request.SpecPath
	if request.BoundSpecPath != "" {
		executedSpecPath = request.BoundSpecPath
	}
	// The candidate snapshot was already verified during deploy preparation.
	// Recheck the exact source/lease/runtime and producer binding before PLAN.
	if stringFromMap(job.Payload, "rollout_retry_kind") != "exact-existing-runtime" ||
		stringFromMap(job.Payload, RolloutRetryCandidateSHA256Field) == "" ||
		stringFromMap(job.Payload, "rollout_retry_replacement_job_id") != job.ID ||
		stringFromMap(prior, "source_job_id") != stringFromMap(job.Payload, "rollout_retry_source_job_id") ||
		stringFromMap(prior, "lease_id") != stringFromMap(job.Payload, "rollout_retry_lease_id") ||
		stringFromMap(prior, "server_id") != stringFromMap(job.Payload, "rollout_retry_server_id") ||
		stringFromMap(prior, "lease_id") != leaseID || stringFromMap(prior, "server_id") != serverID ||
		stringFromMap(prior, "agent_id") != request.AgentID || stringFromMap(prior, "stackkit") != request.StackKit ||
		stringFromMap(prior, "release") != release ||
		stringFromMap(prior, "spec_path") == "" || filepath.Base(stringFromMap(prior, "spec_path")) != executedSpecPath {
		return fmt.Errorf("native retry current intent does not match its exact dispatch binding")
	}
	hash, hasHash := job.Payload[RolloutRetryCurrentSpecHashField]
	// A split prefix binding has two independently persisted documents. Its
	// bound generation hash must never authorize replacement of the init file,
	// so its retry sends no CAS precondition: StackKits accepts the exact init
	// candidate as already applied and refuses any differing replacement.
	if request.AddressPrefix != "" || request.BoundSpecPath != "" {
		if hasHash || stringFromMap(prior, "apply_source_job_id") != "" {
			return fmt.Errorf("native retry current intent requires one canonical node document")
		}
		return nil
	}
	if hasHash {
		currentHash, valid := hash.(string)
		if !valid || !ValidRolloutRetryCurrentSpecHash(currentHash) ||
			stringFromMap(prior, "apply_source_job_id") == "" ||
			stringFromMap(prior, "apply_source_job_id") == stringFromMap(prior, "source_job_id") {
			return fmt.Errorf("native retry CAS precondition has no validated partial INIT lineage")
		}
		request.ExpectedSpecHash = currentHash
	} else {
		if stringFromMap(prior, "apply_source_job_id") != "" {
			return fmt.Errorf("partial INIT recovery requires its current normalized spec hash")
		}
		request.inspectCurrentIntent = true
	}
	return nil
}

func materializeRolloutRetryProjection(persister *unifier.SpecPersister, job *Job) (string, error) {
	if persister == nil || job == nil || stringFromMap(job.Payload, "rollout_retry_kind") == "" {
		return "", nil
	}
	projection := mapFromInterface(job.Payload[payloadKeyStackSpecV2])
	if len(projection) == 0 {
		if stringFromMap(job.Payload, RolloutRetryCandidateSHA256Field) != "" {
			return "", fmt.Errorf("native retry candidate snapshot is missing")
		}
		return "", nil
	}
	if stringFromMap(job.Payload, "rollout_retry_kind") != "exact-existing-runtime" ||
		stringFromMap(job.Payload, "rollout_retry_source_job_id") == "" ||
		stringFromMap(job.Payload, "rollout_retry_key") == "" ||
		stringFromMap(job.Payload, "rollout_retry_lease_id") == "" ||
		stringFromMap(job.Payload, "rollout_retry_server_id") == "" ||
		stringFromMap(job.Payload, "rollout_retry_replacement_job_id") != job.ID {
		return "", fmt.Errorf("native retry candidate has no exact dispatch context")
	}
	if err := specv2.RequireCanonicalV2(projection); err != nil {
		return "", fmt.Errorf("native retry candidate: %w", err)
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("serialize native retry candidate: %w", err)
	}
	want := strings.TrimSpace(stringFromMap(job.Payload, RolloutRetryCandidateSHA256Field))
	if want == "" || want != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
		return "", fmt.Errorf("native retry candidate does not match its dispatch digest")
	}
	return persister.SaveStackSpecSnapshotBytes(raw)
}

func managedRolloutRetryProjection(persister *unifier.SpecPersister, snapshotPath, dispatchID string) (string, error) {
	release, err := configuredTargetStackKitRelease()
	if err != nil {
		return "", err
	}
	if release == nil {
		return "", fmt.Errorf("native retry requires its pinned StackKits release")
	}
	raw, err := os.ReadFile(snapshotPath) // #nosec G304 -- content-addressed path from the confined SpecPersister.
	if err != nil {
		return "", err
	}
	managed, err := advancedGenerationCandidate(raw, *release)
	if err != nil {
		return "", err
	}
	return persister.SaveStackSpecSnapshotForDispatch(managed, dispatchID)
}
