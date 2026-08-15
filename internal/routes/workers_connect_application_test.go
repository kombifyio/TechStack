package routes

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

type failingServerEnrollmentStore struct {
	*controlplane.MemoryStore
	err error
}

func (s *failingServerEnrollmentStore) ApplyServerEnrollment(
	context.Context,
	controlplane.ServerEnrollment,
) (*controlplane.ServerEventResult, error) {
	return nil, s.err
}

func TestConnectApplicationPreservesObservedWorkerOnCredentialRenewalAndServerRepair(t *testing.T) {
	for _, tc := range []struct {
		name             string
		serverPresent    bool
		rotateCredential bool
	}{
		{name: "credential renewal", serverPresent: true, rotateCredential: true},
		{name: "missing server projection repair", serverPresent: false, rotateCredential: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			observedAt := time.Date(2026, 7, 23, 9, 0, 0, 0, time.UTC)
			if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
				ID: "runtime-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
				Hostname: "observed-host", IP: "192.0.2.10", OS: "ubuntu", Arch: "amd64",
				Status: "connected", Approved: true, ApprovedAt: &observedAt, LastSeenAt: &observedAt,
				CPUCores: 8, RAMMB: 16384, DiskGB: 256, Type: "runtime", Provider: "local",
				Capabilities: map[string]any{
					"server_id": "server-1", "runtime_agent_id": "runtime-1",
					"agent_version": "0.5.2", "observed_capability": "keep",
				},
				Resources: map[string]any{"observed_host": map[string]any{"uptime": 42}},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.serverPresent {
				if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
					ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
					WorkerID: "runtime-1", NodeID: "server-1", Name: "Observed server", ProviderRef: "local",
					LifecycleState:  string(serverregistry.LifecycleActive),
					DesiredState:    string(serverregistry.DesiredRunning),
					ConnectionState: string(serverregistry.ConnectionConnected),
					HealthState:     string(serverregistry.HealthHealthy),
					LastHeartbeatAt: &observedAt, InventoryRevision: 7,
				}); err != nil {
					t.Fatal(err)
				}
			}

			h := workerRouteHandlers{
				wst: store, serverStore: store,
				credentialSecret: []byte("test-worker-credential-secret"),
			}
			result, err := h.connectServerApplication(t.Context(), workerConnectCommand{
				TenantID: "tenant-1", OwnerID: "owner-1",
				Request: workerConnectRequest{
					ServerID: "server-1", RuntimeAgentID: "runtime-1", StackID: "stack-1",
					Hostname: "declared-host", Mode: "advanced", ConnectionMode: "agent", Provider: "local",
				},
				EnrollmentSource: "test-connect",
				IdempotencyKey:   "test-connect-credential-1",
				CredentialPolicy: workerConnectCredentialStrict,
			})
			if err != nil {
				t.Fatalf("connectServerApplication: %v", err)
			}
			if !result.CredentialIssued || result.Enrollment.AgentToken == "" {
				t.Fatalf("repair/renewal did not issue one replacement credential: %#v", result)
			}
			worker, err := store.GetWorker(t.Context(), "tenant-1", "runtime-1")
			if err != nil {
				t.Fatal(err)
			}
			if worker.Status != "connected" || worker.LastSeenAt == nil || !worker.LastSeenAt.Equal(observedAt) ||
				worker.Hostname != "observed-host" || worker.IP != "192.0.2.10" ||
				worker.CPUCores != 8 || worker.RAMMB != 16384 || worker.DiskGB != 256 {
				t.Fatalf("credential operation erased observed worker state: %#v", worker)
			}
			if worker.Capabilities["agent_version"] != "0.5.2" ||
				worker.Capabilities["observed_capability"] != "keep" ||
				worker.Resources["observed_host"] == nil ||
				stringFromAny(worker.Resources["agent_token_sha256"]) == "" {
				t.Fatalf("credential operation erased capabilities/resources or omitted digest: %#v", worker)
			}
			server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.serverPresent {
				if server.HealthState != string(serverregistry.HealthHealthy) ||
					server.ConnectionState != string(serverregistry.ConnectionConnected) ||
					server.LastHeartbeatAt == nil || !server.LastHeartbeatAt.Equal(observedAt) ||
					server.InventoryRevision != 7 {
					t.Fatalf("credential renewal mutated observed server health: %#v", server)
				}
			} else if server.HealthState != string(serverregistry.HealthUnknown) ||
				server.ConnectionState != string(serverregistry.ConnectionPending) ||
				server.LastHeartbeatAt != nil {
				t.Fatalf("server repair invented observation state: %#v", server)
			}
		})
	}
}

