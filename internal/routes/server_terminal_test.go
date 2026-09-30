package routes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/middleware"
	"golang.org/x/crypto/ssh"
)

func TestDemoPrincipalCannotOpenTerminalOrAuthorizeSSHKey(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	publicLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	for _, action := range []struct {
		name, path string
		invoke     func(*serverTerminalHandlers, *httpx.Event) error
		body       map[string]any
		status     int
	}{
		{"terminal", "/terminal-sessions", (*serverTerminalHandlers).createSession, nil, http.StatusCreated},
		{"authorize-key", "/access/authorized-key", (*serverTerminalHandlers).authorizeUserKey,
			map[string]any{"wallet_item_id": "wallet-key-1", "confirm": true}, http.StatusOK},
	} {
		for _, caller := range []struct {
			name, demoTenant, demoUser, principalType string
			signed, denied                            bool
		}{
			{name: "configured-tenant", demoTenant: "tenant-1", denied: true},
			{name: "configured-subject", demoUser: "owner-1", denied: true},
			{name: "signed-demo", principalType: "demo", signed: true, denied: true},
			{name: "signed-owner", principalType: middleware.PrincipalTypeUser, signed: true},
			{name: "unsigned-demo-header", principalType: "demo"},
		} {
			t.Run(action.name+"/"+caller.name, func(t *testing.T) {
				t.Setenv(demoguard.EnvDemoTenantID, caller.demoTenant)
				t.Setenv(demoguard.EnvDemoUserIDs, caller.demoUser)
				store := controlplane.NewMemoryStore()
				if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
					ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
					LeaseID: "lease-1", ConnectionState: "connected", HealthState: "healthy",
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := store.UpsertWalletItem(t.Context(), controlplane.WalletItem{
					ID: "wallet-key-1", TenantID: "tenant-1", StackID: "stack-1",
					Metadata: map[string]any{"owner_id": "owner-1", "kind": "ssh_key", "notes": "Public Key:\n" + publicLine},
				}); err != nil {
					t.Fatal(err)
				}
				installed := false
				h := serverTerminalHandlers{
					servers: store, wallet: store, now: func() time.Time { return now },
					targets: jobs.NewStaticManagedRuntimeTargetResolver(jobs.ManagedRuntimeTarget{
						Host: "203.0.113.10", SSHUser: "kombify", SSHPort: 22,
						SSHPrivateKey: "managed-test-secret", SSHHostKey: publicLine,
					}),
					installKey: func(context.Context, *jobs.ManagedRuntimeTarget, string, string) error {
						installed = true
						return nil
					},
					sessions: map[string]*serverTerminalSession{}, active: map[string]string{},
				}
				post := func(principalType string, signed bool) *httptest.ResponseRecorder {
					t.Helper()
					event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1"+action.path, "owner-1", "tenant-1", action.body)
					event.Request.SetPathValue("serverId", "server-1")
					event.Request.Header.Set(commonedgeauth.HeaderPrincipalType, principalType)
					if signed {
						for name, value := range map[string]string{
							commonedgeauth.HeaderEdgeAuth:    commonedgeauth.EdgeAuthValueJWT,
							commonedgeauth.HeaderEdgeService: "techstack", commonedgeauth.HeaderPublicPrefix: "/v1/techstack",
							commonedgeauth.HeaderUserID: "owner-1", commonedgeauth.HeaderOrgID: "tenant-1",
						} {
							event.Request.Header.Set(name, value)
						}
						signMaintenanceEdgeEnvelope(event.Request, commonedgeauth.EdgeSignatureVersionV7)
						edge := middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{
							Mode: config.ModeSaaS, EdgeAuthSecret: maintenanceEdgeSecret,
						})
						if err := edge(event); err != nil || recorder.Body.Len() > 0 {
							t.Fatalf("edge verification: err=%v status=%d body=%s", err, recorder.Code, recorder.Body.String())
						}
					}
					event.Request = event.Request.WithContext(authsession.WithClaims(event.Request.Context(), &authsession.Claims{
						Subject: "owner-1", TenantID: "tenant-1", ReauthPurpose: terminalReauthPurpose,
						ReauthResource: "server-1", AuthenticatedAt: now.Unix(),
					}))
					if err := action.invoke(&h, event); err != nil {
						t.Fatal(err)
					}
					return recorder
				}
				recorder := post(caller.principalType, caller.signed)
				if !caller.denied {
					if recorder.Code != action.status || (action.name == "authorize-key" && !installed) {
						t.Fatalf("ordinary owner: status=%d installed=%v body=%s", recorder.Code, installed, recorder.Body.String())
					}
					return
				}
				if recorder.Code != http.StatusForbidden || installed {
					t.Fatalf("demo action: status=%d installed=%v body=%s, want 403 before execution", recorder.Code, installed, recorder.Body.String())
				}
				var envelope struct {
					Error struct {
						Details struct {
							ErrorCode  string `json:"error_code"`
							ReasonCode string `json:"reason_code"`
							Capability string `json:"capability"`
							Retryable  bool   `json:"retryable"`
						} `json:"details"`
					} `json:"error"`
				}
				// Stable denial values are part of the public API protocol.
				if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				details := envelope.Error.Details
				if details.ErrorCode != "demo_account_restricted" || details.ReasonCode != "demo_account" || details.Capability != "runtime_ssh" || details.Retryable {
					t.Fatalf("demo denial = %#v", details)
				}
				// A refused terminal cannot reserve the owner's active slot; a refused
				// key request cannot prevent an ordinary owner's later installation.
				t.Setenv(demoguard.EnvDemoTenantID, "")
				t.Setenv(demoguard.EnvDemoUserIDs, "")
				retry := post(middleware.PrincipalTypeUser, true)
				if retry.Code != action.status || (action.name == "authorize-key" && !installed) {
					t.Fatalf("owner retry after demo refusal: status=%d installed=%v body=%s", retry.Code, installed, retry.Body.String())
				}
			})
		}
	}
}

