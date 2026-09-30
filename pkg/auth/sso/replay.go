package sso

import (
	"sync"
	"time"
)

// ReplayGuard makes launch tokens single-use by remembering each token ID
// until the token expires. It is process-local: the SaaS control plane runs
// one instance, and a token outlives no restart by more than its lifetime.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

// NewReplayGuard returns an empty guard.
func NewReplayGuard() *ReplayGuard {
	return &ReplayGuard{seen: map[string]time.Time{}, now: time.Now}
}

// Reserve records id until expiresAt. It reports false when the id is
// already recorded and not yet expired.
func (g *ReplayGuard) Reserve(id string, expiresAt time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for seenID, until := range g.seen {
		if !until.After(now) {
			delete(g.seen, seenID)
		}
	}
	if _, used := g.seen[id]; used {
		return false
	}
	g.seen[id] = expiresAt
	return true
}

// Release forgets a reservation whose exchange failed after verification,
// so the caller can retry with the same token.
func (g *ReplayGuard) Release(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.seen, id)
}
