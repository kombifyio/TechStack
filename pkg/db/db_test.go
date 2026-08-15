package db

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestConfigFromEnvUsesPostgresWhenDatabaseURLIsSet(t *testing.T) {
	t.Setenv(EnvStoreBackend, "")
	t.Setenv(EnvDatabaseURL, "postgres://techstack:secret@localhost:5432/techstack?sslmode=disable")

	cfg, err := ConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}

	if cfg.Backend != StoreBackendPostgres {
		t.Fatalf("Backend = %q, want %q", cfg.Backend, StoreBackendPostgres)
	}
	if cfg.DriverName != PostgresDriverName {
		t.Fatalf("DriverName = %q, want %q", cfg.DriverName, PostgresDriverName)
	}
	if cfg.DSN != "postgres://techstack:secret@localhost:5432/techstack?sslmode=disable" {
		t.Fatalf("DSN was not loaded from %s", EnvDatabaseURL)
	}
	if !cfg.SQLEnabled() {
		t.Fatal("SQLEnabled() = false, want true for postgres")
	}
}

func TestConfigFromEnvRejectsExplicitPostgresWithoutDatabaseURL(t *testing.T) {
	t.Setenv(EnvStoreBackend, string(StoreBackendPostgres))
	t.Setenv(EnvDatabaseURL, "")

	_, err := ConfigFromEnv(t.TempDir())
	if err == nil {
		t.Fatal("ConfigFromEnv returned nil error")
	}
	if !strings.Contains(err.Error(), EnvDatabaseURL) {
		t.Fatalf("error %q does not mention %s", err, EnvDatabaseURL)
	}
}

func TestConfigFromEnvKeepsPocketBaseAsNonSQLDefaultWithoutDatabaseURL(t *testing.T) {
	t.Setenv(EnvStoreBackend, "")
	t.Setenv(EnvDatabaseURL, "")

	cfg, err := ConfigFromEnv(t.TempDir())
	if err != nil {
		t.Fatalf("ConfigFromEnv returned error: %v", err)
	}

	if cfg.Backend != StoreBackendPocketBase {
		t.Fatalf("Backend = %q, want %q", cfg.Backend, StoreBackendPocketBase)
	}
	if cfg.SQLEnabled() {
		t.Fatal("SQLEnabled() = true, want false for PocketBase")
	}
	if cfg.Path == "" {
		t.Fatal("Path should keep the legacy SQLite migration location for later migration mode")
	}
}

func TestOpenRejectsNonSQLBackend(t *testing.T) {
	_, err := Open(Config{Backend: StoreBackendPocketBase})
	if !errors.Is(err, ErrSQLStoreDisabled) {
		t.Fatalf("Open error = %v, want ErrSQLStoreDisabled", err)
	}
}

func TestPostgresDriverNameIsRegistered(t *testing.T) {
	for _, driverName := range sql.Drivers() {
		if driverName == PostgresDriverName {
			return
		}
	}

	t.Fatalf("PostgresDriverName %q is not registered; registered drivers: %v", PostgresDriverName, sql.Drivers())
}

func TestOpenUsesConfiguredPostgresDriverAndDSN(t *testing.T) {
	driverName := registerCaptureDriver(t)
	dsn := "postgres://techstack:secret@localhost:5432/techstack?sslmode=disable"

	db, err := Open(Config{
		Backend:      StoreBackendPostgres,
		DriverName:   driverName,
		DSN:          dsn,
		MaxOpenConns: 7,
		MaxIdleConns: 3,
	})
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer db.Close()

	if db.Backend() != StoreBackendPostgres {
		t.Fatalf("Backend() = %q, want %q", db.Backend(), StoreBackendPostgres)
	}
	if db.DSN() != dsn {
		t.Fatalf("DSN() = %q, want %q", db.DSN(), dsn)
	}
	if got := capturedTestDSN(); got != dsn {
		t.Fatalf("driver saw DSN %q, want %q", got, dsn)
	}
}

var (
	captureDriverOnce sync.Once
	captureDriverDSN  string
)

func registerCaptureDriver(t *testing.T) string {
	t.Helper()

	const name = "techstack_capture_postgres"
	captureDriverOnce.Do(func() {
		sql.Register(name, captureDriver{})
	})
	captureDriverDSN = ""
	return name
}

func capturedTestDSN() string {
	return captureDriverDSN
}

type captureDriver struct{}

func (captureDriver) Open(name string) (driver.Conn, error) {
	captureDriverDSN = name
	return captureConn{}, nil
}

type captureConn struct{}

func (captureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not implemented")
}

func (captureConn) Close() error {
	return nil
}

func (captureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("begin not implemented")
}

func (captureConn) Ping() error {
	return nil
}
