package walletmigration

import "testing"

func TestOwnerResolutionDecidesEligibility(t *testing.T) {
	cases := []struct {
		name      string
		tenant    string
		metadata  map[string]any
		outcome   OwnerOutcome
		owner     string
		plaintext bool
	}{
		{name: "single alias", tenant: testTenant, metadata: map[string]any{"owner_id": "auth0|a"}, outcome: OwnerResolved, owner: "auth0|a"},
		{name: "agreeing aliases with padding", tenant: testTenant, metadata: map[string]any{"owner_id": " auth0|a ", "owner_subject_id": "auth0|a", "user_id": ""}, outcome: OwnerResolved, owner: "auth0|a"},
		{name: "legacy alias only", tenant: testTenant, metadata: map[string]any{"user_id": "auth0|b"}, outcome: OwnerResolved, owner: "auth0|b"},
		{name: "legacy plaintext stays eligible", tenant: testTenant, metadata: map[string]any{"owner_id": "auth0|a", "secret": "plain"}, outcome: OwnerResolved, owner: "auth0|a", plaintext: true},
		{name: "no alias", tenant: testTenant, metadata: map[string]any{"name": "x"}, outcome: OwnerMissing},
		{name: "blank aliases", tenant: testTenant, metadata: map[string]any{"owner_id": "  ", "user_id": nil}, outcome: OwnerMissing},
		{name: "aliases disagree", tenant: testTenant, metadata: map[string]any{"owner_id": "auth0|a", "owner_subject_id": "auth0|b"}, outcome: OwnerConflict},
		{name: "non-string alias", tenant: testTenant, metadata: map[string]any{"owner_id": "auth0|a", "user_id": 42}, outcome: OwnerConflict},
		{name: "row from another tenant", tenant: "org_other", metadata: map[string]any{"owner_id": "auth0|a"}, outcome: TenantMismatch},
		{name: "metadata names another tenant", tenant: testTenant, metadata: map[string]any{"owner_id": "auth0|a", "tenant_id": "org_other"}, outcome: TenantMismatch},
		{name: "empty tenant scope", tenant: "", metadata: map[string]any{"owner_id": "auth0|a"}, outcome: TenantMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := SourceRow{ID: "item", TenantID: testTenant, ItemType: "api_key", Metadata: tc.metadata}
			plan := PlanItem(source, tc.tenant)
			if plan.Owner.Outcome != tc.outcome || plan.Owner.OwnerID != tc.owner {
				t.Fatalf("owner = %s %q, want %s %q", plan.Owner.Outcome, plan.Owner.OwnerID, tc.outcome, tc.owner)
			}
			if eligible := tc.outcome == OwnerResolved; plan.Eligible() != eligible {
				t.Fatalf("eligible = %v, want %v (quarantine %q)", plan.Eligible(), eligible, plan.Quarantine)
			}
			if !plan.Eligible() && plan.Quarantine != QuarantineReason(tc.outcome) {
				t.Fatalf("quarantine = %q, want %q", plan.Quarantine, tc.outcome)
			}
			if plan.HasFlag(FlagLegacyPlaintext) != tc.plaintext {
				t.Fatalf("legacy_plaintext flag = %v, want %v", plan.HasFlag(FlagLegacyPlaintext), tc.plaintext)
			}
		})
	}
}
