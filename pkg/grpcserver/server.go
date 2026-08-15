// Package grpcserver implements the gRPC server for agent communication.
//
// Architecture: Pillar 2 — Runtime Intelligence Layer
//
// This is the core of the beyond-IaC operational layer that manages infrastructure
// after provisioning. It provides the gRPC agent network for Day-2+ operations:
// health monitoring, drift detection, command execution, and auto-remediation.
//
// Components:
//   - server.go: Core Server struct and lifecycle methods (New, Start, Stop)
//   - connection.go: Agent connection management
//   - command_dispatch.go: Command dispatch to agents
//   - command_store.go: Persistent command queue (survives restarts)
//   - health.go: Health/heartbeat handling
//
// Protocol: gRPC over mTLS (TLS 1.3), defined in api/proto/agent.proto
// CONTRACT: See CONTRACT.md in this directory for authoritative constraints.
// ARCHITECTURE: See docs/architecture/ARCHITECTURE_V2.md Section 4
package grpcserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Server handles gRPC connections from agents.
// Implements agentpb.AgentServiceServer interface.
type Server struct {
	agentpb.UnimplementedAgentServiceServer
	collectormetricspb.UnimplementedMetricsServiceServer

	addr             string
	tlsConfig        *tls.Config
	log              *logger.Logger
	readTimeout      time.Duration
	heartbeatTimeout time.Duration

	// Agent management
	agents   map[string]*ConnectedAgent
	agentsMu sync.RWMutex

	// Command dispatch (S7: Backpressure-aware queues)
	commandQueue     *CommandQueue      // Legacy command queue with backpressure
	commandQueueChan chan *AgentCommand // Channel wrapper for backward compatibility
	resultQueue      chan *CommandResult

	// Sprint 9: Command persistence for restart recovery
	commandStore *CommandStore

	// IAC-4: Command router for TofuCommand and TerramateCommand handling
	commandRouter *CommandRouter

	// IAC-4: Tofu command queue with backpressure (S7)
	tofuCommandQueue     *TofuCommandQueue      // Backpressure-aware tofu queue
	tofuCommandQueueChan chan *tofuCommandEntry // Channel wrapper for backward compatibility

	// Typed StackKits lifecycle dispatch. This path never enters AgentCommand.
	stackKitCommandQueue *StackKitCommandQueue
	stackKitHandler      *StackKitCommandHandler

	// Queue configuration (S7)
	queueConfig     QueueConfig
	tofuQueueConfig QueueConfig

	// Monitoring 2.0: Embedded TSDB for metric ingestion
	monitorTSDB         *monitoring.MonitorTSDB
	monitorIngestHealth *monitorIngestHealthTracker

	// Agent log streaming (Sprint 6: F7)
	agentLogsMu         sync.RWMutex
	agentLogs           map[string][]AgentLogEntry
	agentLogSubscribers map[string]map[chan AgentLogEntry]struct{}
	runtimeLogs         *runtimeLogBuffer

	// gRPC server instance
	grpcServer *grpc.Server
	listener   net.Listener
	running    bool

	// Phase 7.1: mTLS identity binding. certManager validates client cert
	// signature/expiry/revocation; enrollmentStore pins the
	// (cert CN → tenant → allowed command classes) mapping. Both are
	// optional in legacy callers; production wiring lives in
	// cmd/techstack/main.go via NewWithEnrollment.
	certManager     *auth.CertManager
	enrollmentStore AgentEnrollmentStore
	defaultTenant   string // fallback tenant for standalone / dev mode
}

// tofuCommandEntry wraps a TofuCommand with routing information.
type tofuCommandEntry struct {
	AgentID string
	Command *agentpb.TofuCommand
}

// ConnectedAgent represents a connected agent.
type ConnectedAgent struct {
	ID           string
	Hostname     string
	OS           string
	Arch         string
	Version      string
	Capabilities []string
	ConnectedAt  time.Time
	LastSeen     time.Time
	Status       string
	Resources    *ResourceUsage

	// Phase 7.1: identity bound to the client cert + enrollment row.
	// Tenant scopes downstream operations; CertSerial pins which cert was
	// used to register; AllowedCommandClasses is the per-agent policy
	// enforced before queueing any command.
	Tenant                string
	CertSerial            string
	AllowedCommandClasses []string

	Services   []AgentService
	Containers []AgentContainer
}

