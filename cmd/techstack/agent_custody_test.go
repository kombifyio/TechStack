package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
)

// The actual daemon denies unsafe custody and revokes only its admitted node,
// even when the configured state pathname is replaced after admission.
func TestGuardDaemonKeepsLifecycleInAdmittedCustody(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent := t.TempDir()
	if runtime.GOOS != "windows" {
		unsafe := filepath.Join(parent, "unsafe")
		if err := os.Mkdir(unsafe, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafe, 0777); err != nil {
			t.Fatal(err)
		}
		if err := runHTTPAgentMode(context.Background(), &agentModeConfig{stateDir: unsafe}, log); !errors.Is(err, privatefs.ErrUnsafeCustody) {
			t.Fatalf("unsafe custody was downgraded into legacy runtime: %v", err)
		}
	}
	dir := filepath.Join(parent, "state")
	manager, err := hostidentity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	root, err := manager.OpenCustodyRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	credential := []byte(`{"runtime_agent_id":"synthetic-node","agent_token":"original-synthetic-credential","heartbeat_url":"https://synthetic.invalid/heartbeat","inventory_url":"https://synthetic.invalid/inventory"}`)
	if err := privatefs.Replace(root, hostidentity.EnrollmentFileName, credential, 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "admitted")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, "outside")
	if err := os.MkdirAll(filepath.Join(other, "telemetry-spool"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(other, "telemetry-spool", "state.json")
	sentinel := []byte("unrelated custody")
	if err := os.WriteFile(outside, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	// The heartbeat producer must create its child beneath the admitted root,
	// rather than redirecting through cfg.stateDir's replaced ancestor.
	t.Setenv(guardOTLPGRPCPortEnv, "0")
	t.Setenv(guardOTLPHTTPPortEnv, "0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &agentModeConfig{stateDir: dir, enrollmentFile: filepath.Join(dir, hostidentity.EnrollmentFileName)}
	enrollment, err := resolveHTTPAgentEnrollmentWithIdentity(cfg, manager)
	if err != nil || enrollment.AgentToken != "original-synthetic-credential" {
		t.Fatalf("daemon did not read admitted credential: %v", err)
	}
	startGuardTelemetry(ctx, cfg, root, agentEnrollment{HeartbeatURL: "https://synthetic.invalid/heartbeat", AgentToken: "synthetic", TenantID: "synthetic"}, guardIdentityRuntime{}, guardExecution{}, log)
	spool, err := guardtelemetry.OpenSpoolRoot(root, "telemetry-spool", guardtelemetry.SpoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := spool.Append(time.Now(), []guardtelemetry.Sample{{Name: "host_cpu_usage_percent", Value: 1}})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: filepath.Join(moved, "telemetry-spool")})
	if err != nil || reopened.LastSequence() != record.Sequence {
		t.Fatalf("child spool left admitted custody: %v", err)
	}
	store, err := journal.OpenRoot(root, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	intent := journal.Intent{CommandID: "pending", TenantID: "synthetic", WorkerID: "synthetic", Operation: "STACKKIT_OPERATION_APPLY", Mutating: true, Generation: 1, PayloadDigest: "synthetic", ContractVersion: "guard.command-binding/v1", AuthorityExpiresAt: time.Now().Add(time.Minute), ResourceKey: "synthetic/node"}
	if _, _, err := store.Admit(intent); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetState(hostidentity.StateRevoked, "synthetic revocation"); err != nil {
		t.Fatal(err)
	}
	decommissionRevokedGuard(manager, cfg, log)
	if got, err := os.ReadFile(outside); err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("daemon changed replacement custody: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "telemetry-spool")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revocation retained node telemetry: %v", err)
	}
	store, err = journal.OpenRoot(root, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok := store.Entry("pending"); !ok {
		t.Fatal("revocation lost unacknowledged node evidence")
	}
}
