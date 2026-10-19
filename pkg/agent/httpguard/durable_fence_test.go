package httpguard

import (
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// A revocation observed after a command was admitted denies its effect: the
// executor never runs and the refusal is reported.
func TestRevocationObservedAfterAdmissionDeniesTheEffect(t *testing.T) {
	executor := &countingExecutor{}
	control := &fakeControlPlane{offers: []map[string]any{offerFor(t, applyCommand(durableTestCommandID), nil)}}
	client := newDurableTestClient(t, control, executor, t.TempDir())
	// The revocation becomes observable once the intent is durable, between
	// admission and the effect.
	client.cfg.AuthorityRevoked = func() bool {
		store := client.durable.readable()
		if store == nil {
			return false
		}
		_, admitted := store.Entry(durableTestCommandID)
		return admitted
	}
	runControlLoop(t, client)

	waitFor(t, "an acknowledged refusal", func() bool {
		entry, ok := client.durable.readable().Entry(durableTestCommandID)
		return ok && entry.State == journal.StateAcked
	})
	if executor.runs.Load() != 0 {
		t.Fatalf("executor ran %d times after the revocation was observed", executor.runs.Load())
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if len(control.reports) == 0 || control.reports[0].State != guardbinding.ReportRejected || !strings.HasPrefix(control.reports[0].Reason, guardbinding.CodeAuthorityRevoked) {
		t.Fatalf("revoked effect was not refused: %+v", control.reports)
	}
}

// An unsettled mutation admitted under a predecessor binding (another tenant
// and worker on this server) still fences new mutations after a rebind.
func TestPredecessorBindingUnsettledMutationFencesNewWork(t *testing.T) {
	dir := t.TempDir()
	previous, err := journal.Open(dir, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := previous.Admit(journal.Intent{
		CommandID: "predecessor-1", TenantID: "tenant-0", WorkerID: "worker-0", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY.String(),
		Mutating: true, Generation: 1, PayloadDigest: "digest", AuthorityExpiresAt: time.Now().Add(time.Minute),
		ContractVersion: guardbinding.ContractVersion, ResourceKey: "tenant-0/worker-0",
	}); err != nil {
		t.Fatal(err)
	}
	if err := previous.MarkDispatched("predecessor-1"); err != nil {
		t.Fatal(err)
	}
	_ = previous.Close() // the effect was in flight when the server was rebound

	executor := &countingExecutor{}
	control := &fakeControlPlane{offers: []map[string]any{offerFor(t, applyCommand(durableTestCommandID), nil)}}
	client := newDurableTestClient(t, control, executor, dir)
	runControlLoop(t, client)

	waitFor(t, "the new command to settle", func() bool {
		entry, ok := client.durable.readable().Entry(durableTestCommandID)
		return ok && entry.State == journal.StateAcked
	})
	if executor.runs.Load() != 0 {
		t.Fatalf("a mutation ran while a predecessor effect was unsettled")
	}
}

// After a rebind, the unacknowledged result of a command admitted under the
// predecessor tenant and worker is neither listed nor reported under the new
// identity: its evidence stays in the journal, unacknowledged, for a governed
// recovery or handoff, while the new binding's own results still converge.
func TestPredecessorBindingResultIsHeldNotReportedUnderTheNewIdentity(t *testing.T) {
	dir := t.TempDir()
	previous, err := journal.Open(dir, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := previous.Admit(journal.Intent{
		CommandID: "predecessor-1", TenantID: "tenant-0", WorkerID: "worker-0", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY.String(),
		Mutating: true, Generation: 1, PayloadDigest: "digest", AuthorityExpiresAt: time.Now().Add(time.Minute),
		ContractVersion: guardbinding.ContractVersion, ResourceKey: "tenant-0/worker-0",
	}); err != nil {
		t.Fatal(err)
	}
	if err := previous.MarkDispatched("predecessor-1"); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"command_id":"predecessor-1","success":true}`)
	predecessorDigest, err := guardbinding.CanonicalDigest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := previous.RecordResult("predecessor-1", journal.ResultRecord{Revision: 1, State: journal.ReportTerminal, Digest: predecessorDigest, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	_ = previous.Close() // rebound before the result was acknowledged

	executor := &countingExecutor{}
	control := &fakeControlPlane{offers: []map[string]any{offerFor(t, applyCommand(durableTestCommandID), nil)}}
	client := newDurableTestClient(t, control, executor, dir)
	runControlLoop(t, client)

	// Redelivery walks the journal oldest first, so the predecessor entry was
	// considered before the new command's result was delivered.
	waitFor(t, "the new binding's result to be acknowledged", func() bool {
		entry, ok := client.durable.readable().Entry(durableTestCommandID)
		return ok && entry.State == journal.StateAcked
	})
	held, ok := client.durable.readable().Entry("predecessor-1")
	if !ok || held.State != journal.StateResultPending || held.AckedRevision != 0 || held.Rejected {
		t.Fatalf("predecessor evidence was not held unacknowledged: %+v", held)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	for _, report := range control.reports {
		if report.Digest == predecessorDigest {
			t.Fatalf("predecessor result was reported under the new identity")
		}
	}
	for _, known := range control.polls {
		for _, id := range known {
			if id == "predecessor-1" {
				t.Fatalf("predecessor command was listed under the new identity")
			}
		}
	}
}