type AgentService struct {
	Name         string
	Status       string
	Type         string
	UptimeSecs   int64
	MemoryBytes  int64
	CPUPercent   float64
	RestartCount int32
}

type AgentContainer struct {
	ID          string
	Name        string
	Image       string
	Status      string
	MemoryBytes int64
	CPUPercent  float64
	Ports       []string
}

// ResourceUsage represents agent resource usage.
type ResourceUsage struct {
	CPUPercent       float64
	MemoryUsedBytes  int64
	MemoryTotalBytes int64
	DiskUsedBytes    int64
	DiskTotalBytes   int64
}

// AgentCommand represents a command to send to an agent.
type AgentCommand struct {
	ID          string
	AgentID     string
	Type        string
	Command     string
	Args        []string
	Environment map[string]string
	WorkDir     string
	Timeout     time.Duration
}

// CommandResult represents the result of a command execution.
type CommandResult struct {
	CommandID  string
	AgentID    string
	ExitCode   int
	Stdout     string
	Stderr     string
	StartedAt  time.Time
	FinishedAt time.Time
	Error      error
}

// Config holds gRPC server configuration.
type Config struct {
	ListenAddr string
	CertFile   string
	KeyFile    string
	CAFile     string
	// ReadTimeout is the timeout for reading from connections (default: 60s)
	// Set higher for slow networks, lower to mitigate slow loris attacks
	ReadTimeout time.Duration
	// HeartbeatTimeout is how long before an agent is considered disconnected (default: 90s)
	HeartbeatTimeout time.Duration
	// RequireMTLS enforces mTLS for all connections (H3: Security Hardening)
	// When true, connections without valid client certificates will be rejected
	RequireMTLS bool

	// S7: Queue Backpressure Configuration
	// QueueMaxSize is the maximum number of commands in the queue (default: 1000)
	QueueMaxSize int
	// QueueOverflowStrategy determines behavior when queue is full: "reject" or "drop-oldest"
	QueueOverflowStrategy string
	// QueueWarningThreshold is the percentage (0-100) at which warnings are logged (default: 80)
	QueueWarningThreshold int
	// TofuQueueMaxSize is the maximum number of tofu commands in queue (default: 100)
	TofuQueueMaxSize int
	// RuntimeLogPath is the optional JSONL spool path for redacted runtime logs.
	RuntimeLogPath string
	// RuntimeLogMaxEntries is the in-memory query buffer size for runtime logs.
	RuntimeLogMaxEntries int
}

// New creates a new gRPC server without command persistence.
// For production use with restart recovery, use NewWithPersistence instead.
func New(cfg Config, log *logger.Logger) (*Server, error) {
	return newServer(cfg, log, nil)
}

// NewWithPersistence creates a new gRPC server with command persistence.
// This enables command recovery after server restarts (Sprint 9: F9).
// The commandStore should be created with NewCommandStore(app) beforehand.
func NewWithPersistence(cfg Config, log *logger.Logger, commandStore *CommandStore) (*Server, error) {
	return newServer(cfg, log, commandStore)
}

