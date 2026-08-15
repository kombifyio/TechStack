package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/identity"
)

// TestRateLimiterBasic tests that rate limiting kicks in after burst.
func TestRateLimiterBasic(t *testing.T) {
	// Create limiter with 1 request/second and burst of 3
	rl := NewRateLimiter(1, 3)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))

	// First 3 requests should succeed (burst)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Request %d: expected status %d, got %d", i+1, http.StatusOK, rr.Code)
		}
	}

	// 4th request should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Request 4: expected status %d, got %d", http.StatusTooManyRequests, rr.Code)
	}

	// Check response body is JSON
	var errResp RateLimitError
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Errorf("Failed to decode error response: %v", err)
	}
	if errResp.Error != "rate limit exceeded" {
		t.Errorf("Expected error 'rate limit exceeded', got '%s'", errResp.Error)
	}

	// Check Retry-After header
	if rr.Header().Get("Retry-After") != "1" {
		t.Errorf("Expected Retry-After header '1', got '%s'", rr.Header().Get("Retry-After"))
	}
}

// TestRateLimiterDifferentIPs tests that different IPs get separate limits.
func TestRateLimiterDifferentIPs(t *testing.T) {
	// Create limiter with 1 request/second and burst of 2
	rl := NewRateLimiter(1, 2)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ips := []string{
		"192.168.1.1:12345",
		"192.168.1.2:12345",
		"10.0.0.1:12345",
	}

	// Each IP should be able to make 2 requests (burst)
	for _, ip := range ips {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = ip
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("IP %s request %d: expected status %d, got %d", ip, i+1, http.StatusOK, rr.Code)
			}
		}

		// 3rd request from each IP should be rate limited
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = ip
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusTooManyRequests {
			t.Errorf("IP %s request 3: expected status %d, got %d", ip, http.StatusTooManyRequests, rr.Code)
		}
	}

	// Verify we have 3 separate visitor entries
	if rl.VisitorCount() != 3 {
		t.Errorf("Expected 3 visitors, got %d", rl.VisitorCount())
	}
}

// TestRateLimiterXForwardedFor tests IP extraction from X-Forwarded-For header.
func TestRateLimiterXForwardedFor(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Make 2 requests with X-Forwarded-For from same original IP
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:12345" // Proxy IP
		req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Request %d: expected status %d, got %d", i+1, http.StatusOK, rr.Code)
		}
	}

	// 3rd request should be rate limited (same original IP)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:12345"                 // Different proxy IP
	req.Header.Set("X-Forwarded-For", "203.0.113.50") // Same original IP
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Request 3: expected status %d, got %d (X-Forwarded-For not working)", http.StatusTooManyRequests, rr.Code)
	}
}

// TestRateLimiterXRealIP tests IP extraction from X-Real-IP header.
func TestRateLimiterXRealIP(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Make 2 requests with X-Real-IP
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		req.Header.Set("X-Real-IP", "198.51.100.25")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Request %d: expected status %d, got %d", i+1, http.StatusOK, rr.Code)
		}
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.2:12345"
	req.Header.Set("X-Real-IP", "198.51.100.25")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Request 3: expected status %d, got %d (X-Real-IP not working)", http.StatusTooManyRequests, rr.Code)
	}
}

func TestRequestRateLimitKeyPrefersSignedEdgeIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	ctx := markEdgeAuthenticatedContext(req.Context())
	ctx = identity.NewContext(ctx, &identity.Identity{
		UserID: "auth0|user-1",
		OrgID:  "tenant-1",
	})
	req = req.WithContext(ctx)

	if got, want := RequestRateLimitKey(req), "edge:user:tenant-1:auth0|user-1"; got != want {
		t.Fatalf("RequestRateLimitKey() = %q, want %q", got, want)
	}
}

func TestRequestRateLimitKeyUsesSignedEdgeServiceWithoutUser(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set(headerEdgeService, "kombify-cloud")
	req = req.WithContext(markEdgeAuthenticatedContext(req.Context()))

	if got, want := RequestRateLimitKey(req), "edge:service:kombify-cloud"; got != want {
		t.Fatalf("RequestRateLimitKey() = %q, want %q", got, want)
	}
}

func TestRequestRateLimitKeyIgnoresUnsignedEdgeService(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	req.Header.Set(headerEdgeService, "kombify-cloud")

	if got, want := RequestRateLimitKey(req), "ip:10.0.0.1"; got != want {
		t.Fatalf("RequestRateLimitKey() = %q, want %q", got, want)
	}
}

