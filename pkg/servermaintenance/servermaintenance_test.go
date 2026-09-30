package servermaintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type recordingDispatcher struct{ reboots int }

func (*recordingDispatcher) Plan(context.Context, Target) (PlanResult, error) {
	return PlanResult{}, nil
}

func (*recordingDispatcher) Apply(context.Context, Target, string) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (d *recordingDispatcher) Reboot(_ context.Context, _ Target, _ time.Duration) (RebootResult, error) {
	d.reboots++
	return RebootResult{BootIDBefore: "boot-a", ScheduledAt: time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)}, nil
}

func queuedReboot(t *testing.T, store *controlplane.MemoryStore, id string, deadline time.Time) controlplane.ServerMaintenanceJob {
	t.Helper()
	job, err := store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{
		ID: id, TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Action: controlplane.ServerMaintenanceActionReboot, RequestDigest: "digest-" + id,
		State: controlplane.ServerMaintenanceStateQueued, InventoryRevision: 7, DeadlineAt: &deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	return *job
}

// A job whose runner died mid-dispatch holds the fence only until its
// deadline: the runner then fails it with deadline_exceeded and the server
// admits a new reboot.
func TestRunnerExpiresAbandonedJobAndReleasesTheFence(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	queuedReboot(t, store, "reboot-1", now.Add(QueueTimeout))
	claimDeadline := now.Add(ExecutionTimeout(controlplane.ServerMaintenanceActionReboot))
	abandoned, err := store.UpdateServerMaintenanceJob(t.Context(), "tenant-1", "reboot-1", controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateRunning, DeadlineAt: &claimDeadline, At: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	at := claimDeadline.Add(-time.Second)
	runner := Runner{Jobs: store, Now: func() time.Time { return at }}
	if job, err := runner.Advance(t.Context(), *abandoned); err != nil || job.State != controlplane.ServerMaintenanceStateRunning {
		t.Fatalf("before the deadline: job=%+v err=%v, want still running", job, err)
	}
	at = claimDeadline
	job, err := runner.Advance(t.Context(), *abandoned)
	if err != nil || job.State != controlplane.ServerMaintenanceStateFailed || job.ReasonCode != ReasonDeadlineExceeded {
		t.Fatalf("at the deadline: job=%+v err=%v, want failed %s", job, err, ReasonDeadlineExceeded)
	}
	queuedReboot(t, store, "reboot-2", at.Add(QueueTimeout))
}

// The runner re-checks the node at dispatch: stack or service work that
// reached the agent keeps the reboot queued, and it dispatches once the node
// is free.
func TestRunnerDefersDispatchWhileTheNodeIsBusy(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{
		ID: "job-restart", TenantID: "tenant-1", Type: "restart", State: "pending",
		Payload: map[string]any{"agent_id": "agent-1"},
	}); err != nil {
		t.Fatal(err)
	}
	reboot := queuedReboot(t, store, "reboot-1", now.Add(QueueTimeout))
	dispatcher := &recordingDispatcher{}
	runner := Runner{Jobs: store, Dispatcher: dispatcher, Now: func() time.Time { return now }}
	if job, err := runner.Advance(t.Context(), reboot); err != nil || job.State != controlplane.ServerMaintenanceStateQueued || dispatcher.reboots != 0 {
		t.Fatalf("busy node: job=%+v err=%v reboots=%d, want queued and no dispatch", job, err, dispatcher.reboots)
	}
	if _, err := store.UpsertJob(t.Context(), controlplane.UpsertJobRequest{
		ID: "job-restart", TenantID: "tenant-1", Type: "restart", State: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if job, err := runner.Advance(t.Context(), reboot); err != nil || job.State != controlplane.ServerMaintenanceStateAwaitingNodeReturn || dispatcher.reboots != 1 {
		t.Fatalf("free node: job=%+v err=%v reboots=%d, want awaiting_node_return after one dispatch", job, err, dispatcher.reboots)
	}
}

// A server removed while it reboots can never return: the watcher fails the
// job instead of erroring forever, which frees the fence.
func TestRunnerFailsRebootWhenTheServerIsRemoved(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reboot := queuedReboot(t, store, "reboot-1", now.Add(QueueTimeout))
	runner := Runner{Jobs: store, Servers: store, Dispatcher: &recordingDispatcher{}, Now: func() time.Time { return now }}
	awaiting, err := runner.Advance(t.Context(), reboot)
	if err != nil || awaiting.State != controlplane.ServerMaintenanceStateAwaitingNodeReturn {
		t.Fatalf("dispatch: job=%+v err=%v", awaiting, err)
	}
	job, err := runner.Advance(t.Context(), *awaiting)
	if err != nil || job.State != controlplane.ServerMaintenanceStateFailed || job.ReasonCode != ReasonServerRemoved {
		t.Fatalf("removed server: job=%+v err=%v, want failed %s", job, err, ReasonServerRemoved)
	}
	queuedReboot(t, store, "reboot-2", now.Add(QueueTimeout))
}

// rebootServer stores server-1 as its agent last reported it.
func rebootServer(t *testing.T, store *controlplane.MemoryStore, bootID string, connected bool, changedAt time.Time) {
	t.Helper()
	state := serverregistry.ConnectionConnected
	if !connected {
		state = serverregistry.ConnectionOffline
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", StackID: "stack-1",
		LifecycleState: string(serverregistry.LifecycleActive), ConnectionState: string(state), ConnectionChangedAt: changedAt,
		Metadata: map[string]any{MetadataBootID: bootID},
	}); err != nil {
		t.Fatal(err)
	}
}

// A reboot completes only when the node comes back with a new boot id. A
// node that keeps its boot id past the guard horizon was not rebooted, and a
// node that went offline and stays away past the return window failed.
func TestRebootCompletesOnlyOnANewBootIDWithinTheWindow(t *testing.T) {
	scheduled := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC) // recordingDispatcher's schedule
	for _, tc := range []struct {
		name      string
		observe   func(t *testing.T, store *controlplane.MemoryStore)
		waitUntil time.Duration
		decideAt  time.Duration
		want      string
		reason    string
	}{
		{
			name: "new boot id",
			observe: func(t *testing.T, store *controlplane.MemoryStore) {
				rebootServer(t, store, "boot-b", true, scheduled.Add(2*time.Minute))
			},
			waitUntil: 3 * time.Minute, decideAt: 4 * time.Minute,
			want: controlplane.ServerMaintenanceStateCompleted, reason: ReasonNodeReturned,
		},
		{
			name:      "same boot id past the guard horizon",
			observe:   func(*testing.T, *controlplane.MemoryStore) {},
			waitUntil: 14 * time.Minute, decideAt: GuardHorizon + ReturnWindow + time.Second,
			want: controlplane.ServerMaintenanceStateFailed, reason: ReasonRebootAbandoned,
		},
		{
			name: "offline past the return window",
			observe: func(t *testing.T, store *controlplane.MemoryStore) {
				rebootServer(t, store, "boot-a", false, scheduled.Add(time.Minute))
			},
			waitUntil: 10 * time.Minute, decideAt: 11*time.Minute + time.Second,
			want: controlplane.ServerMaintenanceStateFailed, reason: ReasonNodeDidNotReturn,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			rebootServer(t, store, "boot-a", true, scheduled.Add(-time.Hour))
			now := scheduled.Add(-time.Minute)
			runner := Runner{Jobs: store, Servers: store, Events: store, Dispatcher: &recordingDispatcher{}, Now: func() time.Time { return now }}
			job, err := runner.Advance(t.Context(), queuedReboot(t, store, "reboot-1", now.Add(QueueTimeout)))
			if err != nil || job.State != controlplane.ServerMaintenanceStateAwaitingNodeReturn {
				t.Fatalf("dispatch: job=%+v err=%v, want awaiting_node_return", job, err)
			}
			if server, _ := store.GetServerRuntime(t.Context(), "tenant-1", "server-1"); server.LastOutcome == nil || server.LastOutcome.ReasonCode != EventRebootAcknowledged {
				t.Fatalf("timeline outcome = %+v, want %s", server.LastOutcome, EventRebootAcknowledged)
			}
			now = scheduled.Add(tc.waitUntil)
			if job, err = runner.Advance(t.Context(), *job); err != nil || job.State != controlplane.ServerMaintenanceStateAwaitingNodeReturn {
				t.Fatalf("old boot id inside the window: job=%+v err=%v, want still awaiting", job, err)
			}
			tc.observe(t, store)
			now = scheduled.Add(tc.decideAt)
			if job, err = runner.Advance(t.Context(), *job); err != nil || job.State != tc.want || job.ReasonCode != tc.reason {
				t.Fatalf("decision: job=%+v err=%v, want %s %s", job, err, tc.want, tc.reason)
			}
		})
	}
}

