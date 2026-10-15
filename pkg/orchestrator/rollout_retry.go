package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

// rolloutRetryJobScanLimit bounds the in-flight admission scan. A stack that
// has accumulated more jobs than this has far older history than any live
// rollout, and the newest rows are the ones that can still be in flight.
const rolloutRetryJobScanLimit = 50

var (
	ErrRolloutRetryInvalid     = errors.New("invalid exact rollout retry")
	ErrRolloutRetryUnavailable = errors.New("exact rollout retry unavailable")
	// ErrRolloutRetryNotYet refuses a retry that an external authority already
	// declared cannot succeed before its retry-after time.
	ErrRolloutRetryNotYet = errors.New("exact rollout retry is refused before the authority retry-after")
)

// RolloutRetryNotYetError carries the authority's retry-after time so the
// route can answer with a structured, retryable denial.
type RolloutRetryNotYetError struct {
	RetryAfter time.Time
}

func (e *RolloutRetryNotYetError) Error() string {
	return fmt.Sprintf("%v: retry after %s", ErrRolloutRetryNotYet, e.RetryAfter.UTC().Format(time.RFC3339))
}

func (e *RolloutRetryNotYetError) Unwrap() error { return ErrRolloutRetryNotYet }

const (
	rolloutRetryKindField        = "rollout_retry_kind"
	rolloutRetryKindExact        = "exact-existing-runtime"
	rolloutRetryKeyField         = "rollout_retry_key"
	rolloutRetrySourceField      = "rollout_retry_source_job_id"
	rolloutRetryLeaseField       = "rollout_retry_lease_id"
	rolloutRetryServerField      = "rollout_retry_server_id"
	rolloutRetryReplacementField = "rollout_retry_replacement_job_id"
)

type RolloutRetryRequest struct {
	RequestContext          context.Context
	StackID                 string
	TenantID                string
	OwnerID                 string
	StackName               string
	SourceJobID             string
	LeaseID                 string
	CandidateSpec           map[string]any
	OwnerApproved           bool
	CurrentSpecHash         string
	IssueOwnerSpecBootstrap func() (*jobs.OwnerSpecBootstrap, error)
}

type RolloutRetryResult struct {
	JobID            string
	LeaseID          string
	ServerID         string
	IdempotentReplay bool
}

// RetryRollout starts only the rollout phase on an already active exact lease.
// It never enters provider provisioning and every repeat maps to the same
// deterministic durable job.
func (o *Orchestrator) RetryRollout(req RolloutRetryRequest) (*RolloutRetryResult, error) {
	if !rolloutRetryAuthoritiesReady(o) {
		return nil, fmt.Errorf("%w: job and lease authorities are required", ErrRolloutRetryUnavailable)
	}
	if normalizeErr := normalizeRolloutRetryRequest(&req); normalizeErr != nil {
		return nil, normalizeErr
	}
	ctx := req.RequestContext
	if ctx == nil {
		ctx = o.ctx
	}

	unlockStack := o.stackLocks.lock(req.StackID)
	defer unlockStack()
	o.mu.RLock()
	defer o.mu.RUnlock()
	stack, source, opts, serverID, prepareErr := o.prepareRolloutRetry(ctx, req)
	if prepareErr != nil {
		return nil, prepareErr
	}
	receipt := rolloutRetryDispatchResult(opts)

	replayID, found, rehydrate, replayErr := o.findRolloutRetryReplay(ctx, *source, opts, receipt)
	if replayErr != nil {
		return nil, replayErr
	}
	if found && !rehydrate {
		o.recordRolloutRetrySupersession(ctx, req.TenantID, req.SourceJobID, replayID)
		return &RolloutRetryResult{
			JobID: replayID, LeaseID: req.LeaseID, ServerID: serverID, IdempotentReplay: true,
		}, nil
	}
	if rehydrate {
		return o.dispatchRolloutRetry(ctx, stack, req, opts, serverID, true)
	}
	// A new retry must not fan a second rollout out beside one that is already
	// in flight. That invariant used to be enforced by requiring the stack
	// status to be exactly "error", but the status is a projection written only
	// while an orchestrator observes the job transition in-process: a job that
	// dies with its process leaves the stack in "provisioning" forever, so the
	// documented recovery path became unreachable for precisely the failures it
	// exists to recover. Ask the authoritative question instead -- is any job
	// for this stack still in flight?
	inFlight, inFlightErr := o.stackHasJobInFlight(ctx, req.TenantID, req.StackID)
	if inFlightErr != nil {
		return nil, fmt.Errorf("%w: load stack jobs for retry admission: %v", ErrRolloutRetryUnavailable, inFlightErr)
	}
	if inFlight {
		return nil, fmt.Errorf("%w: a rollout for this stack is already in flight", ErrRolloutRetryInvalid)
	}
	return o.dispatchRolloutRetry(ctx, stack, req, opts, serverID, false)
}

