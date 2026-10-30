package localdb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	EnvEmbeddedPostgresBundleDir        = "TECHSTACK_EMBEDDED_POSTGRES_BUNDLE_DIR"
)

const (
	embeddedUsername = "techstack"
	// The old public password is admitted only by an explicit, physically
	// pinned legacy transition. Fresh and ordinary starts never use it.
	embeddedPassword = "techstack-local"
	embeddedDatabase = "techstack"
)

type EmbeddedPostgres struct {
	db       *embeddedpostgres.EmbeddedPostgres
	logFile  *os.File
	adminDSN string
	dsn      string
	custody  *operatorCustody
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
	return StartEmbeddedPostgresWithAdoption(dataDir, "")
}

// StartEmbeddedPostgresWithAdoption is an operator-only entry point. A legacy
// profile without private custody requires an explicit verified physical pin.
func StartEmbeddedPostgresWithAdoption(dataDir, adoptIdentity string) (*EmbeddedPostgres, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("data dir is required")
	}

	baseDir := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresDir))
	if baseDir == "" {
		baseDir = filepath.Join(dataDir, "postgres")
	}
	cacheDir := filepath.Join(baseDir, "cache")
	if bundleDir := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresBundleDir)); bundleDir != "" {
		if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
			return nil, fmt.Errorf("bundled PostgreSQL requires Windows amd64")
		}
		if strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresBinariesPath)) != "" {
			return nil, fmt.Errorf("bundled PostgreSQL cannot use an external binaries path")
		}
		var err error
		cacheDir, err = filepath.Abs(bundleDir)
		if err != nil {
			return nil, fmt.Errorf("resolve bundled PostgreSQL directory: %w", err)
		}
		archive := filepath.Join(cacheDir, fmt.Sprintf("embedded-postgres-binaries-windows-amd64-%s.txz", embeddedpostgres.V16))
		info, err := os.Stat(archive)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, fmt.Errorf("required PostgreSQL bundle is missing or invalid at %s; repair the Windows installation", archive)
		}
	}

	port, err := embeddedPostgresPort()
	if err != nil {
		return nil, err
	}

	// #nosec G703 -- baseDir is an explicit local-operator storage setting.
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, fmt.Errorf("create embedded postgres dir: %w", err)
	}

	if err := ensureDataDirCompatible(filepath.Join(baseDir, "data")); err != nil {
		return nil, err
	}
	custody, err := openOperatorCustody(baseDir, adoptIdentity)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			custody.close()
		}
	}()
	startupNonce, err := randomCredential()
	if err != nil {
		return nil, err
	}
	custody.startupNonce = "kombify-native-" + startupNonce[:24]
	custody.dataDirectory, err = filepath.Abs(filepath.Join(baseDir, "data"))
	if err != nil {
		return nil, err
	}

	logPath := filepath.Join(baseDir, "postgres.log")
	// #nosec G703 -- logPath is fixed beneath the operator-selected embedded database directory.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open embedded postgres log: %w", err)
	}

	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Username(embeddedUsername).
		Password(custody.record.Password).
		Database(embeddedDatabase).
		Port(port).
		CachePath(cacheDir).
		RuntimePath(filepath.Join(baseDir, "runtime")).
		DataPath(filepath.Join(baseDir, "data")).
		StartTimeout(embeddedPostgresStartTimeout()).
		StartParameters(map[string]string{"cluster_name": custody.startupNonce}).
		Logger(logFile)

	if repoURL := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresRepositoryURL)); repoURL != "" {
		cfg = cfg.BinaryRepositoryURL(repoURL)
	}
	if binariesPath := strings.TrimSpace(os.Getenv(EnvEmbeddedPostgresBinariesPath)); binariesPath != "" {
		cfg = cfg.BinariesPath(binariesPath)
	}

	pg := embeddedpostgres.NewDatabase(cfg)
	started := &EmbeddedPostgres{
		db:       pg,
		logFile:  logFile,
		adminDSN: fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", embeddedUsername, custody.record.Password, port, embeddedDatabase),
		custody:  custody,
	}
	legacyDSN := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", embeddedUsername, embeddedPassword, port, embeddedDatabase)
	// The pinned library authenticates its configured credential before Start
	// returns. A pending legacy transition must therefore qualify the exact
	// running cluster during that readiness phase, not after it. The library
	// still uses only the new credential; HBA and password policy never widen.
	var qualification <-chan error
	cancelQualification := func() {}
	if custody.record.State == "legacy-pending" {
		qualificationContext, cancel := context.WithTimeout(context.Background(), embeddedPostgresStartTimeout()+runtimeOwnerSetupTimeout)
		cancelQualification = cancel
		result := make(chan error, 1)
		qualification = result
		go func() { result <- custody.qualify(qualificationContext, started.adminDSN, legacyDSN) }()
	}
	startErr := pg.Start()
	var qualificationErr error
	if startErr != nil {
		cancelQualification()
	}
	if qualification != nil {
		qualificationErr = <-qualification
	}
	cancelQualification()
	if startErr != nil {
		_ = pg.Stop()
		_ = logFile.Close()
		return nil, errors.Join(qualificationErr, fmt.Errorf("start embedded postgres: %w", startErr))
	}
	if qualificationErr != nil {
		stopErr := started.Stop()
		return nil, errors.Join(qualificationErr, stopErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeOwnerSetupTimeout)
	defer cancel()
	if qualification == nil {
		if err := custody.qualify(ctx, started.adminDSN, legacyDSN); err != nil {
			_ = started.Stop()
			return nil, err
		}
	}
	dsn, err := ensureRuntimeOwner(ctx, started.adminDSN, port)
	if err != nil {
		_ = started.Stop()
		return nil, fmt.Errorf("prepare embedded postgres runtime owner: %w", err)
	}
	started.dsn = dsn
	success = true
	return started, nil
}

// DSN is private migration custody for the operator process. A server must use
// PrepareApplication's bounded connection in a separately executed process.
func (pg *EmbeddedPostgres) DSN() string {
	if pg == nil {
		return ""
	}
	return pg.dsn
}

// BootstrapDSN is the embedded server's bootstrap superuser. It exists only for
// local operator setup; never hand it or the EmbeddedPostgres object to a server.
func (pg *EmbeddedPostgres) BootstrapDSN() string {
	if pg == nil {
		return ""
	}
	return pg.adminDSN
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
	if stopErr == nil && pg.custody != nil {
		pg.custody.close()
		pg.custody = nil
	}
	return stopErr
}

// PhysicalIdentity is metadata derived by the supervisor from its real cluster.
func (pg *EmbeddedPostgres) PhysicalIdentity() string {
	if pg == nil || pg.custody == nil {
		return ""
	}
	return pg.custody.record.PhysicalIdentity
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
