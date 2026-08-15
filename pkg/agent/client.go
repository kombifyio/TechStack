// Package agent provides the agent-side gRPC client for communication with Core.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Version is the agent version (set at build time).
var Version = "dev"

// nopHandler is a handler that discards all log records.
type nopHandler struct{}

func (nopHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (nopHandler) Handle(context.Context, slog.Record) error { return nil }
func (h nopHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h nopHandler) WithGroup(string) slog.Handler           { return h }

var nopLogger = slog.New(nopHandler{})

// Client handles communication with the kombifyTechstack Core.
type Client struct {
	coreAddr   string
	agentID    string
	hostname   string
	version    string
	tlsConfig  *tls.Config
	registered bool

	// gRPC connection and client
	conn   *grpc.ClientConn
	client agentpb.AgentServiceClient

	// Agent configuration from Core
	config *agentpb.AgentConfig

	// Channels for command handling
	commands         chan Command
	results          chan CommandResult
	stackKitResults  chan *agentpb.StackKitResult
	stackKitLogs     chan *agentpb.LogEntry
	stackKitExecutor *StackKitExecutor

	// resourceProvider, when set, supplies real host resource usage for
	// heartbeats (see ResourceSampler). Without it the heartbeat falls back
	// to process-local proxy metrics.
	resourceProvider func() *agentpb.ResourceUsage

	// lastHeartbeatAt is the wall time of the last acknowledged heartbeat.
	lastHeartbeatAt time.Time

	// dialOptsOverride replaces the mTLS dial options entirely when set.
	// Test-only hook (bufconn); never set in production paths.
	dialOptsOverride []grpc.DialOption

	// Synchronization
	mu     sync.RWMutex
	closed bool

	// Logger
	log *slog.Logger
}

// logger returns the client's logger or a nop logger if not set.
func (c *Client) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return nopLogger
}

// Config holds the agent client configuration.
type Config struct {
	CoreAddress string
	AgentID     string
	Hostname    string // Optional, auto-detected if empty
	CertFile    string // Agent certificate (for mTLS)
	KeyFile     string // Agent private key
	CAFile      string // Core CA certificate
	Logger      *slog.Logger
	// StackKitExecutor overrides the typed StackKits lifecycle adapter.
	// Production defaults to an executor backed by the local release pin/cache.
	StackKitExecutor *StackKitExecutor
}

// Command represents a command received from Core.
type Command struct {
	ID          string
	Type        string
	Command     string
	Args        []string
	Environment map[string]string
	WorkDir     string
	Timeout     time.Duration
}

// CommandResult represents the result of executing a command.
type CommandResult struct {
	CommandID  string
	ExitCode   int
	Stdout     string
	Stderr     string
	StartedAt  time.Time
	FinishedAt time.Time
	Error      error
}

// NewClient creates a new agent client with mTLS configuration.
func NewClient(cfg Config) (*Client, error) {
	// Validate configuration
	if cfg.CoreAddress == "" {
		return nil, fmt.Errorf("core address is required")
	}
	if cfg.AgentID == "" {
		return nil, fmt.Errorf("agent ID is required")
	}
	if cfg.CertFile == "" {
		return nil, fmt.Errorf("certificate file path is required")
	}
	if cfg.KeyFile == "" {
		return nil, fmt.Errorf("key file path is required")
	}
	if cfg.CAFile == "" {
		return nil, fmt.Errorf("CA file path is required")
	}

	tlsConfig, err := loadTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS config: %w", err)
	}

	// Auto-detect hostname if not provided
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	// Use provided logger or create default
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "agent-client", "agent_id", cfg.AgentID)
	stackKitExecutor := cfg.StackKitExecutor
	if stackKitExecutor == nil {
		stackKitExecutor = NewStackKitExecutorFromEnv()
	}

	return &Client{
		coreAddr:         cfg.CoreAddress,
		agentID:          cfg.AgentID,
		hostname:         hostname,
		version:          Version,
		tlsConfig:        tlsConfig,
		commands:         make(chan Command, 100),
		results:          make(chan CommandResult, 100),
		stackKitResults:  make(chan *agentpb.StackKitResult, 32),
		stackKitLogs:     make(chan *agentpb.LogEntry, 512),
		stackKitExecutor: stackKitExecutor,
		log:              log,
	}, nil
}

