// Package jobs provides async job queue processing for kombifyTechstack.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/providererrors"
)

// JobType defines the type of job.
type JobType string

const (
	JobTypeProvision    JobType = "provision"
	JobTypeDeploy       JobType = "deploy"
	JobTypeDestroy      JobType = "destroy"
	JobTypeCommand      JobType = "command"
	JobTypeDriftCheck   JobType = "drift_check"   // F5: Drift detection job
	JobTypeDriftResolve JobType = "drift_resolve" // F5: Drift resolution (re-apply)
	// JobTypeStackKitLifecycle dispatches one closed, typed StackKits command
	// without entering a shell-command, Terramate, or direct OpenTofu path.
	JobTypeStackKitLifecycle JobType = "stackkit_lifecycle"
	// JobTypeReconcileLease decommissions a single managed runtime lease via the
	// provider control plane WITHOUT running the stack's OpenTofu destroy. It is
	// enqueued after a forced decommission of an unreachable runtime so the VM is
	// freed out-of-band and does not keep billing.
	JobTypeReconcileLease JobType = "reconcile_lease"
)

var ErrExecutionTargetBusy = errors.New("job execution target is busy")
var ErrExecutionSnapshotFenced = errors.New("job execution snapshot is fenced by durable state")
var ErrExecutionClaimFenced = errors.New("job execution claim is fenced by durable state")

const (
	WaitReasonStackExecution = "waiting_stack_execution"
	WaitReasonRetryBackoff   = "waiting_retry_backoff"
	WaitReasonExecutionClaim = "waiting_execution_claim"
)

const durableExecutionClaimTimeout = 15 * time.Second

// JobState defines the state of a job.
type JobState string

const (
	JobStatePending   JobState = "pending"
	JobStateWaiting   JobState = "waiting"
	JobStateRunning   JobState = "running"
	JobStateCompleted JobState = "completed"
	JobStateFailed    JobState = "failed"
	JobStateCancelled JobState = "canceled"
)

// WaitingHandoverResult distinguishes a locally claimed wait from absence and
// a terminal source. Only the claimed/absent outcomes can lead to ensuring the
// deterministic replacement job.
type WaitingHandoverResult string

const (
	WaitingHandoverClaimed  WaitingHandoverResult = "claimed"
	WaitingHandoverAbsent   WaitingHandoverResult = "absent"
	WaitingHandoverTerminal WaitingHandoverResult = "terminal"
)

// Job represents an async job.
type Job struct {
	mu           sync.RWMutex           `json:"-"` // H8: Protects all mutable fields
	ID           string                 `json:"id"`
	Type         JobType                `json:"type"`
	TargetType   string                 `json:"target_type"` // stack, node, service
	TargetID     string                 `json:"target_id"`
	TargetName   string                 `json:"target_name"`
	State        JobState               `json:"state"`
	Priority     int                    `json:"priority"`
	Payload      map[string]interface{} `json:"payload"`
	Result       map[string]interface{} `json:"result,omitempty"`
	Error        string                 `json:"error,omitempty"`
	ErrorDetails string                 `json:"error_details,omitempty"` // Detailed error info for troubleshooting
	Step         string                 `json:"step,omitempty"`          // Current step ID for progress tracking
	Message      string                 `json:"message,omitempty"`       // Human-readable status message
	Progress     int                    `json:"progress"`                // 0-100
	Logs         []LogEntry             `json:"logs,omitempty"`
	Attempts     int                    `json:"attempts"`
	MaxAttempts  int                    `json:"max_attempts"`
	CreatedAt    time.Time              `json:"created_at"`
	StartedAt    *time.Time             `json:"started_at,omitempty"`
	CompletedAt  *time.Time             `json:"completed_at,omitempty"`
	WaitReason   string                 `json:"wait_reason,omitempty"`
	NextResumeAt *time.Time             `json:"next_resume_at,omitempty"`
	// In-process only. Job payload/result stay redacted; managed runtime
	// credentials are resolved again after restart or across process boundaries.
	managedRuntimeTarget *ManagedRuntimeTarget     `json:"-"`
	requestAuthority     *requestAuthoritySnapshot `json:"-"`
	suppressPersistence  bool                      `json:"-"`
	// H5: Per-job cancellation support
	cancelFunc            context.CancelFunc `json:"-"` // Not serialized
	cancellationRequested bool               `json:"-"`
	cancellationReason    string             `json:"-"`
}

// LogEntry represents a job log entry.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// JobHandler processes a specific job type.
type JobHandler func(ctx context.Context, job *Job, q *Queue) error

// ExecutionClaim is the immutable durable identity that must be claimed before
// a queue worker may enter a side-effecting handler.
type ExecutionClaim struct {
	JobID      string
	TenantID   string
	JobType    JobType
	TargetType string
	TargetID   string
	StartedAt  time.Time
}

type ExecutionClaimFunc func(context.Context, ExecutionClaim) error
type ExecutionSnapshotSyncFunc func(context.Context, JobSnapshot) error

// Queue manages async job processing.
type Queue struct {
	jobs           map[string]*Job
	jobsMu         sync.RWMutex
	pending        chan *Job
	handlers       map[JobType]JobHandler
	handlersMu     sync.RWMutex // H8: Protects handlers map
	log            *logger.Logger
	running        atomic.Bool // H8: Use atomic for thread-safe access
	workers        int
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	resumeMu       sync.Mutex
	resumes        map[string]*jobResumeSchedule
	resumesClosed  bool
	claimMu        sync.RWMutex
	claimExecution ExecutionClaimFunc
	syncExecution  ExecutionSnapshotSyncFunc
	targetMu       sync.Mutex
	targets        map[string]*jobTargetExecution
	deferMu        sync.Mutex
	defers         map[string]*executionDeferState
	deferObserver  ExecutionDeferObserver
	// maxCompletedJobs limits how many completed/failed jobs are kept in memory
	maxCompletedJobs int
	// completedJobTTL is how long completed jobs are kept before cleanup
	completedJobTTL time.Duration
}

type jobResumeSchedule struct {
	cancel context.CancelFunc
}

