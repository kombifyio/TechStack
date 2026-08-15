package actions

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Domain type tests (no PocketBase instance required)
// ---------------------------------------------------------------------------

func TestActionCardFieldDefaults(t *testing.T) {
	card := ActionCard{
		ServerID:    "srv-001",
		OwnerID:     "user-001",
		Type:        TypeUpdate,
		Severity:    SeverityWarning,
		Title:       "Kernel update available",
		Description: "Linux 6.12.1 -> 6.12.4",
		Engine:      "update-scanner",
	}

	if card.ServerID != "srv-001" {
		t.Errorf("expected server_id srv-001, got %s", card.ServerID)
	}
	if card.Type != TypeUpdate {
		t.Errorf("expected type %s, got %s", TypeUpdate, card.Type)
	}
	if card.Severity != SeverityWarning {
		t.Errorf("expected severity %s, got %s", SeverityWarning, card.Severity)
	}
	if card.Title != "Kernel update available" {
		t.Errorf("expected title 'Kernel update available', got %s", card.Title)
	}
	if card.Engine != "update-scanner" {
		t.Errorf("expected engine update-scanner, got %s", card.Engine)
	}
}

func TestActionPlanRoundTrip(t *testing.T) {
	plan := ActionPlan{
		Steps: []ActionStep{
			{Description: "Stop service", Command: "systemctl stop nginx", TargetService: "nginx"},
			{Description: "Apply update", Command: "apt upgrade nginx"},
			{Description: "Start service", Command: "systemctl start nginx", TargetService: "nginx"},
		},
		DryRunResult: "no conflicts detected",
		Rollback:     "apt install nginx=1.24.0",
		EstDuration:  "2m30s",
	}

	if len(plan.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].TargetService != "nginx" {
		t.Errorf("expected target_service nginx, got %s", plan.Steps[0].TargetService)
	}
	if plan.Rollback != "apt install nginx=1.24.0" {
		t.Errorf("unexpected rollback: %s", plan.Rollback)
	}
}

func TestAuditEntryFields(t *testing.T) {
	now := time.Now().UTC()
	entry := AuditEntry{
		Timestamp: now,
		Action:    "approved",
		Actor:     "user-001",
		Detail:    "manual approval via dashboard",
	}

	if entry.Action != "approved" {
		t.Errorf("expected action approved, got %s", entry.Action)
	}
	if entry.Actor != "user-001" {
		t.Errorf("expected actor user-001, got %s", entry.Actor)
	}
	if entry.Timestamp != now {
		t.Errorf("timestamp mismatch")
	}
}

func TestCardStatusConstants(t *testing.T) {
	statuses := []CardStatus{
		StatusPending, StatusApproved, StatusExecuting,
		StatusCompleted, StatusDismissed, StatusFailed,
	}
	expected := []string{
		"pending", "approved", "executing",
		"completed", "dismissed", "failed",
	}

	for i, s := range statuses {
		if string(s) != expected[i] {
			t.Errorf("status %d: expected %s, got %s", i, expected[i], s)
		}
	}
}

func TestCardTypeConstants(t *testing.T) {
	types := []CardType{TypeUpdate, TypeErrorFix, TypeResource, TypeDrift, TypeSecurity}
	expected := []string{"update", "error_fix", "resource", "drift", "security"}

	for i, ct := range types {
		if string(ct) != expected[i] {
			t.Errorf("type %d: expected %s, got %s", i, expected[i], ct)
		}
	}
}

func TestSeverityConstants(t *testing.T) {
	sevs := []Severity{SeverityInfo, SeverityWarning, SeverityCritical}
	expected := []string{"info", "warning", "critical"}

	for i, s := range sevs {
		if string(s) != expected[i] {
			t.Errorf("severity %d: expected %s, got %s", i, expected[i], s)
		}
	}
}

