package guardposture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// RepairTarget addresses one repair command to a Guard.
type RepairTarget struct {
	TenantID  string
	AgentID   string
	CommandID string
	Controls  []string
}

// RepairResult is what the Guard reported for a repair command.
type RepairResult struct {
	// Outcome is the stackkit.host-security-repair/v1 outcome.
	Outcome string
	Steps   []RepairStep
}

// ErrRepairNotDelivered means the command ended before any Guard received it:
// nothing ran on the host.
var ErrRepairNotDelivered = errors.New("guardposture: the repair command was not delivered to the Guard")

// RepairRefusal is a StackKits refusal (a change that would cut the management
// path, or lock the owner out): nothing on the host changed.
type RepairRefusal struct {
	Code  string
	Steps []RepairStep
}

func (r *RepairRefusal) Error() string { return "host security repair refused: " + r.Code }

// RepairDispatcher sends one typed repair command through the durable Guard
// command channel and waits for its result.
type RepairDispatcher interface {
	Repair(ctx context.Context, target RepairTarget) (RepairResult, error)
}

// availabilityReporter lets a dispatcher that binds its transport late say
// whether it can dispatch right now.
type availabilityReporter interface{ Available() bool }

// repairAvailable reports whether this deployment can dispatch a repair now.
func (s *Service) repairAvailable() bool {
	if s.dispatcher == nil {
		return false
	}
	if reporter, ok := s.dispatcher.(availabilityReporter); ok {
		return reporter.Available()
	}
	return true
}

// RepairAvailable reports whether a repair can be dispatched at this moment.
func (s *Service) RepairAvailable() bool { return s != nil && s.repairAvailable() }

// DenyUnavailable is the denial when this deployment cannot dispatch repairs.
const DenyUnavailable = "repair_unavailable"

// ServiceConfig wires a Service.
type ServiceConfig struct {
	Store      Store
	Policy     Policy
	Dispatcher RepairDispatcher
	// LocalRelease resolves the StackKits release a Guard's local repair runs
	// on. Nil means no local repair grant is issued: Guards only observe while
	// they cannot reach the control plane.
	LocalRelease func() (*guardbinding.LocalReleasePin, error)
	Logger       *slog.Logger
	Now          func() time.Time
}

// Service is the posture use-case layer: it reads what a Guard reports, keeps
// the findings honest and admits, runs and records bounded repairs.
type Service struct {
	store        Store
	policy       Policy
	dispatcher   RepairDispatcher
	localRelease func() (*guardbinding.LocalReleasePin, error)
	log          *slog.Logger
	now          func() time.Time

	background context.Context
	stop       context.CancelFunc
	tasks      sync.WaitGroup

	guardMu   sync.Mutex
	guardSeen map[string]guardSeen
}

type guardSeen struct {
	digest string
	at     time.Time
}

// guardRewriteEvery is how often an unchanged Guard observation is rewritten;
// a change is written at once.
const guardRewriteEvery = 10 * time.Minute

// NewService builds a Service. A nil dispatcher means repairs are unavailable;
// observation and alerts work without it.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("guardposture: a store is required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Service{
		store: cfg.Store, policy: cfg.Policy.Normalize(), dispatcher: cfg.Dispatcher, localRelease: cfg.LocalRelease, log: logger.With("component", "guard-posture"),
		now: cfg.Now, background: ctx, stop: stop, guardSeen: map[string]guardSeen{},
	}, nil
}

// Policy returns the bounds in force.
func (s *Service) Policy() Policy { return s.policy }

// Close stops background repairs and waits for them.
func (s *Service) Close() {
	s.stop()
	s.tasks.Wait()
}

