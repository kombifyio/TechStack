package agentcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardcompat"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	// reofferAfter throttles re-offering a dispatched command that the Guard did
	// not list as known.
	reofferAfter = guardcompat.ReofferAfter
	// maxPollCandidates bounds the queued commands examined in one poll.
	maxPollCandidates = 16
)

// PollOptions describes one Guard poll.
type PollOptions struct {
	TenantID     string
	AgentID      string
	Capabilities []string
	// CurrentGeneration is the credential generation the Guard authenticated
	// with; dispatch binds the command to it.
	CurrentGeneration int64
	// KnownCommands are the command IDs the Guard holds in its durable journal
	// without an acknowledged terminal outcome. They are never re-offered.
	KnownCommands []string
	// ObserveOnly withholds mutating commands: the Guard authenticated with a
	// bearer credential alone (no key-bound identity) and may only observe.
	ObserveOnly bool
}

// Offer is one command handed to a Guard together with its admission binding.
type Offer struct {
	Command *agentpb.StackKitCommand
	// Binding is nil for a legacy dispatch to a Guard that does not advertise
	// guardbinding.Capability.
	Binding *guardbinding.Binding
	Legacy  bool
}

// ReportRequest is one outcome revision delivered by a Guard.
type ReportRequest struct {
	TenantID  string
	AgentID   string
	CommandID string
	// Generation is the credential generation that authenticated the report.
	Generation int64
	Report     guardbinding.ResultReport
	// Result is required for a terminal report and ignored otherwise.
	Result *agentpb.StackKitResult
}

// inMemoryAuthority is how long a command offered by the in-memory hub may
// start; the database hub uses the command's own expiry.
const inMemoryAuthority = 10 * time.Minute

// PollBoundForTenant is the binding-aware poll. A Guard that advertises
// guardbinding.Capability receives the command with its binding, and a command
// already dispatched to it that it does not know is offered again under the same
// identity so a restarted Guard can reconcile from its journal. Conflicting
// mutating work is withheld while an earlier mutating command on the same
// worker has an unsettled outcome.
func (h *Hub) PollBoundForTenant(ctx context.Context, opts PollOptions) (*Offer, bool, error) {
	if h == nil {
		return nil, false, nil
	}
	if h.db == nil {
		command, found, err := h.pollInMemory(ctx, opts.AgentID, opts.Capabilities, opts.ObserveOnly)
		if err != nil || !found {
			return nil, false, err
		}
		if !containsCapability(opts.Capabilities, guardbinding.Capability) {
			return &Offer{Command: command, Legacy: true}, true, nil
		}
		// A bound Guard refuses a mutation without a binding, so the in-memory
		// hub binds what it offers too.
		digest, err := guardbinding.DigestOf(command)
		if err != nil {
			return nil, false, err
		}
		return &Offer{Command: command, Binding: &guardbinding.Binding{
			CommandID: command.GetCommandId(), TenantID: strings.TrimSpace(opts.TenantID), WorkerID: strings.TrimSpace(opts.AgentID),
			Operation: command.Operation.String(), PayloadDigest: digest,
			WorkerGeneration: opts.CurrentGeneration, CurrentGeneration: opts.CurrentGeneration,
			AuthorityExpiresAt: time.Now().UTC().Add(inMemoryAuthority), ContractVersion: guardbinding.ContractVersion,
		}}, true, nil
	}
	opts.TenantID, opts.AgentID = strings.TrimSpace(opts.TenantID), strings.TrimSpace(opts.AgentID)
	if opts.TenantID == "" || opts.AgentID == "" {
		return nil, false, ErrCommandNotPending
	}
	if len(opts.KnownCommands) > guardbinding.MaxKnownCommands {
		opts.KnownCommands = opts.KnownCommands[:guardbinding.MaxKnownCommands]
	}
	bound := containsCapability(opts.Capabilities, guardbinding.Capability)
	for attempt := 0; attempt < 8; attempt++ {
		offer, retry, err := h.pollBoundOnce(ctx, opts, bound)
		if err != nil || offer != nil || !retry {
			return offer, offer != nil, err
		}
	}
	return nil, false, nil
}

