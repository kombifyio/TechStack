package postureagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardexec"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

// LocalRepairer runs one typed host-security repair under a local repair
// grant. It returns guardexec.ErrLocalRepairNotStarted (wrapped) when nothing
// ran, and an unknown-outcome result when the effect may have started without
// an observable result.
type LocalRepairer interface {
	LocalHostSecurityRepair(ctx context.Context, grant guardbinding.LocalRepairGrant, operationID string, controls []string) (*agentpb.StackKitResult, error)
}

// LocalConfig lets a Guard repair under the owner's local policy while the
// control plane is unreachable (Guard G07). Without it, or without a current
// grant, the Guard only observes.
type LocalConfig struct {
	// Pins are the control-plane binding keys from the enrollment file; a
	// grant must be signed by one of them.
	Pins []guardbinding.PinnedKey
	// Expect names this Guard: tenant, worker and server.
	Expect guardbinding.Expectation
	// Revoked reports an observed revocation of this Guard's identity.
	Revoked  func() bool
	Repairer LocalRepairer
	Ledger   *Ledger
	// Monotonic reads a process-monotonic clock. Nil anchors one at New.
	Monotonic func() time.Duration
}

func (a *Agent) monotonic() time.Duration { return a.cfg.Local.Monotonic() }

// receiveGrant replaces the held grant with the one an online answer carried.
// An answer without one (approval missing, withdrawn or expired) and a grant
// that does not verify both leave the Guard observe-only.
func (a *Agent) receiveGrant(grant *guardbinding.LocalRepairGrant) {
	local := a.cfg.Local
	if local == nil {
		return
	}
	if grant == nil {
		a.held = nil
		return
	}
	expect := local.Expect
	expect.HighestGeneration = a.highestGeneration
	held, denial := guardbinding.ReceiveLocalGrant(*grant, local.Pins, expect, a.cfg.Now(), a.monotonic())
	if denial != nil {
		a.held = nil
		a.log.Warn("guard_local_policy_refused", "code", denial.Code, "reason", denial.Message)
		return
	}
	a.held = held
	a.highestGeneration = max(a.highestGeneration, held.Grant.Generation)
}