// scriptedApplyDispatcher answers applies and plans from queued scripts, one
// entry per call.
type scriptedApplyDispatcher struct {
	applies []func() (ApplyResult, error)
	plans   []func() (PlanResult, error)
	applied int
	planned int
}

func (d *scriptedApplyDispatcher) Plan(context.Context, Target) (PlanResult, error) {
	next := d.plans[0]
	d.plans = d.plans[1:]
	d.planned++
	return next()
}

func (d *scriptedApplyDispatcher) Apply(context.Context, Target, string) (ApplyResult, error) {
	next := d.applies[0]
	d.applies = d.applies[1:]
	d.applied++
	return next()
}

func (*scriptedApplyDispatcher) Reboot(context.Context, Target, time.Duration) (RebootResult, error) {
	return RebootResult{}, nil
}

// StackKits exit 4 (the CLI stopped waiting, the install unit keeps running)
// is not a failure. Read-only plans observe the unit without starting another
// install: busy and dpkg problems keep the job alive, failed probes do not
// extend its deadline, and a settled plan confirms completion.
func TestApplyStillRunningIsPolledNotFailed(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now.Add(-MaxApplyDuration + 25*time.Minute) })
	planDigest := "sha256:" + strings.Repeat("1", 64)
	job, err := store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{
		ID: "update-1", TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", OwnerSubjectID: "owner-1",
		Action: controlplane.ServerMaintenanceActionUpdate, RequestDigest: "digest", State: controlplane.ServerMaintenanceStateQueued,
		InventoryRevision: 7, PlanDigest: planDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	store.SetNow(func() time.Time { return now })
	dispatcher := &scriptedApplyDispatcher{
		applies: []func() (ApplyResult, error){
			func() (ApplyResult, error) {
				return ApplyResult{Outcome: "running", Unit: "kombify-host-update-111111111111-1", FailureCode: "apply_wait_expired"}, nil
			},
			func() (ApplyResult, error) {
				t.Fatal("polling an update dispatched another install")
				return ApplyResult{}, nil
			},
		},
		plans: []func() (PlanResult, error){
			func() (PlanResult, error) { return PlanResult{}, &Refusal{Code: RefusalPackageManagerBusy} },
			func() (PlanResult, error) { return PlanResult{}, &Refusal{Code: "stackkits_upgrade_required"} },
			func() (PlanResult, error) { return PlanResult{}, &Failure{Code: FailureHostProbe} },
			func() (PlanResult, error) {
				return PlanResult{PlanDigest: "sha256:" + strings.Repeat("2", 64), DpkgProblems: []string{"libc6 half-configured"}}, nil
			},
			func() (PlanResult, error) { return PlanResult{PlanDigest: "sha256:" + strings.Repeat("2", 64)}, nil },
		},
	}
	runner := Runner{Jobs: store, Dispatcher: dispatcher, Now: func() time.Time { return now }}
	step := func(want string) {
		t.Helper()
		if job, err = runner.Advance(t.Context(), *job); err != nil || job.State != want {
			t.Fatalf("job=%+v err=%v, want %s", job, err, want)
		}
		now = now.Add(ApplyPollInterval)
	}
	step(controlplane.ServerMaintenanceStateWaiting) // apply exit 4
	firstDeadline := *job.DeadlineAt
	now = now.Add(ApplyStillRunningExtension / 2)
	step(controlplane.ServerMaintenanceStateWaiting) // busy
	if !job.DeadlineAt.After(firstDeadline) {
		t.Fatalf("a busy poll did not move the deadline past %s", firstDeadline)
	}
	if !job.DeadlineAt.Equal(job.CreatedAt.Add(MaxApplyDuration)) {
		t.Fatalf("busy polling did not respect the maximum duration: job=%+v", job)
	}
	busyDeadline := *job.DeadlineAt
	step(controlplane.ServerMaintenanceStateWaiting) // old admitted update needs a newer observation release
	if !job.DeadlineAt.Equal(busyDeadline) {
		t.Fatalf("upgrade refusal extended the deadline: %s -> %s", busyDeadline, job.DeadlineAt)
	}
	if held, err := store.ActiveServerMaintenanceJob(t.Context(), job.TenantID, controlplane.ServerMaintenanceScope{AgentID: job.AgentID}); err != nil || held == nil || held.ID != job.ID {
		t.Fatalf("observation upgrade refusal released the node fence: held=%+v err=%v", held, err)
	}
	step(controlplane.ServerMaintenanceStateWaiting) // failed unit probe
	if !job.DeadlineAt.Equal(busyDeadline) {
		t.Fatalf("failed probe extended the deadline: %s -> %s", busyDeadline, job.DeadlineAt)
	}
	step(controlplane.ServerMaintenanceStateWaiting)   // dpkg still settling
	step(controlplane.ServerMaintenanceStateCompleted) // clean plan
}