// loadTLSConfig creates mTLS configuration for secure agent communication.
func loadTLSConfig(cfg Config) (*tls.Config, error) {
	// Validate file paths exist before attempting to load
	if _, err := os.Stat(cfg.CertFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("certificate file not found: %s", cfg.CertFile)
	}
	if _, err := os.Stat(cfg.KeyFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("key file not found: %s", cfg.KeyFile)
	}
	if _, err := os.Stat(cfg.CAFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("CA file not found: %s", cfg.CAFile)
	}

	// Load agent certificate and key
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load agent certificate: %w", err)
	}

	// Load CA certificate to verify Core
	caCert, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate: invalid PEM data")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		MinVersion:   tls.VersionTLS13, // Enforce TLS 1.3
	}, nil
}

// Connect establishes gRPC connection to Core with mTLS.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil // Already connected
	}

	c.logger().Info("connecting_to_core", "address", c.coreAddr)

	// Create gRPC dial options with mTLS
	opts := c.dialOptsOverride
	if opts == nil {
		creds := credentials.NewTLS(c.tlsConfig)
		opts = []grpc.DialOption{
			grpc.WithTransportCredentials(creds),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                30 * time.Second, // Ping server every 30s
				Timeout:             10 * time.Second, // Wait 10s for pong
				PermitWithoutStream: true,             // Ping even without active streams
			}),
		}
	}

	// Establish connection
	conn, err := grpc.DialContext(ctx, c.coreAddr, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to core: %w", err)
	}

	c.conn = conn
	c.client = agentpb.NewAgentServiceClient(conn)

	c.logger().Info("connected_to_core", "address", c.coreAddr)
	return nil
}

// Register sends registration request to Core and receives agent configuration.
func (c *Client) Register(ctx context.Context) error {
	c.mu.Lock()
	if c.registered {
		c.mu.Unlock()
		return nil
	}
	client := c.client
	c.mu.Unlock()

	if client == nil {
		return fmt.Errorf("not connected to core")
	}

	c.logger().Info("registering_with_core",
		"hostname", c.hostname,
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
		"version", c.version,
	)

	capabilities := []string{
		string(CommandTypeHealthCheck),
		string(CommandTypeGetLogs),
		string(CommandTypeTofu),
		string(CommandTypeTerramate),
		string(CommandTypePulumiOperation),
		"status_report",
	}
	if c.stackKitExecutor.Available() {
		capabilities = append(capabilities, StackKitAgentCapability, stackkitcommand.ExpectedPlanHashCapability)
	}
	req := &agentpb.RegisterRequest{
		AgentId:      c.agentID,
		Hostname:     c.hostname,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Version:      c.version,
		Capabilities: capabilities,
	}

	resp, err := client.Register(ctx, req)
	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}

	if !resp.Accepted {
		return fmt.Errorf("registration rejected by core")
	}

	c.mu.Lock()
	c.registered = true
	c.config = resp.Config
	// Update agent ID if core assigned a different one
	if resp.AssignedId != "" && resp.AssignedId != c.agentID {
		c.logger().Info("agent_id_reassigned",
			"old_id", c.agentID,
			"new_id", resp.AssignedId,
		)
		c.agentID = resp.AssignedId
	}
	c.mu.Unlock()

	c.logger().Info("registered_with_core",
		"assigned_id", resp.AssignedId,
		"config_received", resp.Config != nil,
	)

	return nil
}

// SetResourceProvider installs a callback that supplies host resource usage
// for heartbeats. Pass nil to fall back to process-local proxy metrics.
func (c *Client) SetResourceProvider(fn func() *agentpb.ResourceUsage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resourceProvider = fn
}

// LastHeartbeatAt returns the wall time of the last acknowledged heartbeat
// (zero if none succeeded yet).
func (c *Client) LastHeartbeatAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastHeartbeatAt
}

// maxConsecutiveHeartbeatFailures aborts the heartbeat loop (and thereby the
// supervisor session) once the connection is evidently dead. The command
// stream usually notices first; this is the backstop for half-open links.
const maxConsecutiveHeartbeatFailures = 5

