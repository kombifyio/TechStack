package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/reachagent"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifyme"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// reachRelay is a kombify.me relay stand-in that verifies the Guard's host
// proof the way the real relay does and can revoke its binding.
type reachRelay struct {
	t          *testing.T
	thumbprint func() string
	tenant     string
	worker     string
	server     string
	revoked    atomic.Bool
	admitted   atomic.Int32
	attempts   atomic.Int32
	mu         sync.Mutex
	conn       *websocket.Conn
	generation atomic.Int64
}

func (r *reachRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.attempts.Add(1)
	deny := func(status int, reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error_code": "guard_relay_denied", "reason_code": reason, "retryable": false})
	}
	header := strings.TrimPrefix(req.Header.Get("Authorization"), "KombifyGuard ")
	if header == req.Header.Get("Authorization") || req.Header.Get("X-Kombify-API-Key") != "" {
		deny(http.StatusUnauthorized, "host_proof_required")
		return
	}
	verified, err := guardidentity.VerifyHostProof(header, guardidentity.HostProofExpectation{
		Issuer: r.worker, Subject: r.server, TenantID: r.tenant, Audience: guardidentity.AudienceRelay, Generation: r.generation.Load(),
	})
	if err != nil || verified.Thumbprint != r.thumbprint() {
		deny(http.StatusUnauthorized, "binding_unknown")
		return
	}
	if r.revoked.Load() {
		deny(http.StatusForbidden, "binding_revoked")
		return
	}
	conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
	if err != nil {
		return
	}
	r.admitted.Add(1)
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (r *reachRelay) revoke() {
	r.revoked.Store(true)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		_ = r.conn.Close()
	}
}

type reachMe struct {
	relay      *reachRelay
	mu         sync.Mutex
	registered []guardreach.MeRegistration
	revoked    int
}

func (m *reachMe) EnsureRoute(_ context.Context, _ kombifyme.ManagedAddress, service, _, _ string) (guardreach.Route, error) {
	return guardreach.Route{Name: "sh-x-" + service, FQDN: "sh-x-" + service + ".kombify.me"}, nil
}

func (m *reachMe) RegisterServer(_ context.Context, in guardreach.MeRegistration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered = append(m.registered, in)
	m.relay.generation.Store(in.Generation)
	return nil
}

func (m *reachMe) RevokeServer(context.Context, string, string, string) error {
	m.mu.Lock()
	m.revoked++
	m.mu.Unlock()
	m.relay.revoke()
	return nil
}

type reachConnect struct {
	relay    *reachRelay
	verified atomic.Int32
	revoked  atomic.Int32
}

func (c *reachConnect) PutNode(_ context.Context, in guardreach.NodeUpsert) (*guardreach.Node, error) {
	verified, err := guardidentity.VerifyHostProof(in.HostProof, guardidentity.HostProofExpectation{
		Issuer: c.relay.worker, Subject: c.relay.server, TenantID: in.ConnectTenantID, Audience: guardidentity.AudienceConnect, Generation: c.relay.generation.Load(),
	})
	if err != nil || verified.Thumbprint != in.KeyThumbprint {
		return nil, &guardreach.GatewayError{Status: http.StatusForbidden, ReasonCode: "host_proof_signature_invalid"}
	}
	c.verified.Add(1)
	return &guardreach.Node{NodeID: "tsn_e2e", Status: "active", Created: true}, nil
}

func (c *reachConnect) RevokeNode(context.Context, string, string, []string, string, string) (*guardreach.Node, error) {
	c.revoked.Add(1)
	return &guardreach.Node{NodeID: "tsn_e2e", Status: "revoked", RevokedNow: true}, nil
}