// blockingRebootDispatcher blocks a reboot until its context ends, like an
// agent command in flight while the control plane shuts down, and then
// reports the command's outcome to a re-attaching runner.
type blockingRebootDispatcher struct {
	sent     chan Target
	reattach RebootResult
	attached []Target
}

func (*blockingRebootDispatcher) Plan(context.Context, Target) (PlanResult, error) {
	return PlanResult{}, nil
}

func (*blockingRebootDispatcher) Apply(context.Context, Target, string) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (d *blockingRebootDispatcher) Reboot(ctx context.Context, target Target, _ time.Duration) (RebootResult, error) {
	if target.Reattach {
		d.attached = append(d.attached, target)
		return d.reattach, nil
	}
	d.sent <- target
	<-ctx.Done()
	return RebootResult{}, ctx.Err()
}

// A shutdown while a reboot command is in flight must not fail the job: the
// command may be running on the node. The job stays running with its command
// id, and the next runner re-attaches to that command's outcome.
func TestLoopShutdownKeepsADispatchedRebootAndReattaches(t *testing.T) {
	store := controlplane.NewMemoryStore()
	scheduled := time.Now().UTC().Add(30 * time.Second)
	job := queuedReboot(t, store, "reboot-1", time.Now().UTC().Add(QueueTimeout))
	dispatcher := &blockingRebootDispatcher{sent: make(chan Target, 1), reattach: RebootResult{BootIDBefore: "boot-a", ScheduledAt: scheduled}}
	runner := Runner{Jobs: store, Dispatcher: dispatcher}
	loop := NewLoop(store, runner, time.Hour, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { loop.Run(ctx); close(done) }()
	sent := <-dispatcher.sent
	cancel()
	<-done
	stored, err := store.GetServerMaintenanceJob(t.Context(), "tenant-1", job.ID)
	if err != nil || stored.State != controlplane.ServerMaintenanceStateRunning || stored.Result[ResultCommandID] != sent.CommandID || sent.CommandID == "" {
		t.Fatalf("after shutdown job=%+v err=%v sent=%+v, want running with the sent command id", stored, err, sent)
	}
	next, err := runner.Advance(t.Context(), *stored)
	if err != nil || next.State != controlplane.ServerMaintenanceStateAwaitingNodeReturn ||
		len(dispatcher.attached) != 1 || dispatcher.attached[0].CommandID != sent.CommandID {
		t.Fatalf("re-attach job=%+v err=%v attached=%+v, want awaiting_node_return from the same command", next, err, dispatcher.attached)
	}
}

// A host code that is not a bounded snake_case token would violate the job
// table's reason check and leave the job running; it becomes
// host_command_failed.
func TestRunnerStoresOnlyBoundedHostCodes(t *testing.T) {
	store := controlplane.NewMemoryStore()
	job := queuedReboot(t, store, "reboot-1", time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC))
	runner := Runner{
		Jobs: store, Now: func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) },
		Dispatcher: refusingRebootDispatcher{code: strings.Repeat("Refused Because ", 20)},
	}
	next, err := runner.Advance(t.Context(), job)
	if err != nil || next.State != controlplane.ServerMaintenanceStateFailed || next.ReasonCode != ReasonHostCommandFailed {
		t.Fatalf("job=%+v err=%v, want failed with %s", next, err, ReasonHostCommandFailed)
	}
}

