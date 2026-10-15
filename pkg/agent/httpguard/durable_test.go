package httpguard

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

const durableTestCommandID = "command-1"

type countingExecutor struct{ runs atomic.Int32 }

func (e *countingExecutor) Execute(_ context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	e.runs.Add(1)
	return &agentpb.StackKitResult{CommandId: command.GetCommandId(), Success: true, CommandResultJson: []byte(`{"status":"success"}`)}
}

// fakeControlPlane is the smallest control plane that speaks the binding
// contract: it offers scripted commands and records every poll and report.
type fakeControlPlane struct {
	mu          sync.Mutex
	offers      []map[string]any
	reports     []guardbinding.ResultReport
	polls       [][]string
	failResults int
}

func (f *fakeControlPlane) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/commands/next", func(w http.ResponseWriter, r *http.Request) {
		var request durableRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls = append(f.polls, request.KnownCommands)
		if len(f.offers) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		offer := f.offers[0]
		f.offers = f.offers[1:]
		_ = json.NewEncoder(w).Encode(map[string]any{"data": offer})
	})
	mux.HandleFunc("/commands/result", func(w http.ResponseWriter, r *http.Request) {
		var request durableRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		f.mu.Lock()
		defer f.mu.Unlock()
		if request.Report != nil {
			f.reports = append(f.reports, *request.Report)
		}
		if f.failResults > 0 {
			f.failResults--
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": guardbinding.Acknowledgement{
			CommandID: request.CommandID, Revision: request.Report.Revision, Digest: request.Report.Digest, Accepted: true, Acknowledged: true,
		}})
	})
	mux.HandleFunc("/runtime/logs", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	return mux
}

func (f *fakeControlPlane) reportStates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	states := make([]string, 0, len(f.reports))
	for _, report := range f.reports {
		states = append(states, report.State)
	}
	return states
}

func applyCommand(id string) *agentpb.StackKitCommand {
	return &agentpb.StackKitCommand{CommandId: id, Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY, WorkingDirectory: "/srv/stack"}
}

func offerFor(t *testing.T, command *agentpb.StackKitCommand, mutate func(*guardbinding.Binding)) map[string]any {
	t.Helper()
	digest, err := guardbinding.DigestOf(command)
	if err != nil {
		t.Fatal(err)
	}
	binding := guardbinding.Binding{
		CommandID: command.CommandId, TenantID: "tenant-1", WorkerID: "worker-1", Operation: command.Operation.String(),
		PayloadDigest: digest, WorkerGeneration: 2, CurrentGeneration: 2,
		AuthorityExpiresAt: time.Now().Add(10 * time.Minute), ContractVersion: guardbinding.ContractVersion,
	}
	if mutate != nil {
		mutate(&binding)
	}
	return map[string]any{"command": command, "binding": binding}
}

func newDurableTestClient(t *testing.T, control *fakeControlPlane, executor CommandExecutor, dir string) *Client {
	t.Helper()
	server := httptest.NewServer(control.handler())
	t.Cleanup(server.Close)
	client, err := New(Config{
		HeartbeatURL: server.URL + "/heartbeat", InventoryURL: server.URL + "/inventory",
		CommandURL: server.URL + "/commands/next", CommandResultURL: server.URL + "/commands/result",
		AgentToken: "token", RuntimeAgentID: "worker-1", TenantID: "tenant-1",
		Collector: staticCollector{}, CommandExecutor: executor, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.EnableDurableOperations(DurableOptions{Dir: dir})
	return client
}

func runControlLoop(t *testing.T, client *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); client.runControlLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if store := client.durable.readable(); store != nil {
			_ = store.Close()
		}
	})
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A lost acknowledgement converges by redelivering the identical immutable
// result; the command is never executed again.
func TestLostAcknowledgementRedeliversTheSameResultWithoutRerunning(t *testing.T) {
	executor := &countingExecutor{}
	control := &fakeControlPlane{failResults: 1}
	control.offers = []map[string]any{offerFor(t, applyCommand(durableTestCommandID), nil)}
	client := newDurableTestClient(t, control, executor, t.TempDir())
	runControlLoop(t, client)

	waitFor(t, "an acknowledged result", func() bool {
		entry, ok := client.durable.readable().Entry(durableTestCommandID)
		return ok && entry.State == journal.StateAcked
	})
	control.mu.Lock()
	defer control.mu.Unlock()
	if executor.runs.Load() != 1 {
		t.Fatalf("executor ran %d times, want 1", executor.runs.Load())
	}
	if len(control.reports) != 2 || control.reports[0] != control.reports[1] || control.reports[0].State != guardbinding.ReportTerminal {
		t.Fatalf("redelivery was not the identical revision: %+v", control.reports)
	}
}

