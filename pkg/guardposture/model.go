package guardposture

import (
	"errors"
	"time"
)

// ErrNotFound means no posture row exists for the worker.
var ErrNotFound = errors.New("guardposture: no posture for this worker")

// SecurityRecord is the latest host security observation of one Guard.
type SecurityRecord struct {
	Status     string
	ReasonCode string
	// Evidence is the last accepted document. It is cleared when the pinned
	// StackKits release stops supporting the observation or a document is
	// rejected, so a rejected report never leaves an old pass standing.
	Evidence   *Evidence
	ReportedAt time.Time
}

// BackupRecord is the latest backup and restore-drill observation of one Guard.
type BackupRecord struct {
	Status         string
	ReasonCode     string
	Ready          bool
	SnapshotState  string
	LastSnapshotAt *time.Time
	RestoreState   string
	LastRestoreAt  *time.Time
	ReportedAt     time.Time
}

// Posture is the persisted posture of one Guard: the latest of each observation,
// never a verdict. Verdicts are computed from it at read time.
type Posture struct {
	TenantID       string
	WorkerID       string
	ServerID       string
	OwnerSubjectID string
	DisplayName    string

	SourceEpoch    string
	SourceSequence int64
	ReportedAt     time.Time
	GuardVersion   string
	GuardSigned    bool

	Security SecurityRecord
	Backup   BackupRecord

	// Guard update posture, as the heartbeat reports it.
	GuardMode          string
	GuardUpdateState   string
	GuardUpdateVersion string
	GuardDenyMutations bool
	GuardObservedAt    time.Time

	// VerifyRequestedAt is when an operator last asked for a fresh observation.
	VerifyRequestedAt *time.Time
	// VerifyRequestID is the opaque token of the pending request, empty when
	// none is pending. The request is answered by a security report that echoes
	// it: the Guard learns the token from the directive and echoes it only in an
	// observation it started afterwards, so an observation already in flight
	// when the request arrived never answers it. A newer request replaces the
	// token. See AnswerVerifyRequest.
	VerifyRequestID string
	// VerifyUnechoed counts the security reports that arrived without the
	// token while the request was pending.
	VerifyUnechoed int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Identity is who a report comes from, taken from the authenticated worker and
// never from the report body.
type Identity struct {
	TenantID       string
	WorkerID       string
	ServerID       string
	OwnerSubjectID string
}