type jobTargetExecution struct {
	token chan struct{}
	refs  int
}

// NewQueue creates a new job queue.
func NewQueue(workers int, log *logger.Logger) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		jobs:             make(map[string]*Job),
		pending:          make(chan *Job, 1000),
		handlers:         make(map[JobType]JobHandler),
		workers:          workers,
		ctx:              ctx,
		cancel:           cancel,
		resumes:          make(map[string]*jobResumeSchedule),
		targets:          make(map[string]*jobTargetExecution),
		maxCompletedJobs: 1000,           // Keep max 1000 completed jobs
		completedJobTTL:  24 * time.Hour, // Clean up jobs older than 24 hours
	}
	if log != nil {
		q.log = log.WithComponent("jobs")
	} else {
		q.log = logger.Default().WithComponent("jobs")
	}
	return q
}

// SetExecutionClaimer installs the durable compare-and-set required before a
// worker starts a side-effecting handler. It must be configured before Start.
func (q *Queue) SetExecutionClaimer(claimer ExecutionClaimFunc) {
	q.claimMu.Lock()
	q.claimExecution = claimer
	q.claimMu.Unlock()
}

// SetExecutionSnapshotSyncer installs the durable acknowledgement required
// before a waiting/retry timer may submit the same job for another execution.
func (q *Queue) SetExecutionSnapshotSyncer(syncer ExecutionSnapshotSyncFunc) {
	q.claimMu.Lock()
	q.syncExecution = syncer
	q.claimMu.Unlock()
}

func (q *Queue) executionClaimer() ExecutionClaimFunc {
	q.claimMu.RLock()
	defer q.claimMu.RUnlock()
	return q.claimExecution
}

func (q *Queue) executionSnapshotSyncer() ExecutionSnapshotSyncFunc {
	q.claimMu.RLock()
	defer q.claimMu.RUnlock()
	return q.syncExecution
}

// RegisterHandler registers a handler for a job type.
// Note: All handlers should be registered before calling Start().
func (q *Queue) RegisterHandler(jobType JobType, handler JobHandler) {
	q.handlersMu.Lock()
	defer q.handlersMu.Unlock()
	q.handlers[jobType] = handler
}

// getHandler safely retrieves a handler for a job type.
func (q *Queue) getHandler(jobType JobType) (JobHandler, bool) {
	q.handlersMu.RLock()
	defer q.handlersMu.RUnlock()
	handler, ok := q.handlers[jobType]
	return handler, ok
}

// Start starts the job queue workers.
func (q *Queue) Start(ctx context.Context) {
	q.running.Store(true)
	q.log.Info("starting_job_queue", "workers", q.workers)

	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx, i)
	}

	// Start cleanup goroutine
	q.wg.Add(1)
	go q.cleanupWorker()
}

// Stop stops the job queue gracefully.
func (q *Queue) Stop() {
	q.log.Info("stopping_job_queue")
	q.running.Store(false)
	q.cancel() // Signal workers and scheduled resumptions to stop.
	q.cancelAllResumes()
	q.cancelJobsForShutdown()
	q.wg.Wait()
	q.log.Info("job_queue_stopped")
}

func (q *Queue) cancelJobsForShutdown() {
	q.jobsMu.RLock()
	queued := make([]*Job, 0, len(q.jobs))
	for _, job := range q.jobs {
		queued = append(queued, job)
	}
	q.jobsMu.RUnlock()

	for _, job := range queued {
		var cancel context.CancelFunc
		job.mu.Lock()
		switch job.State {
		case JobStatePending, JobStateWaiting:
			// Unclaimed durable work belongs to the control plane, not this
			// process. Detach the local projection without turning a restart into
			// a user cancellation or erasing enrollment recovery evidence.
			job.suppressPersistence = true
			job.cancelFunc = nil
		case JobStateRunning:
			// The handler owns the terminal transition. Cancel its context but
			// keep the durable execution active until the handler has unwound.
			job.cancellationRequested = true
			job.cancellationReason = "Job canceled during orchestrator shutdown"
			cancel = job.cancelFunc
		}
		job.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

// DetachFencedExecution stops this process from executing a job whose durable
// row has been claimed by someone else.
//
// A durable fence means another executor owns the row, so this process is a
// zombie: continuing would let two processes drive the same job against real
// providers, which is exactly what the fence exists to prevent. Previously the
// progress-sync goroutine simply returned on a fence, which stopped the
// reporting but left the handler running.
//
// The semantics are the shutdown ones, not Cancel's. Cancel writes a terminal
// cancellation, which would corrupt a row this process no longer owns. Here the
// local context is cancelled, persistence is suppressed so the unwinding
// handler cannot write over the new owner, and the durable row is left entirely
// to whoever holds it.
//
// Returns true when a running execution was detached.
func (q *Queue) DetachFencedExecution(jobID, reason string) bool {
	return q.DetachFencedExecutionIfUnchanged(jobID, JobSnapshot{ID: jobID}, reason)
}

// DetachFencedExecutionIfUnchanged atomically verifies that the local
// execution still matches the snapshot whose durable sync was fenced before
// suppressing or cancelling it. A waiting job can be claimed by this same
// process between a caller's conflict check and the detach; detaching that new
// execution would strand a valid durable claim with no executor.
func (q *Queue) DetachFencedExecutionIfUnchanged(jobID string, observed JobSnapshot, reason string) bool {
	q.jobsMu.RLock()
	job, ok := q.jobs[jobID]
	q.jobsMu.RUnlock()
	if !ok {
		return false
	}
	if reason == "" {
		reason = "Durable job execution was claimed by another process"
	}
	var cancel context.CancelFunc
	job.mu.Lock()
	if observed.ID != "" && observed.State != "" && !jobSnapshotMatchesFenceLocked(observed, job) {
		job.mu.Unlock()
		return false
	}
	detached := job.State == JobStateRunning
	if detached {
		// The handler owns its own unwinding; only its context is cancelled.
		job.cancellationRequested = true
		job.cancellationReason = reason
		cancel = job.cancelFunc
	}
	// Suppress in every non-terminal state: a fenced projection must never write
	// over the executor that now owns the row.
	switch job.State {
	case JobStatePending, JobStateWaiting, JobStateRunning:
		job.suppressPersistence = true
	}
	job.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return detached
}

func jobSnapshotMatchesFenceLocked(observed JobSnapshot, job *Job) bool {
	if observed.Type != job.Type || observed.State != job.State {
		return false
	}
	if observed.StartedAt == nil || job.StartedAt == nil {
		return observed.StartedAt == nil && job.StartedAt == nil
	}
	return observed.StartedAt.Equal(*job.StartedAt)
}

// cleanupWorker periodically removes old completed jobs to prevent memory leaks.
func (q *Queue) cleanupWorker() {
	defer q.wg.Done()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-q.ctx.Done():
			return
		case <-ticker.C:
			q.cleanupOldJobs()
		}
	}
}

