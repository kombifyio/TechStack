package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BackupScheduleProjection is the cadence the control plane needs in order to
// know a stack is due. It is derived from the ResolvedPlan at deploy, so the
// projection tracks the plan instead of becoming a second source of truth:
// StackKits owns #BackupScheduleV1 and this carries a copy of its decision.
type BackupScheduleProjection struct {
	TenantID  string
	StackID   string
	OwnerID   string
	StackName string
	// Cadence is the closed #BackupScheduleV1 vocabulary: hourly, daily, weekly.
	Cadence    string
	HourUTC    int
	MinuteUTC  int
	WeekdayUTC string
	// IncludeContent is true when the policy selects any class beyond the
	// config group, which is what the entitlement gate charges for.
	IncludeContent bool
}

// BackupScheduleProjector persists one stack's cadence. It is a narrow
// callback in the same shape as AutoDeployAdmission: the jobs package never
// owns a schedule table, it only reports what the plan selected.
type BackupScheduleProjector func(context.Context, BackupScheduleProjection) error

// contentBackupDataClasses are the #BackupDataClassV1 members outside the
// config coverage group, and they are only consulted for plans resolved before
// #BackupPolicyV1 carried a coverage group. Keep them in step with
// #BackupCoverageClassesV1 in kombify-StackKits: documents is content rather
// than media by the 2026-09-18 decision, because a Paperless customer whose
// search index is backed up without the PDFs it indexes has an index of
// nothing, and telemetry-timeseries is content because it grows unattended and
// must not ride along with config.
var contentBackupDataClasses = map[string]struct{}{
	"database": {}, "user-content": {}, "documents": {}, "telemetry-timeseries": {},
	"photos": {}, "large-media": {},
}

// backupScheduleFromResolvedPlan reads backupPolicy out of the plan. A plan
// without one is not an error: most stacks select no backup, and the caller
// distinguishes absence from malformed intent by the returned flag.
func backupScheduleFromResolvedPlan(resolvedPlan []byte) (BackupScheduleProjection, bool, error) {
	if len(resolvedPlan) == 0 || len(resolvedPlan) > 32<<20 {
		return BackupScheduleProjection{}, false, errors.New("managed Cloud ResolvedPlan must be a bounded document")
	}
	var plan struct {
		BackupPolicy *struct {
			// Coverage is the resolved #BackupCoverageGroupV1. It is the plan's
			// own answer to "how much does this stack back up", so reading it
			// beats re-deriving the group from the class list here.
			Coverage string `json:"coverage"`
			Schedule *struct {
				Cadence    string `json:"cadence"`
				HourUTC    *int   `json:"hourUTC"`
				MinuteUTC  *int   `json:"minuteUTC"`
				WeekdayUTC string `json:"weekdayUTC"`
			} `json:"schedule"`
			DataClasses []string `json:"dataClasses"`
		} `json:"backupPolicy"`
	}
	if err := json.Unmarshal(resolvedPlan, &plan); err != nil {
		return BackupScheduleProjection{}, false, fmt.Errorf("decode managed Cloud ResolvedPlan backup policy: %w", err)
	}
	if plan.BackupPolicy == nil || plan.BackupPolicy.Schedule == nil {
		return BackupScheduleProjection{}, false, nil
	}

	schedule := plan.BackupPolicy.Schedule
	cadence := strings.ToLower(strings.TrimSpace(schedule.Cadence))
	switch cadence {
	case "hourly", "daily", "weekly":
	default:
		// A malformed cadence is refused rather than defaulted. Silently
		// choosing one would give the customer a backup rhythm nobody selected.
		return BackupScheduleProjection{}, false, fmt.Errorf("backup schedule cadence %q is not admissible", schedule.Cadence)
	}

	projection := BackupScheduleProjection{
		Cadence:    cadence,
		HourUTC:    2,
		MinuteUTC:  0,
		WeekdayUTC: "sunday",
	}
	if schedule.HourUTC != nil {
		projection.HourUTC = *schedule.HourUTC
	}
	if schedule.MinuteUTC != nil {
		projection.MinuteUTC = *schedule.MinuteUTC
	}
	if weekday := strings.ToLower(strings.TrimSpace(schedule.WeekdayUTC)); weekday != "" {
		projection.WeekdayUTC = weekday
	}
	switch coverage := strings.ToLower(strings.TrimSpace(plan.BackupPolicy.Coverage)); coverage {
	case "", "config":
		// An absent group is a plan resolved before the contract carried one.
		// Either way the class list below still has to agree.
	case "content", "media":
		projection.IncludeContent = true
	default:
		// An unknown group is refused rather than read as config. Guessing the
		// cheaper answer would bill a media stack as a config one.
		return BackupScheduleProjection{}, false, fmt.Errorf("backup coverage group %q is not admissible", plan.BackupPolicy.Coverage)
	}
	if !projection.IncludeContent {
		// The class list is what actually reaches the repository, so it is
		// checked even when the group says config. #BackupPolicyV1 rejects a
		// plan where the two disagree; if one ever arrives anyway, the answer
		// that costs storage is the true one.
		for _, class := range plan.BackupPolicy.DataClasses {
			if _, content := contentBackupDataClasses[strings.ToLower(strings.TrimSpace(class))]; content {
				projection.IncludeContent = true
				break
			}
		}
	}
	return projection, true, nil
}
