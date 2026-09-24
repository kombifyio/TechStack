package monitoring

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockNotifier records all alerts it receives.
type mockNotifier struct {
	mu     sync.Mutex
	alerts []Alert
	err    error // optional: return this error from Notify
}

func (m *mockNotifier) Notify(_ context.Context, alert Alert) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = append(m.alerts, alert)
	return m.err
}

func (m *mockNotifier) Name() string { return "mock" }

func (m *mockNotifier) received() []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]Alert, len(m.alerts))
	copy(cp, m.alerts)
	return cp
}

func newAlertTestTSDB(t *testing.T) *MonitorTSDB {
	t.Helper()
	m, err := NewMonitorTSDB(TSDBConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewMonitorTSDB: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestEvaluate_NoData_NoAlerts(t *testing.T) {
	tsdb := newAlertTestTSDB(t)
	notif := &mockNotifier{}
	rules := []AlertRule{
		{Name: "HighCPU", Expr: `node_cpu_usage_percent > 90`, For: 0, Severity: "critical", Message: "cpu high"},
	}
	engine := NewAlertEngine(tsdb, notif, rules, AlertEngineConfig{})

	// Evaluate with no data in TSDB -- should not fire.
	engine.evaluate(context.Background())

	if len(engine.ActiveAlerts()) != 0 {
		t.Error("expected no active alerts when TSDB is empty")
	}
	if len(notif.received()) != 0 {
		t.Error("expected no notifications when TSDB is empty")
	}
}

func TestEvaluate_FiresImmediately_WhenForIsZero(t *testing.T) {
	tsdb := newAlertTestTSDB(t)
	notif := &mockNotifier{}
	now := time.Now()

	// Write a sample that triggers the rule.
	err := tsdb.Write([]MetricSample{
		{Name: "test_value", Value: 100, Labels: map[string]string{"host": "a"}, Timestamp: now},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	rules := []AlertRule{
		{Name: "TestHigh", Expr: `test_value > 50`, For: 0, Severity: "warning", Message: "value too high"},
	}
	engine := NewAlertEngine(tsdb, notif, rules, AlertEngineConfig{})

	engine.evaluate(context.Background())

	active := engine.ActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert, got %d", len(active))
	}
	if active[0].Rule.Name != "TestHigh" {
		t.Errorf("expected rule name 'TestHigh', got %q", active[0].Rule.Name)
	}
	if !active[0].Active {
		t.Error("expected alert to be active")
	}
	if active[0].FiredAt == nil {
		t.Error("expected FiredAt to be set")
	}

	// Verify notification was sent.
	received := notif.received()
	if len(received) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(received))
	}
	if received[0].RuleName != "TestHigh" {
		t.Errorf("expected notification for 'TestHigh', got %q", received[0].RuleName)
	}
	if received[0].Severity != "warning" {
		t.Errorf("expected severity 'warning', got %q", received[0].Severity)
	}
}

func TestEvaluate_PendingThenFires_WhenForElapsed(t *testing.T) {
	tsdb := newAlertTestTSDB(t)
	notif := &mockNotifier{}
	now := time.Now()

	err := tsdb.Write([]MetricSample{
		{Name: "test_gauge", Value: 100, Labels: map[string]string{"host": "b"}, Timestamp: now},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// For=1h so first eval only transitions to pending, never fires.
	rules := []AlertRule{
		{Name: "Pending", Expr: `test_gauge > 50`, For: time.Hour, Severity: "info", Message: "pending test"},
	}
	engine := NewAlertEngine(tsdb, notif, rules, AlertEngineConfig{})

	engine.evaluate(context.Background())

	// Should be pending (active=true) but not yet fired.
	states := engine.AllStates()
	if len(states) != 1 {
		t.Fatalf("expected 1 state, got %d", len(states))
	}
	if !states[0].Active {
		t.Error("expected state to be active (pending)")
	}
	if states[0].FiredAt != nil {
		t.Error("expected FiredAt to be nil (For not elapsed)")
	}
	if states[0].ActiveSince == nil {
		t.Error("expected ActiveSince to be set")
	}

	// No notification should be sent yet.
	if len(notif.received()) != 0 {
		t.Error("expected no notifications during pending phase")
	}

	// ActiveAlerts should not include pending-only rules.
	if len(engine.ActiveAlerts()) != 0 {
		t.Error("ActiveAlerts should not include pending (not yet fired) alerts")
	}
}

func TestEvaluate_Resolves_WhenConditionClears(t *testing.T) {
	tsdb := newAlertTestTSDB(t)
	notif := &mockNotifier{}

	// Use past timestamps so evaluate()'s time.Now() is always after both samples.
	base := time.Now().Add(-10 * time.Second)

	// Write a sample that triggers the rule.
	err := tsdb.Write([]MetricSample{
		{Name: "resolve_test", Value: 100, Labels: map[string]string{"host": "c"}, Timestamp: base},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	rules := []AlertRule{
		{Name: "ResolveTest", Expr: `resolve_test > 50`, For: 0, Severity: "warning", Message: "high"},
	}
	engine := NewAlertEngine(tsdb, notif, rules, AlertEngineConfig{})

	// First eval: fires.
	engine.evaluate(context.Background())
	if len(engine.ActiveAlerts()) != 1 {
		t.Fatal("expected 1 active alert after first eval")
	}

	// Write a more recent sample that clears the condition.
	err = tsdb.Write([]MetricSample{
		{Name: "resolve_test", Value: 10, Labels: map[string]string{"host": "c"}, Timestamp: base.Add(5 * time.Second)},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Second eval: condition no longer met → resolve.
	engine.evaluate(context.Background())

	if len(engine.ActiveAlerts()) != 0 {
		t.Error("expected 0 active alerts after condition cleared")
	}

	// Check that a "resolved" notification was sent.
	received := notif.received()
	if len(received) < 2 {
		t.Fatalf("expected at least 2 notifications (fire + resolve), got %d", len(received))
	}
	last := received[len(received)-1]
	if last.Severity != "resolved" {
		t.Errorf("expected resolved notification, got severity %q", last.Severity)
	}
	if last.ResolvedAt == nil {
		t.Error("expected ResolvedAt to be set on resolved notification")
	}
}

func TestEvaluate_NoNotifier(t *testing.T) {
	tsdb := newAlertTestTSDB(t)
	now := time.Now()

	err := tsdb.Write([]MetricSample{
		{Name: "no_notif", Value: 100, Labels: map[string]string{}, Timestamp: now},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	rules := []AlertRule{
		{Name: "NoNotif", Expr: `no_notif > 50`, For: 0, Severity: "warning", Message: "test"},
	}
	// nil notifier -- should not panic.
	engine := NewAlertEngine(tsdb, nil, rules, AlertEngineConfig{})
	engine.evaluate(context.Background())

	if len(engine.ActiveAlerts()) != 1 {
		t.Error("expected 1 active alert even without notifier")
	}
}

func TestRun_CancelsCleanly(t *testing.T) {
	tsdb := newAlertTestTSDB(t)
	engine := NewAlertEngine(tsdb, nil, nil, AlertEngineConfig{Interval: 10 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		engine.Run(ctx)
		close(done)
	}()

	// Let it tick a few times.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
