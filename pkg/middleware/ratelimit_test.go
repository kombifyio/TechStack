package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{name: "remote address", remoteAddr: "192.168.1.1:12345", want: "192.168.1.1"},
		{name: "remote address without port", remoteAddr: "192.168.1.1", want: "192.168.1.1"},
		{
			name:       "forwarded address",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50"},
			want:       "203.0.113.50",
		},
		{
			name:       "first forwarded address",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.50, 70.41.3.18"},
			want:       "203.0.113.50",
		},
		{
			name:       "real IP",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Real-IP": "198.51.100.25"},
			want:       "198.51.100.25",
		},
		{
			name:       "forwarded address precedence",
			remoteAddr: "10.0.0.1:12345",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.50",
				"X-Real-IP":       "198.51.100.25",
			},
			want: "203.0.113.50",
		},
		{name: "IPv6", remoteAddr: "[::1]:12345", want: "::1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			if got := ExtractClientIP(req); got != tt.want {
				t.Fatalf("ExtractClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplicaBudgetDividesTheAggregateBudget(t *testing.T) {
	tests := []struct {
		name      string
		rps       float64
		burst     int
		replicas  int
		wantRPS   float64
		wantBurst int
	}{
		{name: "single replica keeps the configured budget", rps: 10, burst: 20, replicas: 1, wantRPS: 10, wantBurst: 20},
		{name: "four replicas divide the budget", rps: 10, burst: 20, replicas: 4, wantRPS: 2.5, wantBurst: 5},
		{name: "tiny budgets keep a usable floor", rps: 0.2, burst: 2, replicas: 8, wantRPS: 0.1, wantBurst: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotRPS, gotBurst := ReplicaBudget(test.rps, test.burst, test.replicas)
			if gotRPS != test.wantRPS || gotBurst != test.wantBurst {
				t.Fatalf("ReplicaBudget(%v, %d, %d) = (%v, %d), want (%v, %d)",
					test.rps, test.burst, test.replicas, gotRPS, gotBurst, test.wantRPS, test.wantBurst)
			}
		})
	}
}
