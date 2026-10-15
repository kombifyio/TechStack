package stackrouting

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// Sensitive boundary: public observations must never dial local/private or
// translated/reserved addresses, including literal and mapped IPv4 targets.
func TestDomainCollectorRejectsPrivateTargets(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fc00::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::1"} {
		if domainPublicAddress(netip.MustParseAddr(value)) {
			t.Fatalf("nonpublic target admitted: %s", value)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, err := dialDomainPublic(ctx, "tcp", "127.0.0.1:443")
	if connection != nil {
		_ = connection.Close()
		t.Fatal("collector connected to loopback")
	}
	if err == nil {
		t.Fatal("loopback rejection was not reported")
	}
	if !domainPublicAddress(netip.MustParseAddr("1.1.1.1")) || !domainPublicAddress(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("public observation address rejected")
	}
}

type domainRDAPTransport func(*http.Request) (*http.Response, error)

func (f domainRDAPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// Regression: a valid expiration followed by conflicting or malformed registry
// data must return an unknown fact, never a known fact with a missing deadline.
func TestDomainConflictingRegistrationStaysUnknown(t *testing.T) {
	for _, date := range []string{"2027-08-14T04:00:00Z", "unpublished"} {
		collector := NewDomainCollector()
		collector.bootstrap, collector.bootstrapAt = map[string]string{"com": "https://registry.example"}, time.Now()
		collector.client = &http.Client{Transport: domainRDAPTransport(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"objectClassName":"domain","ldhName":"example.com","events":[{"eventAction":"expiration","eventDate":"2027-08-13T04:00:00Z"},{"eventAction":"expiration","eventDate":"` + date + `"}]}`))}, nil
		})}
		_, fact := collector.registration(t.Context(), "example.com")
		row := RefreshDomain(DomainRow{Registration: fact}, time.Now())
		if row.Registration.State != "unknown" || row.Registration.ExpiresAt != nil || row.Registration.RemainingSeconds != nil {
			t.Fatalf("conflicting registration presented a deadline: %+v", row.Registration)
		}
	}
}