func TestServerAccessIsOwnerScopedHostKeyPinnedAndSecretRedacted(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		LeaseID: "lease-1", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewPublicKey(publicKey)
	secret := "PRIVATE-KEY-MUST-NOT-LEAK"
	h := serverTerminalHandlers{
		servers: store,
		now:     time.Now,
		targets: jobs.NewStaticManagedRuntimeTargetResolver(jobs.ManagedRuntimeTarget{
			Host: "203.0.113.10", SSHUser: "kombify", SSHPort: 22,
			SSHPrivateKey: secret, SSHHostKey: string(ssh.MarshalAuthorizedKey(hostKey)),
		}),
	}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/server-1/access", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serverId", "server-1")
	if err := h.access(event); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recorder.Body.String(), secret) || strings.Contains(recorder.Body.String(), "private_key") || strings.Contains(recorder.Body.String(), "password") {
		t.Fatalf("access response leaked credential material: %s", recorder.Body.String())
	}
	var envelope struct {
		Data serverAccessResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.TerminalEnabled || envelope.Data.HostKeyFingerprint != ssh.FingerprintSHA256(hostKey) || !strings.Contains(envelope.Data.SSHCommand, "kombify@203.0.113.10") {
		t.Fatalf("access response = %#v", envelope.Data)
	}
	foreign, foreignRecorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers/server-1/access", "owner-2", "tenant-1", nil)
	foreign.Request.SetPathValue("serverId", "server-1")
	if err := h.access(foreign); err != nil {
		t.Fatal(err)
	}
	if foreignRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign owner status = %d", foreignRecorder.Code)
	}
}

