// Package middleware provides HTTP middleware components for kombifyTechstack.
package middleware

import (
	"encoding/json"
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
	mu       sync.RWMutex
	rate     rate.Limit // requests per second
	burst    int        // max burst size
	cleanup  time.Duration
	stopCh   chan struct{}
}

// visitorEntry tracks a visitor's rate limiter and last seen time.
type visitorEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitConfig holds configuration for the rate limiter.
type RateLimitConfig struct {
	// RequestsPerSecond is the rate limit (requests per second).
	// Default: 10
	RequestsPerSecond float64

	// Burst is the maximum number of requests allowed in a burst.
	// Default: 20
	Burst int

	// CleanupInterval is how often to clean up old visitor entries.
	// Default: 5 minutes
	CleanupInterval time.Duration
}

// PathRateLimitConfig holds configuration for path-based rate limiting (H1 enhancement).
type PathRateLimitConfig struct {
	// PathPrefixes maps path prefixes to their rate limit configs.
	// More specific paths should come first.
	PathPrefixes map[string]RateLimitConfig

	// Default is the fallback rate limit for paths not matching any prefix.
	Default RateLimitConfig

	// CleanupInterval for all path-based limiters.
	CleanupInterval time.Duration
}

// DefaultPathRateLimitConfig returns security-hardened defaults for path-based rate limiting.
// Sensitive endpoints like auth and registration have stricter limits.
func DefaultPathRateLimitConfig() PathRateLimitConfig {
	return PathRateLimitConfig{
		PathPrefixes: map[string]RateLimitConfig{
			// Authentication endpoints: strict limits to prevent brute-force
			"/api/collections/users/auth": {RequestsPerSecond: 1, Burst: 5},
			"/api/v1/auth":                {RequestsPerSecond: 2, Burst: 5},
			// Worker/agent registration: prevent registration spam
			"/api/v1/workers/register": {RequestsPerSecond: 0.5, Burst: 3},
			"/api/v1/agents":           {RequestsPerSecond: 5, Burst: 10},
			// Discovery scans: resource-intensive operations
			"/api/v1/discovery/scan": {RequestsPerSecond: 0.2, Burst: 2},
			// Provisioning jobs: expensive operations
			"/api/v1/stacks": {RequestsPerSecond: 2, Burst: 5},
			"/api/v1/jobs":   {RequestsPerSecond: 5, Burst: 10},
		},
		Default:         DefaultRateLimitConfig(),
		CleanupInterval: 5 * time.Minute,
	}
}

// DefaultRateLimitConfig returns sensible defaults for rate limiting.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerSecond: 10,
		Burst:             20,
		CleanupInterval:   5 * time.Minute,
	}
}

// NewRateLimiter creates a new RateLimiter with the specified rate and burst.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	return NewRateLimiterWithConfig(RateLimitConfig{
		RequestsPerSecond: rps,
		Burst:             burst,
		CleanupInterval:   5 * time.Minute,
	})
}

// NewRateLimiterWithConfig creates a new RateLimiter with full configuration.
func NewRateLimiterWithConfig(cfg RateLimitConfig) *RateLimiter {
	// Apply defaults for zero values
	if cfg.RequestsPerSecond <= 0 {
		cfg.RequestsPerSecond = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	rl := &RateLimiter{
		visitors: make(map[string]*visitorEntry),
		rate:     rate.Limit(cfg.RequestsPerSecond),
		burst:    cfg.Burst,
		cleanup:  cfg.CleanupInterval,
		stopCh:   make(chan struct{}),
	}

	// Start background cleanup goroutine
	go rl.cleanupLoop()

	return rl
}

// getLimiter returns the rate limiter for the given IP, creating one if needed.
func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rate, rl.burst)
		rl.visitors[ip] = &visitorEntry{
			limiter:  limiter,
			lastSeen: time.Now(),
		}
		return limiter
	}

	// Update last seen time
	entry.lastSeen = time.Now()
	return entry.limiter
}

// cleanupLoop removes old visitor entries periodically.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanupOldVisitors()
		case <-rl.stopCh:
			return
		}
	}
}

// cleanupOldVisitors removes visitors that haven't been seen recently.
func (rl *RateLimiter) cleanupOldVisitors() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Remove entries older than 3x the cleanup interval
	threshold := time.Now().Add(-3 * rl.cleanup)
	for ip, entry := range rl.visitors {
		if entry.lastSeen.Before(threshold) {
			delete(rl.visitors, ip)
		}
	}
}