// stackHasJobInFlight reports whether any job for the stack is still pending,
// waiting or running. Terminal jobs -- however they reached a terminal state --
// do not block a fresh recovery dispatch.
func (o *Orchestrator) stackHasJobInFlight(ctx context.Context, tenantID, stackID string) (bool, error) {
	stackJobs, err := o.jobStore.ListJobsByStack(ctx, tenantID, stackID, rolloutRetryJobScanLimit)
	if err != nil {
		return false, err
	}
	for _, job := range stackJobs {
		switch canonicalEnrollmentJobState(job.State) {
		case string(jobs.JobStatePending), string(jobs.JobStateWaiting), string(jobs.JobStateRunning):
			return true, nil
		}
	}
	return false, nil
}

func rolloutRetryAuthoritiesReady(o *Orchestrator) bool {
	return o != nil && o.jobStore != nil && o.leaseLister != nil
}

func normalizeRolloutRetryRequest(req *RolloutRetryRequest) error {
	if !normalizeExactRecoveryIdentity(&req.StackID, &req.TenantID, &req.OwnerID, &req.SourceJobID, &req.LeaseID) {
		return fmt.Errorf("%w: stack, tenant, owner, source job, and lease are required", ErrRolloutRetryInvalid)
	}
	if len(req.CandidateSpec) > 0 && !req.OwnerApproved {
		return fmt.Errorf("%w: canonical repair requires owner approval", ErrRolloutRetryInvalid)
	}
	if req.CurrentSpecHash != "" && !jobs.ValidRolloutRetryCurrentSpecHash(req.CurrentSpecHash) {
		return fmt.Errorf("%w: current spec hash must be a normalized SHA-256 binding", ErrRolloutRetryInvalid)
	}
	return nil
}

