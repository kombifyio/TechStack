package guardreach

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

type fakeMe struct {
	mu          sync.Mutex
	registered  []MeRegistration
	revoked     []string
	failRevoke  error
	failEnsure  error
	routeCalls  int
	serviceName string
}

func (m *fakeMe) EnsureRoute(_ context.Context, _ kombifyme.ManagedAddress, service, _, _ string) (Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routeCalls++
	m.serviceName = service
	if m.failEnsure != nil {
		return Route{}, m.failEnsure
	}
	return Route{Name: "sh-base-" + service, FQDN: "sh-base-" + service + ".kombify.me"}, nil
}

func (m *fakeMe) RegisterServer(_ context.Context, in MeRegistration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered = append(m.registered, in)
	return nil
}

func (m *fakeMe) RevokeServer(_ context.Context, tenantID, serverID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failRevoke != nil {
		return m.failRevoke
	}
	m.revoked = append(m.revoked, tenantID+"/"+serverID+"/"+reason)
	return nil
}

type fakeConnect struct {
	mu       sync.Mutex
	puts     []NodeUpsert
	revokes  []string
	pages    []RevocationPage
	putErr   error
	pageErr  error
	requests []int64
}

func (c *fakeConnect) PutNode(_ context.Context, in NodeUpsert) (*Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.putErr != nil {
		return nil, c.putErr
	}
	c.puts = append(c.puts, in)
	return &Node{NodeID: "tsn_node1", Status: "active", Created: true}, nil
}

func (c *fakeConnect) RevokeNode(_ context.Context, owner, tenant string, _ []string, serverID, reason string) (*Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revokes = append(c.revokes, owner+"|"+tenant+"|"+serverID+"|"+reason)
	return &Node{NodeID: "tsn_node1", Status: "revoked", RevokedNow: true}, nil
}

func (c *fakeConnect) Revocations(_ context.Context, after int64, _ int) (*RevocationPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, after)
	if c.pageErr != nil {
		return nil, c.pageErr
	}
	for _, page := range c.pages {
		if len(page.Events) > 0 && page.Events[0].Seq > after {
			copy := page
			return &copy, nil
		}
	}
	return &RevocationPage{NextCursor: after}, nil
}

type fakeIdentities struct {
	mu      sync.Mutex
	subject Subject
}

func (f *fakeIdentities) set(subject Subject) {
	f.mu.Lock()
	f.subject = subject
	f.mu.Unlock()
}

func (f *fakeIdentities) Resolve(context.Context, string, string, string) (Subject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subject, nil
}

type fakeAccess struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeAccess) RevokeGuardAccess(_ context.Context, tenantID, workerID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tenantID+"/"+workerID)
	return nil
}

type harness struct {
	svc        *Service
	store      *MemoryStore
	me         *fakeMe
	connect    *fakeConnect
	identities *fakeIdentities
	access     *fakeAccess
	clock      *time.Time
	key        []byte
}

// newHarness is a server whose owner has published it; newUnpublishedHarness is
// the state of every freshly paired server.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := newUnpublishedHarness(t)
	if _, err := h.svc.SetPublic(t.Context(), "org_acme", "worker-1", "auth0|owner", PublicReach{Enabled: true, Service: "web", Port: 8080}); err != nil {
		t.Fatal(err)
	}
	return h
}

