package routes

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

type guardTestCollector struct{}

func (guardTestCollector) Collect(context.Context) (httpguard.Snapshot, error) {
	return httpguard.Snapshot{}, nil
}

type guardTestExecutor struct{ runs atomic.Int32 }

func (e *guardTestExecutor) Execute(_ context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	e.runs.Add(1)
	return &agentpb.StackKitResult{
		CommandId: command.CommandId, Success: true, Release: command.Release,
		CommandResultSchemaVersion: "stackkit.command-result/v1",
		CommandResultJson:          []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"service_logs","status":"success"}`),
		EventsSchemaVersion:        "stackkit.rollout-event/v1",
	}
}

func openGuardIntegrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Guard typed command PostgreSQL integration test")
	}
	schema := "guard_routes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminDB := stdlib.OpenDB(*admin)
	if _, err := adminDB.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = adminDB.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	config, err := pgx.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })
	for _, name := range []string{"087_durable_typed_agent_commands.sql", "109_substrate_worker_commands.sql", "132_guard_command_outcome_binding.sql", "134_guard_operator_outcome_resolution.sql"} {
		migration, err := os.ReadFile("../../pkg/db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return database
}

// A real Guard against the real route and durable hub: the offered command's
// binding verifies on the Guard, the command runs once, and the result is
// acknowledged only after the control plane committed the revision.
func TestGuardRunsBoundCommandOnceAndResultIsCommittedAndAcknowledged(t *testing.T) {
	const token = "opaque-runtime-token"
	database := openGuardIntegrationDatabase(t)
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		Status: "connected", Approved: true,
		Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
		Capabilities: map[string]any{"server_id": "server-1", "runtime_agent_id": "runtime-1"},
	}); err != nil {
		t.Fatal(err)
	}
	hub := agentcontrol.NewHub(database)
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store, TypedControl: hub})
	server := httptest.NewServer(router)
	defer server.Close()

	executor := &guardTestExecutor{}
	base := server.URL + "/api/v1/workers/runtime-1"
	client, err := httpguard.New(httpguard.Config{
		HeartbeatURL: base + "/heartbeat", InventoryURL: base + "/inventory",
		CommandURL: base + "/commands/next", CommandResultURL: base + "/commands/result",
		AgentToken: token, RuntimeAgentID: "runtime-1", TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", ServerID: "server-1",
		Collector: guardTestCollector{}, CommandExecutor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	journalDir := t.TempDir()
	client.EnableDurableOperations(httpguard.DurableOptions{Dir: journalDir})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	digest := strings.Repeat("a", 64)
	command := &agentpb.StackKitCommand{
		CommandId: "guard-e2e-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS,
		WorkingDirectory: "/srv/stack", StackkitInstanceId: "stackkit-main", ServiceKey: "base", LogTail: 100,
		Release: &agentpb.StackKitReleasePin{Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
	}
	result, err := hub.SendStackKitCommandForTenant(ctx, "tenant-1", "runtime-1", command)
	if err != nil || !result.GetSuccess() {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if executor.runs.Load() != 1 {
		t.Fatalf("executor ran %d times", executor.runs.Load())
	}
	var revision int
	var contract string
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', 'tenant-1', true)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(t.Context(), `SELECT result_revision, contract_version FROM typed_agent_commands WHERE command_id = 'guard-e2e-1'`).Scan(&revision, &contract); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || contract != "guard.command-binding/v1" {
		t.Fatalf("committed revision = %d under contract %q", revision, contract)
	}
	cancel()
	var reopened *journal.Store
	for deadline := time.Now().Add(5 * time.Second); ; {
		// The Guard releases the journal when its control loop stops.
		if reopened, err = journal.Open(journalDir, journal.Options{}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer reopened.Close()
	if entry, ok := reopened.Entry("guard-e2e-1"); !ok || entry.State != journal.StateAcked {
		t.Fatalf("journal entry = %+v", entry)
	}
}