type refusingRebootDispatcher struct{ code string }

func (refusingRebootDispatcher) Plan(context.Context, Target) (PlanResult, error) {
	return PlanResult{}, nil
}

func (refusingRebootDispatcher) Apply(context.Context, Target, string) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (d refusingRebootDispatcher) Reboot(context.Context, Target, time.Duration) (RebootResult, error) {
	return RebootResult{}, &Refusal{Code: d.code}
}

// silentRebootDispatcher never reports an outcome for its command and records
// withdrawals.
type silentRebootDispatcher struct {
	withdrawal Withdrawal
	withdrawn  []string
}

func (*silentRebootDispatcher) Plan(context.Context, Target) (PlanResult, error) {
	return PlanResult{}, nil
}

func (*silentRebootDispatcher) Apply(context.Context, Target, string) (ApplyResult, error) {
	return ApplyResult{}, nil
}

func (*silentRebootDispatcher) Reboot(ctx context.Context, _ Target, _ time.Duration) (RebootResult, error) {
	<-ctx.Done()
	return RebootResult{}, ctx.Err()
}

func (d *silentRebootDispatcher) Withdraw(_ context.Context, target Target) (Withdrawal, error) {
	d.withdrawn = append(d.withdrawn, target.CommandID)
	return d.withdrawal, nil
}

