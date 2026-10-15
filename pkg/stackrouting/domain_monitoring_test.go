package stackrouting

import (
	"testing"
	"time"
)

// An observation failure, stale matching result, and restart read must preserve
// the same mismatch episode; only fresh matching evidence proves recovery.
func TestDomainConditionRequiresFreshRecovery(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour)
	row := DomainRow{ID: "test-domain", Domain: "example.com", ServerID: "server", DNS: DriftEvidence{State: "drifted", Source: "authoritative-dns", ObservedAt: &now, FreshUntil: &until}}
	first := EvaluateDomain(row, nil, now)
	condition := func(r DomainRow) DomainCondition {
		t.Helper()
		for _, c := range r.Conditions {
			if c.Kind == "dns_mismatch" {
				return c
			}
		}
		t.Fatal("mismatch disappeared")
		return DomainCondition{}
	}
	original := condition(first)
	unknown := row
	unknown.DNS = DriftEvidence{State: "unknown", ReasonCode: "dns_observation_unavailable"}
	repeated := EvaluateDomain(unknown, &first, now.Add(time.Minute))
	c := condition(repeated)
	if c.State != "active" || c.ID != original.ID || c.Episode != original.Episode || !c.FirstDetectedAt.Equal(original.FirstDetectedAt) {
		t.Fatalf("unknown observation changed condition episode: %+v", c)
	}
	if repeated.DNS.ObservedAt == nil || !repeated.DNS.ObservedAt.Equal(now) {
		t.Fatal("failed poll changed observation timestamp")
	}
	stale := row
	stale.DNS.State = "matching"
	after := now.Add(2 * time.Hour)
	unrecovered := EvaluateDomain(stale, &repeated, after)
	if condition(unrecovered).State != "active" {
		t.Fatal("stale match fabricated recovery")
	}
	freshUntil := after.Add(time.Hour)
	stale.DNS.ObservedAt = &after
	stale.DNS.FreshUntil = &freshUntil
	recovered := EvaluateDomain(stale, &unrecovered, after)
	if condition(recovered).State != "resolved" || condition(recovered).ResolvedAt == nil {
		t.Fatal("fresh matching evidence did not resolve")
	}
	row.DNS.ObservedAt = &after
	row.DNS.FreshUntil = &freshUntil
	recurrence := condition(EvaluateDomain(row, &recovered, after.Add(time.Minute)))
	if recurrence.State != "active" || recurrence.ID != original.ID || recurrence.Episode <= original.Episode {
		t.Fatal("recurrence reused old notification episode")
	}
	if first.Registration.ExpiresAt != nil || first.Registration.RemainingSeconds != nil || first.Registration.State != "unknown" {
		t.Fatal("DNS inferred registration expiry")
	}
}
