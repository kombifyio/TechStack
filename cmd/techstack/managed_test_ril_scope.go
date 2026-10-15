package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Native module probe: real assigned app login and two actual Auth0 customers.
// No schema/grant SQL, fixture token, workflow activity or provider action.
func verifyManagedTestRILScope(ctx context.Context, app *db.DB, a db.ManagedTestAssignment, cancelAfterPersist bool) (probeErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type observedRun struct {
		TenantID  string             `json:"tenant_id"`
		SubjectID string             `json:"subject_id"`
		RunID     string             `json:"run_id"`
		Status    workflow.RunStatus `json:"status"`
	}
	observed := []observedRun{}
	peer, err := verifyManagedRILScopeAssignments(ctx, app, a)
	if err != nil {
		return err
	}
	pool := workflow.NewPgStore(app.DB)
	planned := []managedRILProbeRun{}
	for _, x := range []db.ManagedTestAssignment{a, *peer} {
		planned = append(planned, managedRILProbeRun{TenantID: x.TenantID, SubjectID: x.SubjectID, RunID: "managed-ril-scope-" + uuid.NewString(), TimerID: uuid.NewString()})
	}
	// A failed deadline must not strand runnable workflow data. Cleanup has its
	// own bounded context; external workers stay quiescent until this readback.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		readback := []managedRILProbeTerminal{}
		var cleanupErr error
		for _, p := range planned {
			actual, err := settleManagedRILProbe(cleanupCtx, pool, p)
			readback = append(readback, actual)
			cleanupErr = errors.Join(cleanupErr, err)
		}
		outputErr := json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-ril-scope-terminal/v1", "runs": readback, "workers_must_remain_quiescent": cleanupErr != nil, "provider_effects": false})
		probeErr = errors.Join(probeErr, cleanupErr, outputErr)
	}()
	// Record every identity before the first potentially committed write, so a
	// killed process has exact recovery custody even if no deferred code ran.
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-ril-scope-intent/v1", "runs": planned, "workers_must_remain_quiescent": true, "provider_effects": false}); err != nil {
		return err
	}
	for i, x := range []db.ManagedTestAssignment{a, *peer} {
		store, err := pool.ForTenant(ctx, x.TenantID)
		if err != nil {
			return err
		}
		run := &workflow.Run{RunID: planned[i].RunID, TenantID: x.TenantID, Type: workflow.TypeDriftCorrection, OwnerID: x.SubjectID, Input: map[string]any{"tenant_id": x.TenantID, "scope_probe": true}}
		if err := store.CreateRun(run); err != nil {
			return err
		}
		// Explicit native cancellation probe for the reproduced cleanup risk:
		// a real committed pending run must terminalize on an expired context.
		if cancelAfterPersist {
			cancel()
			return ctx.Err()
		}
		run.Status = workflow.RunRunning
		if err := store.UpdateRun(run); err != nil {
			return err
		}
		step := &workflow.Step{RunID: run.RunID, Name: "durable-scope-check", Status: workflow.StepPending, IdempotencyKey: run.RunID + ":0"}
		if err := store.CreateStep(step); err != nil {
			return err
		}
		step.Status = workflow.StepRunning
		if err := store.UpdateStep(step); err != nil {
			return err
		}
		step.Status = workflow.StepCompleted
		if err := store.UpdateStep(step); err != nil {
			return err
		}
		run.Status = workflow.RunSuspended
		run.AwaitingSignal = run.RunID + ":approval"
		if err := store.UpdateRun(run); err != nil {
			return err
		}
		timer := &workflow.Timer{ID: planned[i].TimerID, RunID: run.RunID, Kind: workflow.TimerReminder, FireAt: time.Now().Add(-time.Second), SignalKey: run.AwaitingSignal}
		if err := store.CreateTimer(timer); err != nil {
			return err
		}
		timers, err := store.ListDueTimers(time.Now(), 50)
		if err != nil {
			return err
		}
		found := false
		for _, t := range timers {
			if t.ID == timer.ID {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("RIL due timer did not read back")
		}
		if err := store.MarkTimerFired(timer.ID); err != nil {
			return err
		}
		run.Status = workflow.RunRunning
		run.AwaitingSignal = ""
		if err := store.UpdateRun(run); err != nil {
			return err
		}
		run.Status = workflow.RunCompleted
		run.CurrentStep = 1
		if err := store.UpdateRun(run); err != nil {
			return err
		}
		actual, err := store.GetRun(run.RunID)
		if err != nil || actual.TenantID != x.TenantID || actual.Status != workflow.RunCompleted {
			return fmt.Errorf("RIL scoped lifecycle did not read back")
		}
		events, err := store.(interface {
			ListAudit(string, string, int) ([]workflow.AuditEvent, error)
		}).ListAudit(x.TenantID, run.RunID, 50)
		if err != nil || len(events) == 0 {
			return fmt.Errorf("RIL tenant audit did not read back")
		}
		other := a.TenantID
		if other == x.TenantID {
			other = peer.TenantID
		}
		foreign, err := pool.ForTenant(ctx, other)
		if err != nil {
			return err
		}
		if _, err := foreign.GetRun(run.RunID); !errors.Is(err, workflow.ErrNotFound) {
			return fmt.Errorf("RIL foreign run was not isolated")
		}
		if steps, err := foreign.ListSteps(run.RunID); err != nil || len(steps) != 0 {
			return fmt.Errorf("RIL foreign steps were not isolated")
		}
		if err := foreign.MarkTimerFired(timer.ID); !errors.Is(err, workflow.ErrNotFound) {
			return fmt.Errorf("RIL foreign timer was not isolated")
		}
		if events, err := foreign.(interface {
			ListAudit(string, string, int) ([]workflow.AuditEvent, error)
		}).ListAudit(other, run.RunID, 50); err != nil || len(events) != 0 {
			return fmt.Errorf("RIL foreign audit was not isolated")
		}
		var denied *pgconn.PgError
		if err := foreign.CreateStep(&workflow.Step{RunID: run.RunID, Name: "foreign-denied", StepIndex: 1}); !errors.As(err, &denied) || denied.Code != "23503" {
			return fmt.Errorf("RIL foreign child creation was not refused by tenant custody")
		}
		observed = append(observed, observedRun{TenantID: x.TenantID, SubjectID: x.SubjectID, RunID: run.RunID, Status: actual.Status})
	}
	tenants, err := pool.ListWorkerTenants(ctx, "", 50)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range tenants {
		seen[id] = true
	}
	if !seen[a.TenantID] || !seen[peer.TenantID] {
		return fmt.Errorf("RIL protected worker tenant readback absent")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-ril-scope/v1", "runs": observed, "foreign_read_write": "denied", "worker_projection": "observed", "provider_effects": false})
}

// Recovery adopts only the previously emitted identities of this assigned
// native probe. It never creates or replays work and cannot target another run.
func recoverManagedTestRILScope(ctx context.Context, app *db.DB, a db.ManagedTestAssignment) error {
	peer, err := verifyManagedRILScopeAssignments(ctx, app, a)
	if err != nil {
		return err
	}
	f, err := os.Open(os.Getenv("TECHSTACK_MANAGED_TEST_RIL_PROBE_INTENT"))
	if err != nil {
		return fmt.Errorf("RIL recovery intent is unavailable")
	}
	defer f.Close()
	var intent struct {
		Schema                     string               `json:"schema"`
		Runs                       []managedRILProbeRun `json:"runs"`
		WorkersMustRemainQuiescent bool                 `json:"workers_must_remain_quiescent"`
		ProviderEffects            bool                 `json:"provider_effects"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return fmt.Errorf("RIL recovery intent is invalid")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || intent.Schema != "techstack.managed-ril-scope-intent/v1" || intent.ProviderEffects || !intent.WorkersMustRemainQuiescent {
		return fmt.Errorf("RIL recovery intent is not an owned quiescent probe")
	}
	expected := map[string]string{a.TenantID: a.SubjectID, peer.TenantID: peer.SubjectID}
	if len(intent.Runs) != len(expected) {
		return fmt.Errorf("RIL recovery assignments differ")
	}
	for _, p := range intent.Runs {
		subject, assigned := expected[p.TenantID]
		runSuffix, ok := strings.CutPrefix(p.RunID, "managed-ril-scope-")
		runUUID, runErr := uuid.Parse(runSuffix)
		timerUUID, timerErr := uuid.Parse(p.TimerID)
		if !assigned || subject != p.SubjectID || !ok || runErr != nil || timerErr != nil || runUUID.String() != runSuffix || timerUUID.String() != p.TimerID {
			return fmt.Errorf("RIL recovery custody differs")
		}
		delete(expected, p.TenantID)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	pool := workflow.NewPgStore(app.DB)
	readback := []managedRILProbeTerminal{}
	var cleanupErr error
	for _, p := range intent.Runs {
		actual, err := settleManagedRILProbe(cleanupCtx, pool, p)
		readback = append(readback, actual)
		cleanupErr = errors.Join(cleanupErr, err)
	}
	outputErr := json.NewEncoder(os.Stdout).Encode(map[string]any{"schema": "techstack.managed-ril-scope-terminal/v1", "runs": readback, "workers_must_remain_quiescent": cleanupErr != nil, "provider_effects": false})
	return errors.Join(cleanupErr, outputErr)
}

type managedRILProbeRun struct {
	TenantID  string `json:"tenant_id"`
	SubjectID string `json:"subject_id"`
	RunID     string `json:"run_id"`
	TimerID   string `json:"timer_id"`
}
type managedRILProbeTerminal struct {
	managedRILProbeRun
	RunState   string `json:"run_state"`
	TimerState string `json:"timer_state"`
	Terminal   bool   `json:"terminal"`
}

// Uses only the canonical scoped persistence interface, never schema SQL.
func settleManagedRILProbe(ctx context.Context, pool *workflow.PgStore, p managedRILProbeRun) (managedRILProbeTerminal, error) {
	result := managedRILProbeTerminal{managedRILProbeRun: p, RunState: "unverified", TimerState: "unverified"}
	s, err := pool.ForTenant(ctx, p.TenantID)
	if err != nil {
		return result, err
	}
	run, err := s.GetRun(p.RunID)
	if errors.Is(err, workflow.ErrNotFound) {
		result.RunState = "absent"
	} else if err != nil {
		return result, err
	} else {
		if run.TenantID != p.TenantID || run.OwnerID != p.SubjectID || run.Type != workflow.TypeDriftCorrection || run.Input["scope_probe"] != true {
			return result, fmt.Errorf("RIL probe recovery custody differs")
		}
		if !workflow.IsTerminal(run.Status) {
			previous := run.Status
			run.Status = workflow.RunCancelled
			if previous == workflow.RunCompensating {
				run.Status = workflow.RunFailed
			}
			run.AwaitingSignal = ""
			now := time.Now().UTC()
			run.FinishedAt = &now
			if err := s.UpdateRun(run); err != nil {
				return result, err
			}
		}
		run, err = s.GetRun(p.RunID)
		if err != nil {
			return result, err
		}
		result.RunState = string(run.Status)
		if !workflow.IsTerminal(run.Status) {
			return result, fmt.Errorf("RIL probe remains runnable")
		}
	}
	pg := s.(*workflow.PgStore)
	timer, err := pg.GetTimer(p.TimerID)
	if errors.Is(err, workflow.ErrNotFound) {
		result.TimerState = "absent"
	} else if err != nil {
		return result, err
	} else {
		if timer.RunID != p.RunID {
			return result, fmt.Errorf("RIL probe timer recovery custody differs")
		}
		if err := s.MarkTimerFired(p.TimerID); err != nil {
			return result, err
		}
		timer, err = pg.GetTimer(p.TimerID)
		if err != nil {
			return result, err
		}
		if !timer.Fired {
			return result, fmt.Errorf("RIL probe timer remains dispatchable")
		}
		result.TimerState = "fired"
	}
	result.Terminal = true
	return result, nil
}

func verifyManagedRILScopeAssignments(ctx context.Context, app *db.DB, a db.ManagedTestAssignment) (*db.ManagedTestAssignment, error) {
	peer, err := db.LoadManagedTestAssignmentFile(os.Getenv("TECHSTACK_MANAGED_TEST_PEER_ASSIGNMENT"))
	if err != nil {
		return nil, err
	}
	if err := peer.VerifyPin(); err != nil {
		return nil, err
	}
	if peer.ServiceID != a.ServiceID || peer.Database != a.Database || peer.SystemIdentifier != a.SystemIdentifier || peer.AuthAudience != a.AuthAudience || peer.TenantID == a.TenantID || peer.SubjectID == a.SubjectID {
		return nil, fmt.Errorf("RIL probe requires distinct actual tenants on the same assigned target")
	}
	auth := controlplane.NewPostgresStore(app.DB)
	for i, x := range []db.ManagedTestAssignment{a, *peer} {
		name := "TECHSTACK_MANAGED_TEST_ID_TOKEN"
		if i == 1 {
			name = "TECHSTACK_MANAGED_TEST_PEER_ID_TOKEN"
		}
		if _, err := verifyManagedTestIdentity(ctx, x, os.Getenv(name)); err != nil {
			return nil, err
		}
		m, err := auth.GetMembership(ctx, x.TenantID, x.SubjectID)
		if err != nil || m == nil || m.Status != "active" || m.SubjectID != x.SubjectID || m.UserID != x.SubjectID {
			return nil, fmt.Errorf("RIL probe requires independently admitted actual membership")
		}
	}
	return peer, nil
}