// Expiry never releases a node past a command that could still run: a
// command still queued is withdrawn first, and one an agent already received
// moves the deadline once before the job fails.
func TestExpiryWithdrawsTheRecordedCommandBeforeReleasingTheNode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withdrawal Withdrawal
		extended   bool
	}{
		{name: "still queued", withdrawal: Withdrawal{Withdrawn: true}},
		{name: "received by the agent", withdrawal: Withdrawal{InFlight: true}, extended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
			store.SetNow(func() time.Time { return now })
			queuedReboot(t, store, "reboot-1", now.Add(QueueTimeout))
			claimed, err := store.ClaimServerMaintenanceJob(t.Context(), "tenant-1", "reboot-1", "cmd-reboot-1", now.Add(time.Minute), now)
			if err != nil {
				t.Fatal(err)
			}
			dispatcher := &silentRebootDispatcher{withdrawal: tc.withdrawal}
			at := now.Add(time.Minute)
			runner := Runner{Jobs: store, Dispatcher: dispatcher, Now: func() time.Time { return at }}
			job, err := runner.Advance(t.Context(), *claimed)
			if err != nil || len(dispatcher.withdrawn) != 1 || dispatcher.withdrawn[0] != "cmd-reboot-1" {
				t.Fatalf("job=%+v err=%v withdrawn=%v, want the recorded command withdrawn", job, err, dispatcher.withdrawn)
			}
			if !tc.extended {
				if job.State != controlplane.ServerMaintenanceStateFailed || job.ReasonCode != ReasonDeadlineExceeded {
					t.Fatalf("job=%+v, want failed %s", job, ReasonDeadlineExceeded)
				}
				return
			}
			if job.State != controlplane.ServerMaintenanceStateRunning || job.DeadlineAt == nil || !job.DeadlineAt.Equal(at.Add(InFlightExpiryGrace)) {
				t.Fatalf("job=%+v, want running with the deadline moved by the grace", job)
			}
			at = job.DeadlineAt.UTC()
			if job, err = runner.Advance(t.Context(), *job); err != nil || job.State != controlplane.ServerMaintenanceStateFailed {
				t.Fatalf("after the grace job=%+v err=%v, want failed", job, err)
			}
		})
	}
}

