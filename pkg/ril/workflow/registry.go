package workflow

import (
	"context"
	"fmt"
	"sync"
)

// ActivityFunc is a single side-effecting unit of work (dispatch a command,
// query monitoring, verify health, ...). Activities are the ONLY place a step
// touches the network or external stores; steps call them through the
// ActivityRunner so retries/idempotency are uniform.
type ActivityFunc func(ctx context.Context, input map[string]any) (map[string]any, error)

// ActivityRunner is the registry + dispatcher implementing the Activities
// interface consumed by steps. The idempotency key is injected into the
// context so activities that cause external side effects (e.g. an agent
// command) can dedup across retries/restarts.
type ActivityRunner struct {
	mu   sync.RWMutex
	acts map[string]ActivityFunc
}

// NewActivityRunner creates an empty activity registry.
func NewActivityRunner() *ActivityRunner {
	return &ActivityRunner{acts: make(map[string]ActivityFunc)}
}

// Register adds (or replaces) an activity by name. Not safe to call
// concurrently with Run for the same name; register all activities at wiring
// time before the engine starts.
func (r *ActivityRunner) Register(name string, fn ActivityFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acts[name] = fn
}

// Run dispatches to the named activity, injecting idempotencyKey into the
// context. Returns an error if the activity is not registered.
func (r *ActivityRunner) Run(ctx context.Context, name string, input map[string]any, idempotencyKey string) (map[string]any, error) {
	r.mu.RLock()
	fn, ok := r.acts[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workflow: unknown activity %q", name)
	}
	ctx = WithIdempotencyKey(ctx, idempotencyKey)
	return fn(ctx, input)
}

type idemKeyCtx struct{}

// WithIdempotencyKey returns a context carrying the idempotency key.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idemKeyCtx{}, key)
}

// IdempotencyKeyFromContext returns the idempotency key set by the runner, or
// "" if none. Side-effecting activities (e.g. DispatchAgentCommand) use it as a
// stable external dedup id.
func IdempotencyKeyFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(idemKeyCtx{}).(string); ok {
		return v
	}
	return ""
}