func (o *Orchestrator) prepareRolloutRetry(
	ctx context.Context,
	req RolloutRetryRequest,
) (*orchestratorStack, *controlplane.Job, ProvisionStackOptions, string, error) {
	opts := ProvisionStackOptions{
		RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID, StackName: req.StackName,
	}
	stack, stackErr := o.findStackForJob(ctx, req.StackID, opts)
	if stackErr != nil {
		return nil, nil, opts, "", fmt.Errorf("%w: stack not found", ErrRolloutRetryInvalid)
	}
	if strings.TrimSpace(stack.tenantID) != req.TenantID || strings.TrimSpace(stack.ownerID) != req.OwnerID {
		return nil, nil, opts, "", fmt.Errorf("%w: stack ownership does not match request identity", ErrRolloutRetryInvalid)
	}
	source, sourceErr := o.jobStore.GetJob(ctx, req.TenantID, req.SourceJobID)
	if sourceErr != nil {
		return nil, nil, opts, "", fmt.Errorf("%w: source job is not available", ErrRolloutRetryInvalid)
	}
	serverID := runtimeidentity.LeaseServerID(req.LeaseID)
	if serverID == "" {
		return nil, nil, opts, "", fmt.Errorf("%w: lease has no canonical server identity", ErrRolloutRetryInvalid)
	}
	if validationErr := validateRolloutRetrySource(*source, req.StackID, req.LeaseID, serverID); validationErr != nil {
		return nil, nil, opts, "", validationErr
	}
	// A rollout the certificate authority rate-limited fails again until the
	// authority's retry-after; refusing it here spends no rollout or CA order.
	now := time.Now().UTC()
	if o.now != nil {
		now = o.now()
	}
	if retryAfter, limited := jobs.JobResultRetryAfter(source.Result); limited && now.Before(retryAfter) {
		return nil, nil, opts, "", &RolloutRetryNotYetError{RetryAfter: retryAfter}
	}
	opts.RequiredLeaseID = req.LeaseID
	opts.RequiredServerID = serverID
	opts.rolloutRetryJobID = req.SourceJobID
	opts.rolloutRetryKey = exactRolloutRetryKey(req.StackID, req.SourceJobID, req.LeaseID)
	opts.rolloutRetryNewID = exactRolloutRetryJobID(opts.rolloutRetryKey)
	candidate := req.CandidateSpec
	if len(candidate) == 0 && strings.TrimSpace(stringFromAny(stack.config[rolloutRetryCandidateField])) == "" {
		// Even an unchanged native retry snapshots its current durable intent.
		// A cached preparation handoff is not that dispatch's authority.
		candidate, _ = providerIdentityMap(stack.config["stack_spec_v2"])
	}
	var digestErr error
	opts.rolloutRetryCandidateSHA, digestErr = rolloutCandidateDigest(candidate)
	if digestErr != nil {
		return nil, nil, opts, "", digestErr
	}
	opts.rolloutRetryCandidate = cloneProvisionSpec(candidate)
	if bound := strings.TrimSpace(stringFromAny(stack.config[rolloutRetryCandidateField])); bound != "" && bound != opts.rolloutRetryCandidateSHA {
		return nil, nil, opts, "", fmt.Errorf("%w: canonical repair candidate is already bound", ErrRolloutRetryInvalid)
	}
	currentIntent, intentErr := o.rolloutRetryIntentForRequest(ctx, req, *source, serverID, opts.rolloutRetryCandidateSHA)
	if intentErr != nil {
		return nil, nil, opts, "", intentErr
	}
	opts.rolloutRetryIntent = currentIntent
	opts.rolloutRetryCurrentHash = req.CurrentSpecHash
	return stack, source, opts, serverID, nil
}

func (o *Orchestrator) dispatchRolloutRetry(
	ctx context.Context,
	stack *orchestratorStack,
	req RolloutRetryRequest,
	opts ProvisionStackOptions,
	serverID string,
	idempotentReplay bool,
) (*RolloutRetryResult, error) {
	if targetErr := o.validateExactRetryTarget(ctx, stack, req.LeaseID); targetErr != nil {
		return nil, targetErr
	}
	if runtimeErr := o.requireExactManagedRuntimeDeploy(ctx, stack, req.LeaseID, serverID); runtimeErr != nil {
		return nil, runtimeErr
	}
	if repairErr := o.repairRolloutCandidate(ctx, req, stack, opts); repairErr != nil {
		return nil, repairErr
	}
	if bootstrapErr := issueRolloutRetryOwnerSpecBootstrap(req, &opts); bootstrapErr != nil {
		return nil, bootstrapErr
	}
	jobID, deployErr := o.deployStackWithOptionsLocked(ctx, req.StackID, opts)
	if deployErr != nil {
		return nil, deployErr
	}
	o.recordRolloutRetrySupersession(ctx, req.TenantID, req.SourceJobID, jobID)
	return &RolloutRetryResult{
		JobID: jobID, LeaseID: req.LeaseID, ServerID: serverID, IdempotentReplay: idempotentReplay,
	}, nil
}

// recordRolloutRetrySupersession points the failed source job at its
// deterministic replacement so a client polling the source can follow it. The
// pointer is not an authority: a failed write leaves the dispatched retry
// intact and the next idempotent request records it again.
func (o *Orchestrator) recordRolloutRetrySupersession(ctx context.Context, tenantID, sourceJobID, replacementJobID string) {
	recorder, ok := o.jobStore.(controlplane.JobSupersessionRecorder)
	if !ok || strings.TrimSpace(replacementJobID) == "" {
		return
	}
	if err := recorder.RecordJobSupersededBy(ctx, tenantID, sourceJobID, replacementJobID); err != nil {
		o.log.Warn("rollout_retry_supersession_not_recorded",
			"tenant_id", tenantID, "source_job_id", sourceJobID, "replacement_job_id", replacementJobID, "error", err)
	}
}