// After the install unit ends, plan_stale alone does not prove the approved
// plan was installed: a package still pending at its approved starting version
// makes the update partial. A host probe that fails while polling is not an
// install failure.
func TestApplyAfterWaitIsPartialWhenApprovedPackagesArePending(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	planDigest := "sha256:" + strings.Repeat("1", 64)
	if _, err := store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{
		ID: "plan-1", TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", OwnerSubjectID: "owner-1",
		Action: controlplane.ServerMaintenanceActionPlan, RequestDigest: "digest-plan", State: controlplane.ServerMaintenanceStateQueued, InventoryRevision: 7,
	}); err != nil {
		t.Fatal(err)
	}
	approved := PlanResult{PlanDigest: planDigest, PendingCount: 2, Packages: []PlanPackage{
		{Name: "curl", From: "8.5.0-1", To: "8.5.0-2"}, {Name: "openssl", From: "3.0.13-1", To: "3.0.13-2", Security: true},
	}}
	if _, err := store.UpdateServerMaintenanceJob(t.Context(), "tenant-1", "plan-1", controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateCompleted,
		PlanDigest: planDigest, Result: PlanResultDocument(approved), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{
		ID: "update-1", TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", OwnerSubjectID: "owner-1",
		Action: controlplane.ServerMaintenanceActionUpdate, RequestDigest: "digest", State: controlplane.ServerMaintenanceStateQueued,
		InventoryRevision: 7, PlanDigest: planDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &scriptedApplyDispatcher{
		applies: []func() (ApplyResult, error){
			func() (ApplyResult, error) {
				return ApplyResult{Outcome: "running", FailureCode: "apply_wait_expired"}, nil
			},
			func() (ApplyResult, error) {
				t.Fatal("partial-update observation dispatched another install")
				return ApplyResult{}, nil
			},
		},
		plans: []func() (PlanResult, error){
			func() (PlanResult, error) { return PlanResult{}, &Failure{Code: FailureHostProbe} },
			func() (PlanResult, error) {
				return PlanResult{PlanDigest: "sha256:" + strings.Repeat("2", 64), PendingCount: 1, Packages: []PlanPackage{
					{Name: "openssl", From: "3.0.13-1", To: "3.0.13-2", Security: true},
				}}, nil
			},
		},
	}
	runner := Runner{Jobs: store, Dispatcher: dispatcher, Now: func() time.Time { return now }}
	for _, want := range []string{controlplane.ServerMaintenanceStateWaiting, controlplane.ServerMaintenanceStateWaiting, controlplane.ServerMaintenanceStateFailed} {
		if job, err = runner.Advance(t.Context(), *job); err != nil || job.State != want {
			t.Fatalf("job=%+v err=%v, want %s", job, err, want)
		}
		now = now.Add(ApplyPollInterval)
	}
	notInstalled, _ := job.Result["not_installed"].([]any)
	if job.ReasonCode != ReasonApplyPartial || len(notInstalled) != 1 || notInstalled[0] != "openssl" {
		t.Fatalf("job=%+v, want %s naming openssl", job, ReasonApplyPartial)
	}
}
