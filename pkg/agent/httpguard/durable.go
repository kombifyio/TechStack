package httpguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/agent/procfence"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardcompat"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	journalReopenEvery       = 30 * time.Second
	exclusionTimeout         = 15 * time.Second
	redeliveryTick           = time.Second
	redeliveryCap            = time.Minute
	compactEvery             = 10 * time.Minute
	reconcileRetryEvery      = 10 * time.Minute
	ephemeralDeliveryRetries = 6
	// legacyAuthority is the execution window granted to a command from a
	// control plane that predates the binding contract.
	legacyAuthority = guardcompat.LegacyAuthorityLifetime
)

// Reconciler is the observation oracle for a command whose outcome is unknown.
// It must be read-only: it decides whether the desired state is observable, and
// it never runs the original action again.
type Reconciler interface {
	Reconcile(ctx context.Context, request ReconcileRequest) ReconcileOutcome
}

// ReconcileRequest names the command and the non-secret facts recorded in the
// journal when it was admitted.
type ReconcileRequest struct {
	CommandID string
	Operation string
	Hint      ReconcileHint
}

// ReconcileOutcome is the oracle's finding. Any resolution other than
// observed_converged or not_applied is an escalation to an operator.
type ReconcileOutcome struct {
	Resolution string
	Reason     string
}

// ReconcileHint is the non-secret subset of a command a reconciliation needs.
// Candidate specs, capabilities and credentials are never recorded. Connector
// hints retain only the closed source/node correlation metadata of the operation.
type ReconcileHint struct {
	WorkingDirectory         string                      `json:"working_directory,omitempty"`
	SpecPath                 string                      `json:"spec_path,omitempty"`
	Release                  *agentpb.StackKitReleasePin `json:"release,omitempty"`
	Offline                  bool                        `json:"offline,omitempty"`
	StackkitInstanceID       string                      `json:"stackkit_instance_id,omitempty"`
	ExpectedPlanHash         string                      `json:"expected_plan_hash,omitempty"`
	ConnectorRequest         []byte                      `json:"connector_request,omitempty"`
	ConnectorOperationID     string                      `json:"connector_operation_id,omitempty"`
	LocalSiteRef             string                      `json:"local_site_ref,omitempty"`
	LocalNodeRef             string                      `json:"local_node_ref,omitempty"`
	LocalExecutionChannelRef string                      `json:"local_execution_channel_ref,omitempty"`
}

// ProcessRecord identifies a started executor process group.
type ProcessRecord struct {
	PID        int
	PGID       int
	StartTicks uint64
}

// DurableOptions enables the durable operation path of a Guard.
type DurableOptions struct {
	// Dir is the Guard state directory that holds the journal.
	Dir string
	// CustodyRoot binds every open and retry to admitted node custody. The caller
	// keeps it open until the client stops; each journal owns a separate clone.
	CustodyRoot *os.Root
	Journal     journal.Options
	// Reconciler resolves an outcome_unknown command by observation. Nil means
	// every mutating unknown is escalated to an operator.
	Reconciler Reconciler
	// ObserveProcess wraps the execution context so the executor reports its
	// process group right after it starts.
	ObserveProcess func(ctx context.Context, record func(ProcessRecord)) context.Context
	Now            func() time.Time
	// Monotonic reads a process-monotonic clock; it bounds a command's
	// authority independently of the wall clock. Nil uses time.Since of the
	// process-local start of durable operations.
	Monotonic func() time.Duration
}

type offeredCommand struct {
	raw     []byte
	command *agentpb.StackKitCommand
	binding *guardbinding.Binding
	// resolutions are operator resolution revisions for commands this Guard
	// still holds as unsettled. They carry no command.
	resolutions []guardbinding.OperatorResolutionNotice
	// predecessor is a recovery statement for held outcomes of an earlier
	// worker binding. It carries no command.
	predecessor *guardbinding.PredecessorRecovery
}

type durableRequest struct {
	controlRequest
	CommandID     string                     `json:"command_id,omitempty"`
	KnownCommands []string                   `json:"known_commands,omitempty"`
	Report        *guardbinding.ResultReport `json:"report,omitempty"`
	// HeldPredecessorCommands asks for a recovery statement for outcomes held
	// from an earlier worker binding of the same tenant.
	HeldPredecessorCommands []guardbinding.PredecessorCommand `json:"held_predecessor_commands,omitempty"`
	// PredecessorWorkerID and PredecessorRecovery deliver one such outcome.
	PredecessorWorkerID string                            `json:"predecessor_worker_id,omitempty"`
	PredecessorRecovery *guardbinding.PredecessorRecovery `json:"predecessor_recovery,omitempty"`
}

type durableOps struct {
	c    *Client
	opts DurableOptions

	mu           sync.Mutex
	store        *journal.Store
	openErr      error
	lastOpen     time.Time
	recoveredFor *journal.Store
	sensitive    map[string][]byte
	cleared      map[string]bool
	reconciled   map[string]time.Time
	backoff      map[string]*deliveryBackoff
	wake         chan struct{}
	lastCompact  time.Time
	// predecessor is the verified recovery statement for outcomes held from an
	// earlier worker binding; it lives in memory only and expires on its own.
	predecessor      *guardbinding.PredecessorRecovery
	lastPredecessor  time.Time
	predecessorAsked map[string]time.Time
}

type deliveryBackoff struct {
	attempt int
	next    time.Time
}