func (h *Hub) pollBoundOnce(ctx context.Context, opts PollOptions, bound bool) (*Offer, bool, error) {
	tx, err := h.tenantTx(ctx, opts.TenantID)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// One dispatcher per worker at a time: the conflict fence below reads
	// committed state, so two overlapping polls (an old and a restarted Guard)
	// must not both dispatch mutating work.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "typed-agent-dispatch/"+opts.TenantID+"/"+opts.AgentID); err != nil {
		return nil, false, err
	}

	if bound && !opts.ObserveOnly {
		offer, retry, reofferErr := h.reofferUnknownDispatched(ctx, tx, opts)
		if reofferErr != nil {
			return nil, false, reofferErr
		}
		if offer != nil || retry {
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			return offer, retry, nil
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT command_id, command_json, expires_at
		FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id = $2 AND command_family = 'stackkit' AND state = 'queued' AND expires_at > now()
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT $3
	`, opts.TenantID, opts.AgentID, maxPollCandidates)
	if err != nil {
		return nil, false, err
	}
	type candidate struct {
		id      string
		payload []byte
		expires time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.payload, &c.expires); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, false, err
	}
	_ = rows.Close()

	for _, c := range candidates {
		command := &agentpb.StackKitCommand{}
		if err := protojson.Unmarshal(c.payload, command); err != nil {
			return nil, false, err
		}
		for _, capability := range stackkitcommand.RequiredAgentCapabilities(command) {
			if !containsCapability(opts.Capabilities, capability) {
				message := fmt.Sprintf("agent %q does not advertise %s", opts.AgentID, capability)
				_, _ = tx.ExecContext(ctx, `UPDATE typed_agent_commands SET state = 'failed', error = $2, completed_at = now() WHERE command_id = $1`, c.id, message)
				_ = tx.Commit()
				return nil, false, errors.New(message)
			}
		}
		mutating := stackkitcommand.IsMutatingOperation(command.Operation)
		if mutating && opts.ObserveOnly {
			// Left queued: it is offered once the Guard holds a key-bound identity,
			// or expires.
			continue
		}
		if mutating {
			if denial := guardbinding.AdmitMutatingDispatch(opts.Capabilities); denial != nil {
				// Mixed versions fail closed: a Guard that does not speak the binding
				// contract stays observe-only, and the sender learns why at once.
				if _, err := tx.ExecContext(ctx, `
					UPDATE typed_agent_commands SET state = 'failed', error = $2, completed_at = now()
					WHERE command_id = $1 AND state = 'queued'
				`, c.id, denial.Error()+". "+denial.Guidance); err != nil {
					return nil, false, err
				}
				if err := tx.Commit(); err != nil {
					return nil, false, err
				}
				return nil, true, nil
			}
		}
		if mutating {
			var fenced bool
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM typed_agent_commands
					WHERE tenant_id = $1 AND agent_id = $2 AND command_family = 'stackkit' AND mutating
					  AND command_id <> $3 AND state IN ('dispatched', 'outcome_unknown'))
			`, opts.TenantID, opts.AgentID, c.id).Scan(&fenced); err != nil {
				return nil, false, err
			}
			if fenced {
				// An earlier mutating command on this worker has no settled
				// outcome; this one waits, observation-only commands do not.
				continue
			}
		}
		digest, err := guardbinding.DigestOf(command)
		if err != nil {
			return nil, false, err
		}
		contract := guardbinding.LegacyContractVersion
		if bound {
			contract = guardbinding.ContractVersion
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE typed_agent_commands
			SET state = 'dispatched', dispatched_at = now(), last_offered_at = now(),
			    payload_digest = $2, worker_generation = $3, authority_expires_at = expires_at,
			    contract_version = $4, mutating = $5
			WHERE command_id = $1
		`, c.id, digest, opts.CurrentGeneration, contract, bound && mutating); err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		offer := &Offer{Command: command, Legacy: !bound}
		if bound {
			offer.Binding = &guardbinding.Binding{
				CommandID: c.id, TenantID: opts.TenantID, WorkerID: opts.AgentID,
				Operation: command.Operation.String(), PayloadDigest: digest,
				WorkerGeneration: opts.CurrentGeneration, CurrentGeneration: opts.CurrentGeneration,
				AuthorityExpiresAt: c.expires.UTC(), ContractVersion: guardbinding.ContractVersion,
			}
		}
		return offer, false, nil
	}
	return nil, false, nil
}

// reofferUnknownDispatched handles a command dispatched to this worker that the
// Guard does not hold in its journal. The Guard persists intent before any
// effect, so nothing can have started: within its authority it is offered again
// under the same identity, after it the command fails without an effect. The
// retry result tells the caller to look again after a state change.
func (h *Hub) reofferUnknownDispatched(ctx context.Context, tx *sql.Tx, opts PollOptions) (*Offer, bool, error) {
	known, err := json.Marshal(append([]string{}, opts.KnownCommands...))
	if err != nil {
		return nil, false, err
	}
	var id, digest string
	var payload []byte
	var expires time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT command_id, command_json, COALESCE(payload_digest, ''), authority_expires_at
		FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id = $2 AND command_family = 'stackkit' AND state = 'dispatched'
		  AND contract_version = $3 AND authority_expires_at IS NOT NULL
		  AND NOT (command_id = ANY(SELECT jsonb_array_elements_text($4::jsonb)))
		  AND COALESCE(last_offered_at, dispatched_at) < now() - make_interval(secs => $5)
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, opts.TenantID, opts.AgentID, guardbinding.ContractVersion, string(known), reofferAfter.Seconds()).Scan(&id, &payload, &digest, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !expires.After(time.Now()) {
		if _, err := tx.ExecContext(ctx, `
			UPDATE typed_agent_commands
			SET state = 'failed', completed_at = now(), mutating = false,
			    error = 'authority_expired_before_start: the Guard never journaled this command and its authority expired; nothing ran'
			WHERE command_id = $1 AND state = 'dispatched'
		`, id); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	command := &agentpb.StackKitCommand{}
	if err := protojson.Unmarshal(payload, command); err != nil {
		return nil, false, err
	}
	if digest == "" {
		if digest, err = guardbinding.DigestOf(command); err != nil {
			return nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE typed_agent_commands SET worker_generation = $2, last_offered_at = now(), payload_digest = $3
		WHERE command_id = $1
	`, id, opts.CurrentGeneration, digest); err != nil {
		return nil, false, err
	}
	return &Offer{Command: command, Binding: &guardbinding.Binding{
		CommandID: id, TenantID: opts.TenantID, WorkerID: opts.AgentID,
		Operation: command.Operation.String(), PayloadDigest: digest,
		WorkerGeneration: opts.CurrentGeneration, CurrentGeneration: opts.CurrentGeneration,
		AuthorityExpiresAt: expires.UTC(), ContractVersion: guardbinding.ContractVersion, Reoffer: true,
	}}, false, nil
}

