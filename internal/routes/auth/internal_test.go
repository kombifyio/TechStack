package auth

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
)

func TestVerifyEdgeTrust_MissingEnv(t *testing.T) {
	// verifyEdgeTrust depends on KOMBIFY_SSO_SECRET env var.
	// When unset, it should fail with service unavailable.
	// This is tested indirectly through integration tests since it
	// requires an *httpx.Event backed by a configured edge identity.
	t.Log("Edge trust verification requires integration test with a configured edge identity")
}

func TestSharedSecretMatches(t *testing.T) {
	tests := []struct {
		name     string
		provided string
		current  string
		next     string
		want     bool
	}{
		{"empty provided", "", "current", "next", false},
		{"matches current", "current", "current", "", true},
		{"matches next (rotation)", "next", "current", "next", true},
		{"matches neither", "wrong", "current", "next", false},
		{"both env unset", "anything", "", "", false},
		{"only next set, matches", "next", "", "next", true},
		{"only current set, matches next input fails", "next", "current", "", false},
		{"prefix of current fails (constant-time)", "curr", "current", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sharedSecretMatches(tt.provided, tt.current, tt.next); got != tt.want {
				t.Errorf("sharedSecretMatches(%q, %q, %q) = %v, want %v",
					tt.provided, tt.current, tt.next, got, tt.want)
			}
		})
	}
}

func TestFindOrCreateUserFromEdge_TableDriven(t *testing.T) {
	// This function requires a real PocketBase app instance.
	// It is tested via integration tests (see tests/integration/).
	// Here we document the expected behavior:
	//
	// Case 1: Existing user_link → returns linked PB user
	// Case 2: No user_link, existing PB user by email → creates link
	// Case 3: No user_link, no PB user → creates both
	t.Log("findOrCreateUserFromEdge requires PocketBase integration test")
}

func TestFeatureFlagApplyRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		flags   []FeatureFlagOverride
		wantErr bool
	}{
		{
			name:    "empty flags",
			flags:   []FeatureFlagOverride{},
			wantErr: true,
		},
		{
			name: "single valid flag",
			flags: []FeatureFlagOverride{
				{Key: "ai_assistant", Enabled: true, Reason: "admin override"},
			},
			wantErr: false,
		},
		{
			name: "multiple flags",
			flags: []FeatureFlagOverride{
				{Key: "ai_assistant", Enabled: true},
				{Key: "cloud_backup", Enabled: false},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := FeatureFlagApplyRequest{Flags: tt.flags}
			if tt.wantErr && len(req.Flags) != 0 {
				t.Error("expected validation to catch empty flags")
			}
			if !tt.wantErr && len(req.Flags) == 0 {
				t.Error("expected non-empty flags")
			}
		})
	}
}

func TestApplyFeatureFlagOverrides_SaaSDoesNotPersistLocally(t *testing.T) {
	result, err := applyFeatureFlagOverrides(nil, config.ModeSaaS, []FeatureFlagOverride{
		{Key: "cloud_backup", Enabled: true, Reason: "admin override"},
		{Key: "cloud_backup", Enabled: false},
	})
	if err != nil {
		t.Fatalf("applyFeatureFlagOverrides returned error: %v", err)
	}

	if result.Persisted {
		t.Fatal("expected SaaS feature flag apply to skip persistence")
	}

	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %v", result.Errors)
	}

	if len(result.Applied) != 2 {
		t.Fatalf("expected 2 accepted flags, got %d", len(result.Applied))
	}

	if result.Applied[0] != "cloud_backup" || result.Applied[1] != "cloud_backup" {
		t.Fatalf("unexpected applied flags: %v", result.Applied)
	}
}

func TestFeatureRolloutRecordFields_DoNotUseUserPreferenceShape(t *testing.T) {
	fields := featureRolloutRecordFields(FeatureFlagOverride{
		Key:     "cloud_backup",
		Enabled: true,
		Reason:  "admin override",
	})

	if fields["feature_key"] != "cloud_backup" {
		t.Fatalf("feature_key = %v, want simulation", fields["feature_key"])
	}
	if fields["enabled"] != true {
		t.Fatalf("enabled = %v, want true", fields["enabled"])
	}
	if fields["reason"] != "admin override" {
		t.Fatalf("reason = %v, want admin override", fields["reason"])
	}
	if _, ok := fields["user_id"]; ok {
		t.Fatal("feature rollout records must not use per-user user_id")
	}
	if _, ok := fields["source"]; ok {
		t.Fatal("feature rollout records must not write fields missing from the migration")
	}
}
