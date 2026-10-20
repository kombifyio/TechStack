package stackrouting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type DNSRecord struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Values  []string `json:"values"`
	Proxied bool     `json:"proxied,omitempty"`
}
type TLSExpectation struct {
	Hostname          string `json:"hostname,omitempty"`
	Issuer            string `json:"issuer,omitempty"`
	FingerprintSHA256 string `json:"fingerprint_sha256,omitempty"`
}
type Expectations struct {
	DNS []DNSRecord    `json:"dns"`
	TLS TLSExpectation `json:"tls"`
}

// NormalizeExpectations binds expected records to the routing domain. Proxy
// records describe provider configuration; their origin values are never public edge IPs.
func NormalizeExpectations(in Expectations, domain string) (Expectations, error) {
	in = CloneExpectations(in)
	if in.DNS == nil {
		in.DNS = []DNSRecord{}
	}
	if len(in.DNS) > 64 {
		return in, fmt.Errorf("%w: too many DNS expectations", ErrInvalid)
	}
	for i := range in.DNS {
		r := &in.DNS[i]
		r.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Name), "."))
		if r.Name == "" {
			r.Name = domain
		}
		r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
		name := r.Name
		if validateDomain(name) != nil || (name != domain && !strings.HasSuffix(name, "."+domain)) {
			return in, fmt.Errorf("%w: DNS expectation outside routing domain", ErrInvalid)
		}
		if len(r.Values) == 0 || len(r.Values) > 32 {
			return in, fmt.Errorf("%w: DNS expectation values required", ErrInvalid)
		}
		if r.Proxied && r.Type != "A" && r.Type != "AAAA" && r.Type != "CNAME" {
			return in, fmt.Errorf("%w: unsupported proxied DNS type", ErrInvalid)
		}
		for j, v := range r.Values {
			v = strings.TrimSpace(v)
			switch r.Type {
			case "A", "AAAA":
				ip := net.ParseIP(v)
				if ip == nil || (r.Type == "A" && ip.To4() == nil) || (r.Type == "AAAA" && ip.To4() != nil) {
					return in, fmt.Errorf("%w: invalid DNS address", ErrInvalid)
				}
				v = ip.String()
			case "CNAME", "NS":
				v = strings.ToLower(strings.TrimSuffix(v, "."))
				if validateDomain(v) != nil {
					return in, ErrInvalid
				}
			case "TXT":
				if len(v) > 2048 {
					return in, ErrInvalid
				}
			default:
				return in, fmt.Errorf("%w: unsupported DNS expectation type", ErrInvalid)
			}
			r.Values[j] = v
		}
		r.Values = canonicalDNSValues(r.Type, r.Values)
	}
	sort.Slice(in.DNS, func(i, j int) bool {
		a, b := in.DNS[i], in.DNS[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Type < b.Type
	})
	for i := 1; i < len(in.DNS); i++ {
		if in.DNS[i].Name == in.DNS[i-1].Name && in.DNS[i].Type == in.DNS[i-1].Type {
			return in, fmt.Errorf("%w: duplicate DNS expectation", ErrInvalid)
		}
	}
	in.TLS.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(in.TLS.Hostname), "."))
	if in.TLS.Hostname == "" {
		in.TLS.Hostname = domain
	}
	if validateDomain(in.TLS.Hostname) != nil || (in.TLS.Hostname != domain && !strings.HasSuffix(in.TLS.Hostname, "."+domain)) {
		return in, fmt.Errorf("%w: TLS hostname outside routing domain", ErrInvalid)
	}
	in.TLS.Issuer = strings.TrimSpace(in.TLS.Issuer)
	if len(in.TLS.Issuer) > 512 {
		return in, ErrInvalid
	}
	in.TLS.FingerprintSHA256 = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(in.TLS.FingerprintSHA256)), "sha256:")
	if in.TLS.FingerprintSHA256 != "" {
		b, e := hex.DecodeString(in.TLS.FingerprintSHA256)
		if e != nil || len(b) != 32 {
			return in, ErrInvalid
		}
	}
	return in, nil
}
func CloneExpectations(in Expectations) Expectations {
	out := in
	out.DNS = make([]DNSRecord, len(in.DNS))
	copy(out.DNS, in.DNS)
	for i := range out.DNS {
		out.DNS[i].Values = append([]string(nil), in.DNS[i].Values...)
	}
	return out
}
func expectationsEqual(a, b Expectations) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

