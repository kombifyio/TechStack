// Package readcoalesce shares one computation of an expensive read among every
// request that asks for the same key at the same time, and keeps its answer
// briefly.
//
// The dashboard polls per-stack reads (operations, identity) every 5 to 15 s.
// On 2026-10-20 and 2026-10-21 each poll recomputed them on a one-CPU
// instance; a read that outlived the browser's 10 s timeout was abandoned and
// polled again, so the same work ran over and over while Guard heartbeats and
// command polls waited for the database pool. A Group runs at most one
// computation per key, detached from the requests that wait for it, so an
// abandoned read still produces the answer the next poll uses.
package readcoalesce

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrPending is returned when a computation is still running after Options.Wait.
var ErrPending = errors.New("readcoalesce: computation still running")

// Options configure a Group.
type Options struct {
	// FreshFor is how long a successful answer is reused.
	FreshFor time.Duration
	// FailedFor is how long a failed answer is reused; zero recomputes at once.
	FailedFor time.Duration
	// ServeStaleFor lets a read past FreshFor take the last successful answer
	// at once, at most this much older than FreshFor, while one refresh runs.
	ServeStaleFor time.Duration
	// Wait bounds how long a read waits for a running computation; zero waits
	// until the read's own context ends.
	Wait time.Duration
	// Budget bounds one computation, including its wait for a slot.
	Budget time.Duration
	// Slots, when set, bounds how many computations of this and every Group
	// sharing it run at once.
	Slots chan struct{}
	// Retention drops answers nobody asked for in this long.
	Retention time.Duration
}

// Group coalesces reads of values of type T by key.
type Group[T any] struct {
	opts    Options
	mu      sync.Mutex
	entries map[string]*entry[T]
}

type entry[T any] struct {
	done    chan struct{}
	value   T
	err     error
	settled time.Time
	// previous is the last successful answer when this one started, kept for
	// stale reads while it runs; guarded by Group.mu.
	previous *answer[T]
	// stale marks an answer that may predate a change; guarded by Group.mu.
	stale bool
}

type answer[T any] struct {
	value   T
	settled time.Time
}

// New returns a Group with the given options.
func New[T any](opts Options) *Group[T] {
	if opts.Retention <= 0 {
		opts.Retention = 5 * time.Minute
	}
	return &Group[T]{opts: opts, entries: make(map[string]*entry[T])}
}

// Do returns a fresh answer for key, joins the computation already running, or
// starts compute. compute runs detached from ctx (its values are kept), so a
// reader that gives up neither cancels it for the others nor makes the next
// read start over.
func (g *Group[T]) Do(ctx context.Context, key string, compute func(context.Context) (T, error)) (T, error) {
	var zero T
	if g == nil {
		return compute(ctx)
	}
	now := time.Now()
	g.mu.Lock()
	current := g.entries[key]
	if current == nil || current.stale || current.expired(now, g.opts) {
		var after <-chan struct{}
		next := &entry[T]{done: make(chan struct{})}
		if current != nil {
			if current.stale && !current.isSettled() {
				// A computation that may predate a change keeps running; its
				// replacement starts once it ends, so one key never has two.
				after = current.done
			} else if !current.stale {
				next.previous = current.lastSuccess()
			}
		}
		g.entries[key] = next
		g.pruneLocked(now)
		go next.run(context.WithoutCancel(ctx), after, g.opts, compute)
		current = next
	}
	stale := current.staleAnswer(now, g.opts)
	g.mu.Unlock()
	if stale != nil {
		return stale.value, nil
	}

	var timeout <-chan time.Time
	if g.opts.Wait > 0 {
		timer := time.NewTimer(g.opts.Wait)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case <-current.done:
		return current.value, current.err
	case <-timeout:
		return zero, ErrPending
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// Forget drops the answer for key after a change, so the next read computes
// again. A computation still running may have read the state before the
// change: the readers already waiting get it, no later read does.
func (g *Group[T]) Forget(key string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if current := g.entries[key]; current != nil {
		if current.isSettled() {
			delete(g.entries, key)
		} else {
			current.stale = true
			current.previous = nil
		}
	}
	g.mu.Unlock()
}

func (g *Group[T]) pruneLocked(now time.Time) {
	for key, current := range g.entries {
		if current.isSettled() && now.Sub(current.settled) > g.opts.Retention {
			delete(g.entries, key)
		}
	}
}

func (e *entry[T]) run(ctx context.Context, after <-chan struct{}, opts Options, compute func(context.Context) (T, error)) {
	defer close(e.done)
	if after != nil {
		<-after
	}
	if opts.Budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Budget)
		defer cancel()
	}
	if opts.Slots != nil {
		select {
		case opts.Slots <- struct{}{}:
			defer func() { <-opts.Slots }()
		case <-ctx.Done():
			e.err, e.settled = ctx.Err(), time.Now()
			return
		}
	}
	e.value, e.err = compute(ctx)
	e.settled = time.Now()
}

func (e *entry[T]) isSettled() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// expired is true for a settled answer past its reuse window; a running
// computation never expires, it is joined.
func (e *entry[T]) expired(now time.Time, opts Options) bool {
	if !e.isSettled() {
		return false
	}
	keep := opts.FreshFor
	if e.err != nil {
		keep = opts.FailedFor
	}
	return now.Sub(e.settled) > keep
}

// lastSuccess is this answer when it succeeded, otherwise the success it was
// computed after.
func (e *entry[T]) lastSuccess() *answer[T] {
	if e.isSettled() && e.err == nil {
		return &answer[T]{value: e.value, settled: e.settled}
	}
	return e.previous
}

// staleAnswer is the previous success a read may take while e runs.
func (e *entry[T]) staleAnswer(now time.Time, opts Options) *answer[T] {
	if opts.ServeStaleFor <= 0 || e.stale || e.isSettled() || e.previous == nil {
		return nil
	}
	if now.Sub(e.previous.settled) > opts.FreshFor+opts.ServeStaleFor {
		return nil
	}
	return e.previous
}