// StartHeartbeat begins sending periodic heartbeats to Core.
// Uses the interval from config or defaults to 30 seconds.
// Returns an error once maxConsecutiveHeartbeatFailures heartbeats in a row
// have failed, so callers can tear down and re-dial.
func (c *Client) StartHeartbeat(ctx context.Context, interval time.Duration) error {
	// Use config interval if available and no override specified
	if interval == 0 {
		c.mu.RLock()
		if c.config != nil && c.config.HeartbeatIntervalSeconds > 0 {
			interval = time.Duration(c.config.HeartbeatIntervalSeconds) * time.Second
		}
		c.mu.RUnlock()
	}
	if interval == 0 {
		interval = 30 * time.Second // Default
	}

	c.logger().Info("starting_heartbeat", "interval", interval.String())

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	consecutiveFailures := 0

	// Send initial heartbeat immediately
	if err := c.sendHeartbeat(ctx); err != nil {
		c.logger().Warn("initial_heartbeat_failed", "error", err.Error())
		consecutiveFailures++
	}

	for {
		select {
		case <-ctx.Done():
			c.logger().Info("heartbeat_stopped", "reason", ctx.Err().Error())
			return ctx.Err()
		case <-ticker.C:
			if err := c.sendHeartbeat(ctx); err != nil {
				consecutiveFailures++
				c.logger().Warn("heartbeat_failed",
					"error", err.Error(),
					"consecutive_failures", consecutiveFailures,
				)
				if consecutiveFailures >= maxConsecutiveHeartbeatFailures {
					return fmt.Errorf("heartbeat failed %d times in a row: %w", consecutiveFailures, err)
				}
				continue
			}
			consecutiveFailures = 0
		}
	}
}

func (c *Client) sendHeartbeat(ctx context.Context) error {
	c.mu.RLock()
	client := c.client
	agentID := c.agentID
	provider := c.resourceProvider
	c.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("not connected to core")
	}

	var resources *agentpb.ResourceUsage
	if provider != nil {
		resources = provider()
	}
	if resources == nil {
		// Fallback without a ResourceSampler: process-local proxy metrics.
		// CpuPercent carries the core count (true CPU% requires sampling over
		// time), memory is the Go heap, disk is the root filesystem.
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)

		// Disk stats (platform-specific, implemented in resource_*.go)
		diskUsed, diskTotal := getDiskUsage()

		resources = &agentpb.ResourceUsage{
			CpuPercent:       float64(runtime.NumCPU()),          // CPU core count as baseline metric
			MemoryUsedBytes:  clampUint64ToInt64(memStats.Alloc), // Bytes currently allocated on Go heap
			MemoryTotalBytes: clampUint64ToInt64(memStats.Sys),   // Total bytes obtained from OS for Go heap
			DiskUsedBytes:    clampUint64ToInt64(diskUsed),
			DiskTotalBytes:   clampUint64ToInt64(diskTotal),
		}
	}

	req := &agentpb.HeartbeatRequest{
		AgentId:       agentID,
		TimestampUnix: time.Now().Unix(),
		Resources:     resources,
	}

	resp, err := client.Heartbeat(ctx, req)
	if err != nil {
		return fmt.Errorf("heartbeat request failed: %w", err)
	}

	c.mu.Lock()
	c.lastHeartbeatAt = time.Now()
	c.mu.Unlock()

	if !resp.Acknowledged {
		c.logger().Warn("heartbeat_not_acknowledged")
	}

	// Process any pending commands returned with heartbeat
	for _, pending := range resp.PendingCommands {
		c.logger().Debug("pending_command_received",
			"command_id", pending.CommandId,
			"type", pending.Type,
		)
		// Queue command for processing
		cmd := Command{
			ID:   pending.CommandId,
			Type: pending.Type,
		}
		select {
		case c.commands <- cmd:
		default:
			c.logger().Warn("command_queue_full", "command_id", pending.CommandId)
		}
	}

	return nil
}