type ExpiryEvidence struct {
	State            string     `json:"state"`
	Source           string     `json:"source,omitempty"`
	ObservedAt       *time.Time `json:"observed_at,omitempty"`
	FreshUntil       *time.Time `json:"fresh_until,omitempty"`
	Freshness        string     `json:"freshness"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RemainingSeconds *int64     `json:"remaining_seconds,omitempty"`
	ReasonCode       string     `json:"reason_code,omitempty"`
	LastAttemptAt    *time.Time `json:"last_attempt_at,omitempty"`
}
type TLSEvidence struct {
	ExpiryEvidence
	Subject           string `json:"subject,omitempty"`
	Issuer            string `json:"issuer,omitempty"`
	FingerprintSHA256 string `json:"fingerprint_sha256,omitempty"`
	Valid             *bool  `json:"valid,omitempty"`
}
type EvidenceDifference struct {
	Field      string   `json:"field"`
	Expected   []string `json:"expected"`
	Observed   []string `json:"observed"`
	State      string   `json:"state,omitempty"`
	ReasonCode string   `json:"reason_code,omitempty"`
}
type DriftEvidence struct {
	State          string               `json:"state"`
	Source         string               `json:"source,omitempty"`
	ObservedAt     *time.Time           `json:"observed_at,omitempty"`
	FreshUntil     *time.Time           `json:"fresh_until,omitempty"`
	DetectedAt     *time.Time           `json:"detected_at,omitempty"`
	Freshness      string               `json:"freshness"`
	Differences    []EvidenceDifference `json:"differences"`
	ReasonCode     string               `json:"reason_code,omitempty"`
	LastAttemptAt  *time.Time           `json:"last_attempt_at,omitempty"`
	ExpectedSource string               `json:"expected_source,omitempty"`
}
type DomainCondition struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	State           string     `json:"state"`
	Severity        string     `json:"severity"`
	FirstDetectedAt time.Time  `json:"first_detected_at"`
	LastObservedAt  time.Time  `json:"last_observed_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	EvidenceRef     string     `json:"evidence_ref,omitempty"`
	Freshness       string     `json:"freshness"`
	Episode         int64      `json:"episode"`
}
type DomainService struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type DomainRow struct {
	StackID          string            `json:"stack_id"`
	ID               string            `json:"id"`
	Domain           string            `json:"domain"`
	RegisteredDomain string            `json:"registered_domain"`
	ServerID         string            `json:"server_id"`
	Services         []DomainService   `json:"services"`
	RoutingRevision  int64             `json:"routing_revision,omitempty"`
	TLSPort          int               `json:"tls_port,omitempty"`
	Registration     ExpiryEvidence    `json:"registration"`
	TLS              TLSEvidence       `json:"tls"`
	DNS              DriftEvidence     `json:"dns"`
	TLSDrift         DriftEvidence     `json:"tls_drift"`
	Conditions       []DomainCondition `json:"conditions"`
	CheckStartedAt   time.Time         `json:"check_started_at,omitempty"`
	TLSDisabled      bool              `json:"tls_disabled,omitempty"`
}
type DomainReport struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Domains     []DomainRow `json:"domains"`
}
type DomainTarget struct {
	TenantID, OwnerSubjectID, StackID, ServerID, Domain string
	RoutingRevision                                     int64
}

func evidenceFreshness(observed, until *time.Time, now time.Time) string {
	if observed == nil || observed.IsZero() {
		return "unknown"
	}
	if observed.After(now) || until == nil || !until.After(*observed) || !until.After(now) {
		return "stale"
	}
	return "fresh"
}
func refreshExpiry(e *ExpiryEvidence, now time.Time) {
	e.Freshness = evidenceFreshness(e.ObservedAt, e.FreshUntil, now)
	e.RemainingSeconds = nil
	if e.ExpiresAt != nil {
		v := int64(e.ExpiresAt.Sub(now) / time.Second)
		e.RemainingSeconds = &v
	}
	if e.State == "" {
		e.State = "unknown"
	}
}

// RefreshDomain recomputes age and time remaining without inventing an observation or transition.
func RefreshDomain(row DomainRow, now time.Time) DomainRow {
	// Copy onto a non-nil slice. append onto nil stays nil when there are no
	// conditions, and encoding/json then emits null instead of [].
	row.Conditions = append([]DomainCondition{}, row.Conditions...)
	refreshExpiry(&row.Registration, now)
	refreshExpiry(&row.TLS.ExpiryEvidence, now)
	for _, e := range []*DriftEvidence{&row.DNS, &row.TLSDrift} {
		e.Freshness = evidenceFreshness(e.ObservedAt, e.FreshUntil, now)
		if e.State == "" {
			e.State = "unknown"
		}
	}
	for i := range row.Conditions {
		c := &row.Conditions[i]
		switch {
		case strings.HasPrefix(c.Kind, "registration") || c.Kind == "stale_observation:registration":
			c.Freshness = row.Registration.Freshness
		case strings.HasPrefix(c.Kind, "dns") || c.Kind == "stale_observation:dns":
			c.Freshness = row.DNS.Freshness
		case c.Kind == "certificate_mismatch":
			c.Freshness = row.TLSDrift.Freshness
		default:
			c.Freshness = row.TLS.Freshness
		}
	}
	return row
}
func retainExpiry(next ExpiryEvidence, old ExpiryEvidence) ExpiryEvidence {
	if next.ObservedAt != nil {
		return next
	}
	if old.ObservedAt == nil {
		return next
	}
	next.Source = old.Source
	next.ObservedAt = old.ObservedAt
	next.FreshUntil = old.FreshUntil
	next.ExpiresAt = old.ExpiresAt
	return next
}
func retainDrift(next DriftEvidence, old DriftEvidence) DriftEvidence {
	if next.ObservedAt != nil {
		return next
	}
	if old.ObservedAt == nil {
		return next
	}
	next.Source = old.Source
	next.ObservedAt = old.ObservedAt
	next.FreshUntil = old.FreshUntil
	next.Differences = old.Differences
	next.DetectedAt = old.DetectedAt
	return next
}

