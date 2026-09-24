package monthlyruntime

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestManagedRuntimeCapacityReservationDenialDetailsUsesTruthfulCustodyShape(t *testing.T) {
	details := ManagedRuntimeCapacityReservationDenialDetails(ProviderIONOS, 3, 4)
	if got := details["error_code"]; got != ManagedRuntimeMaxServersErrorCode {
		t.Fatalf("error_code = %v, want %s", got, ManagedRuntimeMaxServersErrorCode)
	}
	if got := details["retryable"]; got != false {
		t.Fatalf("retryable = %v, want false", got)
	}
	if got := details["reserved_servers"]; got != 4 {
		t.Fatalf("reserved_servers = %v, want 4", got)
	}
	if _, exists := details["active_servers"]; exists {
		t.Fatalf("capacity custody must not be presented as active servers: %#v", details)
	}
	guidance, ok := details["user_guidance"].(map[string]any)
	if !ok || !strings.Contains(guidance["body"].(string), "conservatively retains every reservation") {
		t.Fatalf("user_guidance = %#v, want truthful reservation wording", details["user_guidance"])
	}
	steps, ok := guidance["next_steps"].([]string)
	if !ok {
		t.Fatalf("next_steps = %#v, want string list", guidance["next_steps"])
	}
	joined := strings.Join(steps, " ")
	if strings.Contains(joined, "Wait for") || strings.Contains(joined, "Decommission an existing") ||
		!strings.Contains(joined, "operator reconciliation") {
		t.Fatalf("next_steps = %#v, want support reconciliation without a false automatic release promise", steps)
	}
}

func TestDecommissionProtectedDetailsShape(t *testing.T) {
	details := DecommissionProtectedDetails(ProviderCentron, "lease-main")
	if got := details["error_code"]; got != DecommissionBlockedProtectedErrorCode {
		t.Fatalf("error_code = %v, want %s", got, DecommissionBlockedProtectedErrorCode)
	}
	if got := details["reason_code"]; got != ReasonLeaseProtected {
		t.Fatalf("reason_code = %v, want %s", got, ReasonLeaseProtected)
	}
	if got := details["force_offered"]; got != false {
		t.Fatalf("force_offered = %v, want false", got)
	}
	if got := details["lease_id"]; got != "lease-main" {
		t.Fatalf("lease_id = %v, want lease-main", got)
	}
}

// TestLeaseVisibleToOwnerRejectsMislabeledOrgSubject pins the visibility
// boundary that a shared tenant makes load-bearing. Kind alone must not grant
// visibility: a lease held by another subject stays hidden even when it claims
// org kind, while a lease genuinely held by the organization stays shared.
func TestLeaseVisibleToOwnerRejectsMislabeledOrgSubject(t *testing.T) {
	newLease := func(kind vmlease.SubjectKind, subjectID, orgID string) vmlease.Lease {
		return vmlease.Lease{Subject: vmlease.Subject{Kind: kind, ID: subjectID, OrgID: orgID}}
	}
	for _, tc := range []struct {
		name  string
		lease vmlease.Lease
		want  bool
	}{
		{"own user lease", newLease(vmlease.SubjectUser, "owner-1", "tenant-1"), true},
		{"organization holds the lease", newLease(vmlease.SubjectOrg, "tenant-1", "tenant-1"), true},
		{"foreign user lease", newLease(vmlease.SubjectUser, "owner-2", "tenant-1"), false},
		{"foreign subject labeled org", newLease(vmlease.SubjectOrg, "owner-2", "tenant-1"), false},
		{"foreign tenant", newLease(vmlease.SubjectUser, "owner-1", "tenant-2"), false},
	} {
		if got := LeaseVisibleToOwner(tc.lease, "tenant-1", "owner-1"); got != tc.want {
			t.Fatalf("%s: visible = %v, want %v", tc.name, got, tc.want)
		}
	}
	if LeaseVisibleToOwner(newLease(vmlease.SubjectOrg, "tenant-1", "tenant-1"), "tenant-1", "") {
		t.Fatal("an unidentified caller must not see tenant inventory")
	}
}
