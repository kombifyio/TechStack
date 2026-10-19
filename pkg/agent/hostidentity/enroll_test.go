package hostidentity_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
)

func TestEnrollmentDiscardsRejectedCapabilityButRetainsTransientRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		status      int
		replacement bool
	}{
		{"expired", http.StatusUnauthorized, false},
		{"rejected", http.StatusForbidden, false},
		{"transient", http.StatusServiceUnavailable, false},
		{"replacement-before-old-rejection", http.StatusUnauthorized, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager, err := hostidentity.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const capability = "kpt1.expiring-fixture-capability"
			const replacement = "kpt1.replacement-fixture-capability"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario.replacement {
					// A second process can persist its new capability while the
					// old request is still awaiting the control-plane response.
					other, err := hostidentity.Open(manager.Dir())
					if err == nil {
						err = other.SaveBootstrap(hostidentity.Bootstrap{Server: "http://" + r.Host, Token: replacement})
					}
					if err != nil {
						t.Errorf("persist replacement: %v", err)
					}
				}
				w.WriteHeader(scenario.status)
			}))
			defer server.Close()
			residue := filepath.Join(t.TempDir(), "user-data")
			if err := os.WriteFile(residue, []byte("KOMBI_TOKEN="+capability+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = hostidentity.Enroll(context.Background(), hostidentity.EnrollOptions{
				Manager: manager, Bootstrap: hostidentity.Bootstrap{Server: server.URL, Token: capability},
				HTTPClient: server.Client(), Backoff: []time.Duration{}, ResiduePaths: []string{residue},
			})
			if err == nil {
				t.Fatal("unsuccessful redemption was accepted")
			}
			transient := scenario.status == http.StatusServiceUnavailable
			if errors.Is(err, hostidentity.ErrRecoveryNeeded) == transient {
				t.Fatalf("wrong recovery classification: %v", err)
			}
			reopened, err := hostidentity.Open(manager.Dir())
			if err != nil {
				t.Fatal(err)
			}
			if reopened.CanResume(server.URL, capability) != transient {
				t.Fatal("restart did not preserve the appropriate capability recovery behavior")
			}
			bootstrap, err := reopened.LoadBootstrap()
			if err != nil || (bootstrap != nil) != (transient || scenario.replacement) {
				t.Fatalf("wrong retained bootstrap state: %v", err)
			}
			if scenario.replacement && bootstrap.Token != replacement {
				t.Fatal("old rejection destroyed the replacement capability")
			}
			remaining, err := os.ReadFile(residue)
			if err != nil || strings.Contains(string(remaining), capability) != transient {
				t.Fatalf("wrong cloud-init capability retention: %v", err)
			}
		})
	}
}

// A redemption that is answered but then refused at confirmation (the owner
// revoked the unconfirmed worker) leaves a capability that can never bind this
// host: neither the Guard's copy nor the cloud-init residue may survive it.
func TestEnrollmentRefusedAtConfirmationDiscardsTheCapability(t *testing.T) {
	manager, err := hostidentity.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const capability = "kpt1.revoked-before-confirmation-capability"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workers/register":
			_, _ = w.Write([]byte(`{"data":{"worker_id":"worker-1","agent_token":"agent-credential","tenant_id":"tenant-1","accepted":true,` +
				`"heartbeat_url":"http://` + r.Host + `/api/v1/workers/worker-1/heartbeat"}}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"guard_identity_revoked","details":{}}}`))
		}
	}))
	defer server.Close()
	residue := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(residue, []byte("KOMBI_TOKEN="+capability+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = hostidentity.Enroll(context.Background(), hostidentity.EnrollOptions{
		Manager: manager, Bootstrap: hostidentity.Bootstrap{Server: server.URL, Token: capability},
		EnrollmentPath: filepath.Join(t.TempDir(), "agent-enrollment.json"),
		HTTPClient:     server.Client(), Backoff: []time.Duration{}, ResiduePaths: []string{residue},
	})
	if !errors.Is(err, hostidentity.ErrRevoked) {
		t.Fatalf("refused confirmation = %v, want revoked", err)
	}
	reopened, err := hostidentity.Open(manager.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap, err := reopened.LoadBootstrap(); err != nil || bootstrap != nil {
		t.Fatalf("the refused capability is still held by the Guard: %v", err)
	}
	if reopened.CanResume(server.URL, capability) {
		t.Fatal("a restart would redeem the refused capability again")
	}
	remaining, err := os.ReadFile(residue)
	if err != nil || strings.Contains(string(remaining), capability) {
		t.Fatalf("the refused capability survived in cloud-init residue: %v", err)
	}
}
