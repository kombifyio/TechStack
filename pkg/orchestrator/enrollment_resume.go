package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

var (
	// ErrEnrollmentResumeInvalid means the requested source job, stack, or
	// exact runtime target does not describe one recoverable enrollment wait.
	ErrEnrollmentResumeInvalid = errors.New("invalid enrollment resume request")
	// ErrEnrollmentResumeNotReady means the in-process resume still has its
	// bounded grace period and must not be duplicated by a second dispatch.
	ErrEnrollmentResumeNotReady = errors.New("enrollment resume is not overdue")
	// ErrEnrollmentResumeUnavailable means the durable authority needed to
	// validate and fence the recovery is unavailable.
	ErrEnrollmentResumeUnavailable = errors.New("enrollment resume unavailable")
)

const enrollmentResumeGrace = jobs.ManagedRuntimeEnrollmentRecoveryGrace

const (
	enrollmentResumeKindField          = "enrollment_resume_kind"
	enrollmentResumeKeyField           = "enrollment_resume_key"
	enrollmentResumeSourceField        = "enrollment_resume_source_job_id"
	enrollmentResumeLeaseField         = "enrollment_resume_lease_id"
	enrollmentResumeServerField        = "enrollment_resume_server_id"
	enrollmentResumeScheduledField     = "enrollment_resume_scheduled_at"
	enrollmentResumeReplacementField   = "enrollment_resume_replacement_job_id"
	enrollmentResumeGenericLeaseField  = runtimeFieldLeaseID
	enrollmentResumeGenericServerField = "server_id"
)

// EnrollmentResumeRequest binds an overdue rollout wait to the authenticated
// stack, its persisted source job, and one exact managed runtime lease.
type EnrollmentResumeRequest struct {
	RequestContext          context.Context
	StackID                 string
	TenantID                string
	OwnerID                 string
	StackName               string
	SourceJobID             string
	LeaseID                 string
	IssueOwnerSpecBootstrap func() (*jobs.OwnerSpecBootstrap, error)
}

// EnrollmentResumeResult describes the accepted or idempotently replayed job.
type EnrollmentResumeResult struct {
	JobID            string
	LeaseID          string
	ServerID         string
	IdempotentReplay bool
}

type enrollmentResumeReplay struct {
	jobID     string
	found     bool
	rehydrate bool
}

