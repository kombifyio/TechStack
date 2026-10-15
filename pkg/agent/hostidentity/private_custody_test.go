package hostidentity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
)

func TestIdentityKeepsLifecycleInAdmittedPrivateCustody(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "identity")
	manager, err := hostidentity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	bootstrap := hostidentity.Bootstrap{Server: "https://synthetic.invalid", Token: "synthetic-private-capability"}
	if err := manager.SaveBootstrap(bootstrap); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := hostidentity.Open(dir); err == nil {
		t.Fatal("identity admitted a symlink custody root")
	}
	if err := manager.SetState(hostidentity.StateRedeeming, "synthetic"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StageRotation(); err != nil {
		t.Fatal(err)
	}
	if err := manager.CommitRotation(); err != nil {
		t.Fatal(err)
	}
	reopened, err := hostidentity.Open(moved)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	if !manager.Key().Equal(reopened.Key()) || reopened.Record().State != hostidentity.StateRedeeming {
		t.Fatal("identity did not reopen the admitted key and state")
	}
	capability, err := reopened.LoadBootstrap()
	if err != nil || capability == nil || capability.Token != bootstrap.Token {
		t.Fatal("identity lost its interrupted-enrollment capability")
	}
	// A server without host identity exercises the existing bearer enrollment
	// compatibility path, including its state-directory credential fallback.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workers/register" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"runtime_agent_id": "synthetic-runtime", "agent_token": "synthetic-enrollment-credential", "tenant_id": "synthetic-tenant", "accepted": true, "heartbeat_url": "http://" + r.Host + "/api/v1/workers/synthetic-runtime/heartbeat"}})
	}))
	defer server.Close()
	if _, err := hostidentity.Enroll(context.Background(), hostidentity.EnrollOptions{Manager: manager, Bootstrap: hostidentity.Bootstrap{Server: server.URL, Token: "synthetic-new-capability"}, HTTPClient: server.Client(), ResiduePaths: []string{}}); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(moved, hostidentity.EnrollmentFileName)); err != nil || !json.Valid(raw) {
		t.Fatal("enrollment fallback did not persist in admitted custody")
	}
	if err := manager.Decommission(); err != nil {
		t.Fatal(err)
	}
	after, err := hostidentity.Open(moved)
	if err != nil {
		t.Fatal(err)
	}
	if capability, err := after.LoadBootstrap(); err != nil || capability != nil || after.Record().State != hostidentity.StateRevoked {
		t.Fatal("revocation did not destroy the capability in admitted custody")
	}
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatal("identity wrote through its replaced root pathname")
	}
	if runtime.GOOS != "windows" {
		unsafe := filepath.Join(parent, "unsafe")
		if err := os.Mkdir(unsafe, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafe, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := hostidentity.Open(unsafe); err == nil {
			t.Fatal("identity admitted custody writable by another principal")
		}
	}
}
