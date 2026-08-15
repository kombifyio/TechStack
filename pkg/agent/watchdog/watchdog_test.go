package watchdog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock recipe
// ---------------------------------------------------------------------------

type mockRecipe struct {
	name       string
	enabled    bool
	autoExec   bool
	checkFn    func(context.Context, *SystemState) (*Condition, error)
	dryRunFn   func(context.Context, *Condition) (*Plan, error)
	executeFn  func(context.Context, *Plan) (*Result, error)
	rollbackFn func(context.Context, *Result) (*Result, error)
}

func (m *mockRecipe) Name() string      { return m.name }
func (m *mockRecipe) AutoExec() bool    { return m.autoExec }
func (m *mockRecipe) Enabled() bool     { return m.enabled }
func (m *mockRecipe) SetEnabled(v bool) { m.enabled = v }

func (m *mockRecipe) Check(ctx context.Context, s *SystemState) (*Condition, error) {
	if m.checkFn != nil {
		return m.checkFn(ctx, s)
	}
	return &Condition{Triggered: false}, nil
}

func (m *mockRecipe) DryRun(ctx context.Context, c *Condition) (*Plan, error) {
	if m.dryRunFn != nil {
		return m.dryRunFn(ctx, c)
	}
	return &Plan{Steps: []string{"mock-step"}, DryRunOutput: "mock dry-run"}, nil
}

func (m *mockRecipe) Execute(ctx context.Context, p *Plan) (*Result, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, p)
	}
	return &Result{Success: true, Output: "mock execute"}, nil
}

func (m *mockRecipe) Rollback(ctx context.Context, r *Result) (*Result, error) {
	if m.rollbackFn != nil {
		return m.rollbackFn(ctx, r)
	}
	return &Result{Success: true, Output: "mock rollback"}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// eventCollector is a thread-safe HealEvent accumulator.
type eventCollector struct {
	mu     sync.Mutex
	events []HealEvent
}

func (c *eventCollector) reporter() HealReporter {
	return func(_ context.Context, ev HealEvent) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events = append(c.events, ev)
		return nil
	}
}

func (c *eventCollector) get() []HealEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]HealEvent, len(c.events))
	copy(out, c.events)
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWatchdogStartStop(t *testing.T) {
	cycled := make(chan struct{}, 10)
	r := &mockRecipe{
		name:     "start-stop-probe",
		enabled:  true,
		autoExec: true,
		checkFn: func(context.Context, *SystemState) (*Condition, error) {
			select {
			case cycled <- struct{}{}:
			default:
			}
			return &Condition{Triggered: false}, nil
		},
	}

	w := New([]Recipe{r}, nil, nil)
	w.SetInterval(5 * time.Millisecond) // fast for tests

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)

	// Wait for at least one cycle.
	select {
	case <-cycled:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not complete a cycle within 2s")
	}

	w.Stop()

	// Stopping again should be safe (idempotent).
	w.Stop()
}

