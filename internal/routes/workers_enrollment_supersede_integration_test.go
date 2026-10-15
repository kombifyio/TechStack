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

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

// A Guard killed while redeeming leaves an unconfirmed worker on the stack. The
// owner's new capability, redeemed from the same host key, must supersede that
// worker: the stack ends with exactly one approved worker and the credential of
// the interrupted redemption stops working.
func TestGuardEnrollmentWithNewCapabilitySupersedesTheInterruptedWorker(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Guard enrollment PostgreSQL integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	tenantID, stackID := "enroll-tenant-"+suffix, "enroll-stack-"+suffix

	if _, err := database.DB.ExecContext(t.Context(), `INSERT INTO techstack_tenants (id, display_name) VALUES ($1, $1) ON CONFLICT DO NOTHING`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: stackID, TenantID: tenantID, OwnerSubjectID: "owner-1", Name: "enrollment-stack",
	}); err != nil {
		t.Fatal(err)
	}

	issueCapability := func(name string) string {
		t.Helper()
		raw, hash, genErr := pairingtoken.Generate(tenantID)
		if genErr != nil {
			t.Fatal(genErr)
		}
		expires := time.Now().Add(time.Hour)
		if _, upsertErr := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
			ID: name + "-" + suffix, TenantID: tenantID, OwnerSubjectID: "owner-1", StackID: stackID,
			TokenHash: hash, Status: "active", ExpiresAt: &expires,
		}); upsertErr != nil {
			t.Fatal(upsertErr)
		}
		return raw
	}
	approvedWorkers := func() []controlplane.Worker {
		t.Helper()
		workers, listErr := store.ListWorkersByTenant(t.Context(), tenantID)
		if listErr != nil {
			t.Fatal(listErr)
		}
		var approved []controlplane.Worker
		for _, worker := range workers {
			if worker.StackID == stackID && worker.Approved && worker.Status == "approved" {
				approved = append(approved, worker)
			}
		}
		return approved
	}

	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store})

	// The first redemption reaches the control plane but its response never
	// reaches the Guard: the Guard dies mid-redemption with the key persisted.
	var dropResponses atomic.Bool
	dropResponses.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workers/register" && dropResponses.Load() {
			router.ServeHTTP(httptest.NewRecorder(), r)
			if connection, _, hijackErr := w.(http.Hijacker).Hijack(); hijackErr == nil {
				_ = connection.(net.Conn).Close()
			}
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()

	stateDir, enrollmentPath := t.TempDir(), filepath.Join(t.TempDir(), "agent-enrollment.json")
	manager, err := hostidentity.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(capability string) (hostidentity.EnrollResult, error) {
		return hostidentity.Enroll(t.Context(), hostidentity.EnrollOptions{
			Manager: manager, EnrollmentPath: enrollmentPath, ResiduePaths: []string{}, Backoff: []time.Duration{time.Millisecond},
			Bootstrap: hostidentity.Bootstrap{Server: server.URL, Token: capability, Hostname: "node-1", OS: "linux", Arch: "x86_64"},
		})
	}
	if _, err := enroll(issueCapability("interrupted")); err == nil {
		t.Fatal("the interrupted enrollment must not report success")
	}
	interrupted := approvedWorkers()
	if len(interrupted) != 1 {
		t.Fatalf("approved workers after the interruption = %d, want 1", len(interrupted))
	}

	dropResponses.Store(false)
	result, err := enroll(issueCapability("replacement"))
	if err != nil {
		t.Fatalf("enrollment with a new capability: %v", err)
	}
	if result.Mode != hostidentity.ModeHostKey || !result.Accepted {
		t.Fatalf("enrollment result = %+v", result)
	}
	remaining := approvedWorkers()
	if len(remaining) != 1 {
		t.Fatalf("approved workers after the new capability = %d, want exactly 1", len(remaining))
	}
	identity, err := store.GetWorkerIdentity(t.Context(), tenantID, remaining[0].ID)
	if err != nil || identity.State != controlplane.WorkerIdentityBound {
		t.Fatalf("remaining worker identity = %+v, %v; want bound", identity, err)
	}
	if record := manager.Record(); record.State != hostidentity.StateBound || record.KeyThumbprint != identity.Thumbprint {
		t.Fatalf("local state = %+v, bound thumbprint %s", record, identity.Thumbprint)
	}
}