// cleanupOldJobs removes completed/failed/canceled jobs older than TTL.
func (q *Queue) cleanupOldJobs() {
	q.jobsMu.Lock()
	defer q.jobsMu.Unlock()

	now := time.Now()
	toDelete := make([]string, 0)
	completedCount := 0

	// Count completed jobs and mark old ones for deletion
	for id, job := range q.jobs {
		job.mu.RLock()
		state := job.State
		completedAt := job.CompletedAt
		job.mu.RUnlock()
		if isTerminalJobState(state) {
			completedCount++
			if completedAt != nil && now.Sub(*completedAt) > q.completedJobTTL {
				toDelete = append(toDelete, id)
			}
		}
	}

	// Delete old jobs
	for _, id := range toDelete {
		delete(q.jobs, id)
		q.clearExecutionDefer(id)
	}

	// If still over limit, delete oldest completed jobs
	if completedCount-len(toDelete) > q.maxCompletedJobs {
		// Find and delete excess completed jobs (oldest first)
		type jobAge struct {
			id  string
			age time.Time
		}
		completed := make([]jobAge, 0)
		for id, job := range q.jobs {
			job.mu.RLock()
			state := job.State
			createdAt := job.CreatedAt
			completedAt := job.CompletedAt
			job.mu.RUnlock()
			if isTerminalJobState(state) {
				// Use CompletedAt if available, otherwise fall back to CreatedAt
				age := createdAt
				if completedAt != nil {
					age = *completedAt
				}
				completed = append(completed, jobAge{id: id, age: age})
			}
		}
		// Sort by age (oldest first) using optimized sort
		sort.Slice(completed, func(i, j int) bool {
			return completed[i].age.Before(completed[j].age)
		})
		// Delete excess
		excess := len(completed) - q.maxCompletedJobs
		for i := 0; i < excess && i < len(completed); i++ {
			delete(q.jobs, completed[i].id)
		}
	}

	if len(toDelete) > 0 {
		q.log.Debug("cleaned_up_old_jobs", "count", len(toDelete))
	}
}

func (q *Queue) worker(ctx context.Context, id int) {
	defer q.wg.Done()
	q.log.Debug("worker_started", "worker_id", id)

	for {
		select {
		case <-ctx.Done():
			return
		case <-q.ctx.Done():
			return
		case job, ok := <-q.pending:
			if !ok {
				return // Channel closed
			}
			q.processJob(ctx, job)
		}
	}
}

type jobExecutionAttempt struct {
	ctx                  context.Context
	cleanup              func()
	attempts             int
	maxAttempts          int
	previousState        JobState
	previousStartedAt    *time.Time
	previousWaitReason   string
	previousNextResumeAt *time.Time
	previousAttempts     int
}

func (q *Queue) processJob(ctx context.Context, job *Job) {
	releaseTarget, acquired := q.acquireTargetExecution(ctx, job)
	if !acquired {
		return
	}
	defer releaseTarget()

	attempt, started := q.beginJobExecution(ctx, job)
	if !started {
		return
	}
	defer attempt.cleanup()

	snapshot := job.Snapshot()
	handler, ok := q.getHandler(snapshot.Type)
	if !ok {
		q.failJob(job, fmt.Sprintf("no handler for job type: %s", snapshot.Type))
		return
	}
	q.addLog(job, "info", fmt.Sprintf("Job started (attempt %d/%d)", attempt.attempts, attempt.maxAttempts))
	q.log.Info("job_started", "id", snapshot.ID, "type", snapshot.Type, "target", snapshot.TargetID, "attempt", attempt.attempts)

	handlerCtx, finishSentry := withJobSentryScope(attempt.ctx, job)
	var handlerErr error
	func() {
		defer func() {
			if isJobWaitError(handlerErr) {
				finishSentry(nil)
				return
			}
			finishSentry(handlerErr)
		}()
		handlerErr = handler(handlerCtx, job, q)
	}()
	q.finishJobExecution(ctx, handlerCtx, job, handlerErr)
}

