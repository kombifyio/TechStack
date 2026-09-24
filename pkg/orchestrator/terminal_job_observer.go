package orchestrator

import (
	"context"

	"github.com/kombifyio/techstack/pkg/jobs"
)

// TerminalJobObserver receives a job only after its terminal snapshot has been
// accepted by the canonical control-plane store. Product integrations observe
// this seam instead of individual handlers, so retry and restart paths share
// one completion authority.
type TerminalJobObserver interface {
	ObserveTerminalJob(context.Context, jobs.JobSnapshot) error
}

// AddTerminalJobObserver adds an independent terminal projection. Observers
// must be idempotent because a failed sibling may cause the terminal snapshot
// to be observed again on the next persistence heartbeat.
func (o *Orchestrator) AddTerminalJobObserver(observer TerminalJobObserver) {
	if o == nil || observer == nil {
		return
	}
	o.mu.Lock()
	o.terminalJobObservers = append(o.terminalJobObservers, observer)
	o.mu.Unlock()
}

func (o *Orchestrator) observeTerminalJob(ctx context.Context, job jobs.JobSnapshot) error {
	if o == nil || !terminalJobSnapshot(job) {
		return nil
	}
	o.mu.RLock()
	observers := append([]TerminalJobObserver(nil), o.terminalJobObservers...)
	o.mu.RUnlock()
	for _, observer := range observers {
		if err := observer.ObserveTerminalJob(ctx, job); err != nil {
			return err
		}
	}
	return nil
}
