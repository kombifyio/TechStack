// Package middleware provides HTTP middleware components for kombifyTechstack.
package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/identity"
	"golang.org/x/time/rate"
)

// RateLimiter implements per-IP rate limiting using token bucket algorithm.
type RateLimiter struct {
	visitors map[string]*visitorEntry
	mu       sync.Mutex
	rate     rate.Limit // requests per second
	burst    int        // max burst size
	lastGC   time.Time
}

const rateLimitCleanupInterval = 5 * time.Minute

// visitorEntry tracks a visitor's rate limiter and last seen time.
type visitorEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ReplicaBudget divides a configured aggregate rate budget across the number
// of control-plane replicas sharing it. The limiter is per-process, so without
// this division N replicas would enforce N times the configured budget. The
// result stays usable: at least 0.1 rps and a burst of 1 per replica.
func ReplicaBudget(rps float64, burst, replicas int) (float64, int) {
	if replicas > 1 {
		rps = rps / float64(replicas)
		burst = burst / replicas
	}
	if rps < 0.1 {
		rps = 0.1
	}
	if burst < 1 {
		burst = 1
	}
	return rps, burst
}

// NewRateLimiter creates a new RateLimiter with the specified rate and burst.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	if rps <= 0 {
		rps = 10
	}
	if burst <= 0 {
		burst = 20
	}

	rl := &RateLimiter{
		visitors: make(map[string]*visitorEntry),
		rate:     rate.Limit(rps),
		burst:    burst,
		lastGC:   time.Now(),
	}
	return rl
}

// getLimiter returns the rate limiter for the given IP, creating one if needed.
func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastGC) >= rateLimitCleanupInterval {
		threshold := now.Add(-3 * rateLimitCleanupInterval)
		for key, entry := range rl.visitors {
			if entry.lastSeen.Before(threshold) {
				delete(rl.visitors, key)
			}
		}
		rl.lastGC = now
	}

	entry, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rate, rl.burst)
		rl.visitors[ip] = &visitorEntry{
			limiter:  limiter,
			lastSeen: now,
		}
		return limiter
	}

	entry.lastSeen = now
	return entry.limiter
}

// Allow checks if a request from the given IP should be allowed.
// This is a convenience method for use with custom middleware patterns.
func (rl *RateLimiter) Allow(ip string) bool {
	limiter := rl.getLimiter(ip)
	return limiter.Allow()
}

// RequestRateLimitKey returns the stable bucket key for a request.
//
// Signed Kombify edge traffic is keyed by verified identity or edge service,
// not by the shared gateway/proxy IP. Anonymous/direct traffic stays IP-based.
func RequestRateLimitKey(r *http.Request) string {
	if r == nil {
		return "ip:"
	}

	ctx := r.Context()
	if IsEdgeAuthenticated(ctx) {
		if key := identityRateLimitKey("edge:user", identity.FromContext(ctx)); key != "" {
			return key
		}
		if service := strings.TrimSpace(r.Header.Get(headerEdgeService)); service != "" {
			return "edge:service:" + service
		}
	}

	if key := identityRateLimitKey("user", identity.FromContext(ctx)); key != "" {
		return key
	}

	return "ip:" + strings.TrimSpace(ExtractClientIP(r))
}

func identityRateLimitKey(prefix string, id *identity.Identity) string {
	if id == nil || !id.IsAuthenticated() {
		return ""
	}
	userID := strings.TrimSpace(id.UserID)
	if userID == "" {
		return ""
	}
	orgID := strings.TrimSpace(id.OrgID)
	if orgID == "" {
		orgID = "default"
	}
	return prefix + ":" + orgID + ":" + userID
}

// ExtractClientIP extracts the real client IP from the request.
// It checks X-Forwarded-For and X-Real-IP headers before falling back to RemoteAddr.
// This is exported for use with custom middleware patterns (e.g., PocketBase).
func ExtractClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (may contain multiple IPs)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (original client)
		if idx := len(xff); idx > 0 {
			for i := 0; i < len(xff); i++ {
				if xff[i] == ',' {
					return xff[:i]
				}
			}
			return xff
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr, stripping port
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr might not have a port
		return r.RemoteAddr
	}
	return ip
}