func TestListFiltersZeroValue(t *testing.T) {
	f := ListFilters{}
	if f.Status != "" || f.Severity != "" || f.ServerID != "" {
		t.Error("zero-value ListFilters should have empty strings")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle transition tests
// ---------------------------------------------------------------------------

func TestLifecycleValidTransitions(t *testing.T) {
	valid := []struct {
		from CardStatus
		to   CardStatus
	}{
		{StatusPending, StatusApproved},
		{StatusPending, StatusDismissed},
		{StatusApproved, StatusExecuting},
		{StatusExecuting, StatusCompleted},
		{StatusExecuting, StatusFailed},
		{StatusFailed, StatusApproved}, // retry
	}

	for _, tc := range valid {
		if err := ValidateTransition(tc.from, tc.to); err != nil {
			t.Errorf("expected valid transition %s -> %s, got error: %v", tc.from, tc.to, err)
		}
	}
}

func TestLifecycleInvalidTransitions(t *testing.T) {
	invalid := []struct {
		from CardStatus
		to   CardStatus
	}{
		{StatusPending, StatusExecuting},
		{StatusPending, StatusCompleted},
		{StatusPending, StatusFailed},
		{StatusApproved, StatusDismissed},
		{StatusApproved, StatusCompleted},
		{StatusApproved, StatusFailed},
		{StatusExecuting, StatusPending},
		{StatusExecuting, StatusApproved},
		{StatusExecuting, StatusDismissed},
		{StatusCompleted, StatusPending},
		{StatusCompleted, StatusApproved},
		{StatusCompleted, StatusFailed},
		{StatusDismissed, StatusPending},
		{StatusDismissed, StatusApproved},
		{StatusFailed, StatusPending},
		{StatusFailed, StatusExecuting},
		{StatusFailed, StatusCompleted},
		{StatusFailed, StatusDismissed},
	}

	for _, tc := range invalid {
		if err := ValidateTransition(tc.from, tc.to); err == nil {
			t.Errorf("expected error for invalid transition %s -> %s, got nil", tc.from, tc.to)
		}
	}
}

func TestLifecycleNoOpTransition(t *testing.T) {
	for _, s := range []CardStatus{StatusPending, StatusApproved, StatusExecuting, StatusCompleted, StatusDismissed, StatusFailed} {
		if err := ValidateTransition(s, s); err == nil {
			t.Errorf("expected error for no-op transition %s -> %s", s, s)
		}
	}
}

func TestLifecycleFullHappyPath(t *testing.T) {
	// pending -> approved -> executing -> completed
	steps := []struct {
		from CardStatus
		to   CardStatus
	}{
		{StatusPending, StatusApproved},
		{StatusApproved, StatusExecuting},
		{StatusExecuting, StatusCompleted},
	}

	current := StatusPending
	for _, step := range steps {
		if current != step.from {
			t.Fatalf("state mismatch: expected %s, at %s", step.from, current)
		}
		if err := ValidateTransition(step.from, step.to); err != nil {
			t.Fatalf("happy path broke at %s -> %s: %v", step.from, step.to, err)
		}
		current = step.to
	}

	if current != StatusCompleted {
		t.Errorf("expected final state completed, got %s", current)
	}
}

func TestLifecycleRetryPath(t *testing.T) {
	// pending -> approved -> executing -> failed -> approved -> executing -> completed
	steps := []struct {
		from CardStatus
		to   CardStatus
	}{
		{StatusPending, StatusApproved},
		{StatusApproved, StatusExecuting},
		{StatusExecuting, StatusFailed},
		{StatusFailed, StatusApproved},
		{StatusApproved, StatusExecuting},
		{StatusExecuting, StatusCompleted},
	}

	current := StatusPending
	for _, step := range steps {
		if current != step.from {
			t.Fatalf("state mismatch: expected %s, at %s", step.from, current)
		}
		if err := ValidateTransition(step.from, step.to); err != nil {
			t.Fatalf("retry path broke at %s -> %s: %v", step.from, step.to, err)
		}
		current = step.to
	}

	if current != StatusCompleted {
		t.Errorf("expected final state completed, got %s", current)
	}
}