func (q *Queue) beginJobExecution(ctx context.Context, job *Job) (*jobExecutionAttempt, bool) {
	jobCtx, jobCancel := context.WithCancel(ctx)
	stopQueueCancellation := context.AfterFunc(q.ctx, jobCancel)
	cleanup := func() {
		stopQueueCancellation()
		jobCancel()
	}
	jobCtx = contextWithJobEdgeFlags(jobCtx, job)
	jobCtx = contextWithJobRequestAuthority(jobCtx, job)
	if jobCtx.Err() != nil {
		cleanup()
		return nil, false
	}

	job.mu.Lock()
	if job.suppressPersistence || (job.State != JobStatePending && job.State != JobStateWaiting) {
		job.mu.Unlock()
		cleanup()
		return nil, false
	}
	attempt := &jobExecutionAttempt{
		ctx: jobCtx, cleanup: cleanup, previousState: job.State,
		previousStartedAt: cloneTimePointer(job.StartedAt), previousWaitReason: job.WaitReason,
		previousNextResumeAt: cloneTimePointer(job.NextResumeAt), previousAttempts: job.Attempts,
	}
	job.cancelFunc = jobCancel
	job.cancellationRequested = false
	job.cancellationReason = ""
	now := time.Now().UTC().Truncate(time.Microsecond)
	job.State = JobStateRunning
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.StartedAt = &now
	job.Attempts++
	attempt.attempts = job.Attempts
	attempt.maxAttempts = job.MaxAttempts
	claim := ExecutionClaim{
		JobID: job.ID, TenantID: firstJobString(job.Payload, job.Result, "tenant_id"),
		JobType: job.Type, TargetType: job.TargetType, TargetID: job.TargetID, StartedAt: now,
	}
	job.mu.Unlock()

	if claimer := q.executionClaimer(); claimer != nil {
		if claim.TenantID == "" {
			q.handleExecutionClaimError(ctx, job, attempt, fmt.Errorf("%w: missing tenant identity", ErrExecutionClaimFenced))
			return nil, false
		}
		claimCtx, cancelClaim := context.WithTimeout(jobCtx, durableExecutionClaimTimeout)
		claimErr := claimer(claimCtx, claim)
		cancelClaim()
		if claimErr != nil {
			q.handleExecutionClaimError(ctx, job, attempt, claimErr)
			return nil, false
		}
	}
	if requested, reason := q.jobCancellation(job, jobCtx); requested {
		attempt.cleanup()
		q.clearExecutionDefer(job.ID)
		q.cancelJobInternal(job, reason)
		return nil, false
	}
	// The claim was won, so any defer streak this job had accumulated is over
	// and the next block must start from the base interval again.
	q.clearExecutionDefer(job.ID)
	return attempt, true
}

func (q *Queue) handleExecutionClaimError(ctx context.Context, job *Job, attempt *jobExecutionAttempt, claimErr error) {
	job.mu.Lock()
	requested := job.cancellationRequested || attempt.ctx.Err() != nil
	reason := job.cancellationReason
	if requested {
		q.restoreUnclaimedExecutionLocked(job, attempt)
		shutdown := q.ctx.Err() != nil || reason == "Job canceled during orchestrator shutdown"
		if shutdown {
			job.suppressPersistence = true
			job.mu.Unlock()
			attempt.cleanup()
			return
		}
		job.mu.Unlock()
		attempt.cleanup()
		if reason == "" {
			reason = "Job canceled by user request"
		}
		q.cancelJobInternal(job, reason)
		return
	}
	if errors.Is(claimErr, ErrExecutionTargetBusy) {
		q.deferBusyExecutionClaim(ctx, job, attempt.cleanup, attempt.previousStartedAt)
		return
	}
	if errors.Is(claimErr, ErrExecutionClaimFenced) {
		q.restoreUnclaimedExecutionLocked(job, attempt)
		job.suppressPersistence = true
		job.mu.Unlock()
		attempt.cleanup()
		q.clearExecutionDefer(job.ID)
		q.log.Warn("job_execution_claim_fenced", "id", job.ID, "type", job.Type, "target", job.TargetID, "error", claimErr)
		return
	}
	q.deferUnavailableExecutionClaim(ctx, job, attempt.cleanup, attempt.previousStartedAt, claimErr)
}

func (q *Queue) restoreUnclaimedExecutionLocked(job *Job, attempt *jobExecutionAttempt) {
	job.State = attempt.previousState
	job.StartedAt = cloneTimePointer(attempt.previousStartedAt)
	job.WaitReason = attempt.previousWaitReason
	job.NextResumeAt = cloneTimePointer(attempt.previousNextResumeAt)
	job.Attempts = attempt.previousAttempts
	job.cancelFunc = nil
}

func (q *Queue) jobCancellation(job *Job, jobCtx context.Context) (bool, string) {
	job.mu.RLock()
	requested := job.cancellationRequested
	reason := job.cancellationReason
	job.mu.RUnlock()
	if !requested && jobCtx.Err() != context.Canceled {
		return false, ""
	}
	if reason == "" {
		if q.ctx.Err() != nil {
			reason = "Job canceled during orchestrator shutdown"
		} else {
			reason = "Job canceled by user request"
		}
	}
	return true, reason
}

func (q *Queue) finishJobExecution(ctx, jobCtx context.Context, job *Job, handlerErr error) {
	if requested, reason := q.jobCancellation(job, jobCtx); requested {
		q.cancelJobInternal(job, reason)
		return
	}
	if handlerErr != nil {
		q.handleJobExecutionError(ctx, job, handlerErr)
		return
	}
	q.completeJob(job)
}

func (q *Queue) handleJobExecutionError(ctx context.Context, job *Job, err error) {
	if waitErr, waiting := asJobWaitError(err); waiting {
		q.waitJob(ctx, job, waitErr)
		return
	}
	if provisionErr, ok := err.(*ProvisionError); ok {
		job.mu.Lock()
		if job.State == JobStateRunning {
			job.Step = provisionErr.Step
		}
		job.mu.Unlock()
		q.failJobWithDetails(job, provisionErr.Message, provisionErr.Details)
		return
	}
	category := ClassifyError(err)
	retryable := IsRetryable(err)
	job.mu.RLock()
	currentAttempts := job.Attempts
	maxAttempts := job.MaxAttempts
	job.mu.RUnlock()
	q.log.Warn("job_error", "id", job.ID, "error", err, "category", category.String(), "retryable", retryable)
	if retryable && currentAttempts < maxAttempts {
		q.deferJobRetry(ctx, job, err, category, currentAttempts)
		return
	}
	reason := fmt.Sprintf("max attempts reached (%d/%d): %s", currentAttempts, maxAttempts, err)
	if !retryable {
		reason = fmt.Sprintf("non-retryable error (%s): %s", category.String(), err)
	}
	q.failJob(job, reason)
}

func (q *Queue) deferJobRetry(ctx context.Context, job *Job, err error, category ErrorCategory, currentAttempts int) {
	delay := DefaultRetryPolicy().CalculateDelay(currentAttempts)
	nextResumeAt := time.Now().UTC().Add(delay)
	q.addLog(job, "warn", fmt.Sprintf("Attempt %d failed (%s): %s. Retrying in %v...", currentAttempts, category.String(), err, delay))
	job.mu.Lock()
	if job.State != JobStateRunning {
		job.mu.Unlock()
		return
	}
	job.State = JobStateWaiting
	job.WaitReason = WaitReasonRetryBackoff
	job.NextResumeAt = &nextResumeAt
	job.Message = fmt.Sprintf("Retrying after a %s error", category.String())
	job.cancelFunc = nil
	job.mu.Unlock()
	q.log.Info("job_retry_waiting", "id", job.ID, "attempt", currentAttempts+1, "resume_at", nextResumeAt)
	q.scheduleResume(ctx, job, delay)
}

