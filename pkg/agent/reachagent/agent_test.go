package reachagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/guardreach"
)

// The Guard signs only what it can verify itself. A control plane (or anything
// in front of it) that asks for another audience, worker or server gets
// nothing signed, and a relay target off the relay's own host is never dialed.
func TestGuardSignsOnlyForItsOwnBindingAndDialsOnlyTheRelayHost(t *testing.T) {
	var mu sync.Mutex
	var bodies []guardreach.ExchangeRequest
	documents := make(chan guardreach.Document, 16)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request guardreach.ExchangeRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		bodies = append(bodies, request)
		mu.Unlock()
		var document guardreach.Document
		select {
		case document = <-documents:
		default:
			document = guardreach.Document{State: guardreach.StatePending}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"reach": document}})
	}))
	defer control.Close()

	manager, err := hostidentity.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	var dials int
	var dialMu sync.Mutex
	relayHost := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		dialMu.Lock()
		dials++
		dialMu.Unlock()
	}))
	defer relayHost.Close()
	agent, err := New(Config{
		ExchangeURL: control.URL, HTTPClient: control.Client(), Manager: manager,
		WorkerID: "worker-1", ServerID: "server-1", TenantID: "org_acme", ManageRelay: true, Version: "test",
		PendingInterval: 10 * time.Millisecond, ReadyInterval: 10 * time.Millisecond,
		AllowRelayURL: func(raw string) bool { return raw == "ws://allowed.invalid/relay" },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); agent.Run(ctx) }()

	ask := func(audience, worker, server string, generation int64) guardreach.ProofRequest {
		return guardreach.ProofRequest{Audience: audience, TenantID: "org_acme", WorkerID: worker, ServerID: server, Generation: generation}
	}
	documents <- guardreach.Document{State: guardreach.StatePending, ProofRequests: []guardreach.ProofRequest{
		ask("https://evil.example", "worker-1", "server-1", 1),
		ask(guardidentity.AudienceConnect, "worker-2", "server-1", 1),
		ask(guardidentity.AudienceConnect, "worker-1", "server-2", 1),
		ask(guardidentity.AudienceRelay, "worker-1", "server-1", 1),
	}}
	documents <- guardreach.Document{State: guardreach.StateReady, Generation: 1, KombifyMe: &guardreach.RelayTarget{
		State: "registered", RelayURL: "ws" + relayHost.URL[len("http"):], TenantID: "org_acme", ServerID: "server-1", WorkerID: "worker-1", Generation: 1,
	}}
	documents <- guardreach.Document{State: guardreach.StatePending, ProofRequests: []guardreach.ProofRequest{ask(guardidentity.AudienceConnect, "worker-1", "server-1", 2)}}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		submitted := 0
		for _, body := range bodies {
			submitted += len(body.Proofs)
		}
		mu.Unlock()
		if submitted >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	var proofs []guardreach.ProofSubmission
	for _, body := range bodies {
		proofs = append(proofs, body.Proofs...)
	}
	if len(proofs) != 1 || proofs[0].Audience != guardidentity.AudienceConnect || proofs[0].Generation != 2 {
		t.Fatalf("submitted proofs = %+v, want exactly the valid request for generation 2", proofs)
	}
	verified, err := guardidentity.VerifyHostProof(proofs[0].Proof, guardidentity.HostProofExpectation{
		Issuer: "worker-1", Subject: "server-1", TenantID: "org_acme", Audience: guardidentity.AudienceConnect, Generation: 2,
	})
	if err != nil || verified.Thumbprint != manager.Record().KeyThumbprint {
		t.Fatalf("proof = %+v, %v", verified, err)
	}
	dialMu.Lock()
	defer dialMu.Unlock()
	if dials != 0 {
		t.Fatalf("the Guard dialed a relay target off the relay host %d times", dials)
	}
}

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 10*time.Second)
}