// ResumeEnrollment atomically retires an overdue managed-runtime wait and
// ensures one deterministic exact-target replacement deploy. It accepts the
// legacy enrollment wait and the preceding provider-provision handover, but it
// never re-enters the provision handler. The source claim, deterministic job
// ID, durable worker claim, and per-stack execution barrier prevent a source
// rollout and its replacement from applying in parallel or creating another
// provider VM.
func (o *Orchestrator) ResumeEnrollment(req EnrollmentResumeRequest) (*EnrollmentResumeResult, error) {
	if !enrollmentResumeAuthoritiesReady(o) {
		return nil, fmt.Errorf("%w: job and lease authorities are required", ErrEnrollmentResumeUnavailable)
	}
	if normalizeErr := normalizeEnrollmentResumeRequest(&req); normalizeErr != nil {
		return nil, normalizeErr
	}
	ctx := req.RequestContext
	if ctx == nil {
		ctx = o.ctx
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	stack, source, opts, serverID, prepareErr := o.prepareEnrollmentResume(ctx, req)
	if prepareErr != nil {
		return nil, prepareErr
	}
	receipt := enrollmentResumeDispatchResult(opts)

	replay, replayErr := o.findEnrollmentResumeReplay(ctx, *source, opts, receipt)
	if replayErr != nil {
		return nil, replayErr
	}
	if replay.found && !replay.rehydrate {
		return &EnrollmentResumeResult{
			JobID: replay.jobID, LeaseID: req.LeaseID, ServerID: serverID,
			IdempotentReplay: true,
		}, nil
	}
	if targetErr := o.validateEnrollmentResumeTarget(ctx, stack, req.LeaseID); targetErr != nil {
		return nil, targetErr
	}
	if runtimeErr := o.requireExactManagedRuntimeDeploy(ctx, stack, req.LeaseID, serverID); runtimeErr != nil {
		return nil, runtimeErr
	}

	if !replay.rehydrate {
		if handoverErr := o.handoverEnrollmentResumeSource(ctx, *source, receipt); handoverErr != nil {
			return nil, handoverErr
		}
	}
	if bootstrapErr := issueEnrollmentResumeOwnerSpecBootstrap(req, &opts); bootstrapErr != nil {
		return nil, bootstrapErr
	}

	jobID, deployErr := o.deployStackWithOptionsLocked(ctx, req.StackID, opts)
	if deployErr != nil {
		return nil, deployErr
	}
	return &EnrollmentResumeResult{
		JobID: jobID, LeaseID: req.LeaseID, ServerID: serverID, IdempotentReplay: replay.rehydrate,
	}, nil
}

func enrollmentResumeAuthoritiesReady(o *Orchestrator) bool {
	return o != nil && o.jobStore != nil && o.leaseLister != nil
}

func normalizeEnrollmentResumeRequest(req *EnrollmentResumeRequest) error {
	if !normalizeExactRecoveryIdentity(&req.StackID, &req.TenantID, &req.OwnerID, &req.SourceJobID, &req.LeaseID) {
		return fmt.Errorf("%w: stack, tenant, owner, source job, and lease are required", ErrEnrollmentResumeInvalid)
	}
	return nil
}

func normalizeExactRecoveryIdentity(stackID, tenantID, ownerID, sourceJobID, leaseID *string) bool {
	*stackID = strings.TrimSpace(*stackID)
	*tenantID = strings.TrimSpace(*tenantID)
	*ownerID = strings.TrimSpace(*ownerID)
	*sourceJobID = strings.TrimSpace(*sourceJobID)
	*leaseID = strings.TrimSpace(*leaseID)
	return *stackID != "" && *tenantID != "" && *ownerID != "" && *sourceJobID != "" && *leaseID != ""
}

func (o *Orchestrator) prepareEnrollmentResume(
	ctx context.Context,
	req EnrollmentResumeRequest,
) (*orchestratorStack, *controlplane.Job, ProvisionStackOptions, string, error) {
	opts := ProvisionStackOptions{
		RequestContext:      ctx,
		TenantID:            req.TenantID,
		OwnerID:             req.OwnerID,
		StackName:           req.StackName,
		requireControlPlane: true,
	}
	stack, stackErr := o.findStackForJob(ctx, req.StackID, opts)
	if stackErr != nil {
		return nil, nil, opts, "", fmt.Errorf("%w: stack not found", ErrEnrollmentResumeInvalid)
	}
	if strings.TrimSpace(stack.tenantID) != req.TenantID || strings.TrimSpace(stack.ownerID) != req.OwnerID {
		return nil, nil, opts, "", fmt.Errorf("%w: stack ownership does not match request identity", ErrEnrollmentResumeInvalid)
	}
	serverID := runtimeidentity.LeaseServerID(req.LeaseID)
	if serverID == "" {
		return nil, nil, opts, "", fmt.Errorf("%w: lease has no canonical server identity", ErrEnrollmentResumeInvalid)
	}
	source, sourceErr := o.jobStore.GetJob(ctx, req.TenantID, req.SourceJobID)
	if sourceErr != nil {
		return nil, nil, opts, "", fmt.Errorf("%w: source job is not available", ErrEnrollmentResumeInvalid)
	}
	nextResumeAt, resumeKind, validationErr := validateEnrollmentResumeSource(*source, req.StackID, req.LeaseID, serverID, time.Now().UTC())
	if validationErr != nil {
		return nil, nil, opts, "", validationErr
	}
	opts.RequiredLeaseID = req.LeaseID
	opts.RequiredServerID = serverID
	opts.enrollmentResumeJobID = req.SourceJobID
	opts.enrollmentResumeAt = nextResumeAt.UTC().Format(time.RFC3339Nano)
	opts.enrollmentResumeKind = resumeKind
	opts.enrollmentResumeKey = enrollmentResumeKey(req.StackID, req.SourceJobID, req.LeaseID, resumeKind, opts.enrollmentResumeAt)
	opts.enrollmentResumeNewID = enrollmentResumeReplacementJobID(opts.enrollmentResumeKey)
	return stack, source, opts, serverID, nil
}

func (o *Orchestrator) validateEnrollmentResumeTarget(ctx context.Context, stack *orchestratorStack, leaseID string) error {
	if strings.ToLower(strings.TrimSpace(stack.status)) != persistentStateProvisioning {
		return fmt.Errorf("%w: stack is %q, not provisioning", ErrEnrollmentResumeInvalid, stack.status)
	}
	lease, leaseErr := o.exactManagedRuntimeLeaseForStack(ctx, stack, leaseID)
	if errors.Is(leaseErr, stackrouting.ErrInvalid) {
		return fmt.Errorf("%w: source lease is not an active foundation target", ErrEnrollmentResumeInvalid)
	}
	if leaseErr != nil {
		return fmt.Errorf("%w: validate exact active lease: %w", ErrEnrollmentResumeUnavailable, leaseErr)
	}
	if lease == nil {
		return fmt.Errorf("%w: source lease is no longer an active foundation target", ErrEnrollmentResumeInvalid)
	}
	return nil
}

func (o *Orchestrator) handoverEnrollmentResumeSource(ctx context.Context, source controlplane.Job, receipt map[string]any) error {
	persistClaim := func() error {
		return o.claimEnrollmentResumeSource(ctx, source, receipt)
	}
	handover, handoverErr := o.queue.SupersedeWaitingJob(
		source.ID,
		jobs.JobType(strings.ToLower(strings.TrimSpace(source.Type))),
		strings.TrimSpace(stringFromAny(receipt[enrollmentResumeKindField])),
		receipt,
		persistClaim,
	)
	if handoverErr != nil {
		return handoverErr
	}
	switch handover {
	case jobs.WaitingHandoverClaimed:
		return nil
	case jobs.WaitingHandoverAbsent:
		return persistClaim()
	case jobs.WaitingHandoverTerminal:
		return fmt.Errorf("%w: source job was canceled or failed before the handover", ErrEnrollmentResumeInvalid)
	default:
		return fmt.Errorf("%w: unknown process-local handover result %q", ErrEnrollmentResumeUnavailable, handover)
	}
}

func issueEnrollmentResumeOwnerSpecBootstrap(req EnrollmentResumeRequest, opts *ProvisionStackOptions) error {
	if req.IssueOwnerSpecBootstrap == nil {
		return nil
	}
	bootstrap, issueErr := req.IssueOwnerSpecBootstrap()
	if issueErr != nil {
		return fmt.Errorf("%w: issue owner bootstrap for enrollment replacement: %w", ErrEnrollmentResumeUnavailable, issueErr)
	}
	opts.OwnerSpecBootstrap = bootstrap
	return nil
}

func validateEnrollmentResumeSource(source controlplane.Job, stackID, leaseID, serverID string, now time.Time) (time.Time, string, error) {
	if strings.TrimSpace(source.StackID) != strings.TrimSpace(stackID) {
		return time.Time{}, "", fmt.Errorf("%w: source job belongs to another stack", ErrEnrollmentResumeInvalid)
	}
	resumeKind, kindErr := managedRolloutResumeKind(source)
	if kindErr != nil {
		return time.Time{}, "", kindErr
	}
	state := canonicalEnrollmentJobState(source.State)
	if state != persistentStatePending && state != string(jobs.JobStateCancelled) {
		if state == persistentStateRunning {
			return time.Time{}, "", fmt.Errorf("%w: source execution state is uncertain", ErrEnrollmentResumeUnavailable)
		}
		return time.Time{}, "", fmt.Errorf("%w: source job is no longer recoverable", ErrEnrollmentResumeInvalid)
	}
	nextResumeAt, scheduleErr := managedRolloutResumeSchedule(source.Result, resumeKind)
	if scheduleErr != nil {
		return time.Time{}, "", scheduleErr
	}
	if now.Before(nextResumeAt.Add(enrollmentResumeGrace)) {
		return time.Time{}, "", fmt.Errorf("%w: recovery opens at %s", ErrEnrollmentResumeNotReady, nextResumeAt.Add(enrollmentResumeGrace).UTC().Format(time.RFC3339))
	}
	if bindingErr := validateEnrollmentResumeBindings(source.Result, leaseID, serverID); bindingErr != nil {
		return time.Time{}, "", bindingErr
	}
	return nextResumeAt, resumeKind, nil
}

func managedRolloutResumeKind(source controlplane.Job) (string, error) {
	jobType := strings.ToLower(strings.TrimSpace(source.Type))
	wait, ok := source.Result["job_wait"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("%w: source job has no managed rollout wait", ErrEnrollmentResumeInvalid)
	}
	reason := strings.TrimSpace(stringFromAny(wait["reason"]))
	switch {
	case jobType == string(jobs.JobTypeDeploy) && reason == jobs.WaitReasonManagedRuntimeEnrollment:
		return reason, nil
	case jobType == string(jobs.JobTypeProvision) && reason == jobs.WaitReasonManagedRuntimeProvider:
		return reason, nil
	default:
		return "", fmt.Errorf("%w: source job type %q and wait reason %q are not a recoverable managed rollout", ErrEnrollmentResumeInvalid, jobType, reason)
	}
}

func managedRolloutResumeSchedule(result map[string]any, expectedReason string) (time.Time, error) {
	wait, ok := result["job_wait"].(map[string]any)
	if !ok || strings.TrimSpace(stringFromAny(wait["state"])) != string(jobs.JobStateWaiting) ||
		strings.TrimSpace(stringFromAny(wait["reason"])) != strings.TrimSpace(expectedReason) {
		return time.Time{}, fmt.Errorf("%w: source job is not waiting on the expected managed runtime phase", ErrEnrollmentResumeInvalid)
	}
	nextResumeAt, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(stringFromAny(wait["next_resume_at"])))
	if parseErr != nil {
		return time.Time{}, fmt.Errorf("%w: source job has no valid resume schedule", ErrEnrollmentResumeInvalid)
	}
	return nextResumeAt, nil
}

