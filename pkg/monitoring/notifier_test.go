package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookNotifier_Success(t *testing.T) {
	var received Alert
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("failed to unmarshal alert: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL)
	if n.Name() != "webhook" {
		t.Errorf("expected name 'webhook', got %q", n.Name())
	}

	alert := Alert{
		RuleName: "TestRule",
		Severity: "critical",
		Message:  "test alert",
		Value:    95.5,
		Labels:   map[string]string{"env": "test"},
		FiredAt:  time.Now(),
	}

	err := n.Notify(context.Background(), alert)
	if err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	if received.RuleName != "TestRule" {
		t.Errorf("expected rule_name 'TestRule', got %q", received.RuleName)
	}
	if received.Severity != "critical" {
		t.Errorf("expected severity 'critical', got %q", received.Severity)
	}
	if received.Value != 95.5 {
		t.Errorf("expected value 95.5, got %v", received.Value)
	}
}

func TestWebhookNotifier_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "server error")
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL)
	err := n.Notify(context.Background(), Alert{RuleName: "Test", Severity: "info", FiredAt: time.Now()})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestMultiNotifier(t *testing.T) {
	n1 := &mockNotifier{}
	n2 := &mockNotifier{}
	multi := NewMultiNotifier(n1, n2)

	if multi.Name() != "multi" {
		t.Errorf("expected name 'multi', got %q", multi.Name())
	}

	alert := Alert{RuleName: "Multi", Severity: "warning", FiredAt: time.Now()}
	err := multi.Notify(context.Background(), alert)
	if err != nil {
		t.Fatalf("MultiNotifier.Notify failed: %v", err)
	}

	if len(n1.received()) != 1 {
		t.Errorf("expected n1 to receive 1 alert, got %d", len(n1.received()))
	}
	if len(n2.received()) != 1 {
		t.Errorf("expected n2 to receive 1 alert, got %d", len(n2.received()))
	}
}

func TestMultiNotifier_PartialFailure(t *testing.T) {
	n1 := &mockNotifier{err: fmt.Errorf("n1 failed")}
	n2 := &mockNotifier{}
	multi := NewMultiNotifier(n1, n2)

	alert := Alert{RuleName: "Partial", Severity: "info", FiredAt: time.Now()}
	err := multi.Notify(context.Background(), alert)

	// Should return an error because n1 failed.
	if err == nil {
		t.Fatal("expected error from MultiNotifier when one child fails")
	}

	// n2 should still have received the alert.
	if len(n2.received()) != 1 {
		t.Error("expected n2 to receive alert despite n1 failure")
	}
}

func TestSeverityIcon(t *testing.T) {
	tests := []struct {
		severity string
		want     string
	}{
		{"critical", "[CRITICAL]"},
		{"warning", "[WARNING]"},
		{"info", "[INFO]"},
		{"unknown", "[ALERT]"},
		{"", "[ALERT]"},
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := severityIcon(tt.severity)
			if got != tt.want {
				t.Errorf("severityIcon(%q) = %q, want %q", tt.severity, got, tt.want)
			}
		})
	}
}
