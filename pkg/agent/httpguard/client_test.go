package httpguard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

type staticCollector struct {
	snapshot Snapshot
	err      error
}

func (c staticCollector) Collect(context.Context) (Snapshot, error) {
	return c.snapshot, c.err
}

type stagedFailingCollector struct{}

type fakeTypedExecutor struct {
	commands chan *agentpb.StackKitCommand
}

func (e fakeTypedExecutor) Execute(_ context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	e.commands <- command
	return &agentpb.StackKitResult{CommandId: command.GetCommandId(), Success: true}
}

func (stagedFailingCollector) Collect(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("single-stage collection must not be used")
}

func (stagedFailingCollector) CollectHostSnapshot(context.Context) (Snapshot, error) {
	return Snapshot{
		Host: Host{Hostname: "real-host", UptimeSeconds: 42},
	}, nil
}

func (stagedFailingCollector) CollectInventory(context.Context, Snapshot) (Snapshot, error) {
	return Snapshot{}, errors.New("invalid access manifest")
}

func TestClientPublishesAuthenticatedHeartbeatAndInventory(t *testing.T) {
	var mu sync.Mutex
	var firstHeartbeatCalls atomic.Int32
	requests := make([]struct {
		path   string
		auth   string
		tenant string
		body   map[string]any
	}, 0, 2)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/inventory" && firstHeartbeatCalls.Load() != 1 {
			t.Errorf("inventory published before first-heartbeat callback: calls=%d", firstHeartbeatCalls.Load())
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		mu.Lock()
		requests = append(requests, struct {
			path   string
			auth   string
			tenant string
			body   map[string]any
		}{path: r.URL.Path, auth: r.Header.Get("Authorization"), tenant: r.Header.Get("X-Kombify-Tenant-ID"), body: payload})
		if len(requests) == 2 {
			close(done)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer server.Close()

	client, err := New(Config{
		HeartbeatURL:   server.URL + "/heartbeat",
		InventoryURL:   server.URL + "/inventory",
		AgentToken:     "secret-token",
		RuntimeAgentID: "runtime-1",
		ServerID:       "server-1",
		StackID:        "stack-1",
		TenantID:       "tenant-1",
		AgentVersion:   "test-version",
		Interval:       time.Hour,
		Collector: staticCollector{snapshot: Snapshot{
			Host:             Host{Hostname: "real-host", CPUPercent: 12.5, MemoryTotalBytes: 1024, UptimeSeconds: 99},
			Services:         []Service{},
			ManifestObserved: true,
		}},
		RuntimeConvergence: func() runtimeconvergence.Snapshot {
			return runtimeconvergence.Aggregate(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
				runtimeconvergence.Component{Name: runtimeconvergence.TechstackRuntimeComponent, State: runtimeconvergence.ComponentReady, ObservedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)},
				runtimeconvergence.Component{Name: runtimeconvergence.StackKitsRuntimeComponent, State: runtimeconvergence.ComponentReady, ObservedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)},
			)
		},
		OnFirstHeartbeat: func() { firstHeartbeatCalls.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- client.Run(ctx) }()
	select {
	case <-done:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("guard did not publish an initial cycle")
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if firstHeartbeatCalls.Load() != 1 {
		t.Fatalf("first heartbeat callbacks = %d, want 1", firstHeartbeatCalls.Load())
	}
	if requests[0].path != "/heartbeat" || requests[1].path != "/inventory" {
		t.Fatalf("paths = %q, %q", requests[0].path, requests[1].path)
	}
	for _, request := range requests {
		if request.auth != "Bearer secret-token" {
			t.Fatalf("Authorization = %q", request.auth)
		}
		if request.tenant != "tenant-1" {
			t.Fatalf("X-Kombify-Tenant-ID = %q", request.tenant)
		}
	}
	if requests[1].body["runtime_agent_id"] != "runtime-1" || requests[1].body["server_id"] != "server-1" {
		t.Fatalf("inventory identity = %#v", requests[1].body)
	}
	if requests[1].body["stack_id"] != "stack-1" || requests[1].body["agent_version"] != "test-version" {
		t.Fatalf("inventory placement/version = %#v", requests[1].body)
	}
	if requests[1].body["manifest_observed"] != true {
		t.Fatalf("inventory manifest authority = %#v", requests[1].body)
	}
	convergence, ok := requests[0].body["runtime_convergence"].(map[string]any)
	if !ok || convergence["state"] != runtimeconvergence.StateReady || convergence["observed_at"] == "" {
		t.Fatalf("heartbeat convergence = %#v", requests[0].body["runtime_convergence"])
	}
	if _, leaked := convergence["error"]; leaked {
		t.Fatalf("heartbeat convergence leaked raw error: %#v", convergence)
	}
}

func TestClientPollsExecutesAndReturnsTypedCommand(t *testing.T) {
	var mu sync.Mutex
	pollCount := 0
	resultDone := make(chan *agentpb.StackKitResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("X-Kombify-Tenant-ID") != "tenant-1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/commands/next":
			mu.Lock()
			pollCount++
			current := pollCount
			mu.Unlock()
			if current > 1 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"command":{"command_id":"command-1","operation":2}}}`))
		case "/commands/result":
			var request controlRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			resultDone <- request.Result
			w.WriteHeader(http.StatusAccepted)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		}
	}))
	defer server.Close()

	executed := make(chan *agentpb.StackKitCommand, 1)
	client, err := New(Config{
		HeartbeatURL: server.URL + "/heartbeat", InventoryURL: server.URL + "/inventory",
		CommandURL: server.URL + "/commands/next", CommandResultURL: server.URL + "/commands/result",
		AgentToken: "secret-token", RuntimeAgentID: "runtime-1", ServerID: "server-1",
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", LeaseID: "lease-1",
		Interval: time.Hour, Collector: staticCollector{}, CommandExecutor: fakeTypedExecutor{commands: executed},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case command := <-executed:
		if command.GetCommandId() != "command-1" {
			t.Fatalf("command = %#v", command)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("typed command was not executed")
	}
	select {
	case result := <-resultDone:
		if result == nil || result.GetCommandId() != "command-1" || !result.GetSuccess() {
			t.Fatalf("result = %#v", result)
		}
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("typed result was not returned")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientRetriesRetryableStatusWithinBound(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{
		HeartbeatURL:   server.URL + "/heartbeat",
		InventoryURL:   server.URL + "/inventory",
		AgentToken:     "token",
		RuntimeAgentID: "runtime-1",
		RetryAttempts:  3,
		Collector:      staticCollector{snapshot: Snapshot{Host: Host{}, Services: []Service{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.sendCycle(t.Context()); err != nil {
		t.Fatalf("sendCycle returned error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("HTTP calls = %d, want 3 (heartbeat retry + inventory)", calls)
	}
}

func TestClientPublishesHeartbeatBeforeFailingServiceInventory(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{
		HeartbeatURL:   server.URL + "/heartbeat",
		InventoryURL:   server.URL + "/inventory",
		AgentToken:     "token",
		RuntimeAgentID: "runtime-1",
		Collector:      stagedFailingCollector{},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.sendCycle(t.Context())
	if err == nil || !strings.Contains(err.Error(), "collect service inventory after heartbeat") {
		t.Fatalf("sendCycle error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/heartbeat" {
		t.Fatalf("request paths = %#v, want heartbeat before inventory failure", paths)
	}
}

func TestClientRejectsRemoteClearTextChannel(t *testing.T) {
	_, err := New(Config{
		HeartbeatURL:   "http://192.0.2.10/heartbeat",
		InventoryURL:   "https://techstack.example/inventory",
		AgentToken:     "token",
		RuntimeAgentID: "runtime-1",
		Collector:      staticCollector{},
	})
	if err == nil {
		t.Fatal("New accepted a remote clear-text heartbeat URL")
	}
}

func TestClientAllowsExplicitPrivateLANClearTextChannel(t *testing.T) {
	_, err := New(Config{
		HeartbeatURL: "http://192.168.10.2:5264/heartbeat", InventoryURL: "http://192.168.10.2:5264/inventory",
		AgentToken: "token", RuntimeAgentID: "runtime-1", Collector: staticCollector{}, PrivateLANHTTPOrigin: "http://192.168.10.2:5264",
	})
	if err != nil {
		t.Fatalf("New rejected explicitly enrolled private-LAN HTTP: %v", err)
	}
}