// localRepair runs one repair on its own after an observation the control
// plane could not receive. Every bound decides before any effect: a held,
// trusted grant that covers the drifted controls, no unreported unknown
// outcome, the persisted cooldown, attempt budget and circuit breaker, and a
// durable record of the operation.
func (a *Agent) localRepair(ctx context.Context, security *guardposture.SecurityReport) {
	local := a.cfg.Local
	if local == nil || a.held == nil || security == nil || security.Status != guardposture.ObservationObserved || local.Ledger == nil {
		return
	}
	now := a.cfg.Now()
	evidence, err := guardposture.ParseEvidence(security.Evidence, now)
	if err != nil {
		return
	}
	grant := a.held.Grant
	controls := guardposture.LocalCandidates(grant, evidence, now)
	if len(controls) == 0 {
		return
	}
	expect := local.Expect
	expect.HighestGeneration = a.highestGeneration
	revoked := local.Revoked != nil && local.Revoked()
	if denial := a.held.Admit(guardbinding.EffectState{Expect: expect, Revoked: revoked, Now: now, Mono: a.monotonic()}, controls); denial != nil {
		if denial.Code != guardbinding.CodeAuthorityExpired {
			a.log.Info("guard_local_repair_observe_only", "code", denial.Code, "reason", denial.Message)
		}
		if denial.Code == guardbinding.CodeAuthorityExpired || denial.Code == guardbinding.CodeRevalidationRequired || denial.Code == guardbinding.CodeAuthorityRevoked {
			a.held = nil
		}
		return
	}
	if err := local.Ledger.Err(); err != nil {
		a.log.Warn("guard_local_repair_ledger_unavailable", "error", err.Error())
		return
	}
	if local.Ledger.Fenced() {
		a.log.Info("guard_local_repair_fenced", "reason", "an earlier local repair has an unreported unknown outcome")
		return
	}
	if err := guardposture.CheckLocalBudget(grant, local.Ledger.Repairs(), controls, now); err != nil {
		code := "repair_bounded"
		if denial, ok := guardposture.IsRepairDenial(err); ok {
			code = denial.Code
		}
		a.log.Info("guard_local_repair_bounded", "code", code)
		return
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	operationID := grant.NewLocalOperationID(hex.EncodeToString(nonce))
	record := guardposture.LocalOperationEvidence{
		OperationID: operationID, TenantID: grant.TenantID, WorkerID: grant.WorkerID, Generation: grant.Generation,
		PolicyID: grant.PolicyID, PolicyVersion: grant.PolicyVersion, PolicyExpiresAt: grant.PolicyExpiresAt,
		Action: guardposture.LocalActionHostSecurityRepair, Controls: controls, StartedAt: now,
	}
	if err := local.Ledger.Begin(record); err != nil {
		a.log.Warn("guard_local_repair_not_recorded", "error", err.Error())
		return
	}
	a.log.Info("guard_local_repair_started", "operation_id", operationID, "policy_version", grant.PolicyVersion, "controls", controls)
	result, err := local.Repairer.LocalHostSecurityRepair(ctx, grant, operationID, controls)
	if err != nil && errors.Is(err, guardexec.ErrLocalRepairNotStarted) {
		a.log.Warn("guard_local_repair_refused", "operation_id", operationID, "error", err.Error())
		if abandonErr := local.Ledger.Abandon(operationID); abandonErr != nil {
			a.log.Warn("guard_local_repair_record_failed", "operation_id", operationID, "error", abandonErr.Error())
		}
		return
	}
	finished := localOutcome(result, err, controls)
	if err := local.Ledger.Finish(operationID, finished); err != nil {
		a.log.Warn("guard_local_repair_record_failed", "operation_id", operationID, "error", err.Error())
	}
	a.log.Info("guard_local_repair_finished", "operation_id", operationID, "state", finished.state, "outcome", finished.outcome)
}

// localOutcome reduces the executor's result to the evidence the control plane
// reads: the stackkit.host-security-repair/v1 outcome and steps of the named
// controls, or outcome_unknown when the result cannot say what happened.
func localOutcome(result *agentpb.StackKitResult, err error, controls []string) outcome {
	if err != nil || result == nil {
		return unknownOutcome("no_result")
	}
	if reason, unknown := guardbinding.IsUnknownOutcome(result); unknown {
		if len(reason) > guardbinding.MaxReasonBytes {
			reason = reason[:guardbinding.MaxReasonBytes]
		}
		return unknownOutcome(reason)
	}
	repair, parseErr := guardposture.ParseRepairResult(result)
	var refusal *guardposture.RepairRefusal
	switch {
	case parseErr == nil:
		if !outcomeTokenOK(repair.Outcome) {
			repair.Outcome = "failed"
		}
		return outcome{state: guardbinding.ReportTerminal, outcome: repair.Outcome, steps: namedSteps(repair.Steps, controls)}
	case errors.As(parseErr, &refusal):
		return outcome{state: guardbinding.ReportTerminal, outcome: "blocked", steps: namedSteps(refusal.Steps, controls)}
	default:
		return outcome{state: guardbinding.ReportTerminal, outcome: "failed"}
	}
}

func namedSteps(steps []guardposture.RepairStep, controls []string) []guardposture.RepairStep {
	out := make([]guardposture.RepairStep, 0, len(steps))
	for _, step := range steps {
		if slices.Contains(controls, step.Control) && len(out) < len(controls) {
			out = append(out, step)
		}
	}
	return out
}

func outcomeTokenOK(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// deliverLocal reports every unacknowledged local repair over the posture
// channel. The control plane commits each revision before it acknowledges it;
// a refusal is kept as rejected, anything else is retried on the next online
// exchange.
func (a *Agent) deliverLocal(ctx context.Context) {
	local := a.cfg.Local
	if local == nil || local.Ledger == nil {
		return
	}
	for _, evidence := range local.Ledger.Pending() {
		body, err := json.Marshal(evidence)
		if err != nil {
			continue
		}
		status, payload, err := a.post(ctx, body)
		if err != nil {
			return
		}
		switch {
		case status >= 200 && status <= 299:
			var envelope struct {
				Data guardbinding.Acknowledgement `json:"data"`
			}
			if json.Unmarshal(payload, &envelope) != nil || !envelope.Data.Acknowledged || envelope.Data.CommandID != evidence.OperationID ||
				envelope.Data.Revision != evidence.Report.Revision || envelope.Data.Digest != evidence.Report.Digest {
				return
			}
			if err := local.Ledger.Acknowledge(evidence.OperationID, evidence.Report.Revision, false); err != nil {
				a.log.Warn("guard_local_repair_ack_not_recorded", "operation_id", evidence.OperationID, "error", err.Error())
			}
		case status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusConflict:
			a.log.Warn("guard_local_repair_evidence_refused", "operation_id", evidence.OperationID, "status", status)
			if err := local.Ledger.Acknowledge(evidence.OperationID, evidence.Report.Revision, true); err != nil {
				a.log.Warn("guard_local_repair_ack_not_recorded", "operation_id", evidence.OperationID, "error", err.Error())
			}
		default:
			return
		}
	}
}