func TestConnectApplicationPersistsCredentialOnlyAfterServerProjection(t *testing.T) {
	const (
		tenantID       = "tenant-1"
		ownerID        = "owner-1"
		stackID        = "stack-1"
		serverID       = "server-1"
		runtimeAgentID = "runtime-1"
		oldDigest      = "old-digest"
	)
	projectionErr := errors.New("server projection failed")
	command := workerConnectCommand{
		TenantID: tenantID,
		OwnerID:  ownerID,
		Request: workerConnectRequest{
			ServerID: serverID, RuntimeAgentID: runtimeAgentID, StackID: stackID,
			Hostname: "declared-host", OS: "ubuntu", Arch: "amd64",
			Mode: "advanced", ConnectionMode: "agent", Provider: "local",
		},
		EnrollmentSource: "test-connect",
		IdempotencyKey:   "test-connect-repair-1",
		CredentialPolicy: workerConnectCredentialStrict,
	}

	t.Run("existing digest survives projection failure", func(t *testing.T) {
		store := controlplane.NewMemoryStore()
		if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
			ID: runtimeAgentID, TenantID: tenantID, StackID: stackID, OwnerSubjectID: ownerID,
			Hostname: "observed-host", Status: "connected", Approved: true, Type: "runtime",
			Capabilities: map[string]any{
				"server_id": serverID, "runtime_agent_id": runtimeAgentID,
			},
			Resources: map[string]any{"agent_token_sha256": oldDigest},
		}); err != nil {
			t.Fatal(err)
		}
		h := workerRouteHandlers{
			wst:              store,
			credentialSecret: []byte("test-worker-credential-secret"),
			serverStore: &failingServerEnrollmentStore{
				MemoryStore: store,
				err:         projectionErr,
			},
		}
		if _, err := h.connectServerApplication(t.Context(), command); !errors.Is(err, projectionErr) {
			t.Fatalf("connectServerApplication error = %v, want %v", err, projectionErr)
		}
		worker, err := store.GetWorker(t.Context(), tenantID, runtimeAgentID)
		if err != nil {
			t.Fatal(err)
		}
		if digest := stringFromAny(worker.Resources["agent_token_sha256"]); digest != oldDigest {
			t.Fatalf("projection failure changed existing digest: got %q want %q", digest, oldDigest)
		}
	})

	t.Run("tokenless partial worker is repairable on retry", func(t *testing.T) {
		store := controlplane.NewMemoryStore()
		h := workerRouteHandlers{
			wst:              store,
			credentialSecret: []byte("test-worker-credential-secret"),
			serverStore: &failingServerEnrollmentStore{
				MemoryStore: store,
				err:         projectionErr,
			},
		}
		if _, err := h.connectServerApplication(t.Context(), command); !errors.Is(err, projectionErr) {
			t.Fatalf("connectServerApplication error = %v, want %v", err, projectionErr)
		}
		partialWorker, err := store.GetWorker(t.Context(), tenantID, runtimeAgentID)
		if err != nil {
			t.Fatalf("missing declarative partial worker: %v", err)
		}
		if digest := stringFromAny(partialWorker.Resources["agent_token_sha256"]); digest != "" {
			t.Fatalf("partial worker persisted credential before server projection: %q", digest)
		}
		if _, err := store.GetServerRuntime(t.Context(), tenantID, serverID); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatalf("failed projection unexpectedly created server: %v", err)
		}

		retry := workerRouteHandlers{
			wst: store, serverStore: store,
			credentialSecret: []byte("test-worker-credential-secret"),
		}
		result, err := retry.connectServerApplication(t.Context(), command)
		if err != nil {
			t.Fatalf("retry connectServerApplication: %v", err)
		}
		if !result.CredentialIssued || result.Enrollment.AgentToken == "" {
			t.Fatalf("retry did not issue recoverable credential: %#v", result)
		}
		repairedWorker, err := store.GetWorker(t.Context(), tenantID, runtimeAgentID)
		if err != nil {
			t.Fatal(err)
		}
		wantDigest := workerauth.SHA256Hex(result.Enrollment.AgentToken)
		if digest := stringFromAny(repairedWorker.Resources["agent_token_sha256"]); digest != wantDigest {
			t.Fatalf("retry digest = %q, want hash of returned credential %q", digest, wantDigest)
		}
		if _, err := store.GetServerRuntime(t.Context(), tenantID, serverID); err != nil {
			t.Fatalf("retry did not repair server projection: %v", err)
		}
	})
}

