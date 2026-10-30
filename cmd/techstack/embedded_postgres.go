package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
)

// These are company/operator carriers, not ordinary customer runtime options.
// A nested secret bag can reintroduce a scrubbed direct variable later at boot.
var nativeOperatorCustodyNames = []string{
	"TECHSTACK_SAAS_CUSTODY_BUNDLE", "TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL",
	db.EnvMigrationDatabaseURL, "TECHSTACK_LOCAL_DATABASE_OPERATOR_URL",
}

func checkNativeServingCustody() error {
	for _, name := range nativeOperatorCustodyNames {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return errors.New("native HTTP runtime must not receive company or operator database custody; keep it in the separate preparation process")
		}
	}
	return nil
}

// Supervision precedes telemetry, Wallet and HTTP. Only this parent owns
// PostgreSQL's operator configuration and migration connection.
func superviseNativePostgres(ctx context.Context) (handled bool, result error) {
	if strings.TrimSpace(os.Getenv(db.EnvDatabaseURL)) != "" || !localdb.EnabledFromEnv() {
		return false, nil
	}
	if err := checkNativeServingCustody(); err != nil {
		return true, err
	}
	cfg, err := config.Load("")
	if err != nil {
		return true, err
	}
	if hostedRuntime(cfg) || !cfg.DeploymentMode.IsSelfHosted() {
		return true, errors.New("native supervision requires a self-hosted installation")
	}
	if strings.TrimSpace(os.Getenv(db.EnvMigrationDatabaseURL)) != "" || os.Getenv("TECHSTACK_LOCAL_DATABASE_OPERATOR_URL") != "" {
		return true, errors.New("embedded supervision refuses external operator or migration custody")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	pg, err := localdb.StartEmbeddedPostgres(cfg.Server.DataDir)
	if pg != nil {
		defer func() { result = errors.Join(result, pg.Stop()) }()
	}
	if err != nil {
		return true, err
	}
	if existing := strings.TrimSpace(os.Getenv(db.EnvExpectedDatabaseIdentity)); existing != "" && existing != pg.PhysicalIdentity() {
		return true, errors.New("native database differs from configured physical identity")
	}
	// This is the actual native cluster, not a Stage override or temporary swap.
	if err := os.Setenv(db.EnvExpectedDatabaseIdentity, pg.PhysicalIdentity()); err != nil {
		return true, err
	}
	if mode := os.Getenv(db.EnvDatabaseMigrationMode); mode != "" && mode != "apply" {
		return true, errors.New("native operator preparation requires apply mode; the serving child always verifies")
	}
	dsn, err := pg.PrepareApplication(ctx)
	if err != nil {
		return true, fmt.Errorf("prepare native application: %w", err)
	}
	removeConnection, err := localdb.PublishApplicationConnection(cfg.Server.DataDir, dsn, pg.PhysicalIdentity())
	if err != nil {
		return true, err
	}
	defer removeConnection()
	return true, runNativeServingChild(ctx, dsn, pg.PhysicalIdentity())
}
func runNativeServingChild(ctx context.Context, dsn, identity string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, os.Args[1:]...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	exclude := map[string]bool{db.EnvDatabaseURL: true, db.EnvMigrationDatabaseURL: true, db.EnvDatabaseMigrationMode: true, db.EnvAllowDBGenesis: true, db.EnvExpectedDatabaseIdentity: true, db.EnvStoreBackend: true, parentPipeShutdownEnv: true, "TECHSTACK_LOCAL_DATABASE_OPERATOR_URL": true, "PGPASSWORD": true, "PGPASSFILE": true, "PGSERVICE": true, "PGSERVICEFILE": true}
	for _, name := range nativeOperatorCustodyNames {
		exclude[name] = true
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !exclude[strings.ToUpper(name)] {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, db.EnvDatabaseURL+"="+dsn, db.EnvStoreBackend+"=postgres", db.EnvDatabaseMigrationMode+"=verify", db.EnvAllowDBGenesis+"=false", db.EnvExpectedDatabaseIdentity+"="+identity, parentPipeShutdownEnv+"=1")
	pipe, err := command.StdinPipe()
	if err != nil {
		return err
	}
	defer pipe.Close()
	if err := command.Start(); err != nil {
		return errors.New("start native serving process failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-done:
		return err
	case <-stopCtx.Done():
		_ = pipe.Close()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			_ = command.Process.Kill()
			<-done
			return errors.New("native serving process exceeded shutdown deadline")
		}
	}
}
