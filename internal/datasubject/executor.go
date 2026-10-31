package datasubject

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	contract "github.com/kombifyio/stackkits/contracts/datasubject"
)

// Blocked reasons of the executor. They are stable: Cloud shows them to the
// operator and the person.
const (
	// ReasonLiveResources: an active lease or a provisioned server still exists.
	// Techstack decommissions provider resources first; erasure never deletes
	// the rows that track them.
	ReasonLiveResources = "techstack_live_provider_resources"
	// ReasonEraseIncomplete: rows remained after the deletion passes.
	ReasonEraseIncomplete = "techstack_erase_incomplete"
	// ReasonEvidenceRetention: the provider-evidence retention is a legal
	// decision, so erasure waits until it is configured.
	ReasonEvidenceRetention = "techstack_evidence_retention_unset"
	// ReasonDirectoryUnwired: the subject directory is absent (not configured or
	// its migration is not applied), so refs cannot be resolved and Techstack
	// refuses to claim that it holds nothing.
	ReasonDirectoryUnwired = "techstack_subject_directory_unwired"
)

const (
	refPrefix = "sr1_"
	refSalt   = "kombify:subject:v1:"

	// Backups and PITR age out within 35 days, logs and error telemetry within
	// their 90-day retention (data-sovereignty decision, section 3).
	backupRetention = 35 * 24 * time.Hour
	logRetention    = 90 * 24 * time.Hour
)

// SubjectRefFor is the opaque SubjectRef of an account: Cloud and Techstack
// derive it from the Auth0 subject the same way, so no mapping table, e-mail
// address or shared secret is needed. Techstack resolves a ref by hashing the
// subjects of its own directory and never inverts it.
func SubjectRefFor(sub string) string {
	sum := sha256.Sum256([]byte(refSalt + sub))
	return refPrefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

// Principal is one tenant-scoped holder of a subject's data.
type Principal struct {
	TenantID string
	Subject  string
}

// Directory resolves an opaque subject ref to the principals Techstack holds
// data for. Row-level security scopes every table to one tenant, so the
// directory is the only cross-tenant view and is migration-owned.
type Directory interface {
	Lookup(ctx context.Context, subjectRef string) ([]Principal, error)
}

// TableRows is the row count of one table for one principal.
type TableRows struct {
	Table string
	Rows  int64
	// Evidence marks provider execution evidence that is retained, not erased.
	Evidence bool
}

// Inventory is what the store holds for one principal.
type Inventory struct {
	Tables []TableRows
	// LiveResources counts active leases and provisioned servers.
	LiveResources int64
}

// Store is the Postgres seam of the executor.
type Store interface {
	Inventory(ctx context.Context, p Principal) (Inventory, error)
	// Erase deletes every non-evidence row of the principal in one
	// transaction and returns the deleted rows per table. It returns
	// BlockedError{ReasonEraseIncomplete} and deletes nothing when rows remain.
	Erase(ctx context.Context, p Principal) (map[string]int64, error)
}

// StoreExecutor implements Executor over a Directory and a Store.
type StoreExecutor struct {
	Directory Directory
	Store     Store
	// EvidenceRetention is how long provider execution evidence (capacity
	// reservations, recovery candidates, mutation receipts) is kept after an
	// erasure. Erasure is blocked until it is configured.
	EvidenceRetention time.Duration
	Now               func() time.Time
}

func (e *StoreExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *StoreExecutor) resolve(ctx context.Context, ref string) ([]Principal, error) {
	if e.Directory == nil || e.Store == nil {
		return nil, BlockedError{Reason: ReasonDirectoryUnwired}
	}
	all, err := e.Directory.Lookup(ctx, ref)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUndefinedFunction {
			return nil, BlockedError{Reason: ReasonDirectoryUnwired}
		}
		return nil, fmt.Errorf("subject directory: %w", err)
	}
	var out []Principal
	for _, p := range all {
		if SubjectRefFor(p.Subject) == ref {
			out = append(out, p)
		}
	}
	return out, nil
}

// Export reports the inventory Techstack holds for the subject: dataset ids and
// row counts only, never row content, so no secret leaves in clear.
func (e *StoreExecutor) Export(ctx context.Context, subjectRef string) (Outcome, error) {
	principals, err := e.resolve(ctx, subjectRef)
	if err != nil {
		return Outcome{}, err
	}
	var out Outcome
	for _, p := range principals {
		inv, err := e.Store.Inventory(ctx, p)
		if err != nil {
			return Outcome{}, err
		}
		for _, t := range inv.Tables {
			out.Datasets = append(out.Datasets, contract.Dataset{ID: t.Table, Action: "exported", Rows: t.Rows, Store: "postgres:kombify_runtime"})
		}
	}
	if len(out.Datasets) == 0 {
		return Outcome{}, ErrNothingHeld
	}
	return out, nil
}

// Erase deletes the subject's control-plane rows. Live provider resources block
// it before anything is deleted. Backups, logs and provider evidence are
// reported as retained with retained_until, never as erased. Redelivery is
// safe: a finished erasure leaves no erasable row and resolves to nothing held.
func (e *StoreExecutor) Erase(ctx context.Context, subjectRef string) (Outcome, error) {
	if e.EvidenceRetention <= 0 {
		return Outcome{}, BlockedError{Reason: ReasonEvidenceRetention}
	}
	principals, err := e.resolve(ctx, subjectRef)
	if err != nil {
		return Outcome{}, err
	}
	type held struct {
		p   Principal
		inv Inventory
	}
	var all []held
	for _, p := range principals {
		inv, err := e.Store.Inventory(ctx, p)
		if err != nil {
			return Outcome{}, err
		}
		if inv.LiveResources > 0 {
			return Outcome{}, BlockedError{Reason: ReasonLiveResources}
		}
		all = append(all, held{p, inv})
	}

	var out Outcome
	now := e.now().UTC()
	evidence := map[string]bool{}
	erasable := false
	for _, h := range all {
		for _, t := range h.inv.Tables {
			if t.Evidence {
				evidence[t.Table] = true
			} else {
				erasable = true
			}
		}
	}
	if erasable {
		for _, h := range all {
			deleted, err := e.Store.Erase(ctx, h.p)
			if err != nil {
				return Outcome{}, err
			}
			for _, t := range h.inv.Tables {
				if n := deleted[t.Table]; n > 0 {
					out.Datasets = append(out.Datasets, contract.Dataset{ID: t.Table, Action: "deleted", Rows: n, Store: "postgres:kombify_runtime"})
				}
			}
		}
	}
	for table := range evidence {
		out.Retained = append(out.Retained, contract.Retained{
			ID: table, Reason: "provider_execution_evidence", RetainedUntil: now.Add(e.EvidenceRetention),
		})
	}
	sort.Slice(out.Retained, func(i, j int) bool { return out.Retained[i].ID < out.Retained[j].ID })
	if len(out.Datasets) == 0 && len(evidence) == 0 {
		return Outcome{}, ErrNothingHeld
	}
	// Backups and logs hold copies until they age out; they are retained, not erased.
	out.Retained = append(out.Retained,
		contract.Retained{ID: "postgres_backups", Reason: "backup_aging", RetainedUntil: now.Add(backupRetention)},
		contract.Retained{ID: "logs_and_error_telemetry", Reason: "log_retention", RetainedUntil: now.Add(logRetention)},
	)
	if len(out.Datasets) == 0 {
		return out, ErrNothingHeld
	}
	return out, nil
}