func (q *Queue) completeJob(job *Job) {
	completed := time.Now()
	job.mu.Lock()
	if job.State != JobStateRunning {
		job.mu.Unlock()
		return
	}
	job.State = JobStateCompleted
	job.CompletedAt = &completed
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.Progress = 100
	job.cancelFunc = nil
	startedAt := cloneTimePointer(job.StartedAt)
	job.mu.Unlock()
	q.addLog(job, "info", "Job completed successfully")
	if startedAt != nil {
		q.log.Info("job_completed", "id", job.ID, "duration", completed.Sub(*startedAt))
	} else {
		q.log.Info("job_completed", "id", job.ID)
	}
}

func (q *Queue) deferBusyExecutionClaim(
	ctx context.Context,
	job *Job,
	cleanup func(),
	previousStartedAt *time.Time,
) {
	q.deferExecutionClaim(
		ctx,
		job,
		cleanup,
		previousStartedAt,
		WaitReasonStackExecution,
		"Another operation for this stack is still running",
		"Waiting for the active stack operation to finish",
	)
}

func (q *Queue) deferUnavailableExecutionClaim(
	ctx context.Context,
	job *Job,
	cleanup func(),
	previousStartedAt *time.Time,
	claimErr error,
) {
	q.deferExecutionClaim(
		ctx,
		job,
		cleanup,
		previousStartedAt,
		WaitReasonExecutionClaim,
		"Durable execution coordination is temporarily unavailable",
		fmt.Sprintf("Waiting for durable execution coordination: %v", claimErr),
	)
}

func (q *Queue) deferExecutionClaim(
	ctx context.Context,
	job *Job,
	cleanup func(),
	previousStartedAt *time.Time,
	waitReason string,
	message string,
	logMessage string,
) {
	// The retry is bounded rather than a flat 1/s hot loop, and a wait that
	// outlives ExecutionDeferAlertAfter is reported instead of staying silent:
	// a stack whose claim is held by a job nothing can reclaim used to defer
	// once per second forever with nothing above info level to show for it.
	now := time.Now().UTC()
	retryAfter, alert := q.noteExecutionDefer(job.ID, waitReason, now)
	nextResumeAt := now.Add(retryAfter)
	job.State = JobStateWaiting
	// The durable claim leaves the record pending. Preserve its previous
	// execution generation so a waiting snapshot can still pass the store's
	// compare-and-swap fence after an earlier waiting attempt.
	job.StartedAt = cloneTimePointer(previousStartedAt)
	if job.Attempts > 0 {
		job.Attempts--
	}
	job.WaitReason = waitReason
	job.NextResumeAt = &nextResumeAt
	job.Message = message
	job.Error = ""
	job.ErrorDetails = ""
	jobType := job.Type
	targetType := job.TargetType
	targetID := job.TargetID
	job.cancelFunc = nil
	job.mu.Unlock()
	cleanup()
	q.addLog(job, "info", logMessage)
	q.log.Info("job_execution_deferred", "id", job.ID, "target", targetID, "reason", waitReason,
		"resume_at", nextResumeAt, "retry_in", retryAfter.String())
	if alert != nil {
		alert.JobType = jobType
		alert.TargetType = targetType
		alert.TargetID = targetID
		q.log.Warn("job_execution_defer_stalled",
			"id", alert.JobID, "type", string(alert.JobType), "target", alert.TargetID,
			"reason", alert.WaitReason, "waiting_for", alert.WaitingFor.String(),
			"attempts", alert.Attempts, "retry_in", alert.NextRetryIn.String())
		if observer := q.executionDeferObserver(); observer != nil {
			observer(*alert)
		}
	}
	q.scheduleResume(ctx, job, retryAfter)
}

func (q *Queue) waitJob(ctx context.Context, job *Job, waitErr *JobWaitError) {
	delay := waitErr.ResumeAfter
	if delay <= 0 {
		delay = defaultJobWaitResumeDelay
	}
	nextResumeAt := time.Now().UTC().Add(delay)
	reason := waitErr.Reason
	if reason == "" {
		reason = "waiting_dependency"
	}
	message := waitErr.Error()
	waitLog := fmt.Sprintf("%s Next resume at %s.", message, nextResumeAt.Format(time.RFC3339))

	job.mu.Lock()
	if job.State != JobStateRunning {
		job.mu.Unlock()
		return
	}
	if job.Attempts > 0 {
		job.Attempts--
	}
	job.State = JobStateWaiting
	job.CompletedAt = nil
	job.WaitReason = reason
	job.NextResumeAt = &nextResumeAt
	job.Message = message
	job.Error = ""
	job.ErrorDetails = ""
	job.cancelFunc = nil
	job.Logs = append(job.Logs, LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   waitLog,
	})
	job.mu.Unlock()

	q.log.Info("job_waiting", "id", job.ID, "reason", reason, "resume_at", nextResumeAt)
	schedule := q.scheduleResume(ctx, job, delay)
	if schedule == nil {
		return
	}
	job.mu.RLock()
	stillWaiting := job.State == JobStateWaiting
	job.mu.RUnlock()
	if !stillWaiting {
		q.cancelResumeSchedule(job.ID, schedule)
	}
}