func TestRateLimiterSeparatesSignedEdgeUsersBehindGateway(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, userID := range []string{"auth0|user-1", "auth0|user-2"} {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = "10.0.0.1:12345"
			ctx := markEdgeAuthenticatedContext(req.Context())
			ctx = identity.NewContext(ctx, &identity.Identity{
				UserID: userID,
				OrgID:  "tenant-1",
			})
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("user %s request %d: expected status %d, got %d", userID, i+1, http.StatusOK, rr.Code)
			}
		}
	}

	if rl.VisitorCount() != 2 {
		t.Fatalf("VisitorCount() = %d, want one bucket per signed edge user", rl.VisitorCount())
	}
}

// TestRateLimiterRecovery tests that tokens are replenished over time.
func TestRateLimiterRecovery(t *testing.T) {
	// Create limiter with 10 requests/second and burst of 2
	rl := NewRateLimiter(10, 2)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ip := "192.168.1.100:12345"

	// Exhaust burst
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = ip
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// Should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = ip
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Expected rate limit, got status %d", rr.Code)
	}

	// Wait for token replenishment (at 10/s, should recover in ~100ms)
	time.Sleep(150 * time.Millisecond)

	// Should succeed again
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = ip
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("Expected success after recovery, got status %d", rr.Code)
	}
}

// TestRateLimiterConcurrent tests thread safety under concurrent access.
func TestRateLimiterConcurrent(t *testing.T) {
	rl := NewRateLimiter(100, 50)
	defer rl.Stop()

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	numGoroutines := 10
	requestsPerGoroutine := 20

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < requestsPerGoroutine; i++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "192.168.1.1:12345"
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				// We don't check status here - just ensuring no panics/races
			}
		}(g)
	}

	wg.Wait()
	// If we get here without panics, the test passes
}

// TestRateLimiterConfig tests configuration with defaults.
func TestRateLimiterConfig(t *testing.T) {
	// Test default config
	cfg := DefaultRateLimitConfig()
	if cfg.RequestsPerSecond != 10 {
		t.Errorf("Expected default RPS 10, got %f", cfg.RequestsPerSecond)
	}
	if cfg.Burst != 20 {
		t.Errorf("Expected default burst 20, got %d", cfg.Burst)
	}

	// Test with custom config
	rl := NewRateLimiterWithConfig(RateLimitConfig{
		RequestsPerSecond: 5,
		Burst:             10,
		CleanupInterval:   1 * time.Minute,
	})
	defer rl.Stop()

	// Verify limiter respects config
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Should allow 10 requests (burst)
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("Request %d: expected status %d, got %d", i+1, http.StatusOK, rr.Code)
		}
	}

	// 11th should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Request 11: expected status %d, got %d", http.StatusTooManyRequests, rr.Code)
	}
}

// TestExtractClientIP tests the IP extraction function.
func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		expectedIP string
	}{
		{
			name:       "RemoteAddr with port",
			remoteAddr: "192.168.1.1:12345",
			expectedIP: "192.168.1.1",
		},
		{
			name:       "RemoteAddr without port",
			remoteAddr: "192.168.1.1",
			expectedIP: "192.168.1.1",
		},
		{
			name:       "X-Forwarded-For single IP",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50"},
			expectedIP: "203.0.113.50",
		},
		{
			name:       "X-Forwarded-For multiple IPs",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50, 70.41.3.18, 150.172.238.178"},
			expectedIP: "203.0.113.50",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Real-IP": "198.51.100.25"},
			expectedIP: "198.51.100.25",
		},
		{
			name:       "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.50",
				"X-Real-IP":       "198.51.100.25",
			},
			expectedIP: "203.0.113.50",
		},
		{
			name:       "IPv6 address",
			remoteAddr: "[::1]:12345",
			expectedIP: "::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			ip := extractClientIP(req)
			if ip != tt.expectedIP {
				t.Errorf("extractClientIP() = %v, want %v", ip, tt.expectedIP)
			}
		})
	}
}

// TestMiddlewareFunc tests the HandlerFunc variant of the middleware.
func TestMiddlewareFunc(t *testing.T) {
	rl := NewRateLimiter(1, 2)
	defer rl.Stop()

	handler := rl.MiddlewareFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// First 2 requests should succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Request %d: expected status %d, got %d", i+1, http.StatusOK, rr.Code)
		}
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Request 3: expected status %d, got %d", http.StatusTooManyRequests, rr.Code)
	}
}