// HandleCommands establishes a bidirectional command stream with Core.
// This is a blocking call that processes commands until the context is canceled.
func (c *Client) HandleCommands(ctx context.Context) error {
	c.mu.RLock()
	client := c.client
	agentID := c.agentID
	c.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("not connected to core")
	}

	c.logger().Info("starting_command_stream")

	// Establish bidirectional stream
	stream, err := client.CommandStream(ctx)
	if err != nil {
		return fmt.Errorf("failed to open command stream: %w", err)
	}
	// Core authenticates the mTLS peer first, then requires the stream's first
	// application frame to bind that peer to the claimed agent ID before it
	// dequeues any command. Without this frame both sides wait on Recv forever.
	if err := stream.Send(&agentpb.AgentMessage{AgentId: agentID}); err != nil {
		return fmt.Errorf("failed to identify command stream: %w", err)
	}

	// Handle incoming commands in a separate goroutine
	errChan := make(chan error, 2)

	// Receive commands from Core
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					errChan <- nil
					return
				}
				errChan <- fmt.Errorf("stream receive error: %w", err)
				return
			}

			c.logger().Debug("command_received", "message_id", msg.MessageId)

			// Process based on payload type
			switch payload := msg.Payload.(type) {
			case *agentpb.CoreMessage_Execute:
				commandType := strings.ToLower(strings.TrimSpace(payload.Execute.Command))
				cmd := Command{
					ID:          payload.Execute.CommandId,
					Type:        commandType,
					Command:     commandType,
					Args:        payload.Execute.Args,
					Environment: payload.Execute.Environment,
					WorkDir:     payload.Execute.WorkingDirectory,
					Timeout:     time.Duration(payload.Execute.TimeoutSeconds) * time.Second,
				}
				select {
				case c.commands <- cmd:
					c.logger().Info("command_queued",
						"command_id", cmd.ID,
						"command", cmd.Command,
					)
				case <-ctx.Done():
					return
				}

			case *agentpb.CoreMessage_TofuCommand:
				cmd := Command{
					ID:      payload.TofuCommand.CommandId,
					Type:    string(CommandTypeTofu),
					Command: payload.TofuCommand.Operation.String(),
					WorkDir: payload.TofuCommand.WorkingDirectory,
					Timeout: time.Duration(payload.TofuCommand.TimeoutSeconds) * time.Second,
				}
				select {
				case c.commands <- cmd:
					c.logger().Info("tofu_command_queued", "command_id", cmd.ID, "operation", cmd.Command)
				case <-ctx.Done():
					return
				}

			case *agentpb.CoreMessage_TerramateCommand:
				cmd := Command{
					ID:      payload.TerramateCommand.CommandId,
					Type:    string(CommandTypeTerramate),
					Command: payload.TerramateCommand.Operation.String(),
					WorkDir: payload.TerramateCommand.WorkingDirectory,
					Timeout: time.Duration(payload.TerramateCommand.TimeoutSeconds) * time.Second,
				}
				select {
				case c.commands <- cmd:
					c.logger().Info("terramate_command_queued", "command_id", cmd.ID, "operation", cmd.Command)
				case <-ctx.Done():
					return
				}

			case *agentpb.CoreMessage_StackkitCommand:
				if payload.StackkitCommand == nil {
					c.logger().Warn("stackkit_command_rejected", "reason", "empty typed command")
					continue
				}
				command := payload.StackkitCommand
				go func() {
					result := c.stackKitExecutor.ExecuteStreaming(ctx, command, func(entry *agentpb.LogEntry) {
						select {
						case c.stackKitLogs <- entry:
						case <-ctx.Done():
						}
					})
					select {
					case c.stackKitResults <- result:
					case <-ctx.Done():
					}
				}()

			case *agentpb.CoreMessage_ConfigUpdate:
				c.logger().Info("config_update_received")
				c.mu.Lock()
				c.config = payload.ConfigUpdate.NewConfig
				c.mu.Unlock()

			case *agentpb.CoreMessage_Shutdown:
				c.logger().Warn("shutdown_requested",
					"reason", payload.Shutdown.Reason,
					"graceful", payload.Shutdown.Graceful,
				)
				errChan <- fmt.Errorf("shutdown requested: %s", payload.Shutdown.Reason)
				return
			}
		}
	}()

	// Send results back to Core
	go func() {
		for {
			select {
			case <-ctx.Done():
				errChan <- ctx.Err()
				return
			case result, ok := <-c.results:
				if !ok {
					errChan <- nil
					return
				}

				msg := &agentpb.AgentMessage{
					AgentId: agentID,
					Payload: &agentpb.AgentMessage_CommandResult{
						CommandResult: &agentpb.CommandResult{
							CommandId:      result.CommandID,
							ExitCode:       clampIntToInt32(result.ExitCode),
							Stdout:         result.Stdout,
							Stderr:         result.Stderr,
							StartedAtUnix:  result.StartedAt.Unix(),
							FinishedAtUnix: result.FinishedAt.Unix(),
						},
					},
				}

				if err := stream.Send(msg); err != nil {
					c.logger().Error("failed_to_send_result",
						"command_id", result.CommandID,
						"error", err.Error(),
					)
				} else {
					c.logger().Debug("result_sent", "command_id", result.CommandID)
				}
			case result := <-c.stackKitResults:
				if result == nil {
					continue
				}
				msg := &agentpb.AgentMessage{
					AgentId: agentID,
					Payload: &agentpb.AgentMessage_StackkitResult{
						StackkitResult: result,
					},
				}
				if err := stream.Send(msg); err != nil {
					c.logger().Error("failed_to_send_stackkit_result",
						"command_id", result.CommandId,
						"error", err.Error(),
					)
				} else {
					c.logger().Debug("stackkit_result_sent", "command_id", result.CommandId)
				}
			case entry := <-c.stackKitLogs:
				if entry == nil {
					continue
				}
				if err := stream.Send(&agentpb.AgentMessage{AgentId: agentID, Payload: &agentpb.AgentMessage_LogEntry{LogEntry: entry}}); err != nil {
					c.logger().Warn("failed_to_send_stackkit_log", "error", err.Error())
				}
			}
		}
	}()

	// Wait for either goroutine to finish
	err = <-errChan
	if err != nil && err != context.Canceled {
		c.logger().Error("command_stream_error", "error", err.Error())
	}
	return err
}

