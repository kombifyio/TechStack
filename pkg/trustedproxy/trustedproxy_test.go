package trustedproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A client can write any forwarded header; only a configured proxy's claim
// about the client may be believed, and an address the client prepended to the
// chain is never reached.
func TestForwardedClaimsAreBelievedOnlyFromTrustedProxies(t *testing.T) {
	set := Parse("10.0.0.0/8")
	request := func(remote, xff, host string) *http.Request {
		r := httptest.NewRequest("POST", "http://internal/x", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", xff)
		r.Header.Set("X-Forwarded-Host", host)
		return r
	}
	if got := set.ClientIP(request("203.0.113.9:4000", "198.51.100.1", "evil.example")); got != "203.0.113.9" {
		t.Fatalf("direct client's own header was believed: %s", got)
	}
	if got := set.ClientIP(request("10.1.2.3:4000", "198.51.100.1, 203.0.113.50, 10.9.9.9", "")); got != "203.0.113.50" {
		t.Fatalf("client behind the proxy = %s, want the rightmost untrusted hop", got)
	}
	if _, host, ok := set.ForwardedOrigin(request("203.0.113.9:4000", "", "evil.example")); ok || host != "" {
		t.Fatalf("forwarded host from an untrusted peer was offered: %q", host)
	}
	if _, host, ok := set.ForwardedOrigin(request("10.1.2.3:4000", "", "app.example")); !ok || host != "app.example" {
		t.Fatalf("forwarded host from the trusted proxy = %q, %v", host, ok)
	}
}