func TestWatchdogTriggeredRecipe(t *testing.T) {
	col := &eventCollector{}

	r := &mockRecipe{
		name:     "trigger-test",
		enabled:  true,
		autoExec: true,
		checkFn: func(context.Context, *SystemState) (*Condition, error) {
			return &Condition{
				Triggered: true,
				Recipe:    "trigger-test",
				Reason:    "unit test trigger",
				Severity:  "warning",
				Evidence:  map[string]string{"key": "val"},
			}, nil
		},
	}

	w := New([]Recipe{r}, col.reporter(), nil)
	w.SetInterval(5 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	w.Start(ctx)

	// Wait for at least one event.
	deadline := time.After(2 * time.Second)
	for {
		events := col.get()
		if len(events) > 0 {
			ev := events[0]
			if ev.RecipeName != "trigger-test" {
				t.Errorf("expected recipe name 'trigger-test', got %q", ev.RecipeName)
			}
			if !ev.AutoExecuted {
				t.Error("expected AutoExecuted=true")
			}
			if !ev.Success {
				t.Error("expected Success=true")
			}
			if ev.TriggerReason != "unit test trigger" {
				t.Errorf("expected reason 'unit test trigger', got %q", ev.TriggerReason)
			}
			if ev.EventID == "" {
				t.Error("expected non-empty EventID")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("no HealEvent reported within 2s")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	w.Stop()
}

func TestWatchdogDisabledRecipe(t *testing.T) {
	checkCalled := false
	r := &mockRecipe{
		name:     "disabled-recipe",
		enabled:  false,
		autoExec: true,
		checkFn: func(context.Context, *SystemState) (*Condition, error) {
			checkCalled = true
			return &Condition{Triggered: true, Recipe: "disabled-recipe"}, nil
		},
	}

	col := &eventCollector{}
	w := New([]Recipe{r}, col.reporter(), nil)
	w.SetInterval(5 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	w.Start(ctx)
	<-ctx.Done()
	w.Stop()

	if checkCalled {
		t.Error("Check() should not have been called on a disabled recipe")
	}
	if len(col.get()) > 0 {
		t.Error("no events should have been reported for a disabled recipe")
	}
}

func TestWatchdogRollbackOnFailure(t *testing.T) {
	var rollbackCalled atomic.Bool

	r := &mockRecipe{
		name:     "fail-recipe",
		enabled:  true,
		autoExec: true,
		checkFn: func(context.Context, *SystemState) (*Condition, error) {
			return &Condition{
				Triggered: true,
				Recipe:    "fail-recipe",
				Reason:    "force failure",
				Severity:  "critical",
			}, nil
		},
		executeFn: func(context.Context, *Plan) (*Result, error) {
			return nil, errors.New("simulated execute failure")
		},
		rollbackFn: func(context.Context, *Result) (*Result, error) {
			rollbackCalled.Store(true)
			return &Result{Success: true, Output: "rolled back"}, nil
		},
	}

	col := &eventCollector{}
	w := New([]Recipe{r}, col.reporter(), nil)
	w.SetInterval(5 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	w.Start(ctx)

	deadline := time.After(2 * time.Second)
	for {
		events := col.get()
		if len(events) > 0 {
			ev := events[0]
			if ev.Success {
				t.Error("expected Success=false for failed execution")
			}
			if ev.RollbackAction == "" {
				t.Error("expected non-empty RollbackAction")
			}
			if !rollbackCalled.Load() {
				t.Error("expected Rollback() to have been called")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("no failure HealEvent reported within 2s")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	w.Stop()
}

// ---------------------------------------------------------------------------
// Registry tests
// ---------------------------------------------------------------------------

func TestDefaultRecipes(t *testing.T) {
	recipes := DefaultRecipes()
	if len(recipes) != 5 {
		t.Fatalf("expected 5 default recipes, got %d", len(recipes))
	}

	expected := map[string]bool{
		"service-restart":  false,
		"tunnel-reconnect": false,
		"cert-rotate":      false,
		"disk-cleanup":     false,
		"oom-recovery":     false,
	}
	for _, r := range recipes {
		if _, ok := expected[r.Name()]; !ok {
			t.Errorf("unexpected recipe %q", r.Name())
			continue
		}
		expected[r.Name()] = true
		if !r.Enabled() {
			t.Errorf("recipe %q should be enabled by default", r.Name())
		}
		if !r.AutoExec() {
			t.Errorf("recipe %q should be auto-exec by default", r.Name())
		}
	}
	for name, seen := range expected {
		if !seen {
			t.Errorf("missing expected recipe %q", name)
		}
	}
}

func TestRecipeByName(t *testing.T) {
	recipes := DefaultRecipes()

	r, ok := RecipeByName(recipes, "cert-rotate")
	if !ok {
		t.Fatal("expected to find cert-rotate")
	}
	if r.Name() != "cert-rotate" {
		t.Errorf("wrong recipe returned: %q", r.Name())
	}

	_, ok = RecipeByName(recipes, "nonexistent")
	if ok {
		t.Error("expected not found for nonexistent recipe")
	}
}

// ---------------------------------------------------------------------------
// Recipe-specific Check() logic tests
// ---------------------------------------------------------------------------

func TestServiceRestartCheck(t *testing.T) {
	r := NewServiceRestart()

	// No services → not triggered.
	cond, err := r.Check(context.Background(), &SystemState{})
	if err != nil {
		t.Fatal(err)
	}
	if cond.Triggered {
		t.Error("should not trigger on empty services")
	}

	// A service failed for enough cycles → triggered.
	state := &SystemState{
		Services: map[string]ServiceInfo{
			"nginx": {State: "failed", FailCount: 3, Since: time.Now().Add(-2 * time.Minute)},
			"api":   {State: "running"},
		},
	}
	cond, err = r.Check(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !cond.Triggered {
		t.Error("should trigger for persistently failed unit")
	}
	if cond.Severity != "critical" {
		t.Errorf("expected severity critical, got %q", cond.Severity)
	}
}

func TestTunnelReconnectCheck(t *testing.T) {
	r := NewTunnelReconnect()
	now := time.Now()

	// Recent heartbeat → not triggered.
	cond, _ := r.Check(context.Background(), &SystemState{
		LastHeartbeatAt: now.Add(-10 * time.Second),
		CollectedAt:     now,
	})
	if cond.Triggered {
		t.Error("should not trigger with recent heartbeat")
	}

	// Stale heartbeat → triggered.
	cond, _ = r.Check(context.Background(), &SystemState{
		LastHeartbeatAt: now.Add(-2 * time.Minute),
		CollectedAt:     now,
	})
	if !cond.Triggered {
		t.Error("should trigger with stale heartbeat")
	}
}

func TestCertRotateCheck(t *testing.T) {
	r := NewCertRotate()

	// Cert far out → not triggered.
	cond, _ := r.Check(context.Background(), &SystemState{
		CertExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	})
	if cond.Triggered {
		t.Error("should not trigger for cert expiring in 30 days")
	}

	// Cert expiring soon → triggered.
	cond, _ = r.Check(context.Background(), &SystemState{
		CertExpiresAt: time.Now().Add(3 * 24 * time.Hour),
	})
	if !cond.Triggered {
		t.Error("should trigger for cert expiring in 3 days")
	}

	// Cert expiring in <24h → critical.
	cond, _ = r.Check(context.Background(), &SystemState{
		CertExpiresAt: time.Now().Add(12 * time.Hour),
	})
	if cond.Severity != "critical" {
		t.Errorf("expected critical severity for <24h cert, got %q", cond.Severity)
	}
}

func TestDiskCleanupCheck(t *testing.T) {
	r := NewDiskCleanup()

	// Below threshold → not triggered.
	cond, _ := r.Check(context.Background(), &SystemState{DiskUsagePercent: 75.0})
	if cond.Triggered {
		t.Error("should not trigger at 75%")
	}

	// Above threshold → triggered.
	cond, _ = r.Check(context.Background(), &SystemState{DiskUsagePercent: 92.5})
	if !cond.Triggered {
		t.Error("should trigger at 92.5%")
	}

	// 95%+ → critical.
	cond, _ = r.Check(context.Background(), &SystemState{DiskUsagePercent: 96.0})
	if cond.Severity != "critical" {
		t.Errorf("expected critical at 96%%, got %q", cond.Severity)
	}
}

func TestOOMRecoveryCheck(t *testing.T) {
	r := NewOOMRecovery()
	now := time.Now()

	// No events → not triggered.
	cond, _ := r.Check(context.Background(), &SystemState{CollectedAt: now})
	if cond.Triggered {
		t.Error("should not trigger without OOM events")
	}

	// Stale event → not triggered.
	cond, _ = r.Check(context.Background(), &SystemState{
		CollectedAt: now,
		OOMEvents: []OOMEvent{
			{ProcessName: "old", PID: 1, Timestamp: now.Add(-10 * time.Minute)},
		},
	})
	if cond.Triggered {
		t.Error("should not trigger on stale OOM events")
	}

	// Recent event → triggered.
	cond, _ = r.Check(context.Background(), &SystemState{
		CollectedAt: now,
		OOMEvents: []OOMEvent{
			{ProcessName: "myapp", PID: 42, Timestamp: now.Add(-1 * time.Minute)},
		},
	})
	if !cond.Triggered {
		t.Error("should trigger on recent OOM event")
	}
}