func (c *reachConnect) Revocations(_ context.Context, after int64, _ int) (*guardreach.RevocationPage, error) {
	return &guardreach.RevocationPage{NextCursor: after}, nil
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A real Guard pairs against the real routes; the control plane registers it
// at kombify.me and Connect once its key is bound; the Guard connects its
// relay with a fresh host proof and no static key; and revoking it everywhere
// closes the relay and keeps it closed.
func TestGuardReachFromPairingToRevokeEverywhere(t *testing.T) {
	const tenant = "org_acme"
	store := controlplane.NewMemoryStore()
	raw, hash, err := pairingtoken.Generate(tenant)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID: "reach-pairing", TenantID: tenant, OwnerSubjectID: "auth0|owner", StackID: "stack-1",
		TokenHash: hash, Status: "active", ExpiresAt: &expires,
	}); err != nil {
		t.Fatal(err)
	}

	relay := &reachRelay{t: t, tenant: tenant}
	// The first binding generation; kombify.me registers it only once the owner
	// publishes the server, while Connect already verifies proofs for it.
	relay.generation.Store(1)
	relayServer := httptest.NewServer(relay)
	defer relayServer.Close()
	me, connect := &reachMe{relay: relay}, &reachConnect{relay: relay}
	reachStore := guardreach.NewMemoryStore()
	service, err := guardreach.New(guardreach.Config{
		Store: reachStore, Me: me, Connect: connect, Identities: guardreach.ControlPlane{Workers: store, Identities: store},
		Access: store, RelayURL: "ws" + strings.TrimPrefix(relayServer.URL, "http"),
	})
	if err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, Reach: service})
	server := httptest.NewServer(router)
	defer server.Close()

	manager, err := hostidentity.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	relay.thumbprint = func() string { return manager.Record().KeyThumbprint }
	enrollmentPath := filepath.Join(t.TempDir(), "agent-enrollment.json")
	if _, err := hostidentity.Enroll(t.Context(), hostidentity.EnrollOptions{
		Manager: manager, EnrollmentPath: enrollmentPath, ResiduePaths: []string{},
		Bootstrap: hostidentity.Bootstrap{Server: server.URL, Token: raw, Hostname: "node-1", OS: "linux", Arch: "x86_64"},
	}); err != nil {
		t.Fatal(err)
	}
	enrollment, _ := os.ReadFile(enrollmentPath)
	view, _ := hostidentity.ParseEnrollment(enrollment)
	var serverID struct {
		ServerID string `json:"server_id"`
		Data     struct {
			ServerID string `json:"server_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(enrollment, &serverID)
	relay.worker, relay.server = view.RuntimeAgentID, firstNonEmpty(serverID.Data.ServerID, serverID.ServerID)
	if relay.server == "" {
		t.Fatalf("enrollment carries no server id: %s", enrollment)
	}
	identity, err := hostidentity.NewClient(hostidentity.ClientConfig{
		Manager: manager, Endpoints: view.Endpoints, AgentToken: view.AgentToken, TenantID: view.TenantID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := identity.Establish(t.Context()); err != nil || mode != hostidentity.ModeHostKey {
		t.Fatalf("establish = %v, %v", mode, err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	guard, err := reachagent.New(reachagent.Config{
		ExchangeURL: reachagent.ExchangeURLFromHeartbeat(view.HeartbeatURL), HTTPClient: &http.Client{Transport: identity.Transport(nil)},
		Manager: manager, WorkerID: relay.worker, ServerID: relay.server, TenantID: view.TenantID,
		ManageRelay: true, Version: "test", AllowRelayURL: func(string) bool { return true },
		PendingInterval: 20 * time.Millisecond, ReadyInterval: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { defer close(finished); guard.Run(ctx) }()

	// Pairing confirmed the key: the row exists, and the reconciler registers
	// kombify.me; the Guard then hands over its Connect proof and connects.
	eventually(t, "the Guard to be registered and connected", func() bool {
		_ = service.ReconcileOnce(t.Context())
		rows, _ := reachStore.ListByTenant(t.Context(), tenant)
		if len(rows) != 1 {
			return false
		}
		// Nothing is published until the owner enables it; this test is that owner.
		_, _ = service.SetPublic(t.Context(), tenant, relay.worker, "auth0|owner", guardreach.PublicReach{Enabled: true, Service: "web", Port: 8080})
		shown := guardreach.ViewOf(rows[0])
		return shown.State == guardreach.StateReady && shown.Relay != nil && shown.Relay.State == "connected" && relay.admitted.Load() >= 1
	})
	if connect.verified.Load() != 1 {
		t.Fatalf("Connect verified %d host proofs, want 1", connect.verified.Load())
	}
	rows, _ := reachStore.ListByTenant(t.Context(), tenant)
	if rows[0].OwnerSubjectID != "auth0|owner" || rows[0].ConnectTenantID != tenant || rows[0].ServerID != relay.server {
		t.Fatalf("reach row = %+v", rows[0])
	}

	// Revoke everywhere: the control plane ends the Guard's access, the
	// reconciler revokes both registries, the relay closes and stays closed.
	if err := store.RevokeGuardAccess(t.Context(), tenant, relay.worker, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := reachStore.Update(t.Context(), tenant, relay.worker, func(r *guardreach.Row) bool { r.NextAttemptAt = time.Now().Add(-time.Second); return true }); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both registries to confirm the revocation", func() bool {
		_ = service.ReconcileOnce(t.Context())
		row, _ := reachStore.Get(t.Context(), tenant, relay.worker)
		return row != nil && guardreach.Summary(*row) == guardreach.StateRevoked && row.Me.State == guardreach.TargetRevoked && row.Connect.State == guardreach.TargetRevoked
	})
	if me.revoked != 1 || connect.revoked.Load() != 1 {
		t.Fatalf("revocations: Me %d, Connect %d", me.revoked, connect.revoked.Load())
	}
	time.Sleep(300 * time.Millisecond)
	settled := relay.attempts.Load()
	time.Sleep(300 * time.Millisecond)
	if relay.attempts.Load() != settled {
		t.Fatalf("the Guard kept connecting after revocation: %d -> %d attempts", settled, relay.attempts.Load())
	}
	cancel()
	<-finished
}
