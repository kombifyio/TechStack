package onboarding

import (
	"testing"
)

const (
	testNow   = "2026-08-21T10:00:00Z"
	testLater = "2026-08-21T11:00:00Z"
)

func testJourney(t *testing.T) Journey {
	t.Helper()
	journey, ok := getJourney("techstack.platform")
	if !ok {
		t.Fatal("techstack.platform journey is not embedded")
	}
	return journey
}

// Mirrors the core suite's "keeps a completion when its predicate later
// reads false" and "a manual tick wins over a disagreeing resolver".
func TestMergeDerivedSignalsIsMonotonic(t *testing.T) {
	journey := testJourney(t)
	record := migrateRecord(nil, journey, testNow)

	record, changed := mergeDerivedSignals(record, map[string]bool{PredicateKitDeploymentExists: true}, journey, testNow)
	if !changed || !record.hasCompleted("techstack.deploy_stackkit") {
		t.Fatalf("expected techstack.deploy_stackkit to complete from its predicate: %+v", record)
	}

	// Resolver flips back to false: the completion stays, nothing is written.
	again, changed := mergeDerivedSignals(record, map[string]bool{PredicateKitDeploymentExists: false}, journey, testLater)
	if changed {
		t.Fatal("a false predicate must not produce a write")
	}
	if !again.hasCompleted("techstack.deploy_stackkit") {
		t.Fatal("a false predicate must never remove a completion")
	}

	// A manual tick on a cost-bearing step survives the resolver too.
	ticked, changed := applySignal(record, journey, ActionSkip, "techstack.add_server", "", testNow)
	if !changed || !ticked.hasCompleted("techstack.add_server") || ticked.Revision != 1 {
		t.Fatalf("manual skip should complete and bump revision: %+v", ticked)
	}
	after, _ := mergeDerivedSignals(ticked, map[string]bool{"techstack.server_registered": false}, journey, testLater)
	if !after.hasCompleted("techstack.add_server") {
		t.Fatal("resolver disagreement must not undo a manual tick")
	}
}

func TestCostBearingStepRefusesReportedCompletion(t *testing.T) {
	journey := testJourney(t)
	record := migrateRecord(nil, journey, testNow)

	if !costBearingRefusesReported(journey, ActionComplete, "techstack.deploy_stackkit", "") {
		t.Fatal("a reported completion of a cost-bearing step must be refused")
	}
	if costBearingRefusesReported(journey, ActionComplete, "techstack.deploy_service", "") {
		t.Fatal("a non-cost-bearing step may be reported")
	}
	next, changed := applySignal(record, journey, ActionComplete, "techstack.deploy_stackkit", "reported", testNow)
	if changed || next.hasCompleted("techstack.deploy_stackkit") {
		t.Fatal("reported completion must not complete a cost-bearing step")
	}
}

func TestResetClearsSeenStepsButAVersionReopenKeepsThem(t *testing.T) {
	journey := testJourney(t)
	record := migrateRecord(nil, journey, testNow)
	record, _ = markCoachSeen(record, "techstack.add_server", testNow)

	older := record
	older.JourneyVersion = 0
	reopened, changed := reopenForVersion(older, journey, testLater)
	if !changed || len(reopened.SeenSteps) != 1 || reopened.Status != "active" {
		t.Fatalf("version reopen must keep seen_steps: %+v", reopened)
	}

	reset, _ := applySignal(record, journey, ActionReset, "", "", testLater)
	if len(reset.SeenSteps) != 0 || len(reset.Completed) != 0 || reset.Revision != record.Revision+1 {
		t.Fatalf("reset must clear seen_steps and completions and bump: %+v", reset)
	}
}

func TestDismissedSurvivesAVersionBump(t *testing.T) {
	journey := testJourney(t)
	record := migrateRecord(nil, journey, testNow)
	record, _ = applySignal(record, journey, ActionDismiss, "", "", testNow)
	record.JourneyVersion = 0

	after, changed := reopenForVersion(record, journey, testLater)
	if !changed || after.Status != "dismissed" || after.JourneyVersion != journey.JourneyVersion {
		t.Fatalf("an explicit dismissal is respected across versions: %+v", after)
	}
}

func TestMigrateDropsUnknownStepsAndKeepsKnownOnes(t *testing.T) {
	journey := testJourney(t)
	raw := map[string]any{
		"journey_version": float64(1),
		"completed": []any{
			map[string]any{"step_id": "retired.step", "at": testNow, "source": "reported"},
			map[string]any{"step_id": "techstack.configure_intent", "at": testNow, "source": "import"},
			map[string]any{"step_id": "techstack.configure_intent", "at": testLater, "source": "reported"},
		},
		"seen_steps": []any{"techstack.add_server", "nope"},
		"status":     "dismissed",
		"revision":   float64(4),
	}
	record := migrateRecord(raw, journey, testNow)
	if len(record.Completed) != 1 || record.Completed[0].Source != "import" {
		t.Fatalf("unknown and duplicate completions must be dropped: %+v", record.Completed)
	}
	if len(record.SeenSteps) != 1 || record.Revision != 4 || record.Status != "dismissed" || record.DismissedAt == nil {
		t.Fatalf("known fields must survive, dismissal gets a timestamp: %+v", record)
	}
}
