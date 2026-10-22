package db

import (
	"os"
	"strings"
)

const EnvMigrationDatabaseURL = "TECHSTACK_MIGRATION_DATABASE_URL"

// MigrationConfigFromEnv selects the separate migration-owner connection when
// one is configured and otherwise falls back to the ordinary runtime binding.
func MigrationConfigFromEnv() (Config, error) {
	dsn := strings.TrimSpace(os.Getenv(EnvMigrationDatabaseURL))
	if dsn == "" {
		return ConfigFromEnv()
	}
	cfg := DefaultConfig()
	cfg.Backend = StoreBackendPostgres
	cfg.DSN = dsn
	cfg.DriverName = PostgresDriverName
	return cfg, nil
}
