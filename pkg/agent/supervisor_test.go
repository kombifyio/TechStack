package agent

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// stubCore is a minimal in-proc AgentService used to exercise the Supervisor's
// reconnect behavior. It can be stopped and restarted on fresh bufconn
// listeners; the client's dialer always resolves the current listener.
type stubCore struct {
	agentpb.UnimplementedAgentServiceServer

	registers      atomic.Int32
	heartbeats     atomic.Int32
	failHeartbeats atomic.Bool
	commandStreams atomic.Int32

	mu       sync.Mutex
	listener *bufconn.Listener
	server   *grpc.Server
}

func (s *stubCore) Register(ctx context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error) {
	s.registers.Add(1)
	return &agentpb.RegisterResponse{Accepted: true, AssignedId: req.AgentId}, nil
}

func (s *stubCore) Heartbeat(ctx context.Context, req *agentpb.HeartbeatRequest) (*agentpb.HeartbeatResponse, error) {
	if s.failHeartbeats.Load() {
		return nil, fmt.Errorf("injected heartbeat failure")
	}
	s.heartbeats.Add(1)
	return &agentpb.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *stubCore) CommandStream(stream agentpb.AgentService_CommandStreamServer) error {
	identity, err := stream.Recv()
	if err != nil {
		return err
	}
	if identity.AgentId != "test-agent" {
		return fmt.Errorf("command stream agent_id = %q", identity.AgentId)
	}
	s.commandStreams.Add(1)
	// Block until the client goes away or the server stops.
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestClientIdentifiesCommandStreamBeforeReceivingCommands(t *testing.T) {
	core := &stubCore{}
	core.start(t)
	defer core.stop()
	client := newTestClient(t, core)
	if err := client.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Register(t.Context()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- client.HandleCommands(ctx) }()
	waitFor(t, time.Second, func() bool { return core.commandStreams.Load() == 1 }, "identified command stream")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleCommands did not stop after cancellation")
	}
}

func (s *stubCore) ReportStatus(ctx context.Context, req *agentpb.StatusReport) (*agentpb.StatusAck, error) {
	return &agentpb.StatusAck{Received: true}, nil
}

// start brings the stub up on a fresh bufconn listener.
func (s *stubCore) start(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.listener = bufconn.Listen(1024 * 1024)
	s.server = grpc.NewServer()
	agentpb.RegisterAgentServiceServer(s.server, s)
	go func(srv *grpc.Server, lis *bufconn.Listener) {
		_ = srv.Serve(lis)
	}(s.server, s.listener)
}

// stop kills the server and its listener, severing all live connections.
func (s *stubCore) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		s.server.Stop()
		s.server = nil
	}
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
}

// dial resolves the current listener, so a client re-dialing after a restart
// reaches the new instance.
func (s *stubCore) dial(ctx context.Context, _ string) (net.Conn, error) {
	s.mu.Lock()
	lis := s.listener
	s.mu.Unlock()
	if lis == nil {
		return nil, fmt.Errorf("stub core is down")
	}
	return lis.DialContext(ctx)
}

func newTestClient(t *testing.T, core *stubCore) *Client {
	t.Helper()
	return &Client{
		coreAddr: "passthrough:///bufnet",
		agentID:  "test-agent",
		hostname: "test-host",
		version:  "test",
		commands: make(chan Command, 10),
		results:  make(chan CommandResult, 10),
		log:      nopLogger,
		dialOptsOverride: []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(core.dial),
		},
	}
}

func newTestSupervisor(t *testing.T, client *Client) *Supervisor {
	t.Helper()
	sup, err := NewSupervisor(SupervisorConfig{
		Client:            client,
		Backoff:           &Backoff{Base: 10 * time.Millisecond, Factor: 2, Cap: 50 * time.Millisecond},
		HeartbeatInterval: 20 * time.Millisecond,
		StableAfter:       time.Hour, // keep attempt counting deterministic
		Router:            routerFunc(func(ctx context.Context, cmd Command) CommandResult { return CommandResult{} }),
	})
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	return sup
}

