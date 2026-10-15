package stackrouting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/publicsuffix"
)

const domainDNSResolver = "https://cloudflare-dns.com/dns-query"
const domainRDAPBootstrap = "https://data.iana.org/rdap/dns.json"

// DomainCollector observes public protocols only. It never contacts a private
// endpoint, supplies provider credentials, or changes routing or registration.
type DomainCollector struct {
	client        *http.Client
	mu            sync.Mutex
	bootstrap     map[string]string
	bootstrapAt   time.Time
	registrations map[string]ExpiryEvidence
}

func NewDomainCollector() *DomainCollector {
	transport := &http.Transport{
		DialContext:           dialDomainPublic,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       time.Minute,
	}
	return &DomainCollector{client: &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.User != nil {
				return fmt.Errorf("unsupported registration redirect")
			}
			return nil
		},
	}, registrations: map[string]ExpiryEvidence{}}
}

// Collect executes one bounded observation. Time fields describe the successful
// observation itself; a failed attempt never renews old evidence.
func (c *DomainCollector) Collect(ctx context.Context, row DomainRow, expected Expectations) DomainRow {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	row.CheckStartedAt = time.Now().UTC()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); row.RegisteredDomain, row.Registration = c.registration(ctx, row.Domain) }()
	go func() { defer wg.Done(); row.DNS = c.observeDNS(ctx, row.Domain, expected.DNS) }()
	go func() {
		defer wg.Done()
		if row.TLSDisabled {
			row.TLS = TLSEvidence{ExpiryEvidence: ExpiryEvidence{State: "unknown", ReasonCode: "tls_not_configured", LastAttemptAt: &row.CheckStartedAt}}
			row.TLSDrift = DriftEvidence{State: "unknown", ReasonCode: "tls_not_configured", Differences: []EvidenceDifference{}, LastAttemptAt: &row.CheckStartedAt}
			return
		}
		row.TLS, row.TLSDrift = c.observeTLS(ctx, row.Domain, row.TLSPort, expected.TLS)
	}()
	wg.Wait()
	if row.RoutingRevision > 0 {
		row.DNS.ExpectedSource, row.TLSDrift.ExpectedSource = "stack_routing_desired", "stack_routing_desired"
	} else {
		row.TLSDrift.ExpectedSource = "inventory_endpoint"
	}
	return row
}