// Ingest reads one report from an authenticated Guard. Identity comes from the
// authentication, never from the body. The answer is the directive for the
// Guard's next observation.
func (s *Service) Ingest(ctx context.Context, identity Identity, displayName string, body []byte) (Directive, error) {
	now := s.now()
	if len(body) == 0 || len(body) > MaxReportBytes {
		return Directive{}, invalidReport("the body is empty or exceeds %d bytes", MaxReportBytes)
	}
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Directive{}, invalidReport("it does not decode: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Directive{}, invalidReport("it holds more than one document")
	}
	if err := report.Validate(now); err != nil {
		return Directive{}, err
	}
	if report.CheckIn {
		posture, err := s.store.Get(ctx, identity.TenantID, identity.WorkerID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Directive{}, err
		}
		return s.directive(posture), nil
	}

	write := ReportWrite{Identity: identity, DisplayName: displayName, Report: report, Now: now}
	if report.Security != nil {
		record, evidence := s.securityRecord(report.Security, now)
		write.Security = &record
		if evidence != nil {
			write.Reconciliations = append(write.Reconciliations, s.policy.SecurityReconciliation(evidence, now))
		}
	}
	if report.Backup != nil {
		record := backupRecord(report.Backup, now)
		// The findings are judged on what the store will keep, so the receipts a
		// failed observation carries over are the ones that age into stale.
		previous, getErr := s.store.Get(ctx, identity.TenantID, identity.WorkerID)
		if getErr != nil && !errors.Is(getErr, ErrNotFound) {
			return Directive{}, getErr
		}
		if previous != nil {
			record = MergeBackup(previous.Backup, record)
		}
		write.Backup = &record
		write.Reconciliations = append(write.Reconciliations, s.policy.RecoveryReconciliation(record, now))
	}
	applied, err := s.store.ApplyReport(ctx, write)
	if err != nil {
		return Directive{}, err
	}
	posture, getErr := s.store.Get(ctx, identity.TenantID, identity.WorkerID)
	if getErr != nil && !errors.Is(getErr, ErrNotFound) {
		return Directive{}, getErr
	}
	if applied && report.Security != nil {
		s.autoRepair(ctx, identity, displayName, posture)
	}
	return s.directive(posture), nil
}

func (s *Service) securityRecord(report *SecurityReport, now time.Time) (SecurityRecord, *Evidence) {
	record := SecurityRecord{Status: report.Status, ReasonCode: report.ReasonCode, ReportedAt: now}
	if report.Status != ObservationObserved {
		return record, nil
	}
	evidence, err := ParseEvidence(report.Evidence, now)
	if err != nil {
		// Fail closed: a document that cannot be read leaves no pass behind.
		s.log.Warn("guard_posture_evidence_rejected", "error", err.Error())
		return SecurityRecord{Status: ObservationFailed, ReasonCode: ReasonEvidenceRejected, ReportedAt: now}, nil
	}
	record.Evidence = evidence
	return record, evidence
}

func backupRecord(report *BackupReport, now time.Time) BackupRecord {
	return BackupRecord{
		Status: report.Status, ReasonCode: report.ReasonCode, Ready: report.Ready,
		SnapshotState: report.SnapshotState, LastSnapshotAt: report.LastSnapshotAt,
		RestoreState: report.RestoreState, LastRestoreAt: report.LastRestoreAt, ReportedAt: now,
	}
}

// directive answers a Guard: observe now while a verify request is pending,
// else keep the routine cadence. The request token tells the Guard what to echo
// in the observation it starts for it.
func (s *Service) directive(posture *Posture) Directive {
	directive := Directive{VerifyIntervalSeconds: int(s.policy.VerifyInterval / time.Second)}
	if posture != nil && posture.VerifyRequestID != "" {
		directive.VerifyRequested, directive.VerifyRequestID = true, posture.VerifyRequestID
	}
	return directive
}

// View projects one Guard's posture at the current time. A nil posture is a
// Guard that never reported: everything is unknown, nothing is secure.
func (s *Service) View(posture *Posture) View { return s.policy.Project(posture, s.now()) }

// Views returns the posture views of a tenant's Guards by worker id.
func (s *Service) Views(ctx context.Context, tenantID string) (map[string]View, error) {
	postures, err := s.store.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make(map[string]View, len(postures))
	for i := range postures {
		out[postures[i].WorkerID] = s.policy.Project(&postures[i], now)
	}
	return out, nil
}