// newServer is the internal constructor shared by New and NewWithPersistence.
func newServer(cfg Config, log *logger.Logger, commandStore *CommandStore) (*Server, error) {
	var tlsConfig *tls.Config

	// Load TLS config if certificates are provided
	if cfg.CertFile != "" && cfg.KeyFile != "" && cfg.CAFile != "" {
		tc, err := loadTLSConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS config: %w", err)
		}
		tlsConfig = tc
	} else if cfg.RequireMTLS {
		// H3: mTLS Enforcement - fail if mTLS is required but certs are missing
		return nil, fmt.Errorf("mTLS required but certificate files not configured (cert_file, key_file, ca_file)")
	} else {
		// H3: Log security warning when running without mTLS
		log.Warn("grpc_insecure_mode",
			"message", "gRPC server running WITHOUT mTLS - agent connections are NOT authenticated",
			"recommendation", "Configure cert_file, key_file, and ca_file for production use",
		)
	}

	// Set defaults for timeouts
	readTimeout := cfg.ReadTimeout
	if readTimeout == 0 {
		readTimeout = 60 * time.Second
	}
	heartbeatTimeout := cfg.HeartbeatTimeout
	if heartbeatTimeout == 0 {
		heartbeatTimeout = 90 * time.Second
	}

	// S7: Configure queue backpressure settings
	queueCfg := QueueConfig{
		MaxSize:          cfg.QueueMaxSize,
		OverflowStrategy: OverflowStrategy(cfg.QueueOverflowStrategy),
		WarningThreshold: cfg.QueueWarningThreshold,
	}
	if queueCfg.MaxSize <= 0 {
		queueCfg.MaxSize = 1000
	}
	if queueCfg.OverflowStrategy == "" {
		queueCfg.OverflowStrategy = OverflowReject
	}
	if queueCfg.WarningThreshold <= 0 || queueCfg.WarningThreshold > 100 {
		queueCfg.WarningThreshold = 80
	}

	tofuQueueCfg := QueueConfig{
		MaxSize:          cfg.TofuQueueMaxSize,
		OverflowStrategy: OverflowStrategy(cfg.QueueOverflowStrategy),
		WarningThreshold: cfg.QueueWarningThreshold,
	}
	if tofuQueueCfg.MaxSize <= 0 {
		tofuQueueCfg.MaxSize = 100
	}
	if tofuQueueCfg.OverflowStrategy == "" {
		tofuQueueCfg.OverflowStrategy = OverflowReject
	}
	if tofuQueueCfg.WarningThreshold <= 0 || tofuQueueCfg.WarningThreshold > 100 {
		tofuQueueCfg.WarningThreshold = 80
	}

	serverLog := log.WithComponent("grpc")

	// Create backpressure-aware queues (S7)
	cmdQueue := NewCommandQueue(queueCfg, serverLog)
	tofuQueue := NewTofuCommandQueue(tofuQueueCfg, serverLog)
	stackKitQueue := NewStackKitCommandQueue(tofuQueueCfg, serverLog)

	serverLog.Info("queue_backpressure_initialized",
		"command_queue_max", queueCfg.MaxSize,
		"tofu_queue_max", tofuQueueCfg.MaxSize,
		"stackkit_queue_max", tofuQueueCfg.MaxSize,
		"overflow_strategy", string(queueCfg.OverflowStrategy),
		"warning_threshold", queueCfg.WarningThreshold,
	)

	runtimeLogs, err := newRuntimeLogBuffer(runtimeLogBufferConfig{
		Path:       cfg.RuntimeLogPath,
		MaxEntries: cfg.RuntimeLogMaxEntries,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize runtime log store: %w", err)
	}

	// Sprint 9: Log if command persistence is enabled
	if commandStore != nil {
		serverLog.Info("command_persistence_enabled", "feature", "F9")
	} else {
		serverLog.Debug("command_persistence_disabled", "note", "Use NewWithPersistence for restart recovery")
	}

	return &Server{
		addr:                 cfg.ListenAddr,
		tlsConfig:            tlsConfig,
		log:                  serverLog,
		readTimeout:          readTimeout,
		heartbeatTimeout:     heartbeatTimeout,
		agents:               make(map[string]*ConnectedAgent),
		commandQueue:         cmdQueue,
		commandQueueChan:     make(chan *AgentCommand, queueCfg.MaxSize), // For backward compatibility
		resultQueue:          make(chan *CommandResult, 1000),
		commandStore:         commandStore, // Sprint 9: F9 Command Persistence
		tofuCommandQueue:     tofuQueue,
		tofuCommandQueueChan: make(chan *tofuCommandEntry, tofuQueueCfg.MaxSize), // For backward compatibility
		stackKitCommandQueue: stackKitQueue,
		stackKitHandler:      NewStackKitCommandHandler(),
		queueConfig:          queueCfg,
		tofuQueueConfig:      tofuQueueCfg,
		monitorIngestHealth:  newMonitorIngestHealthTracker(),
		agentLogs:            make(map[string][]AgentLogEntry),
		agentLogSubscribers:  make(map[string]map[chan AgentLogEntry]struct{}),
		runtimeLogs:          runtimeLogs,
	}, nil
}

