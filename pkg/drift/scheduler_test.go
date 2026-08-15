package drift

import (
	"testing"
	"time"
)

func TestNewSchedulerNormalizesDefaults(t *testing.T) {
	scheduler := NewScheduler(nil, nil, SchedulerConfig{Interval: time.Second})

	if scheduler.config.Interval != 6*time.Hour {
		t.Fatalf("Interval = %s, want 6h", scheduler.config.Interval)
	}
	if scheduler.config.Retention != 50 {
		t.Fatalf("Retention = %d, want 50", scheduler.config.Retention)
	}
	if scheduler.IsRunning() {
		t.Fatal("new scheduler should not be running")
	}
	if !scheduler.LastRun().IsZero() {
		t.Fatalf("LastRun = %s, want zero", scheduler.LastRun())
	}
	if scheduler.LastError() != nil {
		t.Fatalf("LastError = %v, want nil", scheduler.LastError())
	}
}

func TestSchedulerStartStopIdempotent(t *testing.T) {
	scheduler := NewScheduler(nil, nil, SchedulerConfig{
		Interval:  time.Hour,
		Retention: 1,
	})

	scheduler.Start()
	scheduler.Start()
	if !scheduler.IsRunning() {
		t.Fatal("scheduler should be running")
	}

	scheduler.Stop()
	scheduler.Stop()
	if scheduler.IsRunning() {
		t.Fatal("scheduler should be stopped")
	}
}
