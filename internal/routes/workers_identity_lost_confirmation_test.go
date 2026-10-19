package routes

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// The control plane commits the first authenticated confirmation but its answer
// never reaches the Guard. The Guard confirms again with the credential it
// holds instead of redeeming the capability a second time, ends bound, and
// keeps no copy of the capability: neither in its state nor in cloud-init.
func TestGuardAgentLostConfirmationAnswerBindsOnceAndKeepsNoCapability(t *testing.T) {
	store := controlplane.NewMemoryStore()
	raw, hash, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID: "lost-confirmation", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		TokenHash: hash, Status: "active", ExpiresAt: &expires,
	}); err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store})
	var registrations atomic.Int32
	var dropped atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workers/register" {
			registrations.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/identity/token") && dropped.CompareAndSwap(false, true) {
			router.ServeHTTP(httptest.NewRecorder(), r)
			if connection, _, hijackErr := w.(http.Hijacker).Hijack(); hijackErr == nil {
				_ = connection.(net.Conn).Close()
			}
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()

	residue := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(residue, []byte("#cloud-config\nKOMBI_TOKEN="+raw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := hostidentity.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := hostidentity.Enroll(t.Context(), hostidentity.EnrollOptions{
		Manager: manager, EnrollmentPath: filepath.Join(t.TempDir(), "agent-enrollment.json"),
		ResiduePaths: []string{residue}, Backoff: []time.Duration{time.Millisecond, time.Millisecond},
		Bootstrap: hostidentity.Bootstrap{Server: server.URL, Token: raw, Hostname: "node-1", OS: "linux", Arch: "x86_64"},
	})
	if err != nil || result.Mode != hostidentity.ModeHostKey {
		t.Fatalf("enrollment after a lost confirmation answer = %+v, %v", result, err)
	}
	if !dropped.Load() || registrations.Load() != 1 {
		t.Fatalf("confirmation answer dropped: %v; capability redeemed %d times, want once", dropped.Load(), registrations.Load())
	}
	reopened, err := hostidentity.Open(manager.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if record := reopened.Record(); record.State != hostidentity.StateBound {
		t.Fatalf("local state = %s, want bound", record.State)
	}
	if capability, err := reopened.LoadBootstrap(); err != nil || capability != nil {
		t.Fatalf("the capability survived confirmation in Guard state: %v", err)
	}
	if reopened.CanResume(server.URL, raw) {
		t.Fatal("a restart could resume redemption of the confirmed capability")
	}
	if remaining, err := os.ReadFile(residue); err != nil || strings.Contains(string(remaining), raw) {
		t.Fatalf("the capability survived confirmation in cloud-init residue: %v", err)
	}
}