// Stop stops the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// VisitorCount returns the current number of tracked visitors.
// Useful for monitoring and testing.
func (rl *RateLimiter) VisitorCount() int {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return len(rl.visitors)
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
	if isEdgeAuthenticatedContext(ctx) {
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

// RateLimitError represents a rate limit exceeded error response.
type RateLimitError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// Middleware returns an HTTP middleware that applies rate limiting.
// It extracts the client IP from X-Forwarded-For, X-Real-IP, or RemoteAddr.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := RequestRateLimitKey(r)

		limiter := rl.getLimiter(key)
		if !limiter.Allow() {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1") // Suggest retry after 1 second
			w.WriteHeader(http.StatusTooManyRequests)

			errResp := RateLimitError{
				Error:   "rate limit exceeded",
				Message: "Too many requests. Please slow down and try again.",
			}
			_ = json.NewEncoder(w).Encode(errResp)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MiddlewareFunc returns a middleware function compatible with common router patterns.
func (rl *RateLimiter) MiddlewareFunc(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := RequestRateLimitKey(r)

		limiter := rl.getLimiter(key)
		if !limiter.Allow() {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)

			errResp := RateLimitError{
				Error:   "rate limit exceeded",
				Message: "Too many requests. Please slow down and try again.",
			}
			_ = json.NewEncoder(w).Encode(errResp)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// extractClientIP extracts the real client IP from the request.
// It checks X-Forwarded-For and X-Real-IP headers before falling back to RemoteAddr.
func extractClientIP(r *http.Request) string {
	return ExtractClientIP(r)
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

// PathRateLimiter implements path-based rate limiting (H1 Enhancement).
// Different API endpoints can have different rate limits based on their sensitivity.
type PathRateLimiter struct {
	limiters   map[string]*RateLimiter // path prefix -> limiter
	prefixes   []string                // sorted prefixes (longest first for matching)
	defaultLim *RateLimiter
	mu         sync.RWMutex
}

// NewPathRateLimiter creates a path-based rate limiter with the given config.
func NewPathRateLimiter(cfg PathRateLimitConfig) *PathRateLimiter {
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	prl := &PathRateLimiter{
		limiters:   make(map[string]*RateLimiter),
		prefixes:   make([]string, 0, len(cfg.PathPrefixes)),
		defaultLim: NewRateLimiterWithConfig(cfg.Default),
	}

	// Create limiters for each path prefix
	for prefix, limCfg := range cfg.PathPrefixes {
		limCfg.CleanupInterval = cfg.CleanupInterval
		prl.limiters[prefix] = NewRateLimiterWithConfig(limCfg)
		prl.prefixes = append(prl.prefixes, prefix)
	}

	// Sort prefixes by length (longest first) for proper matching
	for i := 0; i < len(prl.prefixes); i++ {
		for j := i + 1; j < len(prl.prefixes); j++ {
			if len(prl.prefixes[j]) > len(prl.prefixes[i]) {
				prl.prefixes[i], prl.prefixes[j] = prl.prefixes[j], prl.prefixes[i]
			}
		}
	}

	return prl
}

// getLimiterForPath returns the appropriate rate limiter for the given path.
func (prl *PathRateLimiter) getLimiterForPath(path string) *RateLimiter {
	prl.mu.RLock()
	defer prl.mu.RUnlock()

	for _, prefix := range prl.prefixes {
		if strings.HasPrefix(path, prefix) {
			return prl.limiters[prefix]
		}
	}
	return prl.defaultLim
}

// Middleware returns an HTTP middleware that applies path-based rate limiting.
func (prl *PathRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := RequestRateLimitKey(r)
		limiter := prl.getLimiterForPath(r.URL.Path)

		if !limiter.Allow(key) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)

			errResp := RateLimitError{
				Error:   "rate limit exceeded",
				Message: "Too many requests. Please slow down and try again.",
			}
			_ = json.NewEncoder(w).Encode(errResp)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Stop stops all path-based rate limiters.
func (prl *PathRateLimiter) Stop() {
	prl.mu.Lock()
	defer prl.mu.Unlock()

	for _, limiter := range prl.limiters {
		limiter.Stop()
	}
	prl.defaultLim.Stop()
}

// Stats returns rate limiter statistics for monitoring.
func (prl *PathRateLimiter) Stats() map[string]int {
	prl.mu.RLock()
	defer prl.mu.RUnlock()

	stats := make(map[string]int, len(prl.limiters)+1)
	for prefix, limiter := range prl.limiters {
		stats[prefix] = limiter.VisitorCount()
	}
	stats["default"] = prl.defaultLim.VisitorCount()
	return stats
}