func (q *Queue) scheduleResume(parent context.Context, job *Job, delay time.Duration) *jobResumeSchedule {
	resumeCtx, cancel := context.WithCancel(q.ctx)
	schedule := &jobResumeSchedule{cancel: cancel}

	q.resumeMu.Lock()
	if q.resumes == nil {
		q.resumes = make(map[string]*jobResumeSchedule)
	}
	if q.resumesClosed {
		q.resumeMu.Unlock()
		cancel()
		return nil
	}
	if existing := q.resumes[job.ID]; existing != nil {
		existing.cancel()
	}
	q.resumes[job.ID] = schedule
	q.wg.Add(1)
	q.resumeMu.Unlock()

	go func() {
		defer q.wg.Done()
		defer q.clearResume(job.ID, schedule)

		if !q.persistWaitingBeforeResume(resumeCtx, parent, job) {
			return
		}
		remaining := delay
		if snapshot := job.Snapshot(); snapshot.NextResumeAt != nil {
			remaining = time.Until(*snapshot.NextResumeAt)
			if remaining < 0 {
				remaining = 0
			}
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()

		select {
		case <-resumeCtx.Done():
			return
		case <-parent.Done():
			return
		case <-timer.C:
		}

		job.mu.RLock()
		waiting := job.State == JobStateWaiting
		reason := job.WaitReason
		job.mu.RUnlock()
		if !waiting {
			return
		}

		select {
		case q.pending <- job:
			q.log.Info("job_resume_scheduled", "id", job.ID, "reason", reason)
		case <-resumeCtx.Done():
		case <-parent.Done():
		}
	}()
	return schedule
}

func (q *Queue) persistWaitingBeforeResume(resumeCtx, parent context.Context, job *Job) bool {
	syncer := q.executionSnapshotSyncer()
	initial := job.Snapshot()
	if syncer == nil || firstJobString(initial.Payload, initial.Result, "tenant_id") == "" {
		return true
	}
	for attempt := 1; ; attempt++ {
		snapshot := job.Snapshot()
		if snapshot.State != JobStateWaiting || snapshot.PersistenceSuppressed {
			return false
		}
		err := syncer(resumeCtx, snapshot)
		if err == nil {
			return true
		}
		if errors.Is(err, ErrExecutionSnapshotFenced) {
			current := job.Snapshot()
			if executionSnapshotFenceChanged(snapshot, current) {
				continue
			}
			job.mu.Lock()
			if executionSnapshotMatchesJobLocked(snapshot, job) {
				job.suppressPersistence = true
			}
			job.mu.Unlock()
			q.log.Warn("job_resume_fenced_by_durable_state", "id", snapshot.ID, "reason", snapshot.WaitReason)
			return false
		}
		if attempt == 1 || attempt%10 == 0 {
			q.log.Warn("job_resume_waiting_for_durable_ack", "id", snapshot.ID, "reason", snapshot.WaitReason, "attempt", attempt, "error", err)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-resumeCtx.Done():
			timer.Stop()
			return false
		case <-parent.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func executionSnapshotMatchesJobLocked(snapshot JobSnapshot, job *Job) bool {
	if snapshot.Type != job.Type || snapshot.State != job.State {
		return false
	}
	if snapshot.StartedAt == nil || job.StartedAt == nil {
		return snapshot.StartedAt == nil && job.StartedAt == nil
	}
	return snapshot.StartedAt.Equal(*job.StartedAt)
}

func executionSnapshotFenceChanged(left, right JobSnapshot) bool {
	if left.Type != right.Type || left.State != right.State {
		return true
	}
	if left.StartedAt == nil || right.StartedAt == nil {
		return left.StartedAt != nil || right.StartedAt != nil
	}
	return !left.StartedAt.Equal(*right.StartedAt)
}

func (q *Queue) clearResume(jobID string, schedule *jobResumeSchedule) {
	q.resumeMu.Lock()
	defer q.resumeMu.Unlock()
	if q.resumes[jobID] == schedule {
		delete(q.resumes, jobID)
	}
}

func (q *Queue) cancelResume(jobID string) {
	q.resumeMu.Lock()
	defer q.resumeMu.Unlock()
	if schedule := q.resumes[jobID]; schedule != nil {
		schedule.cancel()
		delete(q.resumes, jobID)
	}
}

func (q *Queue) cancelResumeSchedule(jobID string, schedule *jobResumeSchedule) {
	q.resumeMu.Lock()
	defer q.resumeMu.Unlock()
	if q.resumes[jobID] == schedule {
		schedule.cancel()
		delete(q.resumes, jobID)
	}
}

func (q *Queue) cancelAllResumes() {
	q.resumeMu.Lock()
	defer q.resumeMu.Unlock()
	q.resumesClosed = true
	for jobID, schedule := range q.resumes {
		schedule.cancel()
		delete(q.resumes, jobID)
	}
}

func (q *Queue) acquireTargetExecution(ctx context.Context, job *Job) (func(), bool) {
	key := strings.TrimSpace(job.TargetType) + "\x00" + strings.TrimSpace(job.TargetID)
	if strings.Trim(key, "\x00") == "" {
		return func() {}, true
	}

	q.targetMu.Lock()
	entry := q.targets[key]
	if entry == nil {
		entry = &jobTargetExecution{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		q.targets[key] = entry
	}
	entry.refs++
	q.targetMu.Unlock()

	select {
	case <-entry.token:
		var once sync.Once
		return func() {
			once.Do(func() {
				entry.token <- struct{}{}
				q.releaseTargetExecutionRef(key, entry)
			})
		}, true
	case <-ctx.Done():
		q.releaseTargetExecutionRef(key, entry)
		return nil, false
	case <-q.ctx.Done():
		q.releaseTargetExecutionRef(key, entry)
		return nil, false
	}
}

func (q *Queue) releaseTargetExecutionRef(key string, entry *jobTargetExecution) {
	q.targetMu.Lock()
	defer q.targetMu.Unlock()
	entry.refs--
	if entry.refs == 0 && q.targets[key] == entry {
		delete(q.targets, key)
	}
}

func firstJobString(primary, secondary map[string]interface{}, key string) string {
	for _, values := range []map[string]interface{}{primary, secondary} {
		if values == nil {
			continue
		}
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (q *Queue) failJob(job *Job, errMsg string) {
	q.cancelResume(job.ID)
	now := time.Now()
	job.mu.Lock()
	if isTerminalJobState(job.State) {
		job.mu.Unlock()
		return
	}
	job.State = JobStateFailed
	job.CompletedAt = &now
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.Error = errMsg
	job.cancelFunc = nil
	job.mu.Unlock()
	q.addLog(job, "error", errMsg)

	q.log.Error("job_failed", providerErrorLogAttrs(errMsg, "id", job.ID, "error", errMsg)...)
}

// failJobWithDetails sets detailed error information for troubleshooting.
func (q *Queue) failJobWithDetails(job *Job, errMsg, details string) {
	q.cancelResume(job.ID)
	now := time.Now()
	job.mu.Lock()
	if isTerminalJobState(job.State) {
		job.mu.Unlock()
		return
	}
	job.State = JobStateFailed
	job.CompletedAt = &now
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.Error = errMsg
	job.ErrorDetails = details
	job.cancelFunc = nil
	job.mu.Unlock()
	q.addLog(job, "error", errMsg)

	q.log.Error("job_failed", providerErrorLogAttrs(errMsg+"\n"+details,
		"id", job.ID,
		"error", errMsg,
		"details", details,
	)...)
}

func isTerminalJobState(state JobState) bool {
	return state == JobStateCompleted || state == JobStateFailed || state == JobStateCancelled
}

func providerErrorLogAttrs(message string, attrs ...any) []any {
	info := providererrors.ClassifyMessage(message)
	if !info.HasSignal() {
		return attrs
	}
	out := make([]any, 0, len(attrs)+10)
	out = append(out, attrs...)
	if info.Provider != "" {
		out = append(out, "provider_id", info.Provider)
	}
	if info.Code != "" {
		out = append(out, "provider_error_code", info.Code)
	}
	if info.Category != "" {
		out = append(out, "provider_error_category", info.Category)
	}
	if info.RetryHint != "" {
		out = append(out, "provider_retry_hint", info.RetryHint)
	}
	if info.Summary != "" {
		out = append(out, "provider_error_summary", info.Summary)
	}
	out = append(out, "terminal_for_attempt", info.Terminal)
	return out
}

// H8: addLog uses job mutex to safely append to Logs slice
func (q *Queue) addLog(job *Job, level, message string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Logs = append(job.Logs, LogEntry{
		Timestamp: time.Now(),
		Level:     level,
		Message:   message,
	})
}

// Enqueue adds a new job to the queue.
func (q *Queue) Enqueue(job *Job) error {
	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	if job.MaxAttempts == 0 {
		job.MaxAttempts = 3
	}
	job.State = JobStatePending
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.CreatedAt = time.Now()
	job.Progress = 0

	q.jobsMu.Lock()
	q.jobs[job.ID] = job
	q.jobsMu.Unlock()

	q.addLog(job, "info", "Job queued")

	select {
	case q.pending <- job:
		q.log.Info("job_enqueued", "id", job.ID, "type", job.Type)
		return nil
	default:
		// Admission failed, so this job is not process-local work. Remove only
		// the exact pointer we inserted; a concurrent replacement under the same
		// deterministic ID must not be deleted. Durable callers can then rebuild
		// and retry the same job ID instead of replaying a dead pending entry.
		q.jobsMu.Lock()
		if q.jobs[job.ID] == job {
			delete(q.jobs, job.ID)
		}
		q.jobsMu.Unlock()
		return fmt.Errorf("job queue full")
	}
}

// Get returns a job by ID.
func (q *Queue) Get(id string) (*Job, bool) {
	q.jobsMu.RLock()
	defer q.jobsMu.RUnlock()
	job, ok := q.jobs[id]
	return job, ok
}

// CancelStackRollouts atomically cancels process-local provision/deploy jobs
// for one stack before a destroy rollout is admitted. In particular this
// removes scheduled enrollment resumptions so teardown cannot race a delayed
// deploy wakeup.
func (q *Queue) CancelStackRollouts(stackID string) []string {
	stackID = strings.TrimSpace(stackID)
	if stackID == "" {
		return nil
	}
	q.jobsMu.RLock()
	candidates := make([]*Job, 0)
	for _, job := range q.jobs {
		job.mu.RLock()
		matches := strings.TrimSpace(job.TargetID) == stackID &&
			(job.Type == JobTypeProvision || job.Type == JobTypeDeploy) &&
			(job.State == JobStatePending || job.State == JobStateWaiting || job.State == JobStateRunning)
		job.mu.RUnlock()
		if matches {
			candidates = append(candidates, job)
		}
	}
	q.jobsMu.RUnlock()

	canceled := make([]string, 0, len(candidates))
	for _, job := range candidates {
		if err := q.Cancel(job.ID); err == nil {
			canceled = append(canceled, job.ID)
		}
	}
	return canceled
}

// SupersedeWaitingEnrollment performs the process-local half of the durable
// enrollment handover. Receipt merge, durable compare-and-set, and terminal
// transition happen while holding job.mu, so the periodic synchronizer cannot
// publish a stale pre-receipt snapshot between those steps.
func (q *Queue) SupersedeWaitingEnrollment(
	jobID string,
	receipt map[string]any,
	persist func() error,
) (WaitingHandoverResult, error) {
	return q.SupersedeWaitingJob(jobID, JobTypeDeploy, WaitReasonManagedRuntimeEnrollment, receipt, persist)
}

// SupersedeWaitingJob performs the process-local half of a deterministic
// managed-rollout handover. The caller supplies the exact job type and wait
// reason that its durable compare-and-set will claim. This keeps provider
// provisioning and enrollment recovery on one fencing primitive instead of
// growing a second queue mutation path.
func (q *Queue) SupersedeWaitingJob(
	jobID string,
	expectedType JobType,
	expectedReason string,
	receipt map[string]any,
	persist func() error,
) (WaitingHandoverResult, error) {
	expectedReason = strings.TrimSpace(expectedReason)
	if expectedType == "" || expectedReason == "" {
		return "", fmt.Errorf("managed rollout handover requires an exact job type and wait reason")
	}

	q.jobsMu.RLock()
	job, ok := q.jobs[jobID]
	q.jobsMu.RUnlock()
	if !ok {
		return WaitingHandoverAbsent, nil
	}

	job.mu.Lock()
	if isTerminalJobState(job.State) {
		job.mu.Unlock()
		return WaitingHandoverTerminal, nil
	}
	if job.Type != expectedType {
		actual := job.Type
		job.mu.Unlock()
		return "", fmt.Errorf("cannot resume job type %q; want %q", actual, expectedType)
	}
	if job.State != JobStateWaiting {
		state := job.State
		job.mu.Unlock()
		return "", fmt.Errorf("cannot resume job in state %q; want %q", state, JobStateWaiting)
	}
	if job.WaitReason != expectedReason {
		actual := job.WaitReason
		job.mu.Unlock()
		return "", fmt.Errorf("job wait reason is %q, want %q", actual, expectedReason)
	}

	if persist == nil {
		job.mu.Unlock()
		return "", fmt.Errorf("durable waiting handover callback is required")
	}
	previousResult := cloneJobMap(job.Result)
	mergeJobResultValues(job, receipt)
	if err := persist(); err != nil {
		job.Result = previousResult
		job.mu.Unlock()
		return "", err
	}
	completed := time.Now()
	job.State = JobStateCancelled
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.CompletedAt = &completed
	job.Message = "Superseded by deterministic managed rollout recovery"
	job.Error = "Job superseded by exact managed rollout recovery"
	job.ErrorDetails = ""
	job.cancelFunc = nil
	job.Logs = append(job.Logs, LogEntry{
		Timestamp: time.Now(),
		Level:     "info",
		Message:   "Managed rollout wait handed over to its deterministic exact-target replacement.",
	})
	job.mu.Unlock()

	q.cancelResume(jobID)
	q.log.Info("job_managed_rollout_handover_claimed", "id", jobID, "reason", expectedReason)
	return WaitingHandoverClaimed, nil
}

func mergeJobResultValues(job *Job, values map[string]any) {
	if job.Result == nil {
		job.Result = make(map[string]interface{}, len(values))
	}
	for key, value := range values {
		job.Result[key] = value
	}
}

// List returns all jobs, optionally filtered by state.
func (q *Queue) List(state JobState) []*Job {
	q.jobsMu.RLock()
	defer q.jobsMu.RUnlock()

	jobs := make([]*Job, 0)
	for _, job := range q.jobs {
		// Job fields are guarded by job.mu, not jobsMu (H8) - reading State
		// without it races with processJob's field writes.
		job.mu.Lock()
		jobState := job.State
		job.mu.Unlock()
		if state == "" || jobState == state {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// UpdateProgress updates a job's progress.
func (q *Queue) UpdateProgress(jobID string, progress int, message string) {
	q.jobsMu.RLock()
	job, ok := q.jobs[jobID]
	q.jobsMu.RUnlock()

	if ok {
		job.mu.Lock()
		job.Progress = progress
		if message != "" {
			job.Message = message
		}
		job.mu.Unlock()
		if message != "" {
			q.addLog(job, "info", message)
		}
	}
}

// Cancel cancels a pending or running job.
// H5: Now supports cancellation of running jobs via context.
func (q *Queue) Cancel(jobID string) error {
	q.jobsMu.RLock()
	job, ok := q.jobs[jobID]
	q.jobsMu.RUnlock()
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}

	// The state decision and terminal transition share the same lock as the
	// worker claim. This makes cancel linearizable with a queued resume.
	job.mu.Lock()
	state := job.State
	cancelFunc := job.cancelFunc
	if state != JobStatePending && state != JobStateWaiting && state != JobStateRunning {
		job.mu.Unlock()
		return fmt.Errorf("cannot cancel job in state: %s", state)
	}
	if state != JobStateRunning {
		now := time.Now()
		job.State = JobStateCancelled
		job.CompletedAt = &now
		job.WaitReason = ""
		job.NextResumeAt = nil
		job.Error = "Job canceled by user request"
		job.cancelFunc = nil
	} else {
		job.cancellationRequested = true
		job.cancellationReason = "Job canceled by user request"
	}
	job.mu.Unlock()

	// A timer that already enqueued this pointer is harmless: processJob's
	// atomic claim rejects the canceled state. Cancel the tracked timer too so
	// no unnecessary delayed wakeup remains.
	q.cancelResume(jobID)
	if cancelFunc != nil {
		cancelFunc()
	}
	if state == JobStateRunning {
		q.addLog(job, "warn", "Job cancellation requested; waiting for the handler to stop")
		q.log.Info("job_cancellation_requested", "id", jobID, "previous_state", state)
	} else {
		q.addLog(job, "warn", "Job canceled")
		q.log.Info("job_canceled", "id", jobID, "previous_state", state)
	}

	return nil
}

// cancelJobInternal is called when a job is canceled via context.
func (q *Queue) cancelJobInternal(job *Job, reason string) {
	q.cancelResume(job.ID)

	now := time.Now()
	job.mu.Lock()
	if isTerminalJobState(job.State) {
		job.mu.Unlock()
		return
	}
	job.State = JobStateCancelled
	job.CompletedAt = &now
	job.WaitReason = ""
	job.NextResumeAt = nil
	job.Error = reason
	job.cancelFunc = nil
	job.mu.Unlock()
	q.addLog(job, "warn", reason)
	q.log.Info("job_canceled", "id", job.ID, "reason", reason)
}

// Stats returns queue statistics.
func (q *Queue) Stats() map[string]int {
	q.jobsMu.RLock()
	defer q.jobsMu.RUnlock()

	stats := map[string]int{
		"total":     len(q.jobs),
		"pending":   0,
		"waiting":   0,
		"running":   0,
		"completed": 0,
		"failed":    0,
		"canceled":  0,
	}

	for _, job := range q.jobs {
		// H8: Read job state with lock
		job.mu.RLock()
		state := job.State
		job.mu.RUnlock()

		switch state {
		case JobStatePending:
			stats["pending"]++
		case JobStateWaiting:
			stats["waiting"]++
		case JobStateRunning:
			stats["running"]++
		case JobStateCompleted:
			stats["completed"]++
		case JobStateFailed:
			stats["failed"]++
		case JobStateCancelled:
			stats["canceled"]++
		}
	}

	return stats
}
