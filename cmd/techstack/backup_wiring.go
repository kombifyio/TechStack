package main

import (
	"context"
	"time"

	"github.com/kombifyio/techstack/internal/backupjobs"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

// backupScheduleProjector records the cadence a managed rollout selected so the
// due-stack scanner can find it. Returning nil disables the projection rather
// than failing the boot: a runtime without a control-plane database has no
// managed rollouts to schedule in the first place.
func backupScheduleProjector(boot *v2Boot, log *logger.Logger) jobs.BackupScheduleProjector {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	store, err := backupjobs.NewStore(boot.db.DB)
	if err != nil {
		if log != nil {
			log.Warn("backup_schedule_projector_unavailable", "error", err)
		}
		return nil
	}
	return func(ctx context.Context, projection jobs.BackupScheduleProjection) error {
		return store.Upsert(ctx, backupjobs.Schedule{
			TenantID:       projection.TenantID,
			StackID:        projection.StackID,
			OwnerID:        projection.OwnerID,
			StackName:      projection.StackName,
			Cadence:        backupjobs.Cadence(projection.Cadence),
			HourUTC:        projection.HourUTC,
			MinuteUTC:      projection.MinuteUTC,
			WeekdayUTC:     projection.WeekdayUTC,
			IncludeContent: projection.IncludeContent,
			Enabled:        true,
		}, time.Now().UTC())
	}
}

// composeBackupScanner builds the due-stack scanner. It returns nil when any
// authority is missing, which disables scheduled backups rather than running
// them ungated: NewScanner refuses an absent admission for the same reason.
//
// A disabled scanner is visible in the log and denies nothing that was already
// promised, because without it no backup is ever due.
func composeBackupScanner(
	boot *v2Boot,
	featureSvc *features.Service,
	orch *orchestrator.Orchestrator,
	log *logger.Logger,
) *backupjobs.Scanner {
	if boot == nil || boot.db == nil || boot.db.DB == nil || orch == nil {
		return nil
	}
	if featureSvc == nil {
		// Without the entitlement chain every backup would have to deny, so
		// running the loop would only burn passes producing denials.
		if log != nil {
			log.Warn("backup_scanner_unavailable", "reason", "feature_service_not_configured")
		}
		return nil
	}
	schedules, err := backupjobs.NewStore(boot.db.DB)
	if err != nil {
		if log != nil {
			log.Warn("backup_scanner_unavailable", "reason", "schedule_store_invalid", "error", err)
		}
		return nil
	}
	usage, err := backupstore.NewPostgresUsageStore(boot.db.DB)
	if err != nil {
		if log != nil {
			log.Warn("backup_scanner_unavailable", "reason", "usage_store_invalid", "error", err)
		}
		return nil
	}
	admission, err := backupjobs.NewAdmission(backupjobs.AdmissionConfig{
		Features: featureSvc,
		Usage:    usage,
	})
	if err != nil {
		if log != nil {
			log.Warn("backup_scanner_unavailable", "reason", "admission_invalid", "error", err)
		}
		return nil
	}
	scanner, err := backupjobs.NewScanner(backupjobs.ScannerConfig{
		Store:     schedules,
		Admission: admission,
		Enqueuer:  orch,
		OnError: func(scanErr error) {
			if log != nil {
				log.Warn("backup_scan_pass_blocked", "error", scanErr)
			}
		},
		OnDenied: func(due backupjobs.Due, decision jobs.BackupAdmissionDecision) {
			if log == nil {
				return
			}
			// A denial is expected operation, not a fault: it is how an
			// over-quota or unentitled stack is meant to end. It is logged at
			// info with the measured basis so support can answer "why did my
			// backup not run" without reading the database.
			log.Info("backup_scan_denied",
				"tenant_id", due.TenantID, "stack_id", due.StackID,
				"quota_bytes", decision.QuotaBytes, "used_bytes", decision.UsedBytes)
		},
	})
	if err != nil {
		if log != nil {
			log.Warn("backup_scanner_unavailable", "reason", "scanner_invalid", "error", err)
		}
		return nil
	}
	return scanner
}