// Posture returns one Guard's stored posture, or nil when it never reported.
func (s *Service) Posture(ctx context.Context, tenantID, workerID string) (*Posture, error) {
	posture, err := s.store.Get(ctx, tenantID, workerID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return posture, err
}

// ObserveGuard records the Guard-reported update and operation state from a
// heartbeat and reconciles its findings. An unchanged observation is rewritten
// only every few minutes: the heartbeat is the hottest path.
func (s *Service) ObserveGuard(ctx context.Context, identity Identity, displayName string, obs GuardObservation) error {
	now := s.now()
	sum := sha256.Sum256([]byte(obs.Mode + "\x00" + obs.UpdateState + "\x00" + obs.UpdateVersion + "\x00" + obs.UpdateReason + "\x00" + boolByte(obs.DenyMutations) + boolByte(obs.NeedsOperator)))
	digest := hex.EncodeToString(sum[:])
	key := identity.TenantID + "\x00" + identity.WorkerID
	s.guardMu.Lock()
	seen, ok := s.guardSeen[key]
	s.guardMu.Unlock()
	if ok && seen.digest == digest && now.Sub(seen.at) < guardRewriteEvery {
		return nil
	}
	if err := s.store.ApplyGuard(ctx, GuardWrite{Identity: identity, DisplayName: displayName, Observation: obs, Reconciliation: GuardReconciliation(obs), Now: now}); err != nil {
		return err
	}
	s.guardMu.Lock()
	if len(s.guardSeen) > 50_000 {
		s.guardSeen = map[string]guardSeen{}
	}
	s.guardSeen[key] = guardSeen{digest: digest, at: now}
	s.guardMu.Unlock()
	return nil
}

func boolByte(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// RequestVerify asks the Guard for a fresh observation; the Guard learns of it
// the next time it checks in and answers with an observation it starts after
// that, which echoes the request's token.
func (s *Service) RequestVerify(ctx context.Context, identity Identity, displayName string) error {
	return s.store.RequestVerify(ctx, identity, displayName, uuid.NewString(), s.now())
}

// Findings returns the open findings and the recent history of one Guard.
func (s *Service) Findings(ctx context.Context, tenantID, workerID string, limit int) ([]FindingRecord, error) {
	return s.store.Findings(ctx, tenantID, workerID, s.now().Add(-findingsRetention), limit)
}

// RepairRequest names the controls to restore and who asked.
type RepairRequest struct {
	Identity    Identity
	DisplayName string
	Controls    []string
	Mode        string
	// Actor is the authenticated operator, or the owner who approved automatic
	// repair.
	Actor string
}

// RequestRepair admits one repair and starts it. The decision and the insert
// are one atomic step; a denial leaves nothing behind.
func (s *Service) RequestRepair(ctx context.Context, request RepairRequest) (*Repair, error) {
	if !s.repairAvailable() {
		return nil, &RepairDenial{Code: DenyUnavailable, Message: "Repairs are not available in this deployment."}
	}
	now := s.now()
	controls := append([]string(nil), request.Controls...)
	sort.Strings(controls)
	repair, err := s.store.AdmitRepair(ctx, request.Identity.TenantID, request.Identity.WorkerID, now, func(history []Repair, posture *Posture) (*Repair, error) {
		if err := s.policy.AdmitRepair(posture, history, controls, now); err != nil {
			return nil, err
		}
		id := uuid.NewString()
		return &Repair{
			ID: id, ServerID: request.Identity.ServerID, Mode: request.Mode, RequestedBy: request.Actor, Controls: controls,
			Status: RepairQueued, CommandID: RepairCommandID(id), RequestedAt: now, ExpiresAt: now.Add(s.policy.Repair.Expiry),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	s.tasks.Add(1)
	go func() {
		defer s.tasks.Done()
		s.executeRepair(request.Identity, request.DisplayName, *repair)
	}()
	return repair, nil
}

// executeRepair dispatches one admitted repair and records what came back. The
// outcome of a command whose result never arrived is unknown, never a failure:
// the next observation says what the host looks like.
func (s *Service) executeRepair(identity Identity, displayName string, repair Repair) {
	ctx, cancel := context.WithTimeout(s.background, s.policy.Repair.Expiry+dispatchGrace)
	defer cancel()
	finish := func(fn func(*Repair)) {
		// The record must land even when the run context ended.
		recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer recordCancel()
		if _, err := s.store.UpdateRepair(recordCtx, identity.TenantID, repair.ID, fn); err != nil {
			s.log.Warn("guard_repair_record_failed", "repair_id", repair.ID, "error", err.Error())
		}
		// Whatever happened, observe the host again.
		if err := s.store.RequestVerify(recordCtx, identity, displayName, uuid.NewString(), s.now()); err != nil {
			s.log.Warn("guard_repair_verify_request_failed", "repair_id", repair.ID, "error", err.Error())
		}
	}
	started := s.now()
	if _, err := s.store.UpdateRepair(ctx, identity.TenantID, repair.ID, func(r *Repair) {
		r.Status, r.DispatchedAt = RepairDispatched, &started
	}); err != nil {
		s.log.Warn("guard_repair_dispatch_record_failed", "repair_id", repair.ID, "error", err.Error())
	}
	result, err := s.dispatcher.Repair(ctx, RepairTarget{TenantID: identity.TenantID, AgentID: identity.WorkerID, CommandID: repair.CommandID, Controls: repair.Controls})
	done := s.now()
	var refusal *RepairRefusal
	switch {
	case err == nil:
		finish(func(r *Repair) {
			r.Status, r.Steps, r.FinishedAt = RepairOutcomeStatus(result.Outcome), result.Steps, &done
			for _, step := range result.Steps {
				if step.ReasonCode != "" {
					r.ReasonCode = step.ReasonCode
					break
				}
			}
		})
	case errors.As(err, &refusal):
		finish(func(r *Repair) {
			r.Status, r.ReasonCode, r.Steps, r.FinishedAt = RepairBlocked, refusal.Code, refusal.Steps, &done
		})
	case errors.Is(err, ErrRepairNotDelivered):
		finish(func(r *Repair) { r.Status, r.ReasonCode, r.FinishedAt = RepairExpired, "not_delivered", &done })
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		finish(func(r *Repair) { r.Status, r.ReasonCode, r.FinishedAt = RepairOutcomeUnknown, "no_result", &done })
	default:
		s.log.Warn("guard_repair_dispatch_failed", "repair_id", repair.ID, "error", err.Error())
		finish(func(r *Repair) { r.Status, r.ReasonCode, r.FinishedAt = RepairFailed, "dispatch_failed", &done })
	}
}

// Repairs lists the recent repairs of one Guard. A repair that is still
// dispatched long past its expiry is shown as outcome_unknown: the control
// plane that awaited it may have restarted.
func (s *Service) Repairs(ctx context.Context, tenantID, workerID string, limit int) ([]Repair, error) {
	repairs, err := s.store.Repairs(ctx, tenantID, workerID, limit)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range repairs {
		if (repairs[i].Status == RepairQueued || repairs[i].Status == RepairDispatched) && !repairs[i].Live(now) {
			repairs[i].Status, repairs[i].ReasonCode = RepairOutcomeUnknown, "no_result"
		}
	}
	return repairs, nil
}

// AutoRepair returns the owner's approval of automatic repair; a Guard without
// one is observe-only.
func (s *Service) AutoRepair(ctx context.Context, tenantID, workerID string) (*AutoRepairPolicy, error) {
	policy, err := s.store.GetAutoRepair(ctx, tenantID, workerID)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return &AutoRepairPolicy{TenantID: tenantID, WorkerID: workerID, Controls: []string{}}, nil
	}
	return policy, nil
}

// SetAutoRepair records an owner's approval, or withdraws it.
func (s *Service) SetAutoRepair(ctx context.Context, tenantID, workerID, actor string, enabled bool, controls []string, expiresAt time.Time) (*AutoRepairPolicy, error) {
	now := s.now()
	policy := AutoRepairPolicy{TenantID: tenantID, WorkerID: workerID, Enabled: false, Controls: []string{}, UpdatedAt: now}
	if enabled {
		if err := s.policy.ValidateAutoRepair(controls, expiresAt, now); err != nil {
			return nil, err
		}
		sorted := append([]string(nil), controls...)
		sort.Strings(sorted)
		expiry := expiresAt.UTC()
		policy.Enabled, policy.Controls, policy.ExpiresAt, policy.ApprovedBy, policy.ApprovedAt = true, sorted, &expiry, actor, &now
	}
	return s.store.SetAutoRepair(ctx, policy)
}

// localOperationNamespace derives a record ID from a Guard-generated operation
// ID that is unique per tenant and worker, so two Guards cannot collide.
var localOperationNamespace = uuid.MustParse("6f1d7b0e-3b8a-4d55-9c1e-5a0f2a7c9e41")

// LocalRepairCommandID is the journal identity of one local-policy operation.
func LocalRepairCommandID(repairID string) string { return "guard-local-repair:" + repairID }

// AcceptLocalOperation commits one revision of evidence a Guard reports for a
// repair it ran under a registered local policy version, and acknowledges it
// only after the commit. An identical repeat is acknowledged again without a
// write; evidence without registered, current authority, out of the policy's
// scope or beyond the persisted repair bounds is refused with a structured
// denial. generation is the credential generation that authenticated the
// report. The accepted operation counts toward the same cooldown, attempt
// budget and circuit breaker as an online repair.
func (s *Service) AcceptLocalOperation(ctx context.Context, identity Identity, displayName string, generation int64, evidence LocalOperationEvidence) (guardbinding.Acknowledgement, error) {
	now := s.now()
	submission := LocalSubmission{Identity: identity, Generation: generation, Evidence: evidence}
	ref := LocalRef{OperationID: evidence.OperationID, PolicyID: evidence.PolicyID, PolicyVersion: evidence.PolicyVersion}
	duplicate := false
	err := s.store.AcceptLocalOperation(ctx, identity.TenantID, identity.WorkerID, ref, func(state LocalState) (*Repair, error) {
		record, dup, denial := s.policy.AcceptLocalOperation(submission, state, now)
		if denial != nil {
			return nil, denial
		}
		duplicate = dup
		if record != nil && record.ID == "" {
			record.ID = uuid.NewSHA1(localOperationNamespace, []byte(identity.TenantID+"\x00"+identity.WorkerID+"\x00"+evidence.OperationID)).String()
			record.CommandID = LocalRepairCommandID(record.ID)
		}
		return record, nil
	})
	if err != nil {
		return guardbinding.Acknowledgement{}, err
	}
	if !duplicate {
		// Whatever the Guard did, observe the host again.
		if verifyErr := s.store.RequestVerify(ctx, identity, displayName, uuid.NewString(), now); verifyErr != nil {
			s.log.Warn("guard_local_repair_verify_request_failed", "operation_id", evidence.OperationID, "error", verifyErr.Error())
		}
	}
	return guardbinding.Acknowledgement{CommandID: evidence.OperationID, Revision: evidence.Report.Revision, Digest: evidence.Report.Digest,
		Accepted: true, Acknowledged: true, Duplicate: duplicate}, nil
}

// autoRepair repairs the controls an active owner approval covers. Missing,
// withdrawn or expired approval means observe-only; the same bounds as an
// operator repair apply, so a control that keeps reverting is repaired a few
// times and then left to a person.
func (s *Service) autoRepair(ctx context.Context, identity Identity, displayName string, posture *Posture) {
	if !s.repairAvailable() {
		return
	}
	policy, err := s.store.GetAutoRepair(ctx, identity.TenantID, identity.WorkerID)
	if err != nil || policy == nil {
		return
	}
	controls := AutoRepairCandidates(policy, posture, s.now())
	if len(controls) == 0 {
		return
	}
	if _, err := s.RequestRepair(ctx, RepairRequest{Identity: identity, DisplayName: displayName, Controls: controls, Mode: RepairAuto, Actor: policy.ApprovedBy}); err != nil {
		if denial, ok := IsRepairDenial(err); ok {
			s.log.Debug("guard_auto_repair_denied", "worker_id", identity.WorkerID, "code", denial.Code, "control", denial.Control)
			return
		}
		s.log.Warn("guard_auto_repair_failed", "worker_id", identity.WorkerID, "error", err.Error())
	}
}
