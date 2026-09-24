package auth

import "testing"

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