// SubmitReportForTenant commits one immutable outcome revision. The
// acknowledgement is returned only after the transaction that stores it has
// committed. An identical repeat of a committed revision is acknowledged again;
// a different record for the same revision is refused.
func (h *Hub) SubmitReportForTenant(ctx context.Context, req ReportRequest) (guardbinding.Acknowledgement, error) {
	req.TenantID, req.AgentID, req.CommandID = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.AgentID), strings.TrimSpace(req.CommandID)
	if h == nil || req.TenantID == "" || req.AgentID == "" || req.CommandID == "" {
		return guardbinding.Acknowledgement{}, ErrResultRejected
	}
	req.Report.Reason = truncateText(req.Report.Reason, guardbinding.MaxReasonBytes)
	req.Report.Resolution = truncateText(req.Report.Resolution, guardbinding.MaxReasonBytes)
	if h.db == nil {
		if req.Report.State == guardbinding.ReportRejected {
			if err := h.rejectInMemory(req.AgentID, req.CommandID, req.Report.Reason); err != nil {
				return guardbinding.Acknowledgement{}, err
			}
			return guardbinding.Acknowledgement{CommandID: req.CommandID, Revision: req.Report.Revision, Digest: req.Report.Digest, Accepted: true, Acknowledged: true}, nil
		}
		if req.Result == nil || req.Report.State != guardbinding.ReportTerminal {
			return guardbinding.Acknowledgement{}, ErrResultRejected
		}
		if err := h.submitResultInMemory(req.AgentID, req.Result); err != nil {
			return guardbinding.Acknowledgement{}, err
		}
		return guardbinding.Acknowledgement{CommandID: req.CommandID, Revision: req.Report.Revision, Digest: req.Report.Digest, Accepted: true, Acknowledged: true}, nil
	}
	return h.submitReportDurable(ctx, req)
}