func (c *DomainCollector) registration(ctx context.Context, domain string) (string, ExpiryEvidence) {
	now := time.Now().UTC()
	fact := ExpiryEvidence{State: "unknown", Source: domainRDAPBootstrap, LastAttemptAt: &now, ReasonCode: "registration_domain_unknown"}
	_, icann := publicsuffix.PublicSuffix(domain)
	registered, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil || !icann {
		return "", fact
	}
	c.mu.Lock()
	cached, ok := c.registrations[registered]
	c.mu.Unlock()
	if ok && cached.FreshUntil != nil && now.Before(*cached.FreshUntil) {
		return registered, cached
	}
	base, err := c.rdapBase(ctx, registered)
	if err != nil {
		fact.ReasonCode = "registration_rdap_unavailable"
		return registered, fact
	}
	if base == "" {
		fact.ReasonCode = "registration_rdap_unsupported"
		return registered, fact
	}
	fact.Source = strings.TrimRight(base, "/") + "/domain/" + url.PathEscape(registered)
	var response struct {
		ObjectClassName string `json:"objectClassName"`
		LDHName         string `json:"ldhName"`
		Events          []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := c.getJSON(ctx, fact.Source, &response); err != nil {
		fact.ReasonCode = "registration_rdap_unavailable"
		return registered, fact
	}
	if response.ObjectClassName != "domain" || !strings.EqualFold(strings.TrimSuffix(response.LDHName, "."), registered) {
		fact.ReasonCode = "registration_data_conflict"
		return registered, fact
	}
	observed := time.Now().UTC()
	fact.ObservedAt = &observed
	validUntil := observed.Add(24 * time.Hour)
	fact.FreshUntil = &validUntil
	fact.ReasonCode = "registration_expiry_unpublished"
	for _, event := range response.Events {
		if event.Action != "expiration" {
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, event.Date)
		if parseErr != nil || (fact.ExpiresAt != nil && !fact.ExpiresAt.Equal(expires)) {
			fact.State = "unknown"
			fact.ExpiresAt = nil
			fact.ReasonCode = "registration_data_conflict"
			return registered, fact
		}
		fact.ExpiresAt = &expires
		fact.State, fact.ReasonCode = "known", ""
	}
	c.mu.Lock()
	// Cache only measured replies. Missing expiry remains an explicit unknown.
	if len(c.registrations) >= 2048 {
		c.registrations = map[string]ExpiryEvidence{}
	}
	c.registrations[registered] = fact
	c.mu.Unlock()
	return registered, fact
}

func (c *DomainCollector) rdapBase(ctx context.Context, domain string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bootstrap == nil || time.Since(c.bootstrapAt) > 24*time.Hour {
		var document struct {
			Services [][][]string `json:"services"`
		}
		if err := c.getJSON(ctx, domainRDAPBootstrap, &document); err != nil {
			return "", err
		}
		services := map[string]string{}
		for _, service := range document.Services {
			if len(service) != 2 {
				continue
			}
			for _, raw := range service[1] {
				base, err := url.Parse(raw)
				if err != nil || base.Scheme != "https" || base.User != nil || base.Hostname() == "" {
					continue
				}
				for _, suffix := range service[0] {
					services[strings.ToLower(suffix)] = base.String()
				}
				break
			}
		}
		c.bootstrap, c.bootstrapAt = services, time.Now()
	}
	labels := strings.Split(domain, ".")
	for i := range labels {
		if base := c.bootstrap[strings.Join(labels[i:], ".")]; base != "" {
			return base, nil
		}
	}
	return "", nil
}

func (c *DomainCollector) getJSON(ctx context.Context, endpoint string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("registration HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result)
}

func (c *DomainCollector) observeDNS(ctx context.Context, domain string, records []DNSRecord) DriftEvidence {
	now := time.Now().UTC()
	fact := DriftEvidence{State: "matching", Source: domainDNSResolver, LastAttemptAt: &now, Differences: []EvidenceDifference{}}
	unknown, mismatch, measured := len(records) == 0, false, false
	if len(records) == 0 {
		fact.State, fact.ReasonCode = "unknown", "dns_expectation_unknown"
		records = []DNSRecord{{Name: domain, Type: "A"}, {Name: domain, Type: "AAAA"}, {Name: domain, Type: "CNAME"}}
	}
	for _, record := range records {
		name := record.Name
		if name == "" {
			name = domain
		}
		values, err := c.queryDNS(ctx, name, record.Type)
		expected := canonicalDNSValues(record.Type, record.Values)
		difference := EvidenceDifference{Field: name + " " + record.Type, Expected: expected, Observed: values, State: "matching"}
		if err != nil {
			unknown, difference.State, difference.ReasonCode = true, "unknown", "dns_observation_unavailable"
			difference.Observed = []string{}
			fact.ReasonCode = difference.ReasonCode
		} else if record.Proxied {
			// A proxy's public edge addresses do not disclose its origin record.
			unknown, difference.State, difference.ReasonCode = true, "unknown", "dns_proxy_origin_unobservable"
			fact.ReasonCode = difference.ReasonCode
		} else if len(expected) == 0 {
			difference.State, difference.ReasonCode = "unknown", "dns_expectation_unknown"
		} else if !reflect.DeepEqual(expected, values) {
			mismatch, difference.State = true, "drifted"
		}
		measured = measured || err == nil
		fact.Differences = append(fact.Differences, difference)
	}
	if mismatch {
		fact.State = "drifted"
	} else if unknown {
		fact.State = "unknown"
	}
	if measured {
		observed := time.Now().UTC()
		freshUntil := observed.Add(10 * time.Minute)
		fact.ObservedAt, fact.FreshUntil = &observed, &freshUntil
	}
	return fact
}

func (c *DomainCollector) queryDNS(ctx context.Context, name, recordType string) ([]string, error) {
	typeID, ok := dns.StringToType[recordType]
	if !ok {
		return nil, fmt.Errorf("unsupported DNS type")
	}
	question := new(dns.Msg)
	question.SetQuestion(dns.Fqdn(name), typeID)
	wire, err := question.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, domainDNSResolver, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DNS HTTP %d", response.StatusCode)
	}
	wire, err = io.ReadAll(io.LimitReader(response.Body, 65536))
	if err != nil {
		return nil, err
	}
	answer := new(dns.Msg)
	if err = answer.Unpack(wire); err != nil {
		return nil, err
	}
	if answer.Id != question.Id || !answer.Response || answer.Truncated || len(answer.Question) != 1 || answer.Question[0] != question.Question[0] {
		return nil, fmt.Errorf("invalid DNS response")
	}
	if answer.Rcode != dns.RcodeSuccess && answer.Rcode != dns.RcodeNameError {
		return nil, fmt.Errorf("DNS response unavailable")
	}
	values := []string{}
	for _, record := range answer.Answer {
		if !strings.EqualFold(record.Header().Name, dns.Fqdn(name)) || record.Header().Rrtype != typeID {
			continue
		}
		switch record := record.(type) {
		case *dns.A:
			values = append(values, record.A.String())
		case *dns.AAAA:
			values = append(values, record.AAAA.String())
		case *dns.CNAME:
			values = append(values, record.Target)
		case *dns.NS:
			values = append(values, record.Ns)
		case *dns.TXT:
			values = append(values, strings.Join(record.Txt, ""))
		case *dns.MX:
			values = append(values, fmt.Sprintf("%d %s", record.Preference, strings.TrimSuffix(record.Mx, ".")))
		case *dns.CAA:
			values = append(values, fmt.Sprintf("%d %s %s", record.Flag, record.Tag, record.Value))
		}
	}
	return canonicalDNSValues(recordType, values), nil
}

func canonicalDNSValues(recordType string, values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		switch recordType {
		case "CNAME", "NS":
			value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		case "A", "AAAA":
			if address, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
				value = address.String()
			}
		case "MX":
			value = strings.ToLower(strings.TrimSpace(value))
		}
		set[value] = true
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (c *DomainCollector) observeTLS(ctx context.Context, domain string, port int, expected TLSExpectation) (TLSEvidence, DriftEvidence) {
	now := time.Now().UTC()
	if port == 0 {
		port = 443
	}
	source := "tls://" + net.JoinHostPort(domain, strconv.Itoa(port)) + " (SNI " + domain + ")"
	expiry := TLSEvidence{ExpiryEvidence: ExpiryEvidence{State: "unknown", Source: source, LastAttemptAt: &now, ReasonCode: "tls_observation_unavailable"}}
	drift := DriftEvidence{State: "unknown", Source: source, LastAttemptAt: &now, ReasonCode: "tls_observation_unavailable", Differences: []EvidenceDifference{}}
	connection, err := dialDomainPublic(ctx, "tcp", net.JoinHostPort(domain, strconv.Itoa(port)))
	if err != nil {
		if strings.Contains(err.Error(), "public endpoint required") {
			expiry.ReasonCode, drift.ReasonCode = "tls_public_endpoint_required", "tls_public_endpoint_required"
		}
		return expiry, drift
	}
	defer connection.Close()
	// Handshake reads an untrusted certificate even when expired or mismatched;
	// no application data is sent, and chain/hostname validation follows below.
	tlsConnection := tls.Client(connection, &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}) //nolint:gosec -- evidence collection, independently verified below
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		return expiry, drift
	}
	certificates := tlsConnection.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return expiry, drift
	}
	leaf := certificates[0]
	observed := time.Now().UTC()
	freshUntil := observed.Add(10 * time.Minute)
	expiry.State, expiry.ReasonCode = "known", ""
	expiry.ObservedAt, expiry.FreshUntil, expiry.ExpiresAt = &observed, &freshUntil, &leaf.NotAfter
	expiry.Subject, expiry.Issuer = leaf.Subject.String(), leaf.Issuer.String()
	digest := sha256.Sum256(leaf.Raw)
	expiry.FingerprintSHA256 = hex.EncodeToString(digest[:])
	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	hostname := expected.Hostname
	if hostname == "" {
		hostname = domain
	}
	_, verifyErr := leaf.Verify(x509.VerifyOptions{DNSName: domain, Intermediates: intermediates, CurrentTime: observed})
	valid := verifyErr == nil
	expiry.Valid = &valid
	drift.State, drift.ReasonCode = "matching", ""
	drift.ObservedAt, drift.FreshUntil = &observed, &freshUntil
	hostState, chainState := "matching", "matching"
	if leaf.VerifyHostname(hostname) != nil {
		hostState = "drifted"
	}
	if !valid {
		chainState = "drifted"
	}
	drift.Differences = append(drift.Differences,
		EvidenceDifference{Field: "hostname", Expected: []string{hostname}, Observed: append([]string{}, leaf.DNSNames...), State: hostState},
		EvidenceDifference{Field: "trusted_chain", Expected: []string{"true"}, Observed: []string{strconv.FormatBool(valid)}, State: chainState})
	if verifyErr != nil || hostState == "drifted" {
		drift.State = "drifted"
	}
	if observed.Before(leaf.NotBefore) {
		drift.Differences = append(drift.Differences, EvidenceDifference{Field: "valid_from", Expected: []string{observed.Format(time.RFC3339)}, Observed: []string{leaf.NotBefore.Format(time.RFC3339)}, State: "drifted"})
	}
	if expected.Issuer != "" {
		state := "matching"
		if expected.Issuer != expiry.Issuer {
			drift.State, state = "drifted", "drifted"
		}
		drift.Differences = append(drift.Differences, EvidenceDifference{Field: "issuer", Expected: []string{expected.Issuer}, Observed: []string{expiry.Issuer}, State: state})
	}
	if expected.FingerprintSHA256 != "" {
		state := "matching"
		if !strings.EqualFold(expected.FingerprintSHA256, expiry.FingerprintSHA256) {
			drift.State, state = "drifted", "drifted"
		}
		drift.Differences = append(drift.Differences, EvidenceDifference{Field: "fingerprint_sha256", Expected: []string{expected.FingerprintSHA256}, Observed: []string{expiry.FingerprintSHA256}, State: state})
	}
	return expiry, drift
}

var domainPrivateRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

func domainPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range domainPrivateRanges {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func dialDomainPublic(ctx context.Context, network, endpoint string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no public endpoint")
	}
	for _, address := range addresses {
		if !domainPublicAddress(address) {
			return nil, fmt.Errorf("public endpoint required")
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	for _, address := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		err = dialErr
	}
	return nil, err
}