type routerFunc func(ctx context.Context, cmd Command) CommandResult

func (f routerFunc) Route(ctx context.Context, cmd Command) CommandResult { return f(ctx, cmd) }

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v: %s", timeout, msg)
}

// TestSupervisorReconnectsAfterCoreRestart is the core Wave-0 guarantee: kill
// the Core, bring it back, and the agent re-registers without human help.
func TestSupervisorReconnectsAfterCoreRestart(t *testing.T) {
	core := &stubCore{}
	core.start(t)
	defer core.stop()

	client := newTestClient(t, core)
	sup := newTestSupervisor(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	waitFor(t, 5*time.Second, func() bool { return core.registers.Load() >= 1 },
		"first registration")
	waitFor(t, 5*time.Second, func() bool { return core.heartbeats.Load() >= 1 },
		"first heartbeat")

	// Kill the Core: all live connections sever, the session dies.
	core.stop()
	// Restart on a fresh listener: the supervisor must find it via backoff.
	core.start(t)

	waitFor(t, 10*time.Second, func() bool { return core.registers.Load() >= 2 },
		"re-registration after core restart")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not exit after context cancel")
	}
}

// TestSupervisorSurvivesCoreDown verifies the loop keeps retrying while Core
// is unreachable instead of exiting.
func TestSupervisorSurvivesCoreDown(t *testing.T) {
	core := &stubCore{}
	// Never started: every dial fails.

	client := newTestClient(t, core)
	sup := newTestSupervisor(t, client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	waitFor(t, 5*time.Second, func() bool { return sup.backoff.Attempt() >= 3 },
		"multiple reconnect attempts while core is down")

	// Bring the Core up: the supervisor must converge.
	core.start(t)
	defer core.stop()
	waitFor(t, 10*time.Second, func() bool { return core.registers.Load() >= 1 },
		"registration once core becomes reachable")

	cancel()
	<-done
}

// TestHeartbeatAbortsAfterConsecutiveFailures verifies the half-open-link
// backstop: persistent heartbeat failures end the session with an error.
func TestHeartbeatAbortsAfterConsecutiveFailures(t *testing.T) {
	core := &stubCore{}
	core.start(t)
	defer core.stop()
	core.failHeartbeats.Store(true)

	client := newTestClient(t, core)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.reset()

	err := client.StartHeartbeat(ctx, 10*time.Millisecond)
	if err == nil {
		t.Fatal("StartHeartbeat returned nil, want consecutive-failure error")
	}
	if ctx.Err() != nil {
		t.Fatalf("heartbeat did not abort before context timeout: %v", err)
	}
}

// TestClientResetAllowsRedial covers the reset() contract: after reset the
// client re-dials and re-registers, and command channels stay usable.
func TestClientResetAllowsRedial(t *testing.T) {
	core := &stubCore{}
	core.start(t)
	defer core.stop()

	client := newTestClient(t, core)
	ctx := context.Background()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect 1: %v", err)
	}
	if err := client.Register(ctx); err != nil {
		t.Fatalf("Register 1: %v", err)
	}

	client.reset()
	if client.IsConnected() {
		t.Fatal("IsConnected() = true after reset")
	}
	if client.IsRegistered() {
		t.Fatal("IsRegistered() = true after reset")
	}

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect 2 after reset: %v", err)
	}
	if err := client.Register(ctx); err != nil {
		t.Fatalf("Register 2 after reset: %v", err)
	}
	if got := core.registers.Load(); got != 2 {
		t.Fatalf("register count = %d, want 2", got)
	}

	// Channels survived the reset.
	client.commands <- Command{ID: "c1"}
	if cmd := <-client.Commands(); cmd.ID != "c1" {
		t.Fatalf("command channel broken after reset")
	}
	client.reset()
	_ = client.Close()
}