func newUnpublishedHarness(t *testing.T) *harness {
	t.Helper()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	h := &harness{store: NewMemoryStore(), me: &fakeMe{}, connect: &fakeConnect{}, identities: &fakeIdentities{}, access: &fakeAccess{}, clock: &now}
	h.store.now = func() time.Time { return *h.clock }
	svc, err := New(Config{
		Store: h.store, Me: h.me, Connect: h.connect, Identities: h.identities, Access: h.access,
		Now: func() time.Time { return *h.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	if err := svc.Enroll(t.Context(), Enrollment{
		TenantID: "org_acme", WorkerID: "worker-1", ServerID: "server-1", StackID: "stack-1", OwnerSubjectID: "auth0|owner",
		DisplayName: "Basement", Placement: "byo", OwnerRoles: []string{"developer", "member"},
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) advance(d time.Duration) { *h.clock = h.clock.Add(d) }

func (h *harness) pass(t *testing.T) {
	t.Helper()
	h.advance(time.Hour) // every retry and lease is due
	if err := h.svc.ReconcileOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) row(t *testing.T) Row {
	t.Helper()
	row, err := h.store.Get(t.Context(), "org_acme", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	return *row
}

func boundSubject(thumbprint string, epoch int64) Subject {
	return Subject{Found: true, WorkerActive: true, IdentityState: "bound", Thumbprint: thumbprint, CredentialEpoch: epoch}
}

// guardKey is a Guard identity key; mint signs a host proof with it at the
// harness clock, as the Guard does when the control plane asks.
type guardKey struct {
	key        ed25519.PrivateKey
	thumbprint string
}

func newGuardKey(t *testing.T) guardKey {
	t.Helper()
	key, err := guardidentity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	thumbprint, _ := guardidentity.PublicJWK(key).Thumbprint()
	return guardKey{key: key, thumbprint: thumbprint}
}

func (k guardKey) mint(t *testing.T, h *harness, in guardidentity.HostProofInput) string {
	t.Helper()
	in.Now = *h.clock
	proof, err := guardidentity.SignHostProof(k.key, in)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func connectProof(generation int64, tenant string) guardidentity.HostProofInput {
	return guardidentity.HostProofInput{Issuer: "worker-1", Subject: "server-1", TenantID: tenant, Audience: guardidentity.AudienceConnect, Generation: generation}
}

func TestConnectTenantMatchesTheTenantGatewayDerives(t *testing.T) {
	for name, test := range map[string]struct {
		tenant, owner, want string
		fails               bool
	}{
		"organization":           {"org_7f3a", "auth0|o", "org_7f3a", false},
		"personal":               {"usr:auth0|o", "auth0|o", "usr:auth0|o", false},
		"legacy naked subject":   {"auth0|o", "auth0|o", "usr:auth0|o", false},
		"another owner personal": {"usr:auth0|someone-else", "auth0|o", "", true},
		"demo tenant":            {"demo-tenant", "auth0|o", "", true},
		"empty":                  {"", "auth0|o", "", true},
	} {
		got, err := ConnectTenant(test.tenant, test.owner)
		if test.fails != (err != nil) || got != test.want {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

// Reach registers a server only for a bound key. An unconfirmed or foreign key
// produces no downstream call at all, and a Connect proof is forwarded only
// when it is for the outstanding request and signed by the bound key.
func TestReachRegistersOnlyABoundKeyAndOnlyForwardsAVerifiedProof(t *testing.T) {
	h := newHarness(t)

	h.identities.set(Subject{Found: true, WorkerActive: true, IdentityState: "issued_unconfirmed", Thumbprint: "T1", CredentialEpoch: 1})
	h.pass(t)
	if len(h.me.registered) != 0 || h.me.routeCalls != 0 || len(h.connect.puts) != 0 {
		t.Fatal("an unconfirmed key was registered downstream")
	}
	if row := h.row(t); Summary(row) != StatePending || row.Generation != 0 {
		t.Fatalf("row = %+v", row)
	}
	if doc, _ := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{}); len(doc.ProofRequests) != 0 || doc.KombifyMe != nil {
		t.Fatalf("document before binding = %+v", doc)
	}

	// Bound: kombify.me gets the route and the server binding, and the control
	// plane asks the Guard for a Connect proof for exactly this generation.
	guard := newGuardKey(t)
	thumbprint := guard.thumbprint
	h.identities.set(boundSubject(thumbprint, 1))
	h.pass(t)
	if len(h.me.registered) != 1 || h.me.registered[0].Generation != 1 || h.me.registered[0].KeyThumbprint != thumbprint ||
		h.me.registered[0].TenantID != "org_acme" || h.me.registered[0].ServerID != "server-1" {
		t.Fatalf("Me registration = %+v", h.me.registered)
	}
	doc, err := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{})
	if err != nil || len(doc.ProofRequests) != 1 || doc.ProofRequests[0].Audience != guardidentity.AudienceConnect ||
		doc.ProofRequests[0].Generation != 1 || doc.ProofRequests[0].TenantID != "org_acme" {
		t.Fatalf("document = %+v, %v", doc, err)
	}
	if doc.KombifyMe == nil || doc.KombifyMe.Generation != 1 || doc.KombifyMe.Audience != guardidentity.AudienceRelay {
		t.Fatalf("relay target = %+v", doc.KombifyMe)
	}

	// Proofs that must never reach Gateway: signed by another key, for another
	// tenant, generation or audience.
	proof := guard.mint(t, h, connectProof(1, "org_acme"))
	foreign := newGuardKey(t).mint(t, h, connectProof(1, "org_acme"))
	wrongTenant := guard.mint(t, h, connectProof(1, "org_other"))
	relayInput := connectProof(1, "org_acme")
	relayInput.Audience = guardidentity.AudienceRelay
	relayAudience := guard.mint(t, h, relayInput)
	for name, bad := range map[string]string{"foreign key": foreign, "other tenant": wrongTenant, "relay audience": relayAudience, "garbage": "x.y.z"} {
		if _, err := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{Proofs: []ProofSubmission{{Audience: guardidentity.AudienceConnect, Generation: 1, Proof: bad}}}); err != nil {
			t.Fatal(err)
		}
		if len(h.connect.puts) != 0 {
			t.Fatalf("%s proof reached Gateway", name)
		}
	}

	doc, err = h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{Proofs: []ProofSubmission{{Audience: guardidentity.AudienceConnect, Generation: 1, Proof: proof}}})
	if err != nil || doc.State != StateReady || len(doc.ProofRequests) != 0 {
		t.Fatalf("document after proof = %+v, %v", doc, err)
	}
	if len(h.connect.puts) != 1 {
		t.Fatalf("Gateway puts = %d", len(h.connect.puts))
	}
	put := h.connect.puts[0]
	if put.OwnerSubject != "auth0|owner" || put.ConnectTenantID != "org_acme" || put.KeyThumbprint != thumbprint ||
		put.StackID != "stack-1" || put.Placement != "byo" || put.HostProof != proof || len(put.Roles) != 1 || put.Roles[0] != "developer" {
		t.Fatalf("Gateway upsert = %+v", put)
	}
	view := ViewOf(h.row(t))
	if view.State != StateReady || view.KombifyMe.Address == "" || view.Connect.NodeID != "tsn_node1" {
		t.Fatalf("view = %+v", view)
	}
}

// A rotated key is a new binding at a higher generation: both registries are
// told, a proof for the old generation is ignored, and the view never calls a
// stale registration reachable.
func TestReachRotationRaisesTheGenerationAndIgnoresStaleProofs(t *testing.T) {
	h := newHarness(t)
	guard := newGuardKey(t)
	h.identities.set(boundSubject(guard.thumbprint, 1))
	h.pass(t)
	proof := guard.mint(t, h, connectProof(1, "org_acme"))
	if _, err := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{Proofs: []ProofSubmission{{Audience: guardidentity.AudienceConnect, Generation: 1, Proof: proof}}}); err != nil {
		t.Fatal(err)
	}
	if Summary(h.row(t)) != StateReady {
		t.Fatal("not ready before rotation")
	}

	h.identities.set(boundSubject("rotated-thumbprint", 1))
	h.pass(t)
	row := h.row(t)
	if row.Generation != 2 || row.KeyThumbprint != "rotated-thumbprint" {
		t.Fatalf("after rotation = %+v", row)
	}
	if last := h.me.registered[len(h.me.registered)-1]; last.Generation != 2 || last.KeyThumbprint != "rotated-thumbprint" {
		t.Fatalf("Me registration = %+v", last)
	}
	if Summary(row) == StateReady || ViewOf(row).Connect.State == string(TargetRegistered) {
		t.Fatal("a stale Connect registration reads as reachable")
	}
	putsBefore := len(h.connect.puts)
	if _, err := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{Proofs: []ProofSubmission{{Audience: guardidentity.AudienceConnect, Generation: 1, Proof: proof}}}); err != nil {
		t.Fatal(err)
	}
	if len(h.connect.puts) != putsBefore {
		t.Fatal("a proof for the old generation was forwarded")
	}
	if doc, _ := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{}); len(doc.ProofRequests) != 1 || doc.ProofRequests[0].Generation != 2 {
		t.Fatalf("proof request after rotation = %+v", doc.ProofRequests)
	}
}

// Detach revokes at both registries, repeats until both confirmed, and a
// re-pairing with a new key returns at a higher generation instead of reviving
// the revoked binding.
func TestReachRevokesEverywhereAndReturnsOnlyAtAHigherGeneration(t *testing.T) {
	h := newHarness(t)
	guard := newGuardKey(t)
	thumbprint := guard.thumbprint
	h.identities.set(boundSubject(thumbprint, 1))
	h.pass(t)
	proof := guard.mint(t, h, connectProof(1, "org_acme"))
	if _, err := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{Proofs: []ProofSubmission{{Audience: guardidentity.AudienceConnect, Generation: 1, Proof: proof}}}); err != nil {
		t.Fatal(err)
	}

	h.identities.set(Subject{Found: true, WorkerActive: false, IdentityState: "revoked", Thumbprint: thumbprint, CredentialEpoch: 1})
	h.me.failRevoke = &MeError{Status: http.StatusBadGateway, ReasonCode: "revocation_incomplete", Retryable: true}
	h.pass(t)
	row := h.row(t)
	if row.Desired != DesiredRevoked || row.Me.State == TargetRevoked || row.Connect.State != TargetRevoked {
		t.Fatalf("after the first revoke pass = %+v", row)
	}
	if len(h.connect.revokes) != 1 {
		t.Fatalf("Connect revokes = %v", h.connect.revokes)
	}
	h.me.failRevoke = nil
	h.pass(t)
	row = h.row(t)
	if row.Me.State != TargetRevoked || Summary(row) != StateRevoked || len(h.me.revoked) != 1 {
		t.Fatalf("after retry = %+v, Me revokes %v", row, h.me.revoked)
	}
	// Once both confirmed nothing repeats.
	callsBefore := len(h.connect.revokes)
	h.pass(t)
	if len(h.connect.revokes) != callsBefore {
		t.Fatal("a confirmed revocation was repeated")
	}
	if doc, _ := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{}); doc.State != StateRevoked || doc.KombifyMe != nil || len(doc.ProofRequests) != 0 {
		t.Fatalf("revoked document = %+v", doc)
	}

	// The same key bound again under the same pairing stays revoked.
	h.identities.set(boundSubject(thumbprint, 1))
	h.pass(t)
	if h.row(t).Desired != DesiredRevoked {
		t.Fatal("a revoked binding came back without a new key or pairing")
	}
	// A new pairing (credential epoch) returns at generation 2.
	h.identities.set(boundSubject(thumbprint, 2))
	h.pass(t)
	row = h.row(t)
	if row.Desired != DesiredActive || row.Generation != 2 || !row.Me.registeredAt(2) {
		t.Fatalf("after re-pairing = %+v", row)
	}
}

// The revocation cursor is consumed in order and never past an event that was
// not applied. An owner-initiated revoke ends Guard access first; an echo of
// the control plane's own revoke and events older than the current binding
// change nothing.
func TestRevocationCursorRespectsOrderGenerationAndOrigin(t *testing.T) {
	h := newHarness(t)
	h.identities.set(boundSubject(newGuardKey(t).thumbprint, 1))
	h.pass(t)
	h.identities.set(boundSubject("rotated", 1))
	h.pass(t) // generation 2
	if h.row(t).Generation != 2 {
		t.Fatal("setup: expected generation 2")
	}

	event := func(seq, generation int64, by string) Revocation {
		return Revocation{Seq: seq, NodeID: "tsn_node1", TenantID: "org_acme", ServerID: "server-1", WorkerID: "worker-1",
			KeyThumbprint: "rotated", BindingGeneration: generation, Reason: "owner_request", RevokedBy: by}
	}
	h.connect.pages = []RevocationPage{
		{Events: []Revocation{event(3, 1, "subject:auth0|owner")}, NextCursor: 3},           // stale binding
		{Events: []Revocation{event(4, 2, "service:techstack:auth0|owner")}, NextCursor: 4}, // our own echo
		{Events: []Revocation{event(5, 2, "subject:auth0|owner")}, NextCursor: 5},           // owner revoke
	}
	for range 4 { // one page per call: stale, echo, owner revoke, drained
		if err := h.svc.ConsumeRevocations(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if position, _ := h.store.Cursor(t.Context(), revocationCursorName); position != 5 {
		t.Fatalf("cursor = %d, want 5", position)
	}
	if len(h.access.calls) != 1 || h.access.calls[0] != "org_acme/worker-1" {
		t.Fatalf("access revocations = %v, want exactly the owner-initiated one", h.access.calls)
	}
	row := h.row(t)
	if row.Desired != DesiredRevoked || row.Connect.State != TargetRevoked || row.RevokeReason != "owner_request" {
		t.Fatalf("row after owner revoke = %+v", row)
	}
	// The reconciler then revokes kombify.me with the owner's reason.
	h.identities.set(Subject{Found: true, WorkerActive: false, IdentityState: "revoked", Thumbprint: "rotated", CredentialEpoch: 1})
	h.pass(t)
	if len(h.me.revoked) != 1 || h.me.revoked[0] != "org_acme/server-1/owner_request" {
		t.Fatalf("Me revokes = %v", h.me.revoked)
	}
	if len(h.connect.revokes) != 0 {
		t.Fatalf("Connect was asked to revoke what it had revoked itself: %v", h.connect.revokes)
	}

	// An event that cannot be applied does not move the cursor.
	h2 := newHarness(t)
	h2.connect.pages = []RevocationPage{{Events: []Revocation{{Seq: 9, TenantID: "org_acme", ServerID: "server-1", WorkerID: "worker-1", BindingGeneration: 1, RevokedBy: "subject:x"}}, NextCursor: 9}}
	h2.identities.set(boundSubject("t", 1))
	h2.pass(t)
	failing := &failingAccess{err: errors.New("database down")}
	h2.svc.cfg.Access = failing
	if err := h2.svc.ConsumeRevocations(t.Context()); err == nil {
		t.Fatal("an unapplied revocation was reported as consumed")
	}
	if position, _ := h2.store.Cursor(t.Context(), revocationCursorName); position != 0 {
		t.Fatalf("cursor advanced to %d past an unapplied event", position)
	}
}

type failingAccess struct{ err error }

func (f *failingAccess) RevokeGuardAccess(context.Context, string, string, time.Time) error {
	return f.err
}

// A freshly paired server is never published: no route is created, no relay
// target is handed to the Guard, and the owner sees "not enabled". Publishing
// needs an explicit service and port, serves exactly that loopback origin, and
// withdrawing it removes the route assignment again.
func TestReachPublishesOnlyWhatTheOwnerEnabledAndWithdrawsIt(t *testing.T) {
	h := newUnpublishedHarness(t)
	guard := newGuardKey(t)
	h.identities.set(boundSubject(guard.thumbprint, 1))
	h.pass(t)
	if h.me.routeCalls != 0 || len(h.me.registered) != 0 {
		t.Fatalf("an unpublished server reached kombify.me: routes=%d registrations=%d", h.me.routeCalls, len(h.me.registered))
	}
	doc, err := h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{})
	if err != nil || doc.KombifyMe != nil || doc.State != StateNotEnabled {
		t.Fatalf("document = %+v, %v", doc, err)
	}
	if view := ViewOf(h.row(t)); view.Public.Enabled || view.KombifyMe.State != string(TargetNotEnabled) {
		t.Fatalf("view = %+v", view)
	}

	for _, invalid := range []PublicReach{
		{Enabled: true, Service: "web"},
		{Enabled: true, Port: 80},
		{Enabled: true, Service: "Web Admin", Port: 80},
		{Enabled: true, Service: "web", Port: 70000},
		{Enabled: true, Service: "web", Port: 80, Scheme: "ftp"},
	} {
		if _, err := h.svc.SetPublic(t.Context(), "org_acme", "worker-1", "auth0|owner", invalid); !errors.Is(err, ErrPublicReachInvalid) {
			t.Fatalf("%+v was accepted: %v", invalid, err)
		}
	}
	if _, err := h.svc.SetPublic(t.Context(), "org_acme", "worker-1", "auth0|someone-else", PublicReach{Enabled: true, Service: "web", Port: 80}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner changed the reach: %v", err)
	}

	if _, err := h.svc.SetPublic(t.Context(), "org_acme", "worker-1", "auth0|owner", PublicReach{Enabled: true, Service: "grafana", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	h.pass(t)
	if h.me.routeCalls != 1 || len(h.me.registered) != 1 || len(h.me.registered[0].Routes) != 1 || h.me.serviceName[:8] != "grafana-" {
		t.Fatalf("published route = %q registrations=%+v", h.me.serviceName, h.me.registered)
	}
	if doc, _ = h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{}); doc.KombifyMe == nil {
		t.Fatal("no relay target after the owner published the server")
	}

	if _, err := h.svc.SetPublic(t.Context(), "org_acme", "worker-1", "auth0|owner", PublicReach{}); err != nil {
		t.Fatal(err)
	}
	h.pass(t)
	last := h.me.registered[len(h.me.registered)-1]
	if len(last.Routes) != 0 || h.me.routeCalls != 1 {
		t.Fatalf("withdrawal left routes assigned: %+v (route calls %d)", last, h.me.routeCalls)
	}
	if doc, _ = h.svc.Exchange(t.Context(), "org_acme", "worker-1", ExchangeRequest{}); doc.KombifyMe != nil || doc.State != StateNotEnabled {
		t.Fatalf("document after withdrawal = %+v", doc)
	}
}