func validateEnrollmentResumeBindings(result map[string]any, leaseID, serverID string) error {
	leaseFields := []string{
		strings.TrimSpace(stringFromAny(result[enrollmentResumeGenericLeaseField])),
		strings.TrimSpace(stringFromAny(result["runtime_lease_id"])),
		strings.TrimSpace(stringFromAny(result[enrollmentResumeLeaseField])),
	}
	foundLease := false
	for _, candidate := range leaseFields {
		if candidate == "" {
			continue
		}
		foundLease = true
		if candidate != strings.TrimSpace(leaseID) {
			return fmt.Errorf("%w: source job is bound to a different lease", ErrEnrollmentResumeInvalid)
		}
	}
	if !foundLease {
		return fmt.Errorf("%w: source job has no managed runtime lease binding", ErrEnrollmentResumeInvalid)
	}
	for _, candidate := range []string{
		strings.TrimSpace(stringFromAny(result[enrollmentResumeGenericServerField])),
		strings.TrimSpace(stringFromAny(result["runtime_server_id"])),
		strings.TrimSpace(stringFromAny(result[enrollmentResumeServerField])),
	} {
		if candidate != "" && candidate != strings.TrimSpace(serverID) {
			return fmt.Errorf("%w: source job is bound to a different canonical server", ErrEnrollmentResumeInvalid)
		}
	}
	return nil
}