// Commands returns a channel of commands received from Core.
func (c *Client) Commands() <-chan Command {
	return c.commands
}

// SendResult sends a command execution result back to Core.
func (c *Client) SendResult(result CommandResult) {
	c.results <- result
}

// IsConnected returns whether the client is connected to Core.
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil
}

// IsRegistered returns whether the agent has been registered with Core.
func (c *Client) IsRegistered() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.registered
}

// AgentID returns the current agent ID.
func (c *Client) AgentID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.agentID
}

// reset tears down the gRPC connection so the client can re-dial, without
// closing the command/result channels — those outlive individual connections
// and are owned by Close. Used by the Supervisor between reconnect attempts.
func (c *Client) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			c.logger().Warn("connection_close_failed", "error", err.Error())
		}
		c.conn = nil
		c.client = nil
	}
	c.registered = false
}

// Close gracefully closes the connection to Core.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	c.logger().Info("closing_connection")

	// Close channels safely
	close(c.commands)
	close(c.results)

	// Close gRPC connection
	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			return fmt.Errorf("failed to close gRPC connection: %w", err)
		}
		c.conn = nil
		c.client = nil
	}

	return nil
}

// PushMetrics sends a batch of metric samples to Core (Monitoring 2.0).
func (c *Client) PushMetrics(ctx context.Context, samples []*agentpb.MetricSample) error {
	c.mu.RLock()
	client := c.client
	agentID := c.agentID
	c.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("not connected to core")
	}
	if len(samples) == 0 {
		return nil
	}

	batch := &agentpb.MetricsBatch{
		AgentId:       agentID,
		TimestampUnix: time.Now().UnixMilli(),
		Samples:       samples,
	}
	ack, err := client.PushMetrics(ctx, batch)
	if err != nil {
		return fmt.Errorf("push metrics failed: %w", err)
	}
	if ack != nil && !ack.Accepted {
		return fmt.Errorf("metrics batch rejected by core: %s", ack.Error)
	}
	return nil
}

// ReportPreCheckResults sends pre-check results to Core via the StatusReport RPC.
// This should be called after agent registration to report system capabilities.
func (c *Client) ReportPreCheckResults(ctx context.Context, results []PreCheckResult) error {
	c.mu.RLock()
	client := c.client
	agentID := c.agentID
	c.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("not connected to core")
	}

	c.logger().Info("reporting_precheck_results", "count", len(results))

	// Convert PreCheckResults to ServiceStatus messages for transport
	// Note: We're reusing the ServiceStatus message type since pre-checks
	// report service readiness. The "service_id" contains the check type.
	var services []*agentpb.ServiceStatus
	for _, result := range results {
		// Serialize details to health message
		var healthMsg string
		if result.Message != "" {
			healthMsg = result.Message
		}
		if result.Error != "" {
			healthMsg = result.Error
		}

		services = append(services, &agentpb.ServiceStatus{
			ServiceId:     "precheck:" + result.Type,
			State:         string(result.Status),
			Healthy:       result.Status == PreCheckStatusPassed,
			HealthMessage: healthMsg,
		})
	}

	req := &agentpb.StatusReport{
		AgentId:       agentID,
		TimestampUnix: time.Now().Unix(),
		Services:      services,
	}

	resp, err := client.ReportStatus(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to report pre-check results: %w", err)
	}

	if !resp.Received {
		c.logger().Warn("precheck_results_not_acknowledged")
	}

	c.logger().Info("precheck_results_reported", "acknowledged", resp.Received)
	return nil
}

// RunAndReportPreChecks executes pre-checks and reports results to Core.
// This is a convenience method that combines PreCheckRunner with reporting.
func (c *Client) RunAndReportPreChecks(ctx context.Context, runner *PreCheckRunner) ([]PreCheckResult, error) {
	results := runner.RunAll(ctx)

	if err := c.ReportPreCheckResults(ctx, results); err != nil {
		c.logger().Warn("failed_to_report_prechecks", "error", err.Error())
		return results, err
	}

	return results, nil
}
