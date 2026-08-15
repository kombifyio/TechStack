package detection

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

func TestDefaultEngines_Count(t *testing.T) {
	engines := DefaultEngines()
	if len(engines) != 5 {
		t.Fatalf("expected 5 default engines, got %d", len(engines))
	}

	names := map[string]bool{
		"update-scanner":        false,
		"error-pattern-matcher": false,
		"resource-monitor":      false,
		"security-scanner":      false,
		"config-drift-detector": false,
	}
	for _, e := range engines {
		if _, ok := names[e.Name()]; !ok {
			t.Errorf("unexpected engine: %s", e.Name())
		}
		names[e.Name()] = true
	}
	for name, seen := range names {
		if !seen {
			t.Errorf("missing engine: %s", name)
		}
	}
}

func TestUpdateScanner_SecurityUpdates(t *testing.T) {
	e := NewUpdateScanner()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
		OSPackages: []PackageInfo{
			{Name: "nginx", CurrentVersion: "1.25.3", LatestVersion: "1.27.0", CVESeverity: "critical", CVEIDs: []string{"CVE-2024-1234"}},
			{Name: "curl", CurrentVersion: "8.1.0", LatestVersion: "8.5.0"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("expected 2 cards (security + normal), got %d", len(cards))
	}
	if cards[0].Type != actions.TypeSecurity {
		t.Errorf("first card should be security, got %s", cards[0].Type)
	}
	if cards[0].Severity != actions.SeverityCritical {
		t.Errorf("security card should be critical, got %s", cards[0].Severity)
	}
	if cards[1].Type != actions.TypeUpdate {
		t.Errorf("second card should be update, got %s", cards[1].Type)
	}
}

func TestUpdateScanner_NoUpdates(t *testing.T) {
	e := NewUpdateScanner()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID:   "srv-1",
		OwnerID:    "user-1",
		OSPackages: []PackageInfo{{Name: "nginx", CurrentVersion: "1.27.0", LatestVersion: "1.27.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Errorf("expected 0 cards for up-to-date packages, got %d", len(cards))
	}
}

func TestErrorMatcher_HighCount(t *testing.T) {
	e := NewErrorMatcher()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
		ErrorPatterns: map[string][]ErrorPattern{
			"grafana": {
				{Pattern: "database locked", Count: 47, FirstSeen: "2h ago", LastSeen: "now", Service: "grafana"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].Type != actions.TypeErrorFix {
		t.Errorf("expected error_fix type, got %s", cards[0].Type)
	}
}

func TestErrorMatcher_BelowThreshold(t *testing.T) {
	e := NewErrorMatcher()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
		ErrorPatterns: map[string][]ErrorPattern{
			"nginx": {{Pattern: "upstream timeout", Count: 3}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Errorf("expected 0 cards below threshold, got %d", len(cards))
	}
}

func TestResourceMonitor_CPUWarning(t *testing.T) {
	e := NewResourceMonitor()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID:        "srv-1",
		OwnerID:         "user-1",
		ResourceMetrics: &ResourceSnapshot{CPUPercent: 90.0, MemPercent: 50.0, DiskPercent: 60.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card (CPU), got %d", len(cards))
	}
	if cards[0].Severity != actions.SeverityWarning {
		t.Errorf("expected warning, got %s", cards[0].Severity)
	}
}

func TestResourceMonitor_AllCritical(t *testing.T) {
	e := NewResourceMonitor()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID:        "srv-1",
		OwnerID:         "user-1",
		ResourceMetrics: &ResourceSnapshot{CPUPercent: 98.0, MemPercent: 97.0, DiskPercent: 96.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 3 {
		t.Fatalf("expected 3 cards (CPU+MEM+DISK), got %d", len(cards))
	}
	for _, c := range cards {
		if c.Severity != actions.SeverityCritical {
			t.Errorf("expected critical for %s, got %s", c.Title, c.Severity)
		}
	}
}

func TestResourceMonitor_AllHealthy(t *testing.T) {
	e := NewResourceMonitor()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID:        "srv-1",
		OwnerID:         "user-1",
		ResourceMetrics: &ResourceSnapshot{CPUPercent: 30.0, MemPercent: 40.0, DiskPercent: 50.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Errorf("expected 0 cards for healthy resources, got %d", len(cards))
	}
}

func TestSecurityScanner_CVEs(t *testing.T) {
	e := NewSecurityScanner()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
		SecurityFindings: []SecurityFinding{
			{Type: "cve", Severity: "critical", Description: "CVE-2024-9999 in openssl"},
			{Type: "expired_cert", Severity: "high", Description: "TLS cert for api.example.com expired 3d ago"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("expected 2 cards (CVE + cert), got %d", len(cards))
	}
}

func TestSecurityScanner_LowSeveritySkipped(t *testing.T) {
	e := NewSecurityScanner()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
		SecurityFindings: []SecurityFinding{
			{Type: "cve", Severity: "low", Description: "minor info disclosure"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Errorf("expected 0 cards for low severity, got %d", len(cards))
	}
}

func TestDriftDetector_DriftFound(t *testing.T) {
	e := NewDriftDetector()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
		DriftEntries: []DriftEntry{
			{Path: "/etc/nginx/nginx.conf", Expected: "worker_processes 4", Actual: "worker_processes 2", Service: "nginx"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards[0].Type != actions.TypeDrift {
		t.Errorf("expected drift type, got %s", cards[0].Type)
	}
}

func TestDriftDetector_NoDrift(t *testing.T) {
	e := NewDriftDetector()
	cards, err := e.Evaluate(context.Background(), &EvaluationInput{
		ServerID: "srv-1",
		OwnerID:  "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Errorf("expected 0 cards without drift, got %d", len(cards))
	}
}

func TestNilInput_AllEngines(t *testing.T) {
	for _, e := range DefaultEngines() {
		cards, err := e.Evaluate(context.Background(), nil)
		if err != nil {
			t.Errorf("%s: unexpected error on nil input: %v", e.Name(), err)
		}
		if len(cards) != 0 {
			t.Errorf("%s: expected 0 cards on nil input, got %d", e.Name(), len(cards))
		}
	}
}
