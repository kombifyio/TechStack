package workflow

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeStore is an in-memory RunStore used by engine tests. It reuses the
// package-level ValidateRunTransition/ValidateStepTransition so the fake
// exercises the same state-machine rules as the real stores.
type fakeStore struct {
	mu     sync.Mutex
	runs   map[string]*Run   // keyed by RunID
	steps  map[string]*Step  // keyed by "runID:stepIndex"
	timers map[string]*Timer // keyed by timer ID
}

func newFakeStore(_ *testing.T) *fakeStore {
	return &fakeStore{
		runs:   make(map[string]*Run),
		steps:  make(map[string]*Step),
		timers: make(map[string]*Timer),
	}
}

func (s *fakeStore) CreateRun(run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.RunID == "" {
		run.RunID = uuid.New().String()
	}
	run.Status = RunPending
	cp := *run
	s.runs[run.RunID] = &cp
	return nil
}

func (s *fakeStore) GetRun(runID string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	cp := *r
	return &cp, nil
}

func (s *fakeStore) UpdateRun(run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.runs[run.RunID]
	if !ok {
		return fmt.Errorf("%w: run %s", ErrNotFound, run.RunID)
	}
	if existing.Status != run.Status {
		if err := ValidateRunTransition(existing.Status, run.Status); err != nil {
			return err
		}
	}
	cp := *run
	s.runs[run.RunID] = &cp
	return nil
}

func (s *fakeStore) ListRunsByStatus(status RunStatus, limit int) ([]*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Run
	for _, r := range s.runs {
		if r.Status == status {
			cp := *r
			out = append(out, &cp)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) FindSuspendedBySignal(signalKey string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.Status == RunSuspended && r.AwaitingSignal == signalKey {
			cp := *r
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("%w: no suspended run awaiting signal %q", ErrNotFound, signalKey)
}

func fakeStepKey(runID string, idx int) string {
	return fmt.Sprintf("%s:%d", runID, idx)
}

func (s *fakeStore) CreateStep(step *Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if step.ID == "" {
		step.ID = uuid.New().String()
	}
	cp := *step
	s.steps[fakeStepKey(step.RunID, step.StepIndex)] = &cp
	return nil
}

func (s *fakeStore) UpdateStep(step *Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fakeStepKey(step.RunID, step.StepIndex)
	existing, ok := s.steps[k]
	if !ok {
		return fmt.Errorf("%w: step %s", ErrNotFound, k)
	}
	if existing.Status != step.Status {
		if err := ValidateStepTransition(existing.Status, step.Status); err != nil {
			return err
		}
	}
	cp := *step
	s.steps[k] = &cp
	return nil
}

func (s *fakeStore) GetStep(runID string, idx int) (*Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fakeStepKey(runID, idx)
	st, ok := s.steps[k]
	if !ok {
		return nil, fmt.Errorf("%w: step %s", ErrNotFound, k)
	}
	cp := *st
	return &cp, nil
}

func (s *fakeStore) ListSteps(runID string) ([]*Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Step
	for _, st := range s.steps {
		if st.RunID == runID {
			cp := *st
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StepIndex < out[j].StepIndex
	})
	return out, nil
}

func (s *fakeStore) CreateTimer(timer *Timer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if timer.ID == "" {
		timer.ID = uuid.New().String()
	}
	cp := *timer
	s.timers[timer.ID] = &cp
	return nil
}

func (s *fakeStore) ListDueTimers(now time.Time, limit int) ([]*Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Timer
	for _, tm := range s.timers {
		if !tm.Fired && !tm.FireAt.After(now) {
			cp := *tm
			out = append(out, &cp)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) MarkTimerFired(timerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tm, ok := s.timers[timerID]
	if !ok {
		return fmt.Errorf("%w: timer %s", ErrNotFound, timerID)
	}
	tm.Fired = true
	return nil
}
