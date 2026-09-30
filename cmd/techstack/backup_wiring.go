package main

import (
	"context"
	"time"

	"fmt"

	"github.com/kombifyio/techstack/internal/backupjobs"
	storageauth "github.com/kombifyio/techstack/pkg/auth"
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
	custody, err := backupstore.NewPostgresCustodyStore(boot.db.DB, storageauth.GetEncryptor())
	if err != nil {
		return nil
	}
	admission, err := backupjobs.NewAdmission(backupjobs.AdmissionConfig{
		Features: featureSvc,
		Usage:    backupjobs.CurrentUsage{Custody: custody, Store: usage},
	})
	if err != nil {
		if log != nil {
			log.Warn("backup_scanner_unavailable", "reason", "admission_invalid", "error", err)
		}
		return nil
	}
	orch.ConfigureManagedRestoreAdmission(func(ctx context.Context, req jobs.BackupAdmissionRequest) (jobs.BackupAdmissionDecision, error) {
		schedule, err := schedules.Get(ctx, req.TenantID, req.StackID)
		if err != nil || !schedule.Enabled || schedule.OwnerID != req.UserID {
			return jobs.BackupAdmissionDecision{Denied: true}, fmt.Errorf("current owner-bound backup schedule is unavailable")
		}
		req.IncludeContent = schedule.IncludeContent
		return jobs.AdmitBackup(ctx, admission, req)
	})
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
			// unentitled stack is meant to end. Backups are retention-limited,
			// never size-limited, so the only denial is a missing entitlement.
			log.Info("backup_scan_denied",
				"tenant_id", due.TenantID, "stack_id", due.StackID,
				"reason_code", decision.Details["reason_code"])
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