func issueRolloutRetryOwnerSpecBootstrap(req RolloutRetryRequest, opts *ProvisionStackOptions) error {
	if req.IssueOwnerSpecBootstrap == nil || opts == nil {
		return nil
	}
	bootstrap, err := req.IssueOwnerSpecBootstrap()
	if err != nil {
		return fmt.Errorf("%w: issue owner bootstrap for rollout retry", ErrRolloutRetryUnavailable)
	}
	opts.OwnerSpecBootstrap = bootstrap
	return nil
}

func (o *Orchestrator) validateExactRetryTarget(ctx context.Context, stack *orchestratorStack, leaseID string) error {
	status := strings.ToLower(strings.TrimSpace(stack.status))
	if status != persistentStateError && status != persistentStateProvisioning {
		return fmt.Errorf("%w: stack is %q, not a failed rollout", ErrRolloutRetryInvalid, stack.status)
	}
	lease, err := o.exactManagedRuntimeLeaseForStack(ctx, stack, leaseID)
	if err != nil {
		if errors.Is(err, stackrouting.ErrInvalid) {
			return fmt.Errorf("%w: source lease is not an active foundation target", ErrRolloutRetryInvalid)
		}
		return fmt.Errorf("%w: validate exact active lease: %v", ErrRolloutRetryUnavailable, err)
	}
	if lease == nil {
		return fmt.Errorf("%w: source lease is no longer active", ErrRolloutRetryInvalid)
	}
	return nil
}

func validateRolloutRetrySource(source controlplane.Job, stackID, leaseID, serverID string) error {
	if strings.TrimSpace(source.StackID) != strings.TrimSpace(stackID) ||
		strings.ToLower(strings.TrimSpace(source.Type)) != string(jobs.JobTypeDeploy) ||
		canonicalEnrollmentJobState(source.State) != string(jobs.JobStateFailed) {
		return fmt.Errorf("%w: source job is not a failed deploy for this stack", ErrRolloutRetryInvalid)
	}
	for _, candidate := range []string{
		strings.TrimSpace(stringFromAny(source.Result[enrollmentResumeGenericLeaseField])),
		strings.TrimSpace(stringFromAny(source.Result["runtime_lease_id"])),
		strings.TrimSpace(stringFromAny(source.Result[rolloutRetryLeaseField])),
	} {
		if candidate == "" {
			continue
		}
		if candidate != strings.TrimSpace(leaseID) {
			return fmt.Errorf("%w: source job is bound to another lease", ErrRolloutRetryInvalid)
		}
	}
	// An orphaned generate job can die after resolving the lease and before
	// persisting lease_id on the result. The request still names the exact
	// lease, and validateExactRetryTarget checks it is this stack's active
	// foundation.
	for _, candidate := range []string{
		strings.TrimSpace(stringFromAny(source.Result[enrollmentResumeGenericServerField])),
		strings.TrimSpace(stringFromAny(source.Result["runtime_server_id"])),
		strings.TrimSpace(stringFromAny(source.Result[rolloutRetryServerField])),
	} {
		if candidate != "" && candidate != strings.TrimSpace(serverID) {
			return fmt.Errorf("%w: source job is bound to another canonical server", ErrRolloutRetryInvalid)
		}
	}
	return nil
}