func (o *Orchestrator) claimEnrollmentResumeSource(ctx context.Context, source controlplane.Job, receipt map[string]any) error {
	_, err := o.jobStore.ClaimWaitingJobResume(ctx, controlplane.ClaimWaitingJobResumeRequest{
		TenantID:     source.TenantID,
		JobID:        source.ID,
		StackID:      source.StackID,
		JobType:      strings.ToLower(strings.TrimSpace(source.Type)),
		WaitReason:   strings.TrimSpace(stringFromAny(receipt[enrollmentResumeKindField])),
		NextResumeAt: strings.TrimSpace(stringFromAny(receipt[enrollmentResumeScheduledField])),
		LeaseID:      strings.TrimSpace(stringFromAny(receipt[enrollmentResumeLeaseField])),
		ServerID:     strings.TrimSpace(stringFromAny(receipt[enrollmentResumeServerField])),
		ResultPatch:  receipt,
		ClaimedAt:    time.Now().UTC(),
	})
	if errors.Is(err, controlplane.ErrConflict) {
		latest, getErr := o.jobStore.GetJob(ctx, source.TenantID, source.ID)
		if getErr == nil && canonicalEnrollmentJobState(latest.State) == string(jobs.JobStateCancelled) {
			exact, exactErr := exactEnrollmentResumeReceipt(latest.Result, receipt)
			if exactErr == nil && exact {
				return nil
			}
		}
		return fmt.Errorf("%w: source wait changed before recovery was claimed", ErrEnrollmentResumeInvalid)
	}
	if err != nil {
		return fmt.Errorf("%w: claim source-job enrollment handover: %w", ErrEnrollmentResumeUnavailable, err)
	}
	return nil
}

