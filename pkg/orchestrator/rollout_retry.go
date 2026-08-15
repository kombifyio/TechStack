package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

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
)

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

	o.mu.Lock()
	defer o.mu.Unlock()
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
	return nil
}

func (o *Orchestrator) prepareRolloutRetry(
	ctx context.Context,
	req RolloutRetryRequest,
) (*orchestratorStack, *controlplane.Job, ProvisionStackOptions, string, error) {
	opts := ProvisionStackOptions{
		RequestContext: ctx, TenantID: req.TenantID, OwnerID: req.OwnerID, StackName: req.StackName,
		requireControlPlane: true,
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
	opts.RequiredLeaseID = req.LeaseID
	opts.RequiredServerID = serverID
	opts.rolloutRetryJobID = req.SourceJobID
	opts.rolloutRetryKey = exactRolloutRetryKey(req.StackID, req.SourceJobID, req.LeaseID)
	opts.rolloutRetryNewID = exactRolloutRetryJobID(opts.rolloutRetryKey)
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
	if bootstrapErr := issueRolloutRetryOwnerSpecBootstrap(req, &opts); bootstrapErr != nil {
		return nil, bootstrapErr
	}
	jobID, deployErr := o.deployStackWithOptionsLocked(ctx, req.StackID, opts)
	if deployErr != nil {
		return nil, deployErr
	}
	return &RolloutRetryResult{
		JobID: jobID, LeaseID: req.LeaseID, ServerID: serverID, IdempotentReplay: idempotentReplay,
	}, nil
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
	foundLease := false
	for _, candidate := range []string{
		strings.TrimSpace(stringFromAny(source.Result[enrollmentResumeGenericLeaseField])),
		strings.TrimSpace(stringFromAny(source.Result["runtime_lease_id"])),
		strings.TrimSpace(stringFromAny(source.Result[rolloutRetryLeaseField])),
	} {
		if candidate == "" {
			continue
		}
		foundLease = true
		if candidate != strings.TrimSpace(leaseID) {
			return fmt.Errorf("%w: source job is bound to another lease", ErrRolloutRetryInvalid)
		}
	}
	if !foundLease {
		return fmt.Errorf("%w: source job has no managed runtime lease binding", ErrRolloutRetryInvalid)
	}
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
	return map[string]any{
		rolloutRetryKindField:              rolloutRetryKindExact,
		rolloutRetryKeyField:               strings.TrimSpace(opts.rolloutRetryKey),
		rolloutRetrySourceField:            strings.TrimSpace(opts.rolloutRetryJobID),
		rolloutRetryLeaseField:             strings.TrimSpace(opts.RequiredLeaseID),
		rolloutRetryServerField:            strings.TrimSpace(opts.RequiredServerID),
		rolloutRetryReplacementField:       strings.TrimSpace(opts.rolloutRetryNewID),
		enrollmentResumeGenericLeaseField:  strings.TrimSpace(opts.RequiredLeaseID),
		enrollmentResumeGenericServerField: strings.TrimSpace(opts.RequiredServerID),
	}
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
	for _, field := range []string{rolloutRetryLeaseField, rolloutRetryServerField, rolloutRetryReplacementField} {
		if strings.TrimSpace(stringFromAny(candidate[field])) != strings.TrimSpace(stringFromAny(expected[field])) {
			return false, fmt.Errorf("%w: rollout retry key belongs to another exact target", stackrouting.ErrIdempotencyConflict)
		}
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
