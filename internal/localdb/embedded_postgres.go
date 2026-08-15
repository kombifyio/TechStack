package localdb

import (
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

const (
	EnvEmbeddedPostgres                 = "TECHSTACK_EMBEDDED_POSTGRES"
	EnvEmbeddedPostgresDir              = "TECHSTACK_EMBEDDED_POSTGRES_DIR"
	EnvEmbeddedPostgresPort             = "TECHSTACK_EMBEDDED_POSTGRES_PORT"
	EnvEmbeddedPostgresStartTimeoutSecs = "TECHSTACK_EMBEDDED_POSTGRES_START_TIMEOUT_SECONDS"
	EnvEmbeddedPostgresRepositoryURL    = "TECHSTACK_EMBEDDED_POSTGRES_BINARY_REPOSITORY_URL"
	EnvEmbeddedPostgresBinariesPath     = "TECHSTACK_EMBEDDED_POSTGRES_BINARIES_PATH"
)

const (
	embeddedUsername = "techstack"
	embeddedPassword = "techstack-local"
	embeddedDatabase = "techstack"
)

type EmbeddedPostgres struct {
	db      *embeddedpostgres.EmbeddedPostgres
	logFile *os.File
	dsn     string
}

func EnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEmbeddedPostgres))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func StartEmbeddedPostgres(dataDir string) (*EmbeddedPostgres, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("data dir is required")
	}

	baseDir := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresDir))
	if baseDir == "" {
		baseDir = filepath.Join(dataDir, "postgres")
	}

	port, err := embeddedPostgresPort()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("create embedded postgres dir: %w", err)
	}

	logPath := filepath.Join(baseDir, "postgres.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open embedded postgres log: %w", err)
	}

	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Username(embeddedUsername).
		Password(embeddedPassword).
		Database(embeddedDatabase).
		Port(port).
		CachePath(filepath.Join(baseDir, "cache")).
		RuntimePath(filepath.Join(baseDir, "runtime")).
		DataPath(filepath.Join(baseDir, "data")).
		StartTimeout(embeddedPostgresStartTimeout()).
		Logger(logFile)

	if repoURL := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresRepositoryURL)); repoURL != "" {
		cfg = cfg.BinaryRepositoryURL(repoURL)
	}
	if binariesPath := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresBinariesPath)); binariesPath != "" {
		cfg = cfg.BinariesPath(binariesPath)
	}

	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start embedded postgres: %w", err)
	}

	return &EmbeddedPostgres{
		db:      pg,
		logFile: logFile,
		dsn:     fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", embeddedUsername, embeddedPassword, port, embeddedDatabase),
	}, nil
}

func (pg *EmbeddedPostgres) DSN() string {
	if pg == nil {
		return ""
	}
	return pg.dsn
}

func (pg *EmbeddedPostgres) Stop() error {
	if pg == nil {
		return nil
	}
	var stopErr error
	if pg.db != nil {
		stopErr = pg.db.Stop()
	}
	if pg.logFile != nil {
		if err := pg.logFile.Close(); stopErr == nil && err != nil {
			stopErr = err
		}
	}
	return stopErr
}

func embeddedPostgresPort() (uint32, error) {
	raw := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresPort))
	if raw != "" {
		port, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || port == 0 || port > 65535 {
			return 0, fmt.Errorf("invalid %s value %q", EnvEmbeddedPostgresPort, raw)
		}
		return uint32(port), nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate embedded postgres port: %w", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 || addr.Port > math.MaxUint16 {
		return 0, fmt.Errorf("allocate embedded postgres port: unexpected address %q", listener.Addr().String())
	}
	return uint32(addr.Port), nil
}

func embeddedPostgresStartTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresStartTimeoutSecs))
	if raw == "" {
		return 120 * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 120 * time.Second
	}
	if seconds > 600 {
		seconds = 600
	}
	return time.Duration(seconds) * time.Second
}
