package db

import (
	"strings"
	"testing"
)

func TestUserOnboardingStateMigrationIsTenantScoped(t *testing.T) {
	content := readDBFile(t, "migrations/099_user_onboarding_state.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS user_onboarding_state",
		"REFERENCES techstack_tenants(id) ON DELETE CASCADE",
		"uq_user_onboarding_state_principal_journey",
		"ON user_onboarding_state (tenant_id, owner_subject_id, product, journey_id)",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation",
		"CHECK (status IN ('active', 'completed', 'dismissed'))",
		"CHECK (revision >= 0)",
		"CREATE TRIGGER set_user_onboarding_state_updated_at",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}