func TestTerminalSessionRequiresFreshBoundReauthAndAllowsOnlyOneActiveSession(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		LeaseID: "lease-1", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	publicKey, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewPublicKey(publicKey)
	h := serverTerminalHandlers{
		servers: store, now: func() time.Time { return now },
		targets: jobs.NewStaticManagedRuntimeTargetResolver(jobs.ManagedRuntimeTarget{
			Host: "203.0.113.10", SSHUser: "kombify", SSHPort: 22,
			SSHPrivateKey: "managed-secret", SSHHostKey: string(ssh.MarshalAuthorizedKey(hostKey)),
		}),
		sessions: map[string]*serverTerminalSession{}, active: map[string]string{},
	}
	claims := &authsession.Claims{Subject: "owner-1", TenantID: "tenant-1", ReauthPurpose: terminalReauthPurpose, ReauthResource: "server-1", AuthenticatedAt: now.Unix()}
	first, firstRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/terminal-sessions", "owner-1", "tenant-1", nil)
	first.Request = first.Request.WithContext(authsession.WithClaims(first.Request.Context(), claims))
	first.Request.SetPathValue("serverId", "server-1")
	if err := h.createSession(first); err != nil {
		t.Fatal(err)
	}
	if firstRecorder.Code != http.StatusCreated {
		t.Fatalf("first session status = %d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}
	second, secondRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/terminal-sessions", "owner-1", "tenant-1", nil)
	second.Request = second.Request.WithContext(authsession.WithClaims(second.Request.Context(), claims))
	second.Request.SetPathValue("serverId", "server-1")
	if err := h.createSession(second); err != nil {
		t.Fatal(err)
	}
	if secondRecorder.Code != http.StatusConflict {
		t.Fatalf("second session status = %d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	wrongClaims := *claims
	wrongClaims.ReauthResource = "server-other"
	denied, deniedRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/terminal-sessions", "owner-1", "tenant-1", nil)
	denied.Request = denied.Request.WithContext(authsession.WithClaims(denied.Request.Context(), &wrongClaims))
	denied.Request.SetPathValue("serverId", "server-1")
	if err := h.createSession(denied); err != nil {
		t.Fatal(err)
	}
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("invalid reauth status = %d", deniedRecorder.Code)
	}
	staleClaims := *claims
	staleClaims.AuthenticatedAt = now.Add(-walletReauthWindow - time.Minute).Unix()
	stale, staleRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/terminal-sessions", "owner-1", "tenant-1", nil)
	stale.Request = stale.Request.WithContext(authsession.WithClaims(stale.Request.Context(), &staleClaims))
	stale.Request.SetPathValue("serverId", "server-1")
	if err := h.createSession(stale); err != nil {
		t.Fatal(err)
	}
	if staleRecorder.Code != http.StatusForbidden {
		t.Fatalf("stale reauth status = %d", staleRecorder.Code)
	}
}

func TestTerminalHostKeyAndOriginFailClosed(t *testing.T) {
	firstPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	secondPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	first, _ := ssh.NewPublicKey(firstPublic)
	second, _ := ssh.NewPublicKey(secondPublic)
	fingerprint, algorithm, callback, err := executionchannel.PinnedHostKey(string(ssh.MarshalAuthorizedKey(first)))
	if err != nil || fingerprint == "" {
		t.Fatalf("pinned host key: %v", err)
	}
	if algorithm != first.Type() {
		t.Fatalf("host key algorithm = %q, want %q", algorithm, first.Type())
	}
	if err := callback("host", &net.TCPAddr{}, second); err == nil {
		t.Fatal("host-key mismatch was accepted")
	}
	if _, _, _, err := executionchannel.PinnedHostKey(""); err == nil {
		t.Fatal("missing host-key custody was accepted")
	}
	request := &http.Request{Host: "techstack.example", Header: http.Header{"Origin": []string{"https://attacker.example"}}, URL: &url.URL{}}
	if terminalOriginAllowed(request) {
		t.Fatal("foreign websocket origin was accepted")
	}
	request.Header.Set("Origin", "https://techstack.example")
	if !terminalOriginAllowed(request) {
		t.Fatal("same websocket origin was rejected")
	}
}

func TestServerSSHKeyAuthorizationUsesOwnedWalletPublicKeyOnly(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		LeaseID: "lease-1", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	userPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	userKey, _ := ssh.NewPublicKey(userPublic)
	publicLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(userKey)))
	privateMarker := "PRIVATE-KEY-MUST-STAY-IN-WALLET"
	if _, err := store.UpsertWalletItem(t.Context(), controlplane.WalletItem{
		ID: "wallet-key-1", TenantID: "tenant-1", StackID: "stack-1", Metadata: map[string]any{
			"owner_id": "owner-1", "kind": "ssh_key", "secret": privateMarker,
			"notes": "Public Key:\n" + publicLine,
		},
	}); err != nil {
		t.Fatal(err)
	}
	hostPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewPublicKey(hostPublic)
	installed := ""
	t.Setenv("TECHSTACK_REAUTH_SECRET", "reauth-test-secret")
	h := serverTerminalHandlers{
		servers: store, wallet: store, now: func() time.Time { return now },
		targets: jobs.NewStaticManagedRuntimeTargetResolver(jobs.ManagedRuntimeTarget{
			Host: "203.0.113.10", SSHUser: "kombify", SSHPort: 22,
			SSHPrivateKey: "managed-secret", SSHHostKey: string(ssh.MarshalAuthorizedKey(hostKey)),
		}),
		installKey: func(_ context.Context, _ *jobs.ManagedRuntimeTarget, _ string, publicKey string) error {
			installed = publicKey
			return nil
		},
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	signature := hex.EncodeToString(signWalletAssertion(terminalReauthPurpose, "owner-1", "server-1", timestamp, "reauth-test-secret"))
	event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/access/authorized-key", "owner-1", "tenant-1", map[string]any{
		"wallet_item_id": "wallet-key-1", "confirm": true,
		"reauth_timestamp": timestamp, "reauth_signature": signature,
	})
	event.Request.SetPathValue("serverId", "server-1")
	if err := h.authorizeUserKey(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || installed != publicLine {
		t.Fatalf("authorization status=%d installed=%q body=%s", recorder.Code, installed, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), privateMarker) || !strings.Contains(recorder.Body.String(), `"connect_ready":true`) {
		t.Fatalf("authorization response leaked secret or stayed unready: %s", recorder.Body.String())
	}
	foreign, foreignRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/access/authorized-key", "owner-2", "tenant-1", map[string]any{
		"wallet_item_id": "wallet-key-1", "confirm": true,
	})
	foreign.Request.SetPathValue("serverId", "server-1")
	if err := h.authorizeUserKey(foreign); err != nil {
		t.Fatal(err)
	}
	if foreignRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign authorization status=%d", foreignRecorder.Code)
	}
}
