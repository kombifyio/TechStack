package hostidentity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/guardidentity"
)

func establishAgainst(t *testing.T, bound bool, handler http.HandlerFunc) (*Client, Mode, error) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	if bound {
		if err := manager.SetState(StateBound, ""); err != nil {
			t.Fatal(err)
		}
	}
	client, err := NewClient(ClientConfig{
		Manager: manager, AgentToken: "credential",
		Endpoints: Endpoints{TokenURL: server.URL + "/token", BindURL: server.URL + "/bind", RotateURL: server.URL + "/rotate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mode, err := client.Establish(context.Background())
	return client, mode, err
}

// A Guard that holds a bound key is never talked back onto its bearer
// credential by a control plane that claims to know no host identity.
func TestBoundGuardRefusesToFallBackToBearerWhenIdentityIsReportedUnsupported(t *testing.T) {
	_, mode, err := establishAgainst(t, true, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	if !errors.Is(err, ErrDowngradeRefused) || mode != ModeLegacy {
		t.Fatalf("Establish = %v, %v; want ErrDowngradeRefused", mode, err)
	}
	// A server that never bound a key keeps the compatibility behavior.
	if _, _, err := establishAgainst(t, false, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }); err != nil {
		t.Fatalf("unbound Guard against a control plane without identity = %v", err)
	}
}

// A key the owner has not approved yet is not an error: the Guard keeps
// reporting on its bearer credential and looks again.
func TestGuardWaitsForOwnerApprovalOfItsKeyInsteadOfFailing(t *testing.T) {
	bound := false
	client, mode, err := establishAgainst(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/bind":
			bound = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"data":{"identity_state":"upgrade_pending"}}`))
		case bound:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"` + guardidentity.CodeIdentityUpgradePending + `"}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"guard_proof_invalid","details":{"reason":"` + guardidentity.ReasonIdentityUnbound + `"}}}`))
		}
	})
	if err != nil || mode != ModeLegacy || client.Mode() != ModeLegacy || !client.pendingApproval.Load() {
		t.Fatalf("Establish = %v, %v (pending=%v)", mode, err, client.pendingApproval.Load())
	}
}

// After the control plane revokes the identity the Guard destroys every secret
// that could authenticate the server: the key, the staged rotation key, a
// persisted pairing capability and the enrollment files that carry the worker
// credential. The record stays, revoked, so a restart idles.
func TestDecommissionDestroysEveryCredentialOfARevokedGuard(t *testing.T) {
	dir := t.TempDir()
	manager, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StageRotation(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveBootstrap(Bootstrap{Server: "https://techstack.example", Token: "kpt1.capability-that-must-not-linger"}); err != nil {
		t.Fatal(err)
	}
	enrollment := filepath.Join(t.TempDir(), "agent-enrollment.json")
	for _, path := range []string{enrollment, filepath.Join(dir, EnrollmentFileName)} {
		if err := os.WriteFile(path, []byte(`{"agent_token":"tsra.secret"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.SetState(StateRevoked, "guard_identity_revoked"); err != nil {
		t.Fatal(err)
	}

	if err := manager.Decommission(enrollment); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dir, "identity.key"), filepath.Join(dir, "identity.key.next"), filepath.Join(dir, "bootstrap.json"),
		filepath.Join(dir, EnrollmentFileName), enrollment,
	} {
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
			t.Fatalf("%s still holds %d bytes", filepath.Base(path), info.Size())
		}
	}
	if record := manager.Record(); record.State != StateRevoked || record.KeyThumbprint != "" || manager.Key() != nil {
		t.Fatalf("record after decommission = %+v", record)
	}
	if err := manager.Decommission(enrollment); err != nil {
		t.Fatalf("repeating the decommission failed: %v", err)
	}
}

// The Guard owns its enrollment file but not the root-owned directory that
// holds it, so a revoked Guard cannot unlink it: it must still leave nothing
// readable behind.
func TestDecommissionEmptiesAnEnrollmentFileInAReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	etc := t.TempDir()
	enrollment := filepath.Join(etc, "agent-enrollment.json")
	if err := os.WriteFile(enrollment, []byte(`{"agent_token":"tsra.secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(etc, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(etc, 0o700) })
	if err := manager.Decommission(enrollment); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(enrollment); err == nil && info.Size() > 0 {
		t.Fatalf("the enrollment file still holds %d bytes", info.Size())
	}
}

// A replayed or stray capability never degrades a confirmed binding: the
// redemption is refused before the control plane is asked, and the state stays
// bound (finding Q-10).
func TestEnrollLeavesABoundGuardBound(t *testing.T) {
	asked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked = true
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	manager, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetState(StateBound, ""); err != nil {
		t.Fatal(err)
	}
	enrollment := filepath.Join(dir, "agent-enrollment.json")
	if err := os.WriteFile(enrollment, []byte(`{"agent_token":"credential"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Enroll(context.Background(), EnrollOptions{
		Manager: manager, Bootstrap: Bootstrap{Server: server.URL, Token: "consumed-capability"}, EnrollmentPath: enrollment,
	})
	if !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("enroll on a bound Guard: err = %v, want ErrAlreadyBound", err)
	}
	if asked {
		t.Fatal("a bound Guard sent its capability to the control plane")
	}
	if state := manager.Record().State; state != StateBound {
		t.Fatalf("state after a refused enroll = %q, want %q", state, StateBound)
	}
}
