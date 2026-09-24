package hooks

import (
	"os"
	"strings"
	"testing"
)

// TestIsDevEnvironment tests the development environment detection.
func TestIsDevEnvironment(t *testing.T) {
	tests := []struct {
		name         string
		techstackEnv string
		envVar       string
		expected     bool
	}{
		{"empty - defaults to non-dev", "", "", false},
		{"development", "development", "", true},
		{"dev", "dev", "", true},
		{"installed local client is not development", "local", "", false},
		{"DEVELOPMENT uppercase", "DEVELOPMENT", "", true},
		{"production", "production", "", false},
		{"staging", "staging", "", false},
		{"test", "test", "", false},
		{"fallback ENVIRONMENT=dev", "", "dev", true},
		{"fallback ENVIRONMENT=production", "", "production", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origKombi := os.Getenv("TECHSTACK_ENV")
			origEnv := os.Getenv("ENVIRONMENT")

			os.Setenv("TECHSTACK_ENV", tt.techstackEnv)
			os.Setenv("ENVIRONMENT", tt.envVar)

			defer func() {
				os.Setenv("TECHSTACK_ENV", origKombi)
				os.Setenv("ENVIRONMENT", origEnv)
			}()

			result := isDevEnvironment()
			if result != tt.expected {
				t.Errorf("isDevEnvironment() = %v, want %v (TECHSTACK_ENV=%q, ENVIRONMENT=%q)", result, tt.expected, tt.techstackEnv, tt.envVar)
			}
		})
	}
}

func TestResolvePassword(t *testing.T) {
	t.Run("uses env var when present", func(t *testing.T) {
		t.Setenv(EnvAdminPassword, "provided-secret")

		password, err := resolvePassword(EnvAdminPassword, "admin@techstack.local", "admin", false)
		if err != nil {
			t.Fatalf("resolvePassword() error = %v", err)
		}
		if password != "provided-secret" {
			t.Fatalf("resolvePassword() = %q, want provided-secret", password)
		}
	})

	t.Run("uses development fallback only in dev", func(t *testing.T) {
		password, err := resolvePassword(EnvDeveloperPassword, "developer@techstack.local", "developer", true)
		if err != nil {
			t.Fatalf("resolvePassword() error = %v", err)
		}
		if password != "dev-developer-password-change-me" {
			t.Fatalf("resolvePassword() = %q, want development fallback", password)
		}
	})

	t.Run("fails closed outside development", func(t *testing.T) {
		password, err := resolvePassword(EnvSuperuserPassword, "superuser@techstack.local", "superuser", false)
		if err == nil {
			t.Fatal("resolvePassword() expected error outside development without env")
		}
		if password != "" {
			t.Fatalf("resolvePassword() password = %q, want empty", password)
		}
		if !strings.Contains(err.Error(), EnvSuperuserPassword) {
			t.Fatalf("resolvePassword() error = %v, want missing env key", err)
		}
	})
}

func TestShouldBootstrapUsers(t *testing.T) {
	t.Run("development always bootstraps", func(t *testing.T) {
		enabled, err := shouldBootstrapUsers(true)
		if err != nil {
			t.Fatalf("shouldBootstrapUsers() error = %v", err)
		}
		if !enabled {
			t.Fatal("shouldBootstrapUsers() = false, want true in development")
		}
	})

	t.Run("production skips when no passwords are provided", func(t *testing.T) {
		enabled, err := shouldBootstrapUsers(false)
		if err != nil {
			t.Fatalf("shouldBootstrapUsers() error = %v", err)
		}
		if enabled {
			t.Fatal("shouldBootstrapUsers() = true, want false without explicit passwords")
		}
	})

	t.Run("production rejects partial password configuration", func(t *testing.T) {
		t.Setenv(EnvAdminPassword, "only-one")

		enabled, err := shouldBootstrapUsers(false)
		if err == nil {
			t.Fatal("shouldBootstrapUsers() expected error for partial configuration")
		}
		if enabled {
			t.Fatal("shouldBootstrapUsers() = true, want false for partial configuration")
		}
	})

	t.Run("production allows bootstrap when all passwords are present", func(t *testing.T) {
		t.Setenv(EnvSuperuserPassword, "super-secret")
		t.Setenv(EnvAdminPassword, "admin-secret")
		t.Setenv(EnvDeveloperPassword, "developer-secret")

		enabled, err := shouldBootstrapUsers(false)
		if err != nil {
			t.Fatalf("shouldBootstrapUsers() error = %v", err)
		}
		if !enabled {
			t.Fatal("shouldBootstrapUsers() = false, want true with explicit passwords")
		}
	})
}
