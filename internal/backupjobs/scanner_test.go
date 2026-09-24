package backupjobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
)

type fakeStore struct {
	due    []Due
	marked []string
	err    error
}

func (f *fakeStore) ListDue(_ context.Context, _ time.Time, _, _ string, _ int) ([]Due, error) {
	out := f.due
	f.due = nil
	return out, f.err
}

func (f *fakeStore) MarkRan(_ context.Context, tenantID, stackID string, _ time.Time) error {
	f.marked = append(f.marked, tenantID+"/"+stackID)
	return nil
}

type fakeEnqueuer struct {
	payloads []jobs.BackupPayload
	err      error
}

func (f *fakeEnqueuer) EnqueueBackup(_ context.Context, payload jobs.BackupPayload) error {
	f.payloads = append(f.payloads, payload)
	return f.err
}

func TestNextDueIsDeterministicPerCadence(t *testing.T) {
	base := time.Date(2026, 9, 18, 13, 30, 0, 0, time.UTC)

	daily := Schedule{Cadence: CadenceDaily, HourUTC: 2, MinuteUTC: 15}
	if got, want := daily.NextDue(base), time.Date(2026, 9, 19, 2, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("daily next = %s, want %s", got, want)
	}

	hourly := Schedule{Cadence: CadenceHourly, MinuteUTC: 45}
	if got, want := hourly.NextDue(base), time.Date(2026, 9, 18, 13, 45, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("hourly next = %s, want %s", got, want)
	}

	// 2026-09-18 is a Friday; the next Sunday slot is the 20th.
	weekly := Schedule{Cadence: CadenceWeekly, HourUTC: 3, MinuteUTC: 0, WeekdayUTC: "sunday"}
	if got, want := weekly.NextDue(base), time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("weekly next = %s, want %s", got, want)
	}

	// A slot that has just passed must move forward, never return the past.
	atSlot := time.Date(2026, 9, 18, 2, 15, 0, 0, time.UTC)
	if got := daily.NextDue(atSlot); !got.After(atSlot) {
		t.Fatalf("next due %s must be strictly after %s", got, atSlot)
	}
}

func TestScheduleRejectsAnInadmissibleCadence(t *testing.T) {
	if _, err := (Schedule{TenantID: "t", StackID: "s", Cadence: "monthly"}).normalize(); err == nil {
		t.Fatal("a cadence outside #BackupScheduleV1 must be refused")
	}
	if _, err := (Schedule{TenantID: "t", StackID: "s", Cadence: CadenceDaily, HourUTC: 24}).normalize(); err == nil {
		t.Fatal("an out-of-range hour must be refused")
	}
	if _, err := (Schedule{StackID: "s", Cadence: CadenceDaily}).normalize(); err == nil {
		t.Fatal("a schedule without tenant identity must be refused")
	}
}

// TestScannerRefusesToStartWithoutAGate is the property that matters most here.
// A scanner without an admission authority would run cost-bearing backups for
// every due stack with nothing able to stop it.
func TestScannerRefusesToStartWithoutAGate(t *testing.T) {
	store := &fakeStore{}
	if _, err := NewScanner(ScannerConfig{Store: store, Enqueuer: &fakeEnqueuer{}}); err == nil {
		t.Fatal("a scanner without an admission authority must refuse to start")
	}
}

func TestScannerEnqueuesOnlyAdmittedStacks(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	var denied []Due

	admitted := map[string]bool{"stack-ok": true}
	scanner, err := NewScanner(ScannerConfig{
		Store:    &fakeStore{},
		Enqueuer: enqueuer,
		Admission: func(_ context.Context, req jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
			if admitted[req.StackID] {
				return jobs.BackupAdmissionDecision{QuotaBytes: 50 << 30, UsedBytes: 1 << 30}, nil
			}
			return jobs.BackupAdmissionDecision{Denied: true, QuotaBytes: 50 << 30, UsedBytes: 60 << 30}, nil
		},
		OnDenied: func(item Due, _ jobs.BackupAdmissionDecision) { denied = append(denied, item) },
		Now:      func() time.Time { return time.Date(2026, 9, 18, 3, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}

	// handle() is exercised directly: ListDue and MarkRan need Postgres, and
	// the decision path is what this test is about.
	ctx := context.Background()
	for _, item := range []Due{
		{TenantID: "tenant-a", StackID: "stack-ok", OwnerID: "owner-a", StackName: "photos"},
		{TenantID: "tenant-b", StackID: "stack-over", OwnerID: "owner-b"},
	} {
		_ = scanner.handle(ctx, item, scanner.cfg.Now())
	}

	if len(enqueuer.payloads) != 1 || enqueuer.payloads[0].StackID != "stack-ok" {
		t.Fatalf("only admitted stacks may be enqueued: %+v", enqueuer.payloads)
	}
	if enqueuer.payloads[0].QuotaBytes != 50<<30 || enqueuer.payloads[0].AdmittedUsedBytes != 1<<30 {
		t.Fatalf("the payload must carry what the gate saw: %+v", enqueuer.payloads[0])
	}
	if len(denied) != 1 || denied[0].StackID != "stack-over" {
		t.Fatalf("a denied stack must be reported, not silently skipped: %+v", denied)
	}
}

func TestScannerDeniesAStackWithNoSubject(t *testing.T) {
	enqueuer := &fakeEnqueuer{}
	scanner, err := NewScanner(ScannerConfig{
		Store:    &fakeStore{},
		Enqueuer: enqueuer,
		Admission: func(context.Context, jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
			t.Fatal("the gate must not be consulted for a stack with no subject")
			return jobs.BackupAdmissionDecision{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.handle(context.Background(), Due{TenantID: "tenant-a", StackID: "stack-a"}, time.Now()); err == nil {
		t.Fatal("a stack with no owner subject must not be backed up")
	}
	if len(enqueuer.payloads) != 0 {
		t.Fatalf("nothing may be enqueued without a gateable subject: %+v", enqueuer.payloads)
	}
}

func TestScannerSurfacesAnEnqueueFailure(t *testing.T) {
	enqueuer := &fakeEnqueuer{err: errors.New("queue down")}
	scanner, err := NewScanner(ScannerConfig{
		Store:    &fakeStore{},
		Enqueuer: enqueuer,
		Admission: func(context.Context, jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
			return jobs.BackupAdmissionDecision{QuotaBytes: 1 << 30}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := scanner.handle(context.Background(), Due{TenantID: "t", StackID: "s", OwnerID: "o"}, time.Now()); err == nil {
		t.Fatal("an enqueue failure must surface rather than be swallowed")
	}
}