func deployDispatchResult(opts ProvisionStackOptions) map[string]any {
	result := routingDispatchResult(opts)
	resume := enrollmentResumeDispatchResult(opts)
	retry := rolloutRetryDispatchResult(opts)
	if len(result) == 0 && len(resume) == 0 && len(retry) == 0 {
		return nil
	}
	merged := make(map[string]any, len(result)+len(resume)+len(retry))
	for key, value := range result {
		merged[key] = value
	}
	for key, value := range resume {
		merged[key] = value
	}
	for key, value := range retry {
		merged[key] = value
	}
	return merged
}

func enrollmentResumeDispatchResult(opts ProvisionStackOptions) map[string]any {
	if !isEnrollmentResume(opts) {
		return nil
	}
	return map[string]any{
		enrollmentResumeKindField:          strings.TrimSpace(opts.enrollmentResumeKind),
		enrollmentResumeKeyField:           strings.TrimSpace(opts.enrollmentResumeKey),
		enrollmentResumeSourceField:        strings.TrimSpace(opts.enrollmentResumeJobID),
		enrollmentResumeLeaseField:         strings.TrimSpace(opts.RequiredLeaseID),
		enrollmentResumeServerField:        strings.TrimSpace(opts.RequiredServerID),
		enrollmentResumeScheduledField:     strings.TrimSpace(opts.enrollmentResumeAt),
		enrollmentResumeReplacementField:   strings.TrimSpace(opts.enrollmentResumeNewID),
		enrollmentResumeGenericLeaseField:  strings.TrimSpace(opts.RequiredLeaseID),
		enrollmentResumeGenericServerField: strings.TrimSpace(opts.RequiredServerID),
	}
}

func isEnrollmentResume(opts ProvisionStackOptions) bool {
	return strings.TrimSpace(opts.enrollmentResumeJobID) != "" &&
		strings.TrimSpace(opts.enrollmentResumeAt) != "" &&
		strings.TrimSpace(opts.enrollmentResumeKind) != "" &&
		strings.TrimSpace(opts.enrollmentResumeKey) != "" &&
		strings.TrimSpace(opts.enrollmentResumeNewID) != "" &&
		strings.TrimSpace(opts.RequiredLeaseID) != "" && strings.TrimSpace(opts.RequiredServerID) != ""
}

func enrollmentResumeKey(stackID, sourceJobID, leaseID, resumeKind, scheduledAt string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(stackID), strings.TrimSpace(sourceJobID), strings.TrimSpace(leaseID), strings.TrimSpace(resumeKind), strings.TrimSpace(scheduledAt),
	}, "\x00")))
	return fmt.Sprintf("resume-%x", sum[:16])
}

