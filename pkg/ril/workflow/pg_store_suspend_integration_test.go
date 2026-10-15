//go:build managed_postgres

package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// One real managed-PostgreSQL regression: timer failure cannot strand a
// suspended workflow; a successful retry persists its timer with the state.
func TestManagedPostgresSuspendAndTimerAreAtomic(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	a, err := db.LoadManagedTestAssignment()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyPin(); err != nil {
		t.Fatal(err)
	}
	verifier, err := oidcclient.NewVerifier(oidcclient.VerifierConfig{Issuer: "https://login.kombify.io/", Audience: a.AuthAudience, JWKSURL: "https://login.kombify.io/.well-known/jwks.json", ClockSkew: 2 * time.Second, HTTPClient: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(ctx, os.Getenv("TECHSTACK_MANAGED_TEST_ID_TOKEN"))
	if err != nil {
		t.Fatal("actual Auth0 verification failed")
	}
	if claims.Subject != a.SubjectID || claims.Email != a.Email || claims.Raw["email_verified"] != true || a.TenantID != "usr:"+claims.Subject {
		t.Fatal("actual personal test identity differs")
	}
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.OpenContext(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := db.VerifyManagedTestSession(ctx, app.DB, *a, db.ManagedTestApp); err != nil {
		t.Fatal(err)
	}
	if err := app.VerifyMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := controlplane.NewPostgresStore(app.DB).GetMembership(ctx, a.TenantID, a.SubjectID)
	if err != nil || m == nil || m.Status != "active" || m.SubjectID != a.SubjectID {
		t.Fatal("actual active test membership required")
	}
	timerIDs := []string{}
	pool := NewPgStore(app.DB)
	store, err := pool.ForTenant(ctx, a.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{RunID: "managed-ril-atomic-" + uuid.NewString(), TenantID: a.TenantID, OwnerID: a.SubjectID, Type: TypeDriftCorrection, Input: map[string]any{"tenant_id": a.TenantID, "atomic_suspend_probe": true}}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.atomic-suspend-intent/v1", "run_id": run.RunID, "tenant_id": a.TenantID, "provider_effects": false})
	defer func() {
		clean, stop := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer stop()
		s, err := pool.ForTenant(clean, a.TenantID)
		if err != nil {
			t.Error(err)
			return
		}
		r, err := s.GetRun(run.RunID)
		if errors.Is(err, ErrNotFound) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		if r.TenantID != a.TenantID || r.OwnerID != a.SubjectID {
			t.Error("cleanup custody differs")
			return
		}
		if !IsTerminal(r.Status) {
			r.Status = RunCancelled
			r.AwaitingSignal = ""
			now := time.Now().UTC()
			r.FinishedAt = &now
			if err := s.UpdateRun(r); err != nil {
				t.Error(err)
				return
			}
		}
		for _, id := range timerIDs {
			tm, err := s.(*PgStore).GetTimer(id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil || tm.RunID != run.RunID {
				t.Error("owned timer custody unverified")
				return
			}
			if err := s.MarkTimerFired(id); err != nil {
				t.Error(err)
				return
			}
			actual, err := s.(*PgStore).GetTimer(id)
			if err != nil || !actual.Fired {
				t.Error("owned timer cleanup unverified")
				return
			}
		}
		r, err = s.GetRun(run.RunID)
		if err != nil || !IsTerminal(r.Status) {
			t.Error("owned probe cleanup unverified")
			return
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.atomic-suspend-terminal/v1", "run_id": r.RunID, "status": r.Status, "provider_effects": false})
	}()
	if err := store.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	run.Status = RunRunning
	if err := store.UpdateRun(run); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(observedSuspendStore{RunStore: pool, timerIDs: &timerIDs}, NewActivityRunner())
	scoped := WithTenant(ctx, a.TenantID)
	err = e.suspend(scoped, run, &SuspendDirective{SignalKey: run.RunID + ":signal", Timeout: time.Minute, TimerKind: TimerKind("invalid-check-kind")})
	var denied *pgconn.PgError
	if !errors.As(err, &denied) || denied.Code != "23514" {
		t.Fatal("real timer constraint failure not reached")
	}
	persisted, err := store.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.atomic-suspend-failure-readback/v1", "run_id": run.RunID, "run_status": persisted.Status, "awaiting_signal": persisted.AwaitingSignal != "", "timer_error_sqlstate": denied.Code, "provider_effects": false})
	if persisted.Status != RunRunning || persisted.AwaitingSignal != "" {
		t.Fatal("timer failure stranded the durable run")
	}
	run = persisted
	if err := e.suspend(scoped, run, &SuspendDirective{SignalKey: run.RunID + ":signal", Timeout: time.Minute, TimerKind: TimerReminder}); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	armed := false
	for _, id := range timerIDs {
		tm, err := store.(*PgStore).GetTimer(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if tm.RunID == run.RunID && tm.SignalKey == persisted.AwaitingSignal && !tm.Fired {
			armed = true
		}
	}
	if persisted.Status != RunSuspended || !armed {
		t.Fatal("successful suspend lost its matching durable timer")
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.atomic-suspend-success-readback/v1", "run_id": run.RunID, "run_status": persisted.Status, "matching_timer_armed": armed, "provider_effects": false})
}

// Observes identities from actual PgStore IO; no substituted store or failure.
type observedSuspendStore struct {
	RunStore
	timerIDs *[]string
}

func (s observedSuspendStore) ForTenant(ctx context.Context, tenant string) (RunStore, error) {
	actual, err := s.RunStore.(interface {
		ForTenant(context.Context, string) (RunStore, error)
	}).ForTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return observedSuspendStore{RunStore: actual, timerIDs: s.timerIDs}, nil
}
func (s observedSuspendStore) SuspendRun(run *Run, timer *Timer) error {
	err := s.RunStore.SuspendRun(run, timer)
	if timer != nil && timer.ID != "" {
		*s.timerIDs = append(*s.timerIDs, timer.ID)
	}
	return err
}
