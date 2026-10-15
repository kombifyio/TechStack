package routes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
)

func nodeAccessProfile(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	value, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func errorDetails(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (body=%s)", err, body)
	}
	return envelope.Error.Details
}

func kombifyWizardHandler(fake *walletfake.Fake, tester *fakeWizardRemoteSSHTester) wizardRouteHandlers {
	return wizardRouteHandlers{cfg: WizardRouteConfig{
		Features: fakeWizardFeatureChecker{enabled: true}, RemoteSSHTester: tester, KombifyWallet: fake.Client(),
	}}
}

// The connection test reads the key from the Wallet item the user picked and
// connects with it.
func TestWizardRemoteSSHTestInKombifyModeConnectsWithTheWalletItem(t *testing.T) {
	fake := walletfake.New(t)
	tester := &fakeWizardRemoteSSHTester{}
	itemID := fake.Grant("owner-1", kombifywallet.ItemTypeNodeAccess, "Box", "account",
		nodeAccessProfile(t, map[string]any{"host": "server.example.test", "user": "ubuntu", "privateKey": "WALLET-PRIVATE-KEY"}), "use")

	event, recorder := wizardRemoteSSHTestEvent(`{"host":"server.example.test","user":"ubuntu","auth_method":"ssh-key","ssh_wallet_item_id":"` + itemID + `"}`)
	if err := kombifyWizardHandler(fake, tester).testRemoteSSH(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"success":true`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if tester.lastKey != "WALLET-PRIVATE-KEY" || tester.lastHost != "server.example.test" {
		t.Fatalf("the probe did not use the delivered profile: host=%q key=%q", tester.lastHost, tester.lastKey)
	}
	if strings.Contains(recorder.Body.String(), "WALLET-PRIVATE-KEY") {
		t.Fatal("the response carries the key")
	}
}

// A typed secret or label is refused, and a Wallet item is used only for the
// host it was saved for, so a request cannot aim the item's credential at a
// server the owner never named in it.
func TestWizardRemoteSSHTestInKombifyModeRefusesTypedSecretsAndAForeignHost(t *testing.T) {
	fake := walletfake.New(t)
	tester := &fakeWizardRemoteSSHTester{}
	handler := kombifyWizardHandler(fake, tester)
	itemID := fake.Grant("owner-1", kombifywallet.ItemTypeNodeAccess, "Box", "account",
		nodeAccessProfile(t, map[string]any{"host": "server.example.test", "user": "ubuntu", "password": "WALLET-PASSWORD"}), "use")

	cases := map[string]struct {
		body   string
		status int
		reason string
	}{
		"typed password":  {`{"host":"server.example.test","user":"u","auth_method":"password","password":"typed"}`, http.StatusBadRequest, kombifywallet.ReasonItemRequired},
		"typed key":       {`{"host":"server.example.test","user":"u","auth_method":"ssh-key","private_key":"typed"}`, http.StatusBadRequest, kombifywallet.ReasonItemRequired},
		"label":           {`{"host":"server.example.test","user":"u","auth_method":"ssh-key","ssh_key_label":"main-key"}`, http.StatusBadRequest, kombifywallet.ReasonItemRequired},
		"typed beside id": {`{"host":"server.example.test","user":"u","auth_method":"password","password":"typed","ssh_wallet_item_id":"` + itemID + `"}`, http.StatusBadRequest, kombifywallet.ReasonItemRequired},
		"foreign host":    {`{"host":"attacker.example.test","user":"u","auth_method":"password","ssh_wallet_item_id":"` + itemID + `"}`, http.StatusConflict, kombifywallet.ReasonHostMismatch},
		"wrong method":    {`{"host":"server.example.test","user":"u","auth_method":"ssh-key","ssh_wallet_item_id":"` + itemID + `"}`, http.StatusUnprocessableEntity, kombifywallet.ReasonCredentialMismatch},
	}
	for name, tc := range cases {
		event, recorder := wizardRemoteSSHTestEvent(tc.body)
		if err := handler.testRemoteSSH(event); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != tc.status || errorDetails(t, recorder.Body.Bytes())["reason_code"] != tc.reason {
			t.Fatalf("%s: status=%d body=%s, want %d %s", name, recorder.Code, recorder.Body.String(), tc.status, tc.reason)
		}
	}
	if tester.lastHost != "" || tester.lastPassword != "" || tester.lastKey != "" {
		t.Fatalf("a refused request still probed a host: %+v", tester)
	}
}

// Authorizing a server key from a Wallet item installs the item's public key,
// derived from the private key it holds, without any legacy wallet store, and
// never echoes the private key.
func TestServerSSHKeyAuthorizationInKombifyModeInstallsThePublicKeyOfTheWalletItem(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		LeaseID: "lease-1", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	userPublic, userPrivate, _ := ed25519.GenerateKey(rand.Reader)
	userKey, _ := ssh.NewPublicKey(userPublic)
	publicLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(userKey)))
	block, err := ssh.MarshalPrivateKey(userPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(block))

	fake := walletfake.New(t)
	itemID := fake.Grant("owner-1", kombifywallet.ItemTypeNodeAccess, "Laptop key", "account",
		nodeAccessProfile(t, map[string]any{"host": "laptop", "user": "me", "privateKey": privatePEM}), "use")
	hostPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := ssh.NewPublicKey(hostPublic)
	installed := ""
	t.Setenv("TECHSTACK_REAUTH_SECRET", "reauth-test-secret")
	h := serverTerminalHandlers{
		servers: store, kombify: fake.Client(), now: func() time.Time { return now },
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
		"wallet_item_id": itemID, "confirm": true, "reauth_timestamp": timestamp, "reauth_signature": signature,
	})
	event.Request.SetPathValue("serverId", "server-1")
	if err := h.authorizeUserKey(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || installed != publicLine {
		t.Fatalf("status=%d installed=%q want=%q body=%s", recorder.Code, installed, publicLine, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "PRIVATE KEY") {
		t.Fatalf("the response carries the private key: %s", recorder.Body.String())
	}

	// An item the owner never shared with Techstack is a structured denial.
	unshared := fake.Grant("owner-1", kombifywallet.ItemTypeNodeAccess, "Other", "account", nodeAccessProfile(t, map[string]any{"privateKey": privatePEM}), "read")
	denied, deniedRecorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/access/authorized-key", "owner-1", "tenant-1", map[string]any{
		"wallet_item_id": unshared, "confirm": true, "reauth_timestamp": timestamp, "reauth_signature": signature,
	})
	denied.Request.SetPathValue("serverId", "server-1")
	installed = ""
	if err := h.authorizeUserKey(denied); err != nil {
		t.Fatal(err)
	}
	details := errorDetails(t, deniedRecorder.Body.Bytes())
	if deniedRecorder.Code != http.StatusForbidden || details["reason_code"] != kombifywallet.ReasonRightMissing || details["required_right"] != "use" || installed != "" {
		t.Fatalf("unshared item: status=%d details=%v installed=%q", deniedRecorder.Code, details, installed)
	}
}

// The system accounts are PocketBase operator accounts, not Wallet owners.
func TestSystemAccountResetIsRefusedWhereCredentialsLiveInTheKombifyWallet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system-accounts/admin/reset", strings.NewReader(`{"confirm":"admin"}`))
	req.SetPathValue("role", "admin")
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "auth0|user-1"}))
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: req, Response: recorder}

	if err := refuseSystemAccountReset(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict || errorDetails(t, recorder.Body.Bytes())["reason_code"] != kombifywallet.ReasonSelfHostedOnly {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
