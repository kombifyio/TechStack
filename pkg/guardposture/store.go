package guardposture

import (
	"context"
	"time"
)

// Store persists posture. Implementations are tenant scoped: a read or write
// for one tenant can never see or change another's rows.
type Store interface {
	// Get returns one Guard's posture, or ErrNotFound.
	Get(ctx context.Context, tenantID, workerID string) (*Posture, error)
	// ListByTenant returns every Guard posture of one tenant (the read model).
	ListByTenant(ctx context.Context, tenantID string) ([]Posture, error)
	// ApplyReport persists one accepted report and reconciles the findings it
	// implies, emitting their alerts in the same transaction. It reports false
	// when the report was a replay of one already applied.
	ApplyReport(ctx context.Context, write ReportWrite) (bool, error)
	// ApplyGuard persists the heartbeat-reported Guard state and reconciles the
	// Guard findings.
	ApplyGuard(ctx context.Context, write GuardWrite) error
	// RequestVerify records that a fresh observation was asked for under a new
	// request token, replacing a pending one.
	RequestVerify(ctx context.Context, identity Identity, displayName, requestID string, at time.Time) error
	// OpenFindings returns the open findings of one Guard.
	OpenFindings(ctx context.Context, tenantID, workerID string) ([]FindingRecord, error)
	// Findings returns the findings history of one Guard since the given time,
	// newest first.
	Findings(ctx context.Context, tenantID, workerID string, since time.Time, limit int) ([]FindingRecord, error)

	// AdmitRepair atomically decides and records one repair: it reads the
	// server's recent repairs under a per-server lock, asks admit, and inserts
	// what admit returns. A denial from admit leaves nothing behind.
	AdmitRepair(ctx context.Context, tenantID, workerID string, now time.Time, admit func(history []Repair, posture *Posture) (*Repair, error)) (*Repair, error)
	// UpdateRepair applies fn to a repair and persists it.
	UpdateRepair(ctx context.Context, tenantID, repairID string, fn func(*Repair)) (*Repair, error)
	// Repairs returns the newest repairs of one Guard.
	Repairs(ctx context.Context, tenantID, workerID string, limit int) ([]Repair, error)
	// GetAutoRepair returns the owner's approval, or nil when none was given.
	GetAutoRepair(ctx context.Context, tenantID, workerID string) (*AutoRepairPolicy, error)
	SetAutoRepair(ctx context.Context, policy AutoRepairPolicy) error
}

// ReportWrite is one accepted report, ready to persist.
type ReportWrite struct {
	Identity    Identity
	DisplayName string
	Report      Report
	// Security and Backup are the new observations; nil leaves the stored one.
	Security        *SecurityRecord
	Backup          *BackupRecord
	Reconciliations []Reconciliation
	Now             time.Time
}

// GuardWrite is the heartbeat-reported Guard state.
type GuardWrite struct {
	Identity       Identity
	DisplayName    string
	Observation    GuardObservation
	Reconciliation Reconciliation
	Now            time.Time
}

// MergeSecurity decides what an incoming security observation leaves behind. A
// document that was rejected or whose command the pinned StackKits does not
// have clears the evidence, so neither leaves an old pass standing. A
// transient failure keeps the last accepted evidence until it expires; it never
// extends it.
func MergeSecurity(previous, next SecurityRecord) SecurityRecord {
	switch next.Status {
	case ObservationFailed, ObservationUnavailable:
		if next.ReasonCode != ReasonEvidenceRejected && next.Evidence == nil {
			next.Evidence = previous.Evidence
		}
	}
	return next
}

// MergeBackup decides what an incoming backup observation leaves behind. A
// failed or unavailable observation (an unreadable repository, a missing
// workspace) keeps the last receipts it replaces so they keep aging: failing
// to read a repository is neither evidence that it is fresh nor a reason to
// forget how old its last snapshot was. A host that never had a receipt has
// none to keep and stays unknown. An unsupported command clears them, as it
// does for security evidence.
func MergeBackup(previous, next BackupRecord) BackupRecord {
	switch next.Status {
	case ObservationFailed, ObservationUnavailable:
		if next.SnapshotState == "" && next.LastSnapshotAt == nil && next.RestoreState == "" && next.LastRestoreAt == nil {
			next.SnapshotState, next.LastSnapshotAt = previous.SnapshotState, previous.LastSnapshotAt
			next.RestoreState, next.LastRestoreAt = previous.RestoreState, previous.LastRestoreAt
		}
	}
	return next
}

// maxUnechoedReports is how many security reports without the request token a
// pending verify request survives. The Guard that was in the middle of a routine
// observation when the request arrived sends one such report and learns the
// token from its answer; a Guard that sends a second does not echo tokens (it
// predates them), so its report is taken as the answer, as it was before
// tokens existed, and the request is never asked in a loop.
const maxUnechoedReports = 2

// AnswerVerifyRequest settles a pending verify request against the security
// report just applied to the posture. It is called by every Store inside the
// transaction that applies the report, with the token the report echoed, and
// reports whether the request is now answered.
func AnswerVerifyRequest(posture *Posture, echoed string) bool {
	if posture.VerifyRequestID == "" {
		return false
	}
	if echoed != posture.VerifyRequestID {
		posture.VerifyUnechoed++
		if posture.VerifyUnechoed < maxUnechoedReports {
			return false
		}
	}
	posture.VerifyRequestID, posture.VerifyUnechoed = "", 0
	return true
}