func rolloutRetryDispatchResult(opts ProvisionStackOptions) map[string]any {
	if !isRolloutRetry(opts) {
		return nil
	}
	result := map[string]any{
		rolloutRetryKindField:              rolloutRetryKindExact,
		rolloutRetryKeyField:               strings.TrimSpace(opts.rolloutRetryKey),
		rolloutRetrySourceField:            strings.TrimSpace(opts.rolloutRetryJobID),
		rolloutRetryLeaseField:             strings.TrimSpace(opts.RequiredLeaseID),
		rolloutRetryServerField:            strings.TrimSpace(opts.RequiredServerID),
		rolloutRetryReplacementField:       strings.TrimSpace(opts.rolloutRetryNewID),
		rolloutRetryCandidateField:         opts.rolloutRetryCandidateSHA,
		enrollmentResumeGenericLeaseField:  strings.TrimSpace(opts.RequiredLeaseID),
		enrollmentResumeGenericServerField: strings.TrimSpace(opts.RequiredServerID),
	}
	if opts.rolloutRetryIntent != nil {
		result[jobs.RolloutRetryCurrentIntentField] = cloneProvisionSpec(opts.rolloutRetryIntent)
	}
	if opts.rolloutRetryCurrentHash != "" {
		result[jobs.RolloutRetryCurrentSpecHashField] = opts.rolloutRetryCurrentHash
	}
	return result
}

// A failed node Apply positively proves that INIT and generation established
// native intent. Preparation failures retain the ordinary fresh INIT path.
// This evidence comes from the validated source job, never the retry request.
func rolloutRetryCurrentIntent(source controlplane.Job, leaseID, serverID string) (map[string]any, error) {
	failure, _ := providerIdentityMap(source.Result["typed_stackkit_rollout"])
	if stringFromAny(failure["operation"]) != jobs.StackKitLifecycleApply {
		return nil, nil
	}
	proof, _ := providerIdentityMap(source.Result["e2e_proof"])
	path := strings.TrimSpace(stringFromAny(proof["stack_spec_path"]))
	kit := jobs.NormalizeStackKitRef(stringFromAny(proof["stackkit_ref"]))
	agentID := strings.TrimSpace(stringFromAny(proof["runtime_agent_id"]))
	release := strings.TrimSpace(stringFromAny(failure["release"]))
	if stringFromAny(failure["status"]) != "failed" ||
		stringFromAny(failure["command_id"]) != source.ID+"-"+jobs.StackKitLifecycleApply ||
		stringFromAny(proof["lease_id"]) != leaseID || stringFromAny(proof["server_id"]) != serverID ||
		path == "" || kit == "" || agentID == "" || release == "" {
		return nil, fmt.Errorf("%w: failed Apply has no exact native intent binding", ErrRolloutRetryInvalid)
	}
	return map[string]any{"source_job_id": source.ID, "lease_id": leaseID, "server_id": serverID,
		"agent_id": agentID, "stackkit": kit, "spec_path": path, "release": release}, nil
}

func isRolloutRetry(opts ProvisionStackOptions) bool {
	return strings.TrimSpace(opts.rolloutRetryJobID) != "" && strings.TrimSpace(opts.rolloutRetryKey) != "" &&
		strings.TrimSpace(opts.rolloutRetryNewID) != "" && strings.TrimSpace(opts.RequiredLeaseID) != "" &&
		strings.TrimSpace(opts.RequiredServerID) != ""
}

func isDeterministicExactRecovery(opts ProvisionStackOptions) bool {
	return isEnrollmentResume(opts) || isRolloutRetry(opts)
}

func deterministicRecoveryJobID(opts ProvisionStackOptions) string {
	if isEnrollmentResume(opts) {
		return strings.TrimSpace(opts.enrollmentResumeNewID)
	}
	if isRolloutRetry(opts) {
		return strings.TrimSpace(opts.rolloutRetryNewID)
	}
	return ""
}

func exactRecoveryDispatchReceipt(candidate, expected map[string]any, opts ProvisionStackOptions) (bool, error) {
	if isEnrollmentResume(opts) {
		return exactEnrollmentResumeReceipt(candidate, expected)
	}
	if isRolloutRetry(opts) {
		return exactRolloutRetryReceipt(candidate, expected)
	}
	return false, nil
}

func exactRolloutRetryKey(stackID, sourceJobID, leaseID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(stackID), strings.TrimSpace(sourceJobID), strings.TrimSpace(leaseID),
	}, "\x00")))
	return fmt.Sprintf("rollout-retry-%x", sum[:16])
}

func exactRolloutRetryJobID(key string) string {
	return "job-rollout-" + strings.TrimPrefix(strings.TrimSpace(key), "rollout-retry-")
}

