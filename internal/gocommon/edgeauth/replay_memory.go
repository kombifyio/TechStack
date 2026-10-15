package edgeauth

import (
	"context"
	"strings"
	"sync"
	"time"
)

// DefaultReplayCapacity bounds a MemoryReplayGuard created with capacity 0.
const DefaultReplayCapacity = 100_000

// MemoryReplayGuard is a process-local ReplayGuard. It protects one replica
// only; an origin running several replicas needs a shared store.
type MemoryReplayGuard struct {
	mu       sync.Mutex
	ttl      time.Duration
	capacity int
	seen     map[string]time.Time
	now      func() time.Time
	nextScan time.Time
}

// NewMemoryReplayGuard returns a ReplayGuard that remembers each (kid, nonce)
// pair for ttl. Pass 2 × Config.SignatureWindow (or more) as ttl; a ttl of 0
// uses 2 × the default 5-minute window. When capacity unexpired pairs are
// held, Seen reports true (fail closed) until entries expire.
func NewMemoryReplayGuard(ttl time.Duration, capacity int) *MemoryReplayGuard {
	if ttl <= 0 {
		ttl = 2 * defaultSignatureWindow
	}
	if capacity <= 0 {
		capacity = DefaultReplayCapacity
	}
	return &MemoryReplayGuard{ttl: ttl, capacity: capacity, seen: make(map[string]time.Time), now: time.Now}
}

// Seen implements ReplayGuard.
func (g *MemoryReplayGuard) Seen(_ context.Context, kid, nonce string) bool {
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		return true
	}
	key := strings.TrimSpace(kid) + "\x00" + nonce
	now := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()
	if expiry, ok := g.seen[key]; ok && expiry.After(now) {
		return true
	}
	if len(g.seen) >= g.capacity || !now.Before(g.nextScan) {
		for k, expiry := range g.seen {
			if !expiry.After(now) {
				delete(g.seen, k)
			}
		}
		g.nextScan = now.Add(g.ttl / 4)
		if len(g.seen) >= g.capacity {
			return true
		}
	}
	g.seen[key] = now.Add(g.ttl)
	return false
}