func (h *Hub) submitReportDurable(ctx context.Context, req ReportRequest) (guardbinding.Acknowledgement, error) {
	commandID, report := req.CommandID, req.Report
	if req.Result != nil && strings.TrimSpace(req.Result.GetCommandId()) != commandID {
		return guardbinding.Acknowledgement{}, guardbinding.Denyf(guardbinding.CodeBindingMismatch, "result names another command")
	}
	var result *agentpb.StackKitResult
	if report.State == guardbinding.ReportTerminal {
		result = req.Result
	}
	if denial := report.Check(commandID, result); denial != nil {
		return guardbinding.Acknowledgement{}, denial
	}

	tx, err := h.tenantTx(ctx, req.TenantID)
	if err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var payload []byte
	var state string
	var contract, priorDigest sql.NullString
	var dispatchGeneration sql.NullInt64
	var authority sql.NullTime
	var currentRevision int
	err = tx.QueryRowContext(ctx, `
		SELECT command_json, state, contract_version, worker_generation, authority_expires_at, result_revision, result_digest
		FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id = $2 AND command_id = $3 AND command_family = 'stackkit'
		FOR UPDATE
	`, req.TenantID, req.AgentID, commandID).Scan(&payload, &state, &contract, &dispatchGeneration, &authority, &currentRevision, &priorDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return guardbinding.Acknowledgement{}, ErrCommandNotPending
	}
	if err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	if contract.String != guardbinding.ContractVersion {
		return guardbinding.Acknowledgement{}, guardbinding.Denyf(guardbinding.CodeResultRejected, "command was not dispatched under the binding contract")
	}

	// Identical repeat: acknowledged again. Conflicting record: refused.
	var sameRevision string
	err = tx.QueryRowContext(ctx, `SELECT digest FROM typed_agent_command_revisions WHERE command_id = $1 AND revision = $2`, commandID, report.Revision).Scan(&sameRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return guardbinding.Acknowledgement{}, err
	}
	stored := guardbinding.Committed{State: state, Revision: int64(currentRevision), Digest: priorDigest.String, SameRevisionDigest: sameRevision}
	if dispatchGeneration.Valid {
		stored.DispatchGeneration = dispatchGeneration.Int64
	}
	if authority.Valid {
		stored.AuthorityExpiresAt = authority.Time
	}
	submission := guardbinding.Submission{Report: report, Generation: req.Generation}
	if started := req.Result.GetStartedAtUnix(); report.State == guardbinding.ReportTerminal && started > 0 {
		submission.StartedAt = time.Unix(started, 0)
	}
	duplicate, denial := guardbinding.AcceptRevision(stored, submission)
	switch {
	case duplicate:
		return guardbinding.Acknowledgement{CommandID: commandID, Revision: report.Revision, Digest: report.Digest, Accepted: true, Acknowledged: true, Duplicate: true}, nil
	case denial != nil && denial.Code == guardbinding.CodeCommandNotPending:
		return guardbinding.Acknowledgement{}, ErrCommandNotPending
	case denial != nil:
		return guardbinding.Acknowledgement{}, denial
	}

	command := &agentpb.StackKitCommand{}
	if err := protojson.Unmarshal(payload, command); err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	var resultPayload []byte
	rejectedResult := ""
	if report.State == guardbinding.ReportTerminal {
		if validateErr := stackkitcommand.ValidateResult(result, command); validateErr != nil {
			rejectedResult = "typed StackKits result rejected: " + validateErr.Error()
		} else if resultPayload, err = protojson.Marshal(result); err != nil {
			return guardbinding.Acknowledgement{}, err
		}
	}

	next, nextError := nextCommandState(report, rejectedResult)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO typed_agent_command_revisions
			(command_id, revision, tenant_id, state, digest, prior_digest, reason, resolution, truncated, worker_generation, result_json)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11::jsonb)
	`, commandID, report.Revision, req.TenantID, report.State, report.Digest, report.PriorDigest, report.Reason, report.Resolution, report.Truncated, req.Generation, nullableJSON(resultPayload)); err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE typed_agent_commands
		SET state = $2, error = $3, result_json = COALESCE($4::jsonb, result_json),
		    completed_at = CASE WHEN $2 = 'outcome_unknown' THEN completed_at ELSE now() END,
		    result_revision = $5, result_digest = $6, result_generation = $7, acked_at = now(),
		    mutating = mutating AND $8
		WHERE command_id = $1
	`, commandID, next, nullableText(nextError), nullableJSON(resultPayload), report.Revision, report.Digest, req.Generation, next == "outcome_unknown"); err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	if next == "completed" {
		if err := recordRoundTrip(ctx, tx, req); err != nil {
			return guardbinding.Acknowledgement{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	if rejectedResult != "" {
		return guardbinding.Acknowledgement{}, guardbinding.Denyf(guardbinding.CodeResultRejected, "%s", rejectedResult)
	}
	return guardbinding.Acknowledgement{CommandID: commandID, Revision: report.Revision, Digest: report.Digest, Accepted: true, Acknowledged: true}, nil
}

// nextCommandState maps a committed report to the command state and its stored
// error text. Only a terminal result or a resolving revision ends the command;
// outcome_unknown (and an escalation) keep it fencing.
func nextCommandState(report guardbinding.ResultReport, rejectedResult string) (state, message string) {
	switch report.State {
	case guardbinding.ReportTerminal:
		if rejectedResult != "" {
			return "failed", rejectedResult
		}
		return "completed", ""
	case guardbinding.ReportOutcomeUnknown:
		return "outcome_unknown", "outcome_unknown: " + report.Reason
	case guardbinding.ReportRejected:
		return "failed", "guard_rejected: " + report.Reason
	default:
		switch report.Resolution {
		case guardbinding.ResolutionNotApplied:
			return "failed", "outcome_resolved_not_applied: " + report.Reason
		case guardbinding.ResolutionObservedConverged:
			return "failed", "outcome_resolved_observed_converged: " + report.Reason + "; the desired state was observed, refresh the StackKits evidence"
		default:
			return "outcome_unknown", "outcome_escalated: " + report.Reason
		}
	}
}

func nullableJSON(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}
	return string(payload)
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return strings.ToValidUTF8(value[:limit], "")
	}
	return value
}