func enrollmentResumeReplacementJobID(key string) string {
	return "job-enrollment-" + strings.TrimPrefix(strings.TrimSpace(key), "resume-")
}

func (o *Orchestrator) findEnrollmentResumeReplay(
	ctx context.Context,
	source controlplane.Job,
	opts ProvisionStackOptions,
	receipt map[string]any,
) (enrollmentResumeReplay, error) {
	if o.jobStore == nil {
		return enrollmentResumeReplay{}, fmt.Errorf("%w: durable job store is required for resume recovery", ErrEnrollmentResumeUnavailable)
	}

	sourceReceipt, sourceErr := enrollmentResumeSourceReceipt(source, receipt)
	if sourceErr != nil {
		return enrollmentResumeReplay{}, sourceErr
	}

	replacementID := strings.TrimSpace(opts.enrollmentResumeNewID)
	replacement, err := o.jobStore.GetJob(ctx, source.TenantID, replacementID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return enrollmentResumeReplay{jobID: replacementID, found: sourceReceipt, rehydrate: sourceReceipt}, nil
	}
	if err != nil {
		return enrollmentResumeReplay{}, fmt.Errorf("%w: load deterministic replacement: %w", ErrEnrollmentResumeUnavailable, err)
	}
	if !sourceReceipt {
		latestSource, latestErr := o.jobStore.GetJob(ctx, source.TenantID, source.ID)
		if latestErr != nil {
			return enrollmentResumeReplay{}, fmt.Errorf("%w: refresh source handover claim: %w", ErrEnrollmentResumeUnavailable, latestErr)
		}
		sourceReceipt, sourceErr = enrollmentResumeSourceReceipt(*latestSource, receipt)
		if sourceErr != nil {
			return enrollmentResumeReplay{}, sourceErr
		}
	}
	if validationErr := validateEnrollmentResumeReplacement(*replacement, source, receipt, sourceReceipt); validationErr != nil {
		return enrollmentResumeReplay{}, validationErr
	}
	if queued, ok := o.queue.Get(replacement.ID); ok {
		return enrollmentResumeReplayForQueued(replacement.ID, queued.Snapshot())
	}
	if hasPersistedEnrollmentWait(replacement.Result) {
		// The deterministic replacement reached a later enrollment wait before
		// its process stopped. This request belongs to the preceding wait cycle;
		// hand the caller the newer source job instead of restarting the old one.
		return enrollmentResumeReplay{jobID: replacement.ID, found: true}, nil
	}
	return enrollmentResumeReplayForDurable(*replacement)
}

func enrollmentResumeSourceReceipt(source controlplane.Job, receipt map[string]any) (bool, error) {
	sourceReceipt, sourceReceiptErr := exactEnrollmentResumeReceipt(source.Result, receipt)
	if sourceReceiptErr != nil {
		return false, sourceReceiptErr
	}
	if sourceReceipt && canonicalEnrollmentJobState(source.State) != string(jobs.JobStateCancelled) {
		return false, fmt.Errorf("%w: source receipt exists without a terminal handover claim", ErrEnrollmentResumeUnavailable)
	}
	return sourceReceipt, nil
}

func validateEnrollmentResumeReplacement(
	replacement controlplane.Job,
	source controlplane.Job,
	receipt map[string]any,
	sourceReceipt bool,
) error {
	if replacement.StackID != source.StackID || strings.ToLower(strings.TrimSpace(replacement.Type)) != string(jobs.JobTypeDeploy) {
		return fmt.Errorf("%w: deterministic replacement ID belongs to another job", stackrouting.ErrIdempotencyConflict)
	}
	if !sourceReceipt {
		return fmt.Errorf("%w: replacement exists without a claimed source wait", stackrouting.ErrIdempotencyConflict)
	}
	replacementReceipt, receiptErr := exactEnrollmentResumeReceipt(replacement.Result, receipt)
	if receiptErr != nil {
		return receiptErr
	}
	if !replacementReceipt {
		return fmt.Errorf("%w: deterministic replacement has no exact receipt", stackrouting.ErrIdempotencyConflict)
	}
	return nil
}