// A command whose dispatched marker is durable but whose result is not is an
// outcome_unknown after a restart: it is reported and never run again, even
// when the control plane offers it again.
func TestRestartAfterDispatchMarkerReportsUnknownAndNeverRerunsTheCommand(t *testing.T) {
	dir := t.TempDir()
	command := applyCommand(durableTestCommandID)
	offer := offerFor(t, command, nil)
	binding := offer["binding"].(guardbinding.Binding)
	previous, err := journal.Open(dir, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	intent := journal.Intent{
		CommandID: command.CommandId, TenantID: binding.TenantID, WorkerID: binding.WorkerID, Operation: binding.Operation,
		Mutating: true, Generation: 2, PayloadDigest: binding.PayloadDigest, AuthorityExpiresAt: binding.AuthorityExpiresAt,
		ContractVersion: binding.ContractVersion, ResourceKey: "tenant-1/worker-1",
	}
	if _, _, err := previous.Admit(intent); err != nil {
		t.Fatal(err)
	}
	if err := previous.MarkDispatched(command.CommandId); err != nil {
		t.Fatal(err)
	}
	_ = previous.Close() // the crash

	executor := &countingExecutor{}
	control := &fakeControlPlane{offers: []map[string]any{offer}}
	client := newDurableTestClient(t, control, executor, dir)
	runControlLoop(t, client)

	waitFor(t, "unknown then escalated resolution", func() bool {
		states := control.reportStates()
		return len(states) >= 2 && states[0] == guardbinding.ReportOutcomeUnknown && states[1] == guardbinding.ReportResolved
	})
	if executor.runs.Load() != 0 {
		t.Fatal("an interrupted command was executed again")
	}
	entry, _ := client.durable.readable().Entry(command.CommandId)
	if entry.State != journal.StateOutcomeUnknown {
		t.Fatalf("an unresolved mutating outcome must keep fencing, got %s", entry.State)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.reports[1].PriorDigest != control.reports[0].Digest || control.reports[1].Resolution != guardbinding.ResolutionEscalated {
		t.Fatalf("resolution does not reference the evidence it supersedes: %+v", control.reports)
	}
	sawKnown := false
	for _, known := range control.polls {
		for _, id := range known {
			sawKnown = sawKnown || id == command.CommandId
		}
	}
	if !sawKnown {
		t.Fatal("the Guard did not tell the control plane it already holds the command")
	}
}

// An altered or mis-addressed command is refused before any effect and leaves
// nothing in the journal.
func TestAlteredCommandIsRejectedBeforeAnyEffect(t *testing.T) {
	executor := &countingExecutor{}
	control := &fakeControlPlane{}
	altered := offerFor(t, applyCommand(durableTestCommandID), func(b *guardbinding.Binding) {
		b.PayloadDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	})
	control.offers = []map[string]any{altered}
	client := newDurableTestClient(t, control, executor, t.TempDir())
	runControlLoop(t, client)

	waitFor(t, "a rejection report", func() bool { return len(control.reportStates()) == 1 })
	if executor.runs.Load() != 0 {
		t.Fatal("an altered command was executed")
	}
	if got := control.reportStates()[0]; got != guardbinding.ReportRejected {
		t.Fatalf("report state = %s", got)
	}
	if entries := client.durable.readable().Entries(nil); len(entries) != 0 {
		t.Fatalf("a rejected command left %d journal entries", len(entries))
	}
}

// An offer without a control-plane binding admits observations only: an
// unbound mutation is rejected before any effect, so a forged offer on the
// clear-text private channel cannot change the host.
func TestUnboundOfferAdmitsObservationsOnly(t *testing.T) {
	executor := &countingExecutor{}
	control := &fakeControlPlane{}
	verify := &agentpb.StackKitCommand{CommandId: "cmd-verify", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, WorkingDirectory: "/srv/stack"}
	control.offers = []map[string]any{{"command": applyCommand(durableTestCommandID)}, {"command": verify}}
	client := newDurableTestClient(t, control, executor, t.TempDir())
	runControlLoop(t, client)

	waitFor(t, "the rejection and the observation", func() bool {
		return len(control.reportStates()) == 1 && executor.runs.Load() == 1
	})
	if got := control.reportStates()[0]; got != guardbinding.ReportRejected {
		t.Fatalf("unbound mutation report state = %s", got)
	}
	time.Sleep(200 * time.Millisecond)
	if executor.runs.Load() != 1 {
		t.Fatalf("executor ran %d commands; only the unbound observation may run", executor.runs.Load())
	}
}

// An operator resolution of an escalated outcome releases the Guard's own
// fence, but only when it supersedes exactly the evidence the journal holds and
// carries a disposition only an operator can commit.
func TestOperatorResolutionReleasesTheLocalFenceOnlyWhenItSupersedesTheHeldEvidence(t *testing.T) {
	dir := t.TempDir()
	command := applyCommand(durableTestCommandID)
	offer := offerFor(t, command, nil)
	binding := offer["binding"].(guardbinding.Binding)
	previous, err := journal.Open(dir, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := previous.Admit(journal.Intent{
		CommandID: command.CommandId, TenantID: binding.TenantID, WorkerID: binding.WorkerID, Operation: binding.Operation,
		Mutating: true, Generation: 2, PayloadDigest: binding.PayloadDigest, AuthorityExpiresAt: binding.AuthorityExpiresAt,
		ContractVersion: binding.ContractVersion, ResourceKey: "tenant-1/worker-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := previous.MarkDispatched(command.CommandId); err != nil {
		t.Fatal(err)
	}
	_ = previous.Close()

	control := &fakeControlPlane{}
	client := newDurableTestClient(t, control, &countingExecutor{}, dir)
	runControlLoop(t, client)
	waitFor(t, "an escalated outcome", func() bool {
		entry, ok := client.durable.readable().Entry(command.CommandId)
		latest, has := entry.Latest()
		return ok && has && latest.Revision == 2 && entry.State == journal.StateOutcomeUnknown
	})
	entry, _ := client.durable.readable().Entry(command.CommandId)
	held, _ := entry.Latest()

	notice := func(mutate func(*guardbinding.OperatorResolutionNotice)) guardbinding.OperatorResolutionNotice {
		n := guardbinding.OperatorResolutionNotice{
			CommandID: command.CommandId, Revision: 3, PriorDigest: held.Digest,
			Resolution: guardbinding.DispositionVerifiedNotApplied, Reason: "checked on the host", ActorID: "operator-1",
		}
		if mutate != nil {
			mutate(&n)
		}
		report := guardbinding.ResultReport{Revision: n.Revision, State: guardbinding.ReportResolved, PriorDigest: n.PriorDigest, Reason: n.Reason, Resolution: n.Resolution}
		n.Digest, _ = report.ComputeDigest(n.CommandID, nil)
		return n
	}
	forgeries := []guardbinding.OperatorResolutionNotice{
		notice(func(n *guardbinding.OperatorResolutionNotice) { n.PriorDigest = "0000" }),
		notice(func(n *guardbinding.OperatorResolutionNotice) { n.Resolution = guardbinding.ResolutionNotApplied }),
		notice(func(n *guardbinding.OperatorResolutionNotice) { n.Revision = 9 }),
	}
	client.durable.applyOperatorResolutions(append(forgeries, func() guardbinding.OperatorResolutionNotice {
		n := notice(nil)
		n.Digest = "ffff"
		return n
	}()))
	if entry, _ = client.durable.readable().Entry(command.CommandId); entry.State != journal.StateOutcomeUnknown {
		t.Fatalf("an invalid notice released the fence: %s", entry.State)
	}

	client.durable.applyOperatorResolutions([]guardbinding.OperatorResolutionNotice{notice(nil)})
	if entry, _ = client.durable.readable().Entry(command.CommandId); entry.State != journal.StateAcked {
		t.Fatalf("the operator resolution did not release the fence: %s", entry.State)
	}
	if fenced := client.durable.readable().Fenced("tenant-1/worker-1", "other"); len(fenced) != 0 {
		t.Fatalf("still fenced by %v", fenced)
	}
}

// Once this worker's key is pinned, only a binding it signed runs: an unbound
// observation, an unsigned binding, or one signed by any other key, such as
// another worker's, is refused before any effect.
func TestPinnedGuardRunsOnlyBindingsTheControlPlaneSigned(t *testing.T) {
	signer := func(seed byte) *guardbinding.Signer {
		s, err := guardbinding.NewSignerFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	controlPlane, stranger := signer(1), signer(2)
	signedBy := func(s *guardbinding.Signer) func(*guardbinding.Binding) {
		return func(b *guardbinding.Binding) {
			if err := s.Sign(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	executor := &countingExecutor{}
	control := &fakeControlPlane{}
	verify := &agentpb.StackKitCommand{CommandId: "cmd-unbound-verify", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, WorkingDirectory: "/srv/stack"}
	control.offers = []map[string]any{
		{"command": verify},
		offerFor(t, applyCommand("cmd-unsigned"), nil),
		offerFor(t, applyCommand("cmd-stranger"), signedBy(stranger)),
		offerFor(t, applyCommand("cmd-signed"), signedBy(controlPlane)),
	}
	client := newDurableTestClient(t, control, executor, t.TempDir())
	client.cfg.BindingSigningKeys = controlPlane.PublicKeys()
	runControlLoop(t, client)

	waitFor(t, "three refusals and one run", func() bool {
		return len(control.reportStates()) == 4 && executor.runs.Load() == 1
	})
	rejected := 0
	for _, state := range control.reportStates() {
		if state == guardbinding.ReportRejected {
			rejected++
		}
	}
	if rejected != 3 {
		t.Fatalf("report states = %v, want the unbound, the unsigned and the foreign offer refused", control.reportStates())
	}
}