// SetEnrollmentStore wires the per-agent enrollment registry used by the
// Register handler (Phase 7.1). Pass a *MemoryAgentEnrollmentStore in
// dev/test, *PostgresAgentEnrollmentStore in production. Must be called
// before Start; not safe for concurrent reconfiguration.
func (s *Server) SetEnrollmentStore(store AgentEnrollmentStore) {
	s.enrollmentStore = store
}

// SetCertManager wires the pkg/auth CertManager so the Register handler
// can run signature/expiry/revocation checks against the same authority
// that issued the cert (Phase 7.1, defense-in-depth on top of the TLS
// stack's verification).
func (s *Server) SetCertManager(cm *auth.CertManager) {
	s.certManager = cm
}

// IssueAgentIdentity mints and pins a client identity through the exact CA and
// enrollment store used by this server. Pairing surfaces call this narrow
// method instead of receiving either signing material or direct store access.
func (s *Server) IssueAgentIdentity(ctx context.Context, req IssueRequest) (*IssuedIdentity, error) {
	if s == nil || s.certManager == nil || s.enrollmentStore == nil {
		return nil, fmt.Errorf("agent identity issuer is not configured")
	}
	return IssueAgentIdentity(ctx, s.certManager, s.enrollmentStore, req)
}

// SetDefaultTenant sets the fallback tenant used by Register when the
// peer cert does not carry an explicit tenant SAN/Organization field.
// Used by standalone / single-tenant deployments. Production multi-tenant
// callers should set this to "" and rely on the enrollment row alone.
func (s *Server) SetDefaultTenant(tenant string) {
	s.defaultTenant = tenant
}

func loadTLSConfig(cfg Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %w", err)
	}

	caCert, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// Start starts the gRPC server.
func (s *Server) Start(ctx context.Context) error {
	var listener net.Listener
	var err error
	var opts []grpc.ServerOption

	if s.tlsConfig != nil {
		listener, err = net.Listen("tcp", s.addr)
		if err != nil {
			return fmt.Errorf("failed to listen: %w", err)
		}
		creds := credentials.NewTLS(s.tlsConfig)
		opts = append(opts, grpc.Creds(creds))
		s.log.Info("starting_grpc_server", "addr", s.addr, "tls", true)
	} else {
		listener, err = net.Listen("tcp", s.addr)
		s.log.Info("starting_grpc_server", "addr", s.addr, "tls", false)
	}

	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// Sprint 9: Recover pending commands from previous session
	if s.commandStore != nil {
		if err := s.recoverPendingCommands(); err != nil {
			s.log.Warn("command_recovery_failed", "error", err)
			// Non-fatal: continue startup even if recovery fails
		}
	}

	s.listener = listener
	s.running = true

	// Create gRPC server with options
	s.grpcServer = grpc.NewServer(opts...)

	// Register the AgentService
	agentpb.RegisterAgentServiceServer(s.grpcServer, s)
	collectormetricspb.RegisterMetricsServiceServer(s.grpcServer, s)
	collectorlogspb.RegisterLogsServiceServer(s.grpcServer, newOTLPLogsService(s))

	// Serve in a goroutine
	go func() {
		if err := s.grpcServer.Serve(listener); err != nil && s.running {
			s.log.Error("grpc_serve_error", "error", err)
		}
	}()

	// Watch for context cancellation
	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	return nil
}

// Stop gracefully stops the server.
func (s *Server) Stop() error {
	s.running = false
	if s.runtimeLogs != nil {
		if err := s.runtimeLogs.Close(); err != nil {
			s.log.Warn("runtime_log_store_close_failed", "error", err.Error())
		}
	}
	if s.grpcServer != nil {
		// GracefulStop will close the listener, so we don't need to do it separately
		s.grpcServer.GracefulStop()
	} else if s.listener != nil {
		// Only close listener manually if gRPC server wasn't created
		return s.listener.Close()
	}
	return nil
}