// EvaluateDomain keeps a condition episode across failed/stale observations and
// resolves it only from fresh positive recovery evidence. Expiry channels never infer one another.
func EvaluateDomain(row DomainRow, previous *DomainRow, now time.Time) DomainRow {
	now = now.UTC()
	old := map[string]DomainCondition{}
	if previous != nil && previous.ID == row.ID && previous.Domain == row.Domain && previous.ServerID == row.ServerID {
		for _, c := range previous.Conditions {
			old[c.Kind] = c
		}
		row.Registration = retainExpiry(row.Registration, previous.Registration)
		if row.TLS.ObservedAt == nil {
			row.TLS.ExpiryEvidence = retainExpiry(row.TLS.ExpiryEvidence, previous.TLS.ExpiryEvidence)
			row.TLS.Subject = previous.TLS.Subject
			row.TLS.Issuer = previous.TLS.Issuer
			row.TLS.FingerprintSHA256 = previous.TLS.FingerprintSHA256
			row.TLS.Valid = previous.TLS.Valid
		}
		if previous.RoutingRevision == row.RoutingRevision {
			row.DNS = retainDrift(row.DNS, previous.DNS)
			row.TLSDrift = retainDrift(row.TLSDrift, previous.TLSDrift)
		}
	}
	row = RefreshDomain(row, now)
	row.Conditions = []DomainCondition{}
	add := func(kind, severity string, bad, recovered bool, fresh string, observed *time.Time) {
		c, exists := old[kind]
		if bad {
			if !exists || c.State != "active" {
				episode := c.Episode + 1
				c = DomainCondition{ID: domainConditionID(row.ID, kind), Kind: kind, State: "active", Severity: severity, FirstDetectedAt: now, LastObservedAt: now, Episode: episode, EvidenceRef: kind}
				if observed != nil {
					c.LastObservedAt = *observed
				}
			}
			c.Severity = severity
			if observed != nil && observed.After(c.LastObservedAt) {
				c.LastObservedAt = *observed
			}
			c.Freshness = fresh
			row.Conditions = append(row.Conditions, c)
		} else if exists {
			if c.State == "active" && recovered {
				c.State = "resolved"
				c.ResolvedAt = &now
				if observed != nil {
					c.LastObservedAt = *observed
				}
			}
			c.Freshness = fresh
			row.Conditions = append(row.Conditions, c)
		}
	}
	expiry := func(kind string, e ExpiryEvidence, threshold time.Duration, condition string) {
		known := e.State == "known" && e.ExpiresAt != nil
		fresh := e.Freshness == "fresh"
		if kind == "registration" {
			add("registration_unavailable", "warning", !known, known && fresh, e.Freshness, e.ObservedAt)
		}
		add("stale_observation:"+kind, "warning", e.ObservedAt != nil && !fresh, fresh, e.Freshness, e.ObservedAt)
		severity := "warning"
		if known && !e.ExpiresAt.After(now) {
			severity = "critical"
		}
		add(condition, severity, known && fresh && e.ExpiresAt.Sub(now) <= threshold, known && fresh && e.ExpiresAt.Sub(now) > threshold, e.Freshness, e.ObservedAt)
	}
	expiry("registration", row.Registration, 30*24*time.Hour, "registration_expiring")
	expiry("tls", row.TLS.ExpiryEvidence, 14*24*time.Hour, "certificate_expiring")
	drift := func(kind, condition string, e *DriftEvidence) {
		fresh := e.Freshness == "fresh"
		if kind == "dns" {
			add("stale_observation:dns", "warning", e.ObservedAt != nil && !fresh, fresh, e.Freshness, e.ObservedAt)
		}
		add(condition, "warning", e.State == "drifted" && fresh, e.State == "matching" && fresh, e.Freshness, e.ObservedAt)
		e.DetectedAt = nil
		for _, c := range row.Conditions {
			if c.Kind == condition && c.State == "active" {
				t := c.FirstDetectedAt
				e.DetectedAt = &t
			}
		}
	}
	drift("dns", "dns_mismatch", &row.DNS)
	drift("tls", "certificate_mismatch", &row.TLSDrift)
	return row
}
func domainConditionID(id, kind string) string {
	d := sha256.Sum256([]byte(id + "\x00" + kind))
	return hex.EncodeToString(d[:16])
}
