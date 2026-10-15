package backupstore

import (
	"context"
	"testing"
	"time"
)

type fakeRetention struct {
	row   *Retirement
	token bool
}

func (f *fakeRetention) Retire(_ context.Context, _, _ string, wipeAfter time.Time) (Retirement, error) {
	if f.row == nil {
		return Retirement{}, ErrCustodyNotFound
	}
	if f.row.WipeAfter.IsZero() {
		f.row.WipeAfter = wipeAfter
	}
	return *f.row, nil
}
func (f *fakeRetention) MarkTokenRevoked(context.Context, string, string) error {
	f.token = true
	return nil
}
func (f *fakeRetention) MarkWiped(context.Context, string, string) error {
	f.row.Wiped = true
	return nil
}
func (f *fakeRetention) DueWipes(_ context.Context, now time.Time, _, _ string, _ int) ([]WipeDue, error) {
	if f.row == nil || f.row.Wiped || f.row.WipeAfter.IsZero() || f.row.WipeAfter.After(now) {
		return nil, nil
	}
	return []WipeDue{{TenantID: "t", StackID: "s", TokenID: f.row.TokenID}}, nil
}

type fakeWiper struct{ revoked, wiped int }

func (w *fakeWiper) RevokeToken(context.Context, string) error { w.revoked++; return nil }
func (w *fakeWiper) Wipe(context.Context, string, string, ObjectPurger) error {
	w.wiped++
	return nil
}

type noopPurger struct{}

func (noopPurger) PurgeBucket(context.Context, string) error { return nil }

func TestDecommissionRevokesAtOnceAndWipesOnlyAfterTheStoredDeadline(t *testing.T) {
	repo, wiper := &fakeRetention{row: &Retirement{TokenID: "tok"}}, &fakeWiper{}
	retirer, err := NewRetirer(repo, wiper, noopPurger{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	retirer.now = func() time.Time { return now }

	if err := retirer.Decommission(context.Background(), "t", "s", 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if wiper.revoked != 1 || !repo.token || wiper.wiped != 0 {
		t.Fatalf("within retention: revoked=%d tokenRecorded=%v wiped=%d", wiper.revoked, repo.token, wiper.wiped)
	}
	// A restart loses every in-memory timer; the stored deadline alone drives the wipe.
	retirer.now = func() time.Time { return now.Add(29 * 24 * time.Hour) }
	if _, err := retirer.SweepDue(context.Background()); err != nil || wiper.wiped != 0 {
		t.Fatalf("before the deadline: wiped=%d err=%v", wiper.wiped, err)
	}
	retirer.now = func() time.Time { return now.Add(31 * 24 * time.Hour) }
	if n, err := retirer.SweepDue(context.Background()); err != nil || n != 1 || wiper.wiped != 1 || !repo.row.Wiped {
		t.Fatalf("after the deadline: n=%d wiped=%d err=%v", n, wiper.wiped, err)
	}
}

func TestDecommissionWithZeroRetentionWipesImmediately(t *testing.T) {
	repo, wiper := &fakeRetention{row: &Retirement{TokenID: "tok"}}, &fakeWiper{}
	retirer, _ := NewRetirer(repo, wiper, noopPurger{})
	if err := retirer.Decommission(context.Background(), "t", "s", 0); err != nil {
		t.Fatal(err)
	}
	if wiper.revoked != 1 || wiper.wiped != 1 || !repo.row.Wiped {
		t.Fatalf("revoked=%d wiped=%d", wiper.revoked, wiper.wiped)
	}
}

func TestDecommissionWithoutAnOffsiteStoreIsANoop(t *testing.T) {
	wiper := &fakeWiper{}
	retirer, _ := NewRetirer(&fakeRetention{}, wiper, noopPurger{})
	if err := retirer.Decommission(context.Background(), "t", "s", 0); err != nil || wiper.revoked != 0 {
		t.Fatalf("revoked=%d err=%v", wiper.revoked, err)
	}
}