// EnableDurableOperations switches the typed command path of this Guard to the
// journaled, bound and acknowledged protocol. It must be called before Run. A
// journal that cannot be opened does not stop the Guard: new mutations are
// denied with guidance and read-only diagnosis keeps working.
func (c *Client) EnableDurableOperations(opts DurableOptions) {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Monotonic == nil {
		anchor := time.Now()
		opts.Monotonic = func() time.Duration { return time.Since(anchor) }
	}
	d := &durableOps{
		c: c, opts: opts, sensitive: map[string][]byte{}, cleared: map[string]bool{},
		reconciled: map[string]time.Time{}, backoff: map[string]*deliveryBackoff{}, wake: make(chan struct{}, 1),
	}
	d.mu.Lock()
	d.openLocked()
	d.mu.Unlock()
	c.durable = d
}

func (d *durableOps) openLocked() {
	d.lastOpen = time.Now()
	if d.store != nil {
		_ = d.store.Close()
		d.store = nil
	}
	var store *journal.Store
	var err error
	if d.opts.CustodyRoot != nil {
		store, err = journal.OpenRoot(d.opts.CustodyRoot, d.opts.Journal)
	} else {
		store, err = journal.Open(d.opts.Dir, d.opts.Journal)
	}
	d.store, d.openErr = store, err
	if err != nil {
		d.c.log.Warn("guard_journal_open_failed", "error", err.Error())
		return
	}
	if denied := store.Denied(); denied != nil {
		d.c.log.Warn("guard_journal_denied", "reason", denied.Reason, "detail", denied.Detail)
	}
}

// writable returns the journal when it can record new work. Recoverable
// denials (full disk, transient write failure) are retried at a bounded rate.
func (d *durableOps) writable() (*journal.Store, *guardbinding.Denial) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.store == nil || recoverable(d.store.Denied()) {
		if time.Since(d.lastOpen) >= journalReopenEvery {
			d.openLocked()
		}
	}
	if d.store == nil {
		return nil, guardbinding.Denyf(guardbinding.CodeJournalUnavailable, "journal cannot be opened: %v", d.openErr)
	}
	if denied := d.store.Denied(); denied != nil {
		return nil, guardbinding.Denyf(guardbinding.CodeJournalUnavailable, "journal refuses writes: %s", denied.Reason)
	}
	return d.store, nil
}

// close releases the journal lock so a restarted Guard in the same process, or
// the next process, can own it.
func (d *durableOps) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.store != nil {
		_ = d.store.Close()
	}
}

func (d *durableOps) readable() *journal.Store {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.store
}

func recoverable(denied *journal.DeniedError) bool {
	if denied == nil {
		return false
	}
	switch denied.Reason {
	case journal.ReasonFull, journal.ReasonWriteFailed, journal.ReasonUnavailable, journal.ReasonPermissions:
		return true
	}
	return false
}

func (d *durableOps) kick() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// health is the heartbeat projection of the journal.
func (d *durableOps) health() *guardbinding.JournalHealth {
	d.mu.Lock()
	store, openErr := d.store, d.openErr
	d.mu.Unlock()
	if store == nil {
		reason := journal.ReasonUnavailable
		if openErr != nil && !errors.Is(openErr, journal.ErrLocked) {
			reason = openErr.Error()
		}
		return &guardbinding.JournalHealth{Contract: guardbinding.ContractVersion, State: guardbinding.JournalDenied, Reason: reason}
	}
	h := store.Health()
	out := guardbinding.JournalHealth{
		Contract: guardbinding.ContractVersion, State: guardbinding.JournalOK, Reason: h.Reason,
		UnresolvedOutcomeUnknown: h.UnresolvedUnknown, UnackedResults: h.UnackedResults, InFlight: h.InFlight,
		SchemaVersion: h.SchemaVersion, Bytes: h.Bytes, BudgetBytes: h.BudgetBytes,
	}
	if h.State != "ok" {
		out.State = guardbinding.JournalDenied
	}
	return &out
}

func (c *Client) heartbeat(snapshot Snapshot) Heartbeat {
	heartbeat := heartbeatFromSnapshot(snapshot)
	if c.durable != nil {
		heartbeat.GuardJournal = c.durable.health()
	}
	if c.cfg.Posture != nil {
		heartbeat.GuardPosture = c.cfg.Posture()
	}
	return heartbeat
}

func (c *Client) durableIdentity() controlRequest {
	identity := c.controlIdentity()
	identity.Capabilities = append(identity.Capabilities, guardbinding.Capability)
	return identity
}

func (c *Client) runDurableControlLoop(ctx context.Context) {
	d := c.durable
	redeliveryDone := make(chan struct{})
	go func() {
		defer close(redeliveryDone)
		d.runRedelivery(ctx)
	}()
	defer func() {
		<-redeliveryDone
		d.close()
	}()
	backoff := newRetryBackoff(time.Second, 30*time.Second)
	for ctx.Err() == nil {
		d.recoverInterrupted()
		offered, err := c.pollDurable(ctx, d.knownCommands(), d.heldPredecessorCommands())
		if err != nil {
			delay := backoff.Next()
			c.log.Warn("guard_control_poll_failed", "error", err.Error(), "retry_in", delay.String())
			_ = waitContext(ctx, delay)
			continue
		}
		backoff.Reset()
		if offered == nil {
			_ = waitContext(ctx, emptyCommandPollDelay)
			continue
		}
		if offered.command == nil {
			d.applyOperatorResolutions(offered.resolutions)
			d.acceptPredecessorRecovery(offered.predecessor)
			continue
		}
		d.handleOffered(ctx, offered)
	}
}