func enrollmentResumeReplayForQueued(jobID string, snapshot jobs.JobSnapshot) (enrollmentResumeReplay, error) {
	if snapshot.Type != jobs.JobTypeDeploy {
		return enrollmentResumeReplay{}, fmt.Errorf("%w: process-local replacement is not a deploy", stackrouting.ErrIdempotencyConflict)
	}
	switch snapshot.State {
	case jobs.JobStatePending, jobs.JobStateRunning, jobs.JobStateWaiting, jobs.JobStateCompleted:
		return enrollmentResumeReplay{jobID: jobID, found: true}, nil
	case jobs.JobStateFailed, jobs.JobStateCancelled:
		return enrollmentResumeReplay{}, fmt.Errorf("%w: replacement job was canceled or failed", ErrEnrollmentResumeInvalid)
	default:
		return enrollmentResumeReplay{}, fmt.Errorf("%w: unknown process-local replacement state %q", ErrEnrollmentResumeUnavailable, snapshot.State)
	}
}

func enrollmentResumeReplayForDurable(replacement controlplane.Job) (enrollmentResumeReplay, error) {
	switch canonicalEnrollmentJobState(replacement.State) {
	case persistentStatePending:
		return enrollmentResumeReplay{jobID: replacement.ID, found: true, rehydrate: true}, nil
	case persistentStateRunning:
		return enrollmentResumeReplay{jobID: replacement.ID, found: true}, nil
	case string(jobs.JobStateCompleted):
		return enrollmentResumeReplay{jobID: replacement.ID, found: true}, nil
	case string(jobs.JobStateFailed), string(jobs.JobStateCancelled):
		return enrollmentResumeReplay{}, fmt.Errorf("%w: replacement job was canceled or failed", ErrEnrollmentResumeInvalid)
	default:
		return enrollmentResumeReplay{}, fmt.Errorf("%w: unknown durable replacement state %q", ErrEnrollmentResumeUnavailable, replacement.State)
	}
}

func hasPersistedEnrollmentWait(result map[string]any) bool {
	wait, ok := result["job_wait"].(map[string]any)
	return ok && strings.TrimSpace(stringFromAny(wait["state"])) == string(jobs.JobStateWaiting) &&
		strings.TrimSpace(stringFromAny(wait["reason"])) == jobs.WaitReasonManagedRuntimeEnrollment &&
		strings.TrimSpace(stringFromAny(wait["next_resume_at"])) != ""
}

func exactEnrollmentResumeReceipt(candidate, expected map[string]any) (bool, error) {
	if strings.TrimSpace(stringFromAny(candidate[enrollmentResumeKindField])) != strings.TrimSpace(stringFromAny(expected[enrollmentResumeKindField])) ||
		strings.TrimSpace(stringFromAny(candidate[enrollmentResumeSourceField])) != strings.TrimSpace(stringFromAny(expected[enrollmentResumeSourceField])) ||
		strings.TrimSpace(stringFromAny(candidate[enrollmentResumeKeyField])) != strings.TrimSpace(stringFromAny(expected[enrollmentResumeKeyField])) {
		return false, nil
	}
	for _, field := range []string{
		enrollmentResumeLeaseField,
		enrollmentResumeServerField,
		enrollmentResumeScheduledField,
		enrollmentResumeReplacementField,
	} {
		if strings.TrimSpace(stringFromAny(candidate[field])) != strings.TrimSpace(stringFromAny(expected[field])) {
			return false, fmt.Errorf("%w: enrollment resume key belongs to another exact wait target", stackrouting.ErrIdempotencyConflict)
		}
	}
	return true, nil
}

func canonicalEnrollmentJobState(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "cancelled" {
		return string(jobs.JobStateCancelled)
	}
	return state
}
