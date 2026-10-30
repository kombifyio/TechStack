package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/internal/executioncustody"
	"github.com/kombifyio/techstack/internal/household"
	"github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/stageowner"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/auth/testidentity"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/releasechannel"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

// runExecutionServer composes the canonical issuer and provider authority in a
// privileged process. It starts no user-facing routes, migration or wallet.
// Deployment installs its exact caller grants and retained key custody here;
// the separately supervised HTTP control plane receives neither credential.
func runExecutionServer(ctx context.Context) error {
	if profile := strings.TrimSpace(os.Getenv("TECHSTACK_EXECUTION_PROFILE")); profile == "platform-projection" {
		return runPlatformProjectionServer(ctx)
	} else if profile != "" {
		return errors.New("unknown private Execution profile")
	}
	ctx, cancelNative := context.WithCancel(ctx)
	defer cancelNative()
	if config.ReleaseChannel() != "preview" || !db.DeclaresStagingDatabaseBinding() {
		return errors.New("native Execution requires the declared non-production Preview Stage database")
	}
	if os.Getenv(db.EnvMigrationDatabaseURL) != "" || os.Getenv("TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL") != "" || os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv("TECHSTACK_ALLOW_DB_GENESIS") != "false" || strings.TrimSpace(os.Getenv("TECHSTACK_EXPECTED_DATABASE_IDENTITY")) == "" {
		return errors.New("native Execution requires verify-only retained custody without admin or migration credentials")
	}
	var grants map[string]executioncustody.CallerGrant
	decoder := json.NewDecoder(strings.NewReader(os.Getenv("TECHSTACK_ADVANCED_ISSUER_CALLER_GRANTS")))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&grants) != nil || len(grants) != 1 || decoder.Decode(new(any)) != io.EOF {
		return errors.New("native Execution requires exactly one explicit caller grant")
	}
	var selectedGrant executioncustody.CallerGrant
	for _, grant := range grants {
		if !testidentity.IsTestSubject(grant.OwnerID) || grant.TenantID != testidentity.OwnerTenant(grant.OwnerID) {
			return errors.New("native Execution requires its canonical synthetic owner tenant")
		}
		selectedGrant = grant
	}
	runtimeConfig, err := config.Load("")
	if err != nil {
		return err
	}
	if !runtimeConfig.DeploymentMode.IsSaaS() {
		return errors.New("managed synthetic Execution requires its hosted runtime admission")
	}
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		return err
	}
	app, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()
	if err := db.VerifyPrivateExecutionApplicationRole(ctx, app.DB); err != nil {
		return err
	}
	if _, err := db.VerifyStagingApplicationBinding(ctx, app.DB); err != nil {
		return err
	}
	if err := db.VerifyExpectedDatabaseIdentity(ctx, app.DB); err != nil {
		return err
	}
	if err := app.VerifyMigrations(ctx); err != nil {
		return err
	}
	issuer, custody, err := advancedissuer.OpenBound(ctx, app, auth.GetEncryptor())
	if err != nil {
		return err
	}
	defer custody.Close()
	providerDB, err := openProviderControlRuntimeDatabase(ctx, app.DB)
	if err != nil {
		return err
	}
	defer providerDB.Close()
	binding, err := db.VerifyStagingRuntimeBinding(ctx, providerDB.DB)
	if err != nil {
		return err
	}
	if err := stageowner.PrepareStageRuntime(binding, selectedGrant.TenantID); err != nil {
		return err
	}
	if err := preparePrivateCandidateRelease(binding, app.DB, selectedGrant.TenantID); err != nil {
		return err
	}
	if !releasechannel.StageRuntimePrepared() {
		return releasechannel.ErrStageOwnerNotAdmitted
	}
	var backupWorker *backupWorkerRuntime
	if strings.TrimSpace(os.Getenv(backupWorkerModeEnv)) != "" || strings.TrimSpace(os.Getenv(db.EnvBackupWorkerDatabaseURL)) != "" || strings.TrimSpace(os.Getenv(backupEntitlementsDSNEnv)) != "" {
		// The explicit profile shares this process's genuine sealed owner
		// admission, but uses its own finite SQL principal. It adds no listener
		// and cannot activate Stage ownership from fixture configuration.
		backupWorker, err = openBackupWorker(ctx, true)
		if err != nil {
			return err
		}
		defer backupWorker.database.Close()
		defer backupWorker.entitlements.Close()
	}
	var householdWorker *household.SystemRuntime
	for _, name := range []string{"TECHSTACK_HOUSEHOLD_WORKER_MODE", "TECHSTACK_HOUSEHOLD_CLOUD_DATABASE_URL", "TECHSTACK_HOUSEHOLD_CLOUD_EXPECTED_DATABASE_IDENTITY", "TECHSTACK_HOUSEHOLD_WORKER_DATABASE_URL", "TECHSTACK_HOUSEHOLD_RELAY_ORIGIN", "TECHSTACK_HOUSEHOLD_RELAY_SIGNING_KEY", "TECHSTACK_HOUSEHOLD_NOTIFICATIONS_ORIGIN", "TECHSTACK_HOUSEHOLD_NOTIFICATIONS_SIGNING_KEY"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			continue
		}
		// Reuse this genuine private Execution admission. Partial custody fails
		// closed in the worker factory; no user token or app SQL role is reused.
		householdWorker, err = household.OpenSystemWorkerFromEnv(ctx, true)
		if err != nil {
			return err
		}
		defer householdWorker.Close()
		break
	}
	// Produce durable notification intent in the existing application outbox.
	// Delivery stays with its existing HTTP worker; this process needs no
	// notifications-engine caller credential.
	outbox := notifications.NewOutbox(app.DB, nil, logger.Default().Logger)
	providerRuntime, actions, err := composeProviderControl(providerDB.DB, runtimeConfig.Edition, logger.Default(), outbox, nil)
	if err != nil {
		return err
	}
	servers := controlplane.NewPostgresStore(providerDB.DB)
	leases := vmleases.NewService(vmleases.NewPostgresStore(providerDB.DB), vmleases.ServiceConfig{})
	monthly := &monthlyruntime.Service{Leases: leases, Runtime: &monthlyruntime.NativeRuntimeClient{Servers: servers}}
	channel := &executionchannel.Channel{Servers: servers, Targets: jobs.NewMonthlyRuntimeTargetResolver(monthly, servers)}
	managedDay2.setChannel(channel)
	provider := &executioncustody.Provider{Database: providerDB.DB, AuditDatabase: app.DB, Runtime: providerRuntime, Actions: actions, Power: managedDay2.powerController(), Channel: channel, Envelopes: executioncustody.NewGatewayAdmission(runtimeConfig, app.DB), BindingBase: []byte(os.Getenv(auth.EncryptionKeyEnvVar))}
	posture := bootGuardPosture(ctx, &v2Boot{db: app}, nil, outbox, logger.Default())
	if posture != nil {
		provider.LocalGrant = func(ctx context.Context, tenant, worker string, generation int64) (*guardbinding.LocalRepairGrant, error) {
			tx, err := db.BeginTenantTransaction(ctx, app.DB, tenant)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			var server, owner string
			if err := tx.QueryRowContext(ctx, `SELECT id,owner_subject_id FROM servers WHERE tenant_id=$1 AND worker_id=$2`, tenant, worker).Scan(&server, &owner); err != nil {
				return nil, err
			}
			return posture.LocalRepairGrant(ctx, guardposture.Identity{TenantID: tenant, WorkerID: worker, ServerID: server, OwnerSubjectID: owner}, generation)
		}
	}

	handler, err := executioncustody.NewRemoteHandler(issuer, grants, provider)
	if err != nil {
		return err
	}
	addr := strings.TrimSpace(os.Getenv("TECHSTACK_EXECUTION_LISTEN"))
	if addr == "" {
		addr = "127.0.0.1:8097"
	}
	mux := http.NewServeMux()
	mux.Handle("/internal/execution/v1/", handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		metadata := map[string]string{"status": "ready", "version": version, "revision": buildRevision}
		if backupWorker != nil {
			metadata["backup_worker"] = "ready"
			metadata["backup_runtime_contract_digest"] = compiledBackupRuntimeContractDigest
			metadata["backup_execution_owner_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(controlplane.ProcessExecutionOwnerID())))
		}
		if householdWorker != nil {
			metadata["household_worker"] = "ready"
		}
		_ = json.NewEncoder(w).Encode(metadata)
	})
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	workerCtx, cancelWorker := context.WithCancel(signalCtx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); providerRuntime.Run(workerCtx) }()
	defer func() { cancelWorker(); <-workerDone }()
	if backupWorker != nil {
		stopBackup := backupWorker.start(workerCtx)
		defer stopBackup()
	}
	if householdWorker != nil {
		householdCtx, stopHousehold := context.WithCancel(workerCtx)
		householdDone := make(chan struct{})
		go func() {
			defer close(householdDone)
			householdWorker.Worker.Run(householdCtx, func(_ int, err error) {
				if err != nil {
					logger.Default().Logger.Warn("household projection remains pending")
				}
			})
		}()
		defer func() { stopHousehold(); <-householdDone }()
	}
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalCtx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// runExecutionVerification uses the exact authenticated transport used by
// HTTP runtime admission; it needs no database or installation credential.
func runExecutionVerification(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote, err := executioncustody.OpenRemote(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(remote.Metadata())
}
