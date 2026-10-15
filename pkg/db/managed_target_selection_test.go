package db

import "testing"

// Fixture admission follows the protected endpoint or an explicit assignment,
// not coincidentally reused database/login names on somebody else's server.
func TestRuntimeConfigurationDoesNotAssignFixtureByNames(t *testing.T) {
	t.Setenv(EnvManagedTestAssignment, "")
	t.Setenv(EnvStoreBackend, "postgres")
	for _, ordinary := range []string{
		"postgres://customer:configuration-only@ordinary.example.invalid:5432/techstack_test?sslmode=verify-full",
		"postgres://techstack_test_app:configuration-only@ordinary.example.invalid:5432/customer?sslmode=verify-full",
		"postgres://techstack_test_owner:configuration-only@ordinary.example.invalid:5432/customer?sslmode=verify-full",
	} {
		t.Setenv(EnvDatabaseURL, ordinary)
		if _, err := ConfigFromEnv(); err != nil {
			t.Fatalf("ordinary runtime configuration incorrectly requires fixture admission: %v", err)
		}
	}
	t.Setenv(EnvDatabaseURL, "postgres://techstack_test_app:configuration-only@"+ManagedTestHost+":5432/techstack_test?sslmode=verify-full")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("protected managed endpoint admitted without its explicit assignment")
	}
}