func exactRolloutRetryReceipt(candidate, expected map[string]any) (bool, error) {
	if strings.TrimSpace(stringFromAny(candidate[rolloutRetryKindField])) != rolloutRetryKindExact ||
		strings.TrimSpace(stringFromAny(candidate[rolloutRetryKeyField])) != strings.TrimSpace(stringFromAny(expected[rolloutRetryKeyField])) ||
		strings.TrimSpace(stringFromAny(candidate[rolloutRetrySourceField])) != strings.TrimSpace(stringFromAny(expected[rolloutRetrySourceField])) {
		return false, nil
	}
	for _, field := range []string{rolloutRetryLeaseField, rolloutRetryServerField, rolloutRetryReplacementField, rolloutRetryCandidateField, jobs.RolloutRetryCurrentSpecHashField} {
		if strings.TrimSpace(stringFromAny(candidate[field])) != strings.TrimSpace(stringFromAny(expected[field])) {
			return false, fmt.Errorf("%w: rollout retry key belongs to another exact target", stackrouting.ErrIdempotencyConflict)
		}
	}
	candidateIntent, _ := providerIdentityMap(candidate[jobs.RolloutRetryCurrentIntentField])
	expectedIntent, _ := providerIdentityMap(expected[jobs.RolloutRetryCurrentIntentField])
	if !reflect.DeepEqual(candidateIntent, expectedIntent) {
		return false, fmt.Errorf("%w: rollout retry native intent lineage changed", stackrouting.ErrIdempotencyConflict)
	}
	return true, nil
}

func (o *Orchestrator) findRolloutRetryReplay(
	ctx context.Context,
	source controlplane.Job,
	opts ProvisionStackOptions,
	receipt map[string]any,
) (jobID string, found, rehydrate bool, err error) {
	replacement, getErr := o.jobStore.GetJob(ctx, source.TenantID, opts.rolloutRetryNewID)
	if errors.Is(getErr, controlplane.ErrNotFound) {
		return "", false, false, nil
	}
	if getErr != nil {
		return "", false, false, fmt.Errorf("%w: load deterministic retry: %v", ErrRolloutRetryUnavailable, getErr)
	}
	exact, exactErr := exactRolloutRetryReceipt(replacement.Result, receipt)
	if exactErr != nil || !exact || replacement.StackID != source.StackID ||
		strings.ToLower(strings.TrimSpace(replacement.Type)) != string(jobs.JobTypeDeploy) {
		return "", false, false, fmt.Errorf("%w: deterministic rollout job belongs to another dispatch", stackrouting.ErrIdempotencyConflict)
	}
	if queued, ok := o.queue.Get(replacement.ID); ok {
		snapshot := queued.Snapshot()
		if opts.rolloutRetryCurrentHash != "" {
			exactPayload, payloadErr := exactRolloutRetryReceipt(snapshot.Payload, receipt)
			if payloadErr != nil || !exactPayload {
				return "", false, false, fmt.Errorf("%w: queued retry precondition differs from its receipt", stackrouting.ErrIdempotencyConflict)
			}
		}
		switch snapshot.State {
		case jobs.JobStatePending, jobs.JobStateRunning, jobs.JobStateWaiting, jobs.JobStateCompleted:
			return replacement.ID, true, false, nil
		case jobs.JobStateFailed, jobs.JobStateCancelled:
			return "", false, false, fmt.Errorf("%w: retry replacement is terminal", ErrRolloutRetryInvalid)
		}
	}
	switch canonicalEnrollmentJobState(replacement.State) {
	case persistentStatePending:
		return replacement.ID, true, true, nil
	case persistentStateRunning, string(jobs.JobStateCompleted):
		return replacement.ID, true, false, nil
	case string(jobs.JobStateFailed), string(jobs.JobStateCancelled):
		return "", false, false, fmt.Errorf("%w: retry replacement is terminal", ErrRolloutRetryInvalid)
	default:
		return "", false, false, fmt.Errorf("%w: unknown retry replacement state %q", ErrRolloutRetryUnavailable, replacement.State)
	}
}
