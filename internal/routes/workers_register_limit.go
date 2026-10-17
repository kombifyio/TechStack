package routes

import (
	"net/http"
	"sync"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Guard threat-model finding F3: /api/v1/workers/register is reachable with a
// stolen or guessed enrollment capability, so one peer must not be able to
// flood it. The window is generous for a healthy Guard (one registration plus
// a few retries) and bounded in memory.
const (
	workerRegisterMaxPerWindow = 20
	workerRegisterWindow       = time.Minute
	workerRegisterMaxTracked   = 4096
)

type workerRegisterLimiter struct {
	mu      sync.Mutex
	clients map[string]workerRegisterWindowState
	now     func() time.Time
}

type workerRegisterWindowState struct {
	count   int
	resetAt time.Time
}

func newWorkerRegisterLimiter() *workerRegisterLimiter {
	return &workerRegisterLimiter{clients: make(map[string]workerRegisterWindowState), now: time.Now}
}

func (l *workerRegisterLimiter) allow(client string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.clients) >= workerRegisterMaxTracked {
		for key, state := range l.clients {
			if !now.Before(state.resetAt) {
				delete(l.clients, key)
			}
		}
		if len(l.clients) >= workerRegisterMaxTracked {
			// Fail closed for new clients while the table is saturated.
			if _, known := l.clients[client]; !known {
				return false
			}
		}
	}
	state := l.clients[client]
	if state.resetAt.IsZero() || !now.Before(state.resetAt) {
		state = workerRegisterWindowState{resetAt: now.Add(workerRegisterWindow)}
	}
	if state.count >= workerRegisterMaxPerWindow {
		l.clients[client] = state
		return false
	}
	state.count++
	l.clients[client] = state
	return true
}

func (l *workerRegisterLimiter) middleware() httpx.MiddlewareFunc {
	return func(e *httpx.Event) error {
		if !l.allow(getClientIP(e)) {
			e.Response.Header().Set("Retry-After", "60")
			return httpx.Error(e, http.StatusTooManyRequests, ksapi.ErrCodeBadRequest, "too many registration attempts", nil)
		}
		return e.Next()
	}
}
