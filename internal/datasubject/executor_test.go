package datasubject

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	contract "github.com/kombifyio/techstack/internal/selfhostcontracts/datasubject"
)

type fakeDirectory []Principal

func (d fakeDirectory) Lookup(context.Context, string) ([]Principal, error) { return d, nil }

type failingDirectory struct{ err error }

func (d failingDirectory) Lookup(context.Context, string) ([]Principal, error) { return nil, d.err }

// fakeStore is the in-memory seam: tables of one principal with live resources.
type fakeStore struct {
	rows   map[string]int64
	live   int64
	erased int
}

func (s *fakeStore) Inventory(context.Context, Principal) (Inventory, error) {
	inv := Inventory{LiveResources: s.live}
	for table, n := range s.rows {
		if n > 0 {
			inv.Tables = append(inv.Tables, TableRows{Table: table, Rows: n, Evidence: evidenceTables[table]})
		}
	}
	return inv, nil
}

func (s *fakeStore) Erase(context.Context, Principal) (map[string]int64, error) {
	s.erased++
	deleted := map[string]int64{}
	for table, n := range s.rows {
		if !evidenceTables[table] {
			deleted[table] = n
			s.rows[table] = 0
		}
	}
	return deleted, nil
}

func newExecutor(store *fakeStore) *StoreExecutor {
	return &StoreExecutor{
		Directory:         fakeDirectory{{TenantID: "usr:auth0|owner", Subject: "auth0|owner"}},
		Store:             store,
		EvidenceRetention: 3 * 365 * 24 * time.Hour,
	}
}

// A live provider resource blocks erasure with a stable reason code and the
// rows that track it are not deleted.
func TestEraseIsBlockedWhileProviderResourcesAreLive(t *testing.T) {
	store := &fakeStore{rows: map[string]int64{"servers": 1, "stacks": 2}, live: 1}
	h, pub := handlerFor(t, newExecutor(store))
	rec := post(t, h, "gateway", request(t, contract.KindErase, SubjectRefFor("auth0|owner")))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var receipt contract.Receipt
	if err := json.Unmarshal(rec.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if err := receipt.Verify(pub); err != nil {
		t.Fatal(err)
	}
	if receipt.State != contract.ReceiptBlocked || receipt.ReasonCode == nil || *receipt.ReasonCode != ReasonLiveResources {
		t.Fatalf("receipt %+v", receipt)
	}
	if store.erased != 0 || store.rows["stacks"] != 2 {
		t.Fatal("rows were deleted while a live resource existed")
	}
}

// Erasure deletes the rows, reports backups and logs as retained (never as
// erased), and a redelivery finds nothing held.
func TestEraseThenRedeliveryIsNothingHeld(t *testing.T) {
	store := &fakeStore{rows: map[string]int64{"stacks": 2, "wizard_runs": 1}}
	exec := newExecutor(store)
	ref := SubjectRefFor("auth0|owner")

	out, err := exec.Erase(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var deleted int64
	for _, d := range out.Datasets {
		deleted += d.Rows
	}
	if deleted != 3 {
		t.Fatalf("datasets %+v", out.Datasets)
	}
	kept := map[string]time.Time{}
	for _, r := range out.Retained {
		kept[r.ID] = r.RetainedUntil
	}
	if !kept["postgres_backups"].After(time.Now()) || !kept["logs_and_error_telemetry"].After(kept["postgres_backups"]) {
		t.Fatalf("retention not reported: %+v", out.Retained)
	}

	_, err = exec.Erase(context.Background(), ref)
	if !errors.Is(err, ErrNothingHeld) {
		t.Fatalf("redelivery returned %v", err)
	}
}

// Provider evidence survives erasure and is reported with retained_until.
func TestEvidenceIsRetainedNotErased(t *testing.T) {
	store := &fakeStore{rows: map[string]int64{"stacks": 1, "managed_runtime_capacity_reservations": 4}}
	out, err := newExecutor(store).Erase(context.Background(), SubjectRefFor("auth0|owner"))
	if err != nil {
		t.Fatal(err)
	}
	if store.rows["managed_runtime_capacity_reservations"] != 4 {
		t.Fatal("evidence rows were deleted")
	}
	var found bool
	for _, r := range out.Retained {
		found = found || r.ID == "managed_runtime_capacity_reservations" && !r.RetainedUntil.IsZero()
	}
	if !found {
		t.Fatalf("evidence not retained: %+v", out.Retained)
	}
}

// A database without the subject directory migration must answer blocked with
// the stable reason, never "nothing held" for data that may exist.
func TestMissingSubjectDirectoryBlocksInsteadOfClaimingNothingHeld(t *testing.T) {
	exec := newExecutor(&fakeStore{rows: map[string]int64{"stacks": 2}})
	exec.Directory = failingDirectory{err: &pgconn.PgError{Code: pgUndefinedFunction}}
	_, err := exec.Export(context.Background(), SubjectRefFor("auth0|owner"))
	var blocked BlockedError
	if !errors.As(err, &blocked) || blocked.Reason != ReasonDirectoryUnwired {
		t.Fatalf("err %v", err)
	}
}
