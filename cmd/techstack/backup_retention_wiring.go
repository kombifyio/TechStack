package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	storageauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

const (
	defaultBackupRetention  = 30 * 24 * time.Hour
	backupWipeSweepInterval = time.Hour
)

// backupRetention drives the decommission of the managed off-site backup
// store: the token is revoked when the stack is destroyed, the bucket is wiped
// once the stored deadline passes.
type backupRetention struct {
	retirer       *backupstore.Retirer
	retention     func(tenantID string) time.Duration
	log           *logger.Logger
	sweepInterval time.Duration
}

// composeBackupRetention returns nil when no managed backup provider or
// encrypted custody is configured: such a runtime holds no off-site store.
func composeBackupRetention(boot *v2Boot, log *logger.Logger) *backupRetention {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	custody, err := backupstore.NewPostgresCustodyStore(boot.db.DB, storageauth.GetEncryptor())
	if err != nil {
		return nil
	}
	providerConfig, err := backupstore.FromEnv()
	if err != nil {
		return nil
	}
	provider, err := backupstore.NewClient(providerConfig)
	if err != nil {
		return nil
	}
	// A missing purger is not fatal here: the token still revokes at once. A
	// wipe without it fails closed and retries, and the log says why.
	purger, purgerErr := backupstore.S3PurgerFromEnv(providerConfig.AccountID, providerConfig.Jurisdiction)
	if purgerErr != nil && log != nil {
		log.Warn("backup_wipe_purger_unavailable", "error", purgerErr)
	}
	var objects backupstore.ObjectPurger
	if purger != nil {
		objects = purger
	}
	retirer, err := backupstore.NewRetirer(custody, provider, objects)
	if err != nil {
		return nil
	}
	e2eTenant := monthlyruntime.LocalRuntimeE2EBackupGrantTenant(boot.saasMode)
	return &backupRetention{
		retirer:       retirer,
		retention:     func(tenantID string) time.Duration { return backupRetentionFor(tenantID, e2eTenant) },
		log:           log,
		sweepInterval: backupWipeSweepInterval,
	}
}

// backupRetentionFor is how long a decommissioned stack's backups stay
// restorable. The runtime E2E tenant keeps none, so its cleanup proof is
// immediate. TECHSTACK_BACKUPSTORE_RETENTION_DAYS overrides the 30-day default.
func backupRetentionFor(tenantID, e2eTenant string) time.Duration {
	if e2eTenant != "" && strings.TrimSpace(tenantID) == e2eTenant {
		return 0
	}
	if days, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TECHSTACK_BACKUPSTORE_RETENTION_DAYS"))); err == nil && days >= 0 {
		return time.Duration(days) * 24 * time.Hour
	}
	return defaultBackupRetention
}

func (b *backupRetention) decommissioner() jobs.BackupDecommissioner {
	if b == nil {
		return nil
	}
	return func(ctx context.Context, tenantID, stackID string) error {
		return b.retirer.Decommission(ctx, tenantID, stackID, b.retention(tenantID))
	}
}

// Run sweeps stores past their stored deadline until the context ends.
func (b *backupRetention) Run(ctx context.Context) {
	if b == nil {
		return
	}
	b.retirer.Run(ctx, b.sweepInterval, func(err error) {
		if b.log != nil {
			b.log.Warn("backup_wipe_sweep_blocked", "error", err)
		}
	})
}

// backupStoreReadback serves the cleanup readback's backup-store fact. Without
// encrypted custody it returns nil, and the readback never reports absence.
func backupStoreReadback(deps routeDeps) monthlyruntime.BackupStoreReadback {
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		return nil
	}
	custody, err := backupstore.NewPostgresCustodyStore(deps.v2.db.DB, storageauth.GetEncryptor())
	if err != nil {
		return nil
	}
	return custody
}
