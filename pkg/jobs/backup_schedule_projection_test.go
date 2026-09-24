package jobs

import (
	"encoding/json"
	"testing"
)

func planWithBackupPolicy(policy map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"backupPolicy": policy})
	return raw
}

func TestBackupScheduleReadsTheSelectedCadence(t *testing.T) {
	plan := planWithBackupPolicy(map[string]any{
		"schedule":    map[string]any{"cadence": "weekly", "hourUTC": 3, "minuteUTC": 30, "weekdayUTC": "Monday"},
		"dataClasses": []string{"config", "secret-material"},
	})

	projection, found, err := backupScheduleFromResolvedPlan(plan)
	if err != nil || !found {
		t.Fatalf("expected a schedule: found=%v err=%v", found, err)
	}
	if projection.Cadence != "weekly" || projection.HourUTC != 3 || projection.MinuteUTC != 30 {
		t.Fatalf("schedule = %+v", projection)
	}
	if projection.WeekdayUTC != "monday" {
		t.Fatalf("weekday must normalise to the wire vocabulary, got %q", projection.WeekdayUTC)
	}
	if projection.IncludeContent {
		t.Fatal("a config-only policy must not be charged as content")
	}
}

// TestBackupScheduleDetectsContentClasses pins the flag the entitlement gate
// charges on. documents counts as content by the 2026-09-18 decision: backing
// up a Paperless search index without the PDFs it indexes protects nothing.
func TestBackupScheduleDetectsContentClasses(t *testing.T) {
	for _, class := range []string{"database", "user-content", "documents", "telemetry-timeseries", "photos", "large-media"} {
		plan := planWithBackupPolicy(map[string]any{
			"schedule":    map[string]any{"cadence": "daily"},
			"dataClasses": []string{"config", class},
		})
		projection, found, err := backupScheduleFromResolvedPlan(plan)
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", class, found, err)
		}
		if !projection.IncludeContent {
			t.Fatalf("data class %q must mark the policy as content-bearing", class)
		}
	}
}

// TestBackupScheduleReadsTheCoverageGroup pins that the plan's own answer to
// "how much does this stack back up" is what the gate charges on, rather than a
// group re-derived here from the class list.
func TestBackupScheduleReadsTheCoverageGroup(t *testing.T) {
	for _, test := range []struct {
		coverage string
		classes  []string
		content  bool
	}{
		{coverage: "config", classes: []string{"config", "serverless-config"}, content: false},
		{coverage: "content", classes: []string{"config", "database"}, content: true},
		// A media stack that has not filled its libraries yet is still a media
		// stack: the group is what it bought, not what it has written.
		{coverage: "media", classes: []string{"config"}, content: true},
		// The two should never disagree - #BackupPolicyV1 refuses it - but if
		// one arrives anyway, the answer that costs storage is the true one.
		{coverage: "config", classes: []string{"config", "photos"}, content: true},
		// A plan resolved before the contract carried a group still reads.
		{coverage: "", classes: []string{"config", "user-content"}, content: true},
		{coverage: "", classes: []string{"config"}, content: false},
	} {
		policy := map[string]any{
			"schedule":    map[string]any{"cadence": "daily"},
			"dataClasses": test.classes,
		}
		if test.coverage != "" {
			policy["coverage"] = test.coverage
		}
		projection, found, err := backupScheduleFromResolvedPlan(planWithBackupPolicy(policy))
		if err != nil || !found {
			t.Fatalf("coverage %q: found=%v err=%v", test.coverage, found, err)
		}
		if projection.IncludeContent != test.content {
			t.Errorf("coverage %q with classes %v: IncludeContent = %v, want %v",
				test.coverage, test.classes, projection.IncludeContent, test.content)
		}
	}
}

// TestBackupScheduleRefusesAnUnknownCoverageGroup keeps an unreadable group
// from being charged as the cheapest one.
func TestBackupScheduleRefusesAnUnknownCoverageGroup(t *testing.T) {
	plan := planWithBackupPolicy(map[string]any{
		"schedule": map[string]any{"cadence": "daily"},
		"coverage": "everything",
	})
	if _, _, err := backupScheduleFromResolvedPlan(plan); err == nil {
		t.Fatal("a coverage group outside the contract must be refused, not read as config")
	}
}

func TestBackupScheduleDefaultsOnlyWhereTheContractDoes(t *testing.T) {
	plan := planWithBackupPolicy(map[string]any{"schedule": map[string]any{"cadence": "daily"}})
	projection, found, err := backupScheduleFromResolvedPlan(plan)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	// #BackupScheduleV1 defaults hourUTC 2, minuteUTC 0, weekdayUTC sunday.
	if projection.HourUTC != 2 || projection.MinuteUTC != 0 || projection.WeekdayUTC != "sunday" {
		t.Fatalf("defaults drifted from the contract: %+v", projection)
	}
}

// TestBackupScheduleRefusesAnInadmissibleCadence keeps a malformed plan from
// silently choosing a backup rhythm the customer never selected.
func TestBackupScheduleRefusesAnInadmissibleCadence(t *testing.T) {
	plan := planWithBackupPolicy(map[string]any{"schedule": map[string]any{"cadence": "monthly"}})
	if _, _, err := backupScheduleFromResolvedPlan(plan); err == nil {
		t.Fatal("a cadence outside the contract must be refused, not defaulted")
	}
}

func TestBackupScheduleAbsenceIsNotAFailure(t *testing.T) {
	// Most stacks select no backup at all.
	if _, found, err := backupScheduleFromResolvedPlan([]byte(`{"stackId":"s"}`)); err != nil || found {
		t.Fatalf("a plan without a backup policy must be absence, not error: found=%v err=%v", found, err)
	}
	// A backup target with no schedule is a manual-only repository.
	plan := planWithBackupPolicy(map[string]any{"dataClasses": []string{"config"}})
	if _, found, err := backupScheduleFromResolvedPlan(plan); err != nil || found {
		t.Fatalf("a policy without a schedule must be absence, not error: found=%v err=%v", found, err)
	}
	if _, _, err := backupScheduleFromResolvedPlan(nil); err == nil {
		t.Fatal("an empty document must be refused")
	}
}