func TestConnectApplicationAtomicallyClaimsOneWorkerBinding(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		store := controlplane.NewMemoryStore()
		h := workerRouteHandlers{
			wst: store, serverStore: store,
			credentialSecret: []byte("test-worker-credential-secret"),
		}
		commands := []workerConnectCommand{
			{
				TenantID: "tenant-1", OwnerID: "owner-1",
				Request: workerConnectRequest{
					ServerID: "server-a", RuntimeAgentID: "shared-runtime", StackID: "stack-a",
					Hostname: "host-a", OS: "ubuntu", Arch: "amd64", Provider: "local",
				},
				EnrollmentSource: "concurrency-test", IdempotencyKey: "binding-attempt-a",
				CredentialPolicy: workerConnectCredentialStrict,
			},
			{
				TenantID: "tenant-1", OwnerID: "owner-1",
				Request: workerConnectRequest{
					ServerID: "server-b", RuntimeAgentID: "shared-runtime", StackID: "stack-b",
					Hostname: "host-b", OS: "debian", Arch: "arm64", Provider: "cloud",
				},
				EnrollmentSource: "concurrency-test", IdempotencyKey: "binding-attempt-b",
				CredentialPolicy: workerConnectCredentialStrict,
			},
		}
		type outcome struct {
			index  int
			result *workerConnectResult
			err    error
		}
		start := make(chan struct{})
		outcomes := make(chan outcome, len(commands))
		var group sync.WaitGroup
		for index := range commands {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				result, err := h.connectServerApplication(t.Context(), commands[index])
				outcomes <- outcome{index: index, result: result, err: err}
			}(index)
		}
		close(start)
		group.Wait()
		close(outcomes)

		winner := -1
		conflicts := 0
		for result := range outcomes {
			switch {
			case result.err == nil:
				if winner != -1 {
					t.Fatalf("iteration %d produced two successful bindings", iteration)
				}
				winner = result.index
				if result.result == nil || result.result.Enrollment.AgentToken == "" {
					t.Fatalf("iteration %d winner omitted credential: %#v", iteration, result.result)
				}
			case errors.Is(result.err, controlplane.ErrConflict):
				conflicts++
			default:
				t.Fatalf("iteration %d unexpected connect error: %v", iteration, result.err)
			}
		}
		if winner == -1 || conflicts != 1 {
			t.Fatalf("iteration %d winner=%d conflicts=%d", iteration, winner, conflicts)
		}

		winningRequest := commands[winner].Request
		losingRequest := commands[1-winner].Request
		worker, err := store.GetWorker(t.Context(), "tenant-1", "shared-runtime")
		if err != nil {
			t.Fatalf("iteration %d get winning worker: %v", iteration, err)
		}
		if worker.StackID != winningRequest.StackID ||
			stringFromAny(worker.Capabilities["server_id"]) != winningRequest.ServerID ||
			worker.LastSeenAt != nil {
			t.Fatalf("iteration %d worker binding/heartbeat = %#v", iteration, worker)
		}
		if server := serverRegistryRecordFromWorkerStore(*worker); server.RolloutReady || server.HealthState == "healthy" {
			t.Fatalf("iteration %d declarative worker was projected healthy: %#v", iteration, server)
		}
		if _, err := store.GetServerRuntime(t.Context(), "tenant-1", winningRequest.ServerID); err != nil {
			t.Fatalf("iteration %d winning server missing: %v", iteration, err)
		}
		if _, err := store.GetServerRuntime(t.Context(), "tenant-1", losingRequest.ServerID); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatalf("iteration %d losing server was projected: %v", iteration, err)
		}
	}
}
