package main

import (
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/logger"
)

func maybeStartEmbeddedPostgres(cfg *config.Config, log *logger.Logger) (*localdb.EmbeddedPostgres, error) {
	if strings.TrimSpace(os.Getenv(db.EnvDatabaseURL)) != "" {
		return nil, nil
	}
	if !localdb.EnabledFromEnv() {
		return nil, nil
	}
	if cfg == nil {
		return nil, nil
	}

	log.Info("embedded_postgres_starting")
	pg, err := localdb.StartEmbeddedPostgres(cfg.Server.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.Setenv(db.EnvDatabaseURL, pg.DSN()); err != nil {
		_ = pg.Stop()
		return nil, err
	}
	if strings.TrimSpace(os.Getenv(db.EnvStoreBackend)) == "" {
		if err := os.Setenv(db.EnvStoreBackend, string(db.StoreBackendPostgres)); err != nil {
			_ = pg.Stop()
			return nil, err
		}
	}
	log.Info("embedded_postgres_ready", "data_dir", cfg.Server.DataDir)
	return pg, nil
}