func (d *durableOps) knownCommands() []string {
	store := d.readable()
	if store == nil {
		return nil
	}
	entries := store.Entries(func(e journal.Entry) bool {
		// An intent alone started nothing: the command is offered again and
		// continues from its intent.
		return e.State != journal.StateAcked && e.State != journal.StateIntent && d.c.ownsBinding(e.Intent)
	})
	known := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0 && len(known) < guardbinding.MaxKnownCommands; i-- {
		known = append(known, entries[i].Intent.CommandID)
	}
	return known
}

func (c *Client) pollDurable(ctx context.Context, known []string, held []guardbinding.PredecessorCommand) (*offeredCommand, error) {
	request := durableRequest{controlRequest: c.durableIdentity(), KnownCommands: known, HeldPredecessorCommands: held}
	status, body, err := c.postJSONResponseLimit(ctx, c.cfg.CommandURL, request, maxCommandResponseBody)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, unexpectedStatusError(status, body)
	}
	var response struct {
		Data struct {
			Command     json.RawMessage                         `json:"command"`
			Binding     *guardbinding.Binding                   `json:"binding"`
			Resolutions []guardbinding.OperatorResolutionNotice `json:"resolutions"`
			Predecessor *guardbinding.PredecessorRecovery       `json:"predecessor_recovery"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode typed command: %w", err)
	}
	if len(response.Data.Command) == 0 && (len(response.Data.Resolutions) > 0 || response.Data.Predecessor != nil) {
		return &offeredCommand{resolutions: response.Data.Resolutions, predecessor: response.Data.Predecessor}, nil
	}
	command := &agentpb.StackKitCommand{}
	if len(response.Data.Command) == 0 || json.Unmarshal(response.Data.Command, command) != nil || strings.TrimSpace(command.GetCommandId()) == "" {
		return nil, errors.New("typed command response is empty")
	}
	return &offeredCommand{raw: response.Data.Command, command: command, binding: response.Data.Binding}, nil
}

// handleOffered admits, fences, records and runs one offered command. Nothing
// reaches the executor before its intent and its dispatched marker are durable.
func (d *durableOps) handleOffered(ctx context.Context, offered *offeredCommand) {
	c := d.c
	command := offered.command
	id := strings.TrimSpace(command.GetCommandId())
	// The authority clock starts when this process received the offer online.
	receivedAt, receivedMono := d.opts.Now(), d.opts.Monotonic()
	mutating := stackkitcommand.IsMutatingOperation(command.Operation)
	if c.revocationObserved() {
		d.rejectUnjournaled(ctx, id, guardbinding.Denyf(guardbinding.CodeAuthorityRevoked, "this Guard observed that its identity was revoked"))
		return
	}
	readable := d.readable()
	known := false
	if readable != nil {
		_, known = readable.Entry(id)
	}
	binding, denial := d.bindingFor(offered, readable, known)
	if denial != nil {
		c.log.Warn("guard_command_rejected", "command_id", id, "code", denial.Code, "reason", denial.Message)
		d.rejectUnjournaled(ctx, id, denial)
		return
	}
	if readable != nil {
		_ = readable.NoteGeneration(binding.CurrentGeneration)
	}
	// The executor re-checks this exact evidence at the side-effect boundary.
	ctx = guardbinding.WithAdmission(ctx, guardbinding.Admission{
		Binding: binding, Raw: offered.raw,
		Window: guardbinding.AuthorityWindow{ExpiresAt: binding.AuthorityExpiresAt, ReceivedAt: receivedAt, ReceivedMono: receivedMono},
	})
	store, unavailable := d.writable()
	if store == nil {
		if mutating {
			d.rejectUnjournaled(ctx, id, unavailable)
			return
		}
		// Read-only diagnosis stays available without a journal; nothing can
		// change, so nothing needs to be preserved.
		d.runUnjournaled(ctx, command, id)
		return
	}
	entry, existing, err := store.Admit(d.intentFor(command, binding, mutating))
	switch {
	case errors.Is(err, journal.ErrTombstoned):
		c.log.Info("guard_command_already_completed", "command_id", id)
		return
	case errors.Is(err, journal.ErrDigestConflict):
		d.rejectUnjournaled(ctx, id, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "command identity is known with a different payload"))
		return
	case err != nil:
		if mutating {
			d.rejectUnjournaled(ctx, id, guardbinding.Denyf(guardbinding.CodeJournalUnavailable, "cannot record intent: %v", err))
		} else {
			d.runUnjournaled(ctx, command, id)
		}
		return
	}
	if existing && entry.State != journal.StateIntent {
		// Already dispatched, uncertain or awaiting acknowledgement: continue
		// from the journal, never execute again.
		d.recoverInterrupted()
		d.kick()
		return
	}
	d.execute(ctx, store, entry, command)
}

func (d *durableOps) bindingFor(offered *offeredCommand, store *journal.Store, known bool) (guardbinding.Binding, *guardbinding.Denial) {
	c := d.c
	command := offered.command
	if offered.binding == nil && len(c.cfg.BindingSigningKeys) > 0 {
		// A Guard that pins the control plane's key takes no unsigned offer,
		// not even an observation.
		return guardbinding.Binding{}, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "an offer without a signed binding is refused once the control plane's key is pinned")
	}
	if offered.binding == nil {
		// A control plane that predates the contract: an observation is still
		// journaled, digested and fenced locally, flagged legacy. A mutation
		// without a binding is refused: nothing but the offer would vouch for it,
		// and an offer can come over the private LAN channel in clear text.
		if stackkitcommand.IsMutatingOperation(command.Operation) {
			return guardbinding.Binding{}, guardbinding.Denyf(guardbinding.CodeContractUnsupported, "a mutating command needs a control-plane binding")
		}
		digest, err := guardbinding.CanonicalDigest(offered.raw)
		if err != nil {
			return guardbinding.Binding{}, guardbinding.Denyf(guardbinding.CodeDigestMismatch, "command payload is not canonical JSON")
		}
		var generation int64
		if store != nil {
			generation = store.HighestGeneration()
		}
		return guardbinding.Binding{
			CommandID: command.GetCommandId(), TenantID: c.cfg.TenantID, WorkerID: c.cfg.RuntimeAgentID,
			Operation: command.Operation.String(), PayloadDigest: digest, WorkerGeneration: generation, CurrentGeneration: generation,
			AuthorityExpiresAt: d.opts.Now().Add(legacyAuthority), ContractVersion: guardbinding.LegacyContractVersion,
		}, nil
	}
	expect := guardbinding.Expectation{TenantID: c.cfg.TenantID, WorkerID: c.cfg.RuntimeAgentID, ServerID: c.cfg.ServerID, Known: known}
	if store != nil {
		expect.HighestGeneration = store.HighestGeneration()
	}
	binding := *offered.binding
	if strings.TrimSpace(binding.CommandID) != strings.TrimSpace(command.GetCommandId()) {
		return binding, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "binding names another command")
	}
	if denial := binding.Validate(offered.raw, command.Operation.String(), expect, d.opts.Now()); denial != nil {
		return binding, denial
	}
	if denial := binding.VerifySignature(c.cfg.BindingSigningKeys); denial != nil {
		return binding, denial
	}
	return binding, nil
}

func (d *durableOps) intentFor(command *agentpb.StackKitCommand, binding guardbinding.Binding, mutating bool) journal.Intent {
	hint, _ := json.Marshal(ReconcileHint{
		WorkingDirectory: command.WorkingDirectory, SpecPath: command.SpecPath, Release: command.Release,
		Offline: command.Offline, StackkitInstanceID: command.StackkitInstanceId, ExpectedPlanHash: command.ExpectedPlanHash,
		ConnectorRequest: command.ApplicationConnectorRequestJson, ConnectorOperationID: command.ApplicationOperationId, LocalSiteRef: command.LocalSiteRef, LocalNodeRef: command.LocalNodeRef, LocalExecutionChannelRef: command.LocalExecutionChannelRef,
	})
	return journal.Intent{
		CommandID: command.GetCommandId(), TenantID: binding.TenantID, WorkerID: binding.WorkerID,
		Operation: binding.Operation, Mutating: mutating, Generation: binding.WorkerGeneration,
		PayloadDigest: binding.PayloadDigest, AuthorityExpiresAt: binding.AuthorityExpiresAt.UTC(),
		ContractVersion: binding.ContractVersion, ResourceKey: binding.TenantID + "/" + binding.WorkerID, Hint: hint,
	}
}

// execute crosses the effect boundary: fence, process exclusion, the
// pre-effect authority check, the dispatched marker, the executor, then the
// durable result.
func (d *durableOps) execute(ctx context.Context, store *journal.Store, entry journal.Entry, command *agentpb.StackKitCommand) {
	id := entry.Intent.CommandID
	if entry.Intent.Mutating {
		if fenced := unsettledMutations(store, id); len(fenced) > 0 {
			d.finishRejected(ctx, store, id, guardbinding.Denyf(guardbinding.CodeResourceFenced, "command %s on this server has no settled outcome", fenced[0].Intent.CommandID))
			return
		}
		if err := d.clearPreviousExecutors(store); err != nil {
			d.finishRejected(ctx, store, id, guardbinding.Denyf(guardbinding.CodeResourceFenced, "%v", err))
			return
		}
	}
	// Everything admitted at dispatch is checked again here, as close to the
	// effect as the journal allows: a revocation observed or authority spent
	// since admission denies this effect.
	if denial := d.checkBeforeEffect(ctx, store, entry); denial != nil {
		d.finishRejected(ctx, store, id, denial)
		return
	}
	if err := store.MarkDispatched(id); err != nil {
		d.finishRejected(ctx, store, id, guardbinding.Denyf(guardbinding.CodeJournalUnavailable, "cannot record the dispatched marker: %v", err))
		return
	}
	// From here until a result is durable the outcome is uncertain.
	execCtx := ctx
	if d.opts.ObserveProcess != nil {
		execCtx = d.opts.ObserveProcess(ctx, func(p ProcessRecord) {
			_ = store.RecordExecutor(id, journal.ExecutorRef{PID: p.PID, PGID: p.PGID, StartTicks: p.StartTicks})
		})
	}
	result := d.c.executeTypedCommand(execCtx, command)
	if ctx.Err() != nil {
		// Shutdown interrupted the executor. The dispatched marker stays: the
		// next start reports outcome_unknown instead of a guessed failure.
		return
	}
	if reason, unknown := guardbinding.IsUnknownOutcome(result); unknown {
		// The privileged side was lost after the effect may have started: that is
		// an unknown outcome to reconcile, not a failure to report.
		d.recordUnknownOutcome(store, id, reason)
		return
	}
	d.recordOutcome(ctx, store, entry, result)
}

func (d *durableOps) checkBeforeEffect(ctx context.Context, store *journal.Store, entry journal.Entry) *guardbinding.Denial {
	admission, ok := guardbinding.AdmissionFrom(ctx)
	if !ok {
		return guardbinding.Denyf(guardbinding.CodeRevalidationRequired, "the command has no admission received by this process")
	}
	c := d.c
	return admission.CheckBeforeEffect(guardbinding.JournaledIntent{
		CommandID: entry.Intent.CommandID, TenantID: entry.Intent.TenantID,
		WorkerID: entry.Intent.WorkerID, PayloadDigest: entry.Intent.PayloadDigest,
	}, guardbinding.EffectState{
		Expect: guardbinding.Expectation{
			TenantID: c.cfg.TenantID, WorkerID: c.cfg.RuntimeAgentID, ServerID: c.cfg.ServerID,
			HighestGeneration: store.HighestGeneration(),
		},
		Revoked: c.revocationObserved(), Now: d.opts.Now(), Mono: d.opts.Monotonic(),
	})
}

// ownsBinding reports whether a journaled command was admitted under this
// Guard's current tenant and worker. Only such a command is listed to, or
// reported to, the control plane under the current credential. A command of a
// predecessor binding (the server was rebound to another tenant or worker)
// keeps its unacknowledged evidence in the journal: posting it under the new
// identity would hand a prior tenant's payload to the new binding, and the
// refusal that follows would be recorded as permanent and let the evidence be
// pruned. It stays held, and still fences conflicting work, until a governed
// predecessor recovery or handoff takes it. An older credential generation of
// the same tenant and worker is not a predecessor: its reports converge under
// the current credential.
func (c *Client) ownsBinding(intent journal.Intent) bool {
	return intent.TenantID == c.cfg.TenantID && intent.WorkerID == c.cfg.RuntimeAgentID
}

// unsettledMutations is the execution fence of this server: every mutation
// whose outcome is not settled, including one admitted under a predecessor
// binding (another tenant, worker or generation). A rebind or a newer
// generation cannot make an in-flight effect disappear; it blocks conflicting
// work until reconciliation or a governed handoff settles it.
func unsettledMutations(store *journal.Store, exceptID string) []journal.Entry {
	return store.Entries(func(e journal.Entry) bool {
		return e.Intent.Mutating && e.Intent.CommandID != exceptID &&
			(e.State == journal.StateDispatched || e.State == journal.StateOutcomeUnknown)
	})
}

func (d *durableOps) recordUnknownOutcome(store *journal.Store, id, reason string) {
	if reason == "" {
		reason = "executor_lost_after_start"
	}
	report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: reason}
	digest, _ := report.ComputeDigest(id, nil)
	if err := store.RecordResult(id, journal.ResultRecord{Revision: 1, State: journal.ReportOutcomeUnknown, Digest: digest, Reason: reason}); err != nil {
		d.c.log.Error("guard_outcome_not_durable", "command_id", id, "error", err.Error())
		return
	}
	d.c.log.Warn("guard_outcome_unknown", "command_id", id, "reason", reason)
	d.kick()
}

func (d *durableOps) recordOutcome(ctx context.Context, store *journal.Store, entry journal.Entry, result *agentpb.StackKitResult) {
	id := entry.Intent.CommandID
	payload, truncated, sensitiveOmitted, err := journal.BoundResult(result, store.MaxResultBytes())
	if err != nil {
		reason := "result_encoding_failed"
		if errors.Is(err, journal.ErrResultTooLarge) {
			reason = "result_exceeds_journal_bound"
		}
		report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: reason, Truncated: truncated}
		digest, _ := report.ComputeDigest(id, nil)
		if recordErr := store.RecordResult(id, journal.ResultRecord{Revision: 1, State: journal.ReportOutcomeUnknown, Digest: digest, Reason: reason, Truncated: truncated}); recordErr != nil {
			d.c.log.Error("guard_outcome_not_durable", "command_id", id, "error", recordErr.Error())
		}
		d.kick()
		return
	}
	digest, _ := guardbinding.CanonicalDigest(payload)
	if sensitiveOmitted && len(result.SensitiveResultJson) != 0 {
		d.mu.Lock()
		d.sensitive[id] = append([]byte(nil), result.SensitiveResultJson...)
		d.mu.Unlock()
	}
	record := journal.ResultRecord{Revision: 1, State: journal.ReportTerminal, Digest: digest, Truncated: truncated, SensitiveOmitted: sensitiveOmitted, Payload: payload}
	if err := store.RecordResult(id, record); err != nil {
		// The journal failed after the effect. The dispatched marker already
		// makes the next start report outcome_unknown; deliver this result now
		// with bounded retries so the control plane usually learns the truth.
		d.c.log.Error("guard_result_not_durable", "command_id", id, "error", err.Error())
		bounded := &agentpb.StackKitResult{}
		if json.Unmarshal(payload, bounded) == nil {
			bounded.SensitiveResultJson = result.SensitiveResultJson
			d.deliverEphemeral(ctx, id, guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportTerminal, Digest: digest, Truncated: truncated}, bounded)
		}
		return
	}
	d.kick()
}

// finishRejected records a no-effect refusal durably so it is delivered until
// acknowledged, falling back to a direct attempt when the journal is failing.
func (d *durableOps) finishRejected(ctx context.Context, store *journal.Store, id string, denial *guardbinding.Denial) {
	d.c.log.Warn("guard_command_rejected", "command_id", id, "code", denial.Code, "reason", denial.Message)
	report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportRejected, Reason: denial.Code + ": " + denial.Message}
	digest, _ := report.ComputeDigest(id, nil)
	if err := store.RecordResult(id, journal.ResultRecord{Revision: 1, State: journal.ReportRejected, Digest: digest, Reason: report.Reason}); err != nil {
		report.Digest = digest
		d.deliverEphemeral(ctx, id, report, nil)
		return
	}
	d.kick()
}

// rejectUnjournaled refuses a command that was never admitted. Nothing is
// durable for it: if delivery fails, the control plane offers it again.
func (d *durableOps) rejectUnjournaled(ctx context.Context, id string, denial *guardbinding.Denial) {
	report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportRejected, Reason: denial.Code + ": " + denial.Message}
	report.Digest, _ = report.ComputeDigest(id, nil)
	d.deliverEphemeral(ctx, id, report, nil)
}

// runUnjournaled runs an observation-only command while the journal is
// unavailable and delivers its result directly.
func (d *durableOps) runUnjournaled(ctx context.Context, command *agentpb.StackKitCommand, id string) {
	result := d.c.executeTypedCommand(ctx, command)
	if ctx.Err() != nil {
		return
	}
	payload, truncated, _, err := journal.BoundResult(result, 24<<20)
	if err != nil {
		return
	}
	digest, _ := guardbinding.CanonicalDigest(payload)
	bounded := &agentpb.StackKitResult{}
	if json.Unmarshal(payload, bounded) != nil {
		return
	}
	d.deliverEphemeral(ctx, id, guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportTerminal, Digest: digest, Truncated: truncated}, bounded)
}

type deliveryOutcome int

const (
	deliveryRetry deliveryOutcome = iota
	deliveryAcked
	deliveryPermanent
)

func (d *durableOps) deliverEphemeral(ctx context.Context, id string, report guardbinding.ResultReport, result *agentpb.StackKitResult) {
	for attempt := 0; attempt < ephemeralDeliveryRetries && ctx.Err() == nil; attempt++ {
		if attempt > 0 {
			if waitContext(ctx, time.Duration(1<<uint(attempt))*500*time.Millisecond) != nil {
				return
			}
		}
		switch d.c.postReport(ctx, id, report, result, false) {
		case deliveryAcked, deliveryPermanent:
			return
		}
	}
}

// postReport delivers one revision and classifies the answer. Only a 2xx that
// acknowledges exactly this revision and digest is an acknowledgement; client
// errors that cannot change are permanent; everything else is retried.
func (c *Client) postReport(ctx context.Context, id string, report guardbinding.ResultReport, result *agentpb.StackKitResult, legacy bool) deliveryOutcome {
	return c.postReportRequest(ctx, durableRequest{controlRequest: c.durableIdentity(), CommandID: id}, report, result, legacy)
}

func (c *Client) postReportRequest(ctx context.Context, request durableRequest, report guardbinding.ResultReport, result *agentpb.StackKitResult, legacy bool) deliveryOutcome {
	id := request.CommandID
	if legacy {
		request.controlRequest = c.controlIdentity()
		if result == nil {
			result = &agentpb.StackKitResult{CommandId: id, Stderr: report.Reason}
		}
		request.Result = result
	} else {
		request.Report = &report
		if report.State == guardbinding.ReportTerminal {
			request.Result = result
		}
	}
	status, body, err := c.postJSONResponse(ctx, c.cfg.CommandResultURL, request)
	if err != nil {
		c.log.Warn("guard_control_result_failed", "command_id", id, "revision", report.Revision, "error", err.Error())
		return deliveryRetry
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		if legacy {
			return deliveryAcked
		}
		var ack struct {
			Data guardbinding.Acknowledgement `json:"data"`
		}
		if json.Unmarshal(body, &ack) == nil && ack.Data.Acknowledged && ack.Data.Revision == report.Revision && ack.Data.Digest == report.Digest {
			return deliveryAcked
		}
		c.log.Warn("guard_control_result_unacknowledged", "command_id", id, "revision", report.Revision)
		return deliveryRetry
	}
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity:
		c.log.Error("guard_control_result_refused", "command_id", id, "revision", report.Revision, "error", unexpectedStatusError(status, body).Error())
		return deliveryPermanent
	}
	c.log.Warn("guard_control_result_failed", "command_id", id, "revision", report.Revision, "error", unexpectedStatusError(status, body).Error())
	return deliveryRetry
}

// executeTypedCommand runs the executor, streaming runtime logs when it can.
func (c *Client) executeTypedCommand(ctx context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	var result *agentpb.StackKitResult
	if streaming, ok := c.cfg.CommandExecutor.(streamingCommandExecutor); ok {
		logQueue := make(chan *agentpb.LogEntry, 512)
		logsDone := make(chan struct{})
		go func() {
			defer close(logsDone)
			for entry := range logQueue {
				if err := c.submitRuntimeLog(ctx, entry); err != nil {
					c.log.Warn("guard_runtime_log_failed", "command_id", command.GetCommandId(), "error", err.Error())
				}
			}
		}()
		result = streaming.ExecuteStreaming(ctx, command, func(entry *agentpb.LogEntry) {
			select {
			case logQueue <- entry:
			case <-ctx.Done():
			}
		})
		close(logQueue)
		<-logsDone
	} else {
		result = c.cfg.CommandExecutor.Execute(ctx, command)
	}
	if result == nil {
		result = &agentpb.StackKitResult{CommandId: command.GetCommandId(), Success: false, Stderr: "typed command executor returned no result"}
	}
	if result.CommandId == "" {
		result.CommandId = command.GetCommandId()
	}
	return result
}

// clearPreviousExecutors proves that no executor from an earlier Guard process
// is still running before conflicting work is admitted. Service control-group
// kill semantics normally make this a no-op; this is the defense for hosts where
// the Guard is not under that unit.
func (d *durableOps) clearPreviousExecutors(store *journal.Store) error {
	entries := store.Entries(func(e journal.Entry) bool {
		return e.Executor != nil && (e.State == journal.StateDispatched || e.State == journal.StateOutcomeUnknown)
	})
	for _, e := range entries {
		d.mu.Lock()
		done := d.cleared[e.Intent.CommandID]
		d.mu.Unlock()
		if done {
			continue
		}
		if err := procfence.EnsureGone(e.Executor.PID, e.Executor.PGID, e.Executor.StartTicks, exclusionTimeout); err != nil {
			return fmt.Errorf("previous executor of %s is not proven gone: %w", e.Intent.CommandID, err)
		}
		d.mu.Lock()
		d.cleared[e.Intent.CommandID] = true
		d.mu.Unlock()
	}
	return nil
}

// recoverInterrupted runs once per opened journal. A command with a dispatched
// marker and no result is an outcome_unknown: it is reported as such and
// reconciled, never executed again. An intent whose authority lapsed is refused.
func (d *durableOps) recoverInterrupted() {
	store, _ := d.writable()
	d.mu.Lock()
	already := store == nil || d.recoveredFor == store
	d.mu.Unlock()
	if already {
		d.reconcileUnknown(context.Background())
		return
	}
	if err := d.clearPreviousExecutors(store); err != nil {
		d.c.log.Warn("guard_executor_exclusion_pending", "error", err.Error())
	}
	for _, e := range store.Entries(func(e journal.Entry) bool { return e.State == journal.StateDispatched }) {
		report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: "interrupted_before_result"}
		digest, _ := report.ComputeDigest(e.Intent.CommandID, nil)
		if err := store.RecordResult(e.Intent.CommandID, journal.ResultRecord{Revision: 1, State: journal.ReportOutcomeUnknown, Digest: digest, Reason: report.Reason}); err != nil {
			d.c.log.Error("guard_unknown_outcome_not_recorded", "command_id", e.Intent.CommandID, "error", err.Error())
			return
		}
		d.c.log.Warn("guard_outcome_unknown", "command_id", e.Intent.CommandID, "operation", e.Intent.Operation)
	}
	for _, e := range store.Entries(func(e journal.Entry) bool { return e.State == journal.StateIntent }) {
		if !d.opts.Now().Before(e.Intent.AuthorityExpiresAt) {
			d.finishRejected(context.Background(), store, e.Intent.CommandID, guardbinding.Denyf(guardbinding.CodeAuthorityExpired, "command authority expired before it started"))
		}
	}
	d.mu.Lock()
	d.recoveredFor = store
	d.mu.Unlock()
	d.kick()
	d.reconcileUnknown(context.Background())
}

// reconcileUnknown settles outcome_unknown commands by observation. Commands
// that only observe are resolved as not applied; a mutating command is
// resolved only when the oracle observes the desired state, and is otherwise
// escalated to an operator while it keeps fencing conflicting work.
func (d *durableOps) reconcileUnknown(ctx context.Context) {
	store, _ := d.writable()
	if store == nil {
		return
	}
	for _, e := range store.Entries(func(e journal.Entry) bool { return e.State == journal.StateOutcomeUnknown }) {
		latest, _ := e.Latest()
		id := e.Intent.CommandID
		d.mu.Lock()
		last, tried := d.reconciled[id]
		d.mu.Unlock()
		if tried && time.Since(last) < reconcileRetryEvery {
			continue
		}
		outcome := d.reconcile(ctx, e)
		d.mu.Lock()
		d.reconciled[id] = time.Now()
		d.mu.Unlock()
		if outcome.Resolution == guardbinding.ResolutionEscalated && latest.State == journal.ReportResolved && latest.Resolution == guardbinding.ResolutionEscalated {
			continue
		}
		report := guardbinding.ResultReport{
			Revision: latest.Revision + 1, State: guardbinding.ReportResolved, PriorDigest: latest.Digest,
			Reason: outcome.Reason, Resolution: outcome.Resolution,
		}
		report.Digest, _ = report.ComputeDigest(id, nil)
		if err := store.RecordResult(id, journal.ResultRecord{
			Revision: report.Revision, State: journal.ReportResolved, Digest: report.Digest,
			PriorDigest: report.PriorDigest, Reason: report.Reason, Resolution: report.Resolution,
		}); err != nil {
			d.c.log.Error("guard_resolution_not_recorded", "command_id", id, "error", err.Error())
			return
		}
		d.c.log.Warn("guard_outcome_reconciled", "command_id", id, "resolution", outcome.Resolution)
		d.kick()
	}
}

// applyOperatorResolutions records operator resolution revisions the control
// plane committed, releasing the local fence of the commands they settle. A
// notice is accepted only for a command this journal holds as unsettled, as the
// next revision, superseding exactly the evidence the journal holds, with a
// digest the Guard recomputes itself and a disposition only an operator can
// commit. Anything else is ignored and logged; it never changes the journal.
func (d *durableOps) applyOperatorResolutions(notices []guardbinding.OperatorResolutionNotice) {
	store, _ := d.writable()
	if store == nil {
		return
	}
	for _, notice := range notices {
		entry, ok := store.Entry(notice.CommandID)
		if !ok {
			continue
		}
		latest, has := entry.Latest()
		report := guardbinding.ResultReport{
			Revision: notice.Revision, State: guardbinding.ReportResolved, PriorDigest: notice.PriorDigest,
			Reason: notice.Reason, Resolution: notice.Resolution,
		}
		digest, _ := report.ComputeDigest(notice.CommandID, nil)
		if !has || !latest.Open() || notice.Revision != latest.Revision+1 || notice.PriorDigest != latest.Digest ||
			!guardbinding.OperatorDisposition(notice.Resolution) || digest != notice.Digest {
			d.c.log.Warn("guard_operator_resolution_ignored", "command_id", notice.CommandID, "revision", notice.Revision)
			continue
		}
		if err := store.RecordResult(notice.CommandID, journal.ResultRecord{
			Revision: notice.Revision, State: journal.ReportResolved, Digest: digest,
			PriorDigest: notice.PriorDigest, Reason: notice.Reason, Resolution: notice.Resolution,
		}); err != nil {
			d.c.log.Error("guard_operator_resolution_not_recorded", "command_id", notice.CommandID, "error", err.Error())
			return
		}
		// The control plane committed this revision itself; there is nothing to
		// deliver back.
		if err := store.Ack(notice.CommandID, notice.Revision, false); err != nil {
			d.c.log.Error("guard_operator_resolution_not_acknowledged", "command_id", notice.CommandID, "error", err.Error())
			return
		}
		d.c.log.Warn("guard_outcome_operator_resolved", "command_id", notice.CommandID, "resolution", notice.Resolution)
	}
}

func (d *durableOps) reconcile(ctx context.Context, e journal.Entry) ReconcileOutcome {
	if !e.Intent.Mutating {
		return ReconcileOutcome{Resolution: guardbinding.ResolutionNotApplied, Reason: "read_only_command_interrupted_safe_to_reissue"}
	}
	if d.opts.Reconciler == nil {
		return ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: "no_reconciliation_oracle_for_action"}
	}
	var hint ReconcileHint
	_ = json.Unmarshal(e.Intent.Hint, &hint)
	outcome := d.opts.Reconciler.Reconcile(ctx, ReconcileRequest{CommandID: e.Intent.CommandID, Operation: e.Intent.Operation, Hint: hint})
	switch outcome.Resolution {
	case guardbinding.ResolutionObservedConverged, guardbinding.ResolutionNotApplied:
		return outcome
	}
	if outcome.Reason == "" {
		outcome.Reason = "outcome_not_observable"
	}
	return ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: outcome.Reason}
}

// runRedelivery delivers every unacknowledged revision, lowest first, with
// bounded jittered backoff per command until the control plane acknowledges it.
func (d *durableOps) runRedelivery(ctx context.Context) {
	ticker := time.NewTicker(redeliveryTick)
	defer ticker.Stop()
	for {
		d.redeliverDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

func (d *durableOps) redeliverDue(ctx context.Context) {
	store := d.readable()
	if store == nil {
		return
	}
	if time.Since(d.lastCompactAt()) > compactEvery {
		_ = store.Compact()
		d.mu.Lock()
		d.lastCompact = time.Now()
		d.mu.Unlock()
	}
	for _, e := range store.Entries(func(e journal.Entry) bool {
		_, ok := e.NextUnacked()
		return ok && (d.c.ownsBinding(e.Intent) || d.predecessorCovers(e.Intent))
	}) {
		if ctx.Err() != nil {
			return
		}
		record, _ := e.NextUnacked()
		id := e.Intent.CommandID
		d.mu.Lock()
		state := d.backoff[id]
		if state == nil {
			state = &deliveryBackoff{}
			d.backoff[id] = state
		}
		due := !time.Now().Before(state.next)
		d.mu.Unlock()
		if !due {
			continue
		}
		outcome := d.deliver(ctx, e, record)
		d.mu.Lock()
		switch outcome {
		case deliveryRetry:
			state.attempt++
			state.next = time.Now().Add(jittered(min(time.Second<<min(state.attempt, 8), redeliveryCap)))
		default:
			delete(d.backoff, id)
			if record.Revision >= int64(len(e.Results)) {
				delete(d.sensitive, id)
			}
		}
		d.mu.Unlock()
	}
}

func (d *durableOps) lastCompactAt() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastCompact.IsZero() {
		d.lastCompact = time.Now()
	}
	return d.lastCompact
}

func jittered(delay time.Duration) time.Duration {
	// #nosec G404 -- jitter spreads retries; it is not used for credentials.
	return delay/2 + time.Duration(rand.Int63n(int64(delay/2)+1))
}

func (d *durableOps) deliver(ctx context.Context, e journal.Entry, record journal.ResultRecord) deliveryOutcome {
	store := d.readable()
	id := e.Intent.CommandID
	legacy := e.Intent.ContractVersion == guardbinding.LegacyContractVersion
	report := guardbinding.ResultReport{
		Revision: record.Revision, State: record.State, Digest: record.Digest, PriorDigest: record.PriorDigest,
		Reason: record.Reason, Resolution: record.Resolution, Truncated: record.Truncated,
	}
	var result *agentpb.StackKitResult
	if record.State == journal.ReportTerminal {
		result = &agentpb.StackKitResult{}
		if err := json.Unmarshal(record.Payload, result); err != nil {
			d.c.log.Error("guard_journal_payload_unreadable", "command_id", id, "error", err.Error())
			return deliveryRetry
		}
		d.mu.Lock()
		sensitive := d.sensitive[id]
		d.mu.Unlock()
		if record.Revision == 1 && len(sensitive) != 0 {
			result.SensitiveResultJson = sensitive
		}
	}
	if legacy && record.State != journal.ReportTerminal && record.State != journal.ReportRejected {
		// A control plane without the contract cannot take an uncertainty; it
		// stays in the local journal and in the heartbeat.
		_ = store.Ack(id, record.Revision, false)
		return deliveryAcked
	}
	if !d.c.ownsBinding(e.Intent) {
		return d.deliverPredecessor(ctx, store, e, report, result)
	}
	outcome := d.c.postReport(ctx, id, report, result, legacy)
	switch outcome {
	case deliveryAcked:
		if err := store.Ack(id, record.Revision, false); err != nil {
			d.c.log.Warn("guard_ack_not_recorded", "command_id", id, "error", err.Error())
		}
	case deliveryPermanent:
		if err := store.Ack(id, record.Revision, true); err != nil {
			d.c.log.Warn("guard_ack_not_recorded", "command_id", id, "error", err.Error())
		}
	}
	return outcome
}
