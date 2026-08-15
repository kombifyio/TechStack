package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnvFloat(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		current  float64
		expected float64
	}{
		{
			name:     "valid float",
			envValue: "15.5",
			current:  10.0,
			expected: 15.5,
		},
		{
			name:     "integer as float",
			envValue: "20",
			current:  10.0,
			expected: 20.0,
		},
		{
			name:     "invalid float",
			envValue: "invalid",
			current:  10.0,
			expected: 10.0, // should use current
		},
		{
			name:     "zero value",
			envValue: "0",
			current:  10.0,
			expected: 10.0, // should use current (0 is not positive)
		},
		{
			name:     "negative value",
			envValue: "-5",
			current:  10.0,
			expected: 10.0, // should use current (negative not allowed)
		},
		{
			name:     "empty string",
			envValue: "",
			current:  10.0,
			expected: 10.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_FLOAT"
			if tt.envValue != "" {
				os.Setenv(key, tt.envValue)
				defer os.Unsetenv(key)
			} else {
				os.Unsetenv(key)
			}

			result := envFloat(key, tt.current)
			if result != tt.expected {
				t.Errorf("envFloat(%q, %f) = %f, want %f", tt.envValue, tt.current, result, tt.expected)
			}
		})
	}
}

func TestEnvInt(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		current  int
		expected int
	}{
		{
			name:     "valid int",
			envValue: "25",
			current:  20,
			expected: 25,
		},
		{
			name:     "invalid int",
			envValue: "invalid",
			current:  20,
			expected: 20, // should use current
		},
		{
			name:     "zero value",
			envValue: "0",
			current:  20,
			expected: 20, // should use current (0 is not positive)
		},
		{
			name:     "negative value",
			envValue: "-5",
			current:  20,
			expected: 20, // should use current (negative not allowed)
		},
		{
			name:     "float value",
			envValue: "25.5",
			current:  20,
			expected: 25, // Sscanf %d parses the integer part
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_INT"
			if tt.envValue != "" {
				os.Setenv(key, tt.envValue)
				defer os.Unsetenv(key)
			} else {
				os.Unsetenv(key)
			}

			result := envInt(key, tt.current)
			if result != tt.expected {
				t.Errorf("envInt(%q, %d) = %d, want %d", tt.envValue, tt.current, result, tt.expected)
			}
		})
	}
}

func TestRateLimitEnvOverride(t *testing.T) {
	// Set environment variables
	os.Setenv("TECHSTACK_RATE_LIMIT_RPS", "25.5")
	os.Setenv("TECHSTACK_RATE_LIMIT_BURST", "50")
	defer func() {
		os.Unsetenv("TECHSTACK_RATE_LIMIT_RPS")
		os.Unsetenv("TECHSTACK_RATE_LIMIT_BURST")
	}()

	// Load config (will apply env overrides)
	cfg := DefaultConfig()
	if err := cfg.loadFromEnv(); err != nil {
		t.Fatalf("loadFromEnv() error = %v", err)
	}

	if cfg.Server.RateLimitRPS != 25.5 {
		t.Errorf("Expected RateLimitRPS 25.5, got %f", cfg.Server.RateLimitRPS)
	}
	if cfg.Server.RateLimitBurst != 50 {
		t.Errorf("Expected RateLimitBurst 50, got %d", cfg.Server.RateLimitBurst)
	}
}

func TestMonitoringEnvOverride(t *testing.T) {
	t.Setenv("TECHSTACK_MONITOR_QUERY_BACKEND_URL", "https://victoriametrics.internal/select/0/prometheus")
	t.Setenv("TECHSTACK_MONITOR_COLLECTOR_MODE", "gateway")
	t.Setenv("TECHSTACK_ALERT_INTERVAL", "5s")
	t.Setenv("TECHSTACK_MONITOR_INGEST_FRESHNESS_TTL", "45s")
	t.Setenv("TECHSTACK_MONITOR_OTLP_REQUIREMENT", "optional")
	t.Setenv("TECHSTACK_MONITOR_LEGACY_REQUIREMENT", "disabled")

	cfg := DefaultConfig()
	if err := cfg.loadFromEnv(); err != nil {
		t.Fatalf("loadFromEnv() error = %v", err)
	}

	if cfg.Monitoring.QueryBackendURL != "https://victoriametrics.internal/select/0/prometheus" {
		t.Fatalf("expected monitor query backend URL override, got %q", cfg.Monitoring.QueryBackendURL)
	}
	if cfg.Monitoring.CollectorMode != "gateway" {
		t.Fatalf("expected monitor collector mode gateway, got %q", cfg.Monitoring.CollectorMode)
	}
	if cfg.Monitoring.AlertIntervalDuration() != 5*time.Second {
		t.Fatalf("expected alert interval override 5s, got %s", cfg.Monitoring.AlertIntervalDuration())
	}
	if cfg.Monitoring.IngestFreshnessTTLDuration() != 45*time.Second {
		t.Fatalf("expected ingest freshness override 45s, got %s", cfg.Monitoring.IngestFreshnessTTLDuration())
	}
	if cfg.Monitoring.OTLPLaneRequirement != "optional" || cfg.Monitoring.LegacyPushLaneRequirement != "disabled" {
		t.Fatalf("unexpected lane requirements: otlp=%q legacy=%q", cfg.Monitoring.OTLPLaneRequirement, cfg.Monitoring.LegacyPushLaneRequirement)
	}
}

func TestBackupDirEnvOverrideIsPreserved(t *testing.T) {
	t.Setenv("TECHSTACK_DATA_DIR", "/data")
	t.Setenv("TECHSTACK_BACKUP_DIR", "/custom/backups")

	cfg := DefaultConfig()
	if err := cfg.loadFromEnv(); err != nil {
		t.Fatalf("loadFromEnv() error = %v", err)
	}

	if got := cfg.Backup.BackupDir; got != "/custom/backups" {
		t.Fatalf("BackupDir override = %q, want /custom/backups", got)
	}
}

// TestIsValidOrigin tests the CORS origin validation helper function.
func TestIsValidOrigin(t *testing.T) {
	tests := []struct {
		name     string
		origin   string
		expected bool
	}{
		{
			name:     "valid http origin",
			origin:   "http://localhost:5173",
			expected: true,
		},
		{
			name:     "valid https origin",
			origin:   "https://example.com",
			expected: true,
		},
		{
			name:     "valid https with port",
			origin:   "https://app.example.com:8080",
			expected: true,
		},
		{
			name:     "invalid - no protocol",
			origin:   "localhost:5173",
			expected: false,
		},
		{
			name:     "invalid - too short",
			origin:   "http:/",
			expected: false,
		},
		{
			name:     "invalid - missing host",
			origin:   "http://",
			expected: false,
		},
		{
			name:     "invalid - seven character https prefix",
			origin:   "https:/",
			expected: false,
		},
		{
			name:     "invalid - origin must not include path",
			origin:   "https://app.example.com/callback",
			expected: false,
		},
		{
			name:     "invalid - empty string",
			origin:   "",
			expected: false,
		},
		{
			name:     "invalid - just protocol",
			origin:   "https:",
			expected: false,
		},
		{
			name:     "invalid - ftp protocol",
			origin:   "ftp://files.example.com",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidOrigin(tt.origin)
			if result != tt.expected {
				t.Errorf("isValidOrigin(%q) = %v, want %v", tt.origin, result, tt.expected)
			}
		})
	}
}

// TestIsProduction tests the production environment detection.
func TestIsProduction(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		expected    bool
	}{
		{
			name:        "production lowercase",
			environment: "production",
			expected:    true,
		},
		{
			name:        "prod shorthand",
			environment: "prod",
			expected:    true,
		},
		{
			name:        "development",
			environment: "development",
			expected:    false,
		},
		{
			name:        "staging",
			environment: "staging",
			expected:    false,
		},
		{
			name:        "empty string",
			environment: "",
			expected:    false,
		},
		{
			name:        "PRODUCTION uppercase",
			environment: "PRODUCTION",
			expected:    false, // case sensitive
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Server.Environment = tt.environment
			result := cfg.IsProduction()
			if result != tt.expected {
				t.Errorf("IsProduction() with env %q = %v, want %v", tt.environment, result, tt.expected)
			}
		})
	}
}

func TestIsLocal(t *testing.T) {
	tests := []struct {
		environment string
		expected    bool
	}{
		{"local", true},
		{"production", false},
		{"development", false},
		{"staging", false},
		{"", false},
		{"LOCAL", false}, // case sensitive, like IsProduction
	}

	for _, tt := range tests {
		t.Run("env_"+tt.environment, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Server.Environment = tt.environment
			if got := cfg.IsLocal(); got != tt.expected {
				t.Errorf("IsLocal() with env %q = %v, want %v", tt.environment, got, tt.expected)
			}
		})
	}
}

func TestValidateLocalPosture(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		listenAddr  string
		grpcAddr    string
		wantErr     bool
	}{
		{"non-local ignores listen addr", "production", ":5260", ":5263", false},
		{"local loopback ipv4", "local", "127.0.0.1:5260", "127.0.0.1:5263", false},
		{"local loopback ipv6", "local", "[::1]:5260", "[::1]:5263", false},
		{"local localhost", "local", "localhost:5260", "localhost:5263", false},
		{"local all-interfaces default rejected", "local", ":5260", ":5263", true},
		{"local 0.0.0.0 rejected", "local", "0.0.0.0:5260", "127.0.0.1:5263", true},
		{"local lan address rejected", "local", "192.168.1.10:5260", "127.0.0.1:5263", true},
		{"local non-loopback grpc rejected", "local", "127.0.0.1:5260", "0.0.0.0:5263", true},
		{"local missing port rejected", "local", "127.0.0.1", "127.0.0.1:5263", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Server.Environment = tt.environment
			cfg.Server.ListenAddr = tt.listenAddr
			cfg.Server.GRPCAddr = tt.grpcAddr
			err := cfg.ValidateLocalPosture()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLocalPosture() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateCORSOrigins tests CORS origin validation and warning generation.
func TestValidateCORSOrigins(t *testing.T) {
	tests := []struct {
		name            string
		origins         []string
		environment     string
		expectedOrigins []string
		expectWarnings  bool
	}{
		{
			name:            "valid origins in development",
			origins:         []string{"http://localhost:5173", "https://app.example.com"},
			environment:     "development",
			expectedOrigins: []string{"http://localhost:5173", "https://app.example.com"},
			expectWarnings:  false,
		},
		{
			name:            "wildcard in development",
			origins:         []string{"*"},
			environment:     "development",
			expectedOrigins: []string{"*"},
			expectWarnings:  true, // warning but allowed
		},
		{
			name:            "wildcard in production",
			origins:         []string{"*"},
			environment:     "production",
			expectedOrigins: []string{}, // cleared for security
			expectWarnings:  true,
		},
		{
			name:            "wildcard in prod shorthand",
			origins:         []string{"*", "http://localhost:5173"},
			environment:     "prod",
			expectedOrigins: []string{}, // cleared for security
			expectWarnings:  true,
		},
		{
			name:            "invalid origin format",
			origins:         []string{"localhost:5173"},
			environment:     "development",
			expectedOrigins: []string{"localhost:5173"},
			expectWarnings:  true,
		},
		{
			name:            "invalid short origin format",
			origins:         []string{"https:/"},
			environment:     "development",
			expectedOrigins: []string{"https:/"},
			expectWarnings:  true,
		},
		{
			name:            "mixed valid and invalid",
			origins:         []string{"http://localhost:5173", "invalid-origin"},
			environment:     "development",
			expectedOrigins: []string{"http://localhost:5173", "invalid-origin"},
			expectWarnings:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Server.CORSOrigins = tt.origins
			cfg.Server.Environment = tt.environment

			origins, warnings := cfg.ValidateCORSOrigins()

			// Check origins count
			if len(origins) != len(tt.expectedOrigins) {
				t.Errorf("ValidateCORSOrigins() returned %d origins, want %d", len(origins), len(tt.expectedOrigins))
			}

			// Check warnings presence
			hasWarnings := len(warnings) > 0
			if hasWarnings != tt.expectWarnings {
				t.Errorf("ValidateCORSOrigins() hasWarnings = %v, want %v", hasWarnings, tt.expectWarnings)
			}
		})
	}
}

func TestSplitOriginListAcceptsCommaAndWhitespace(t *testing.T) {
	got := SplitOriginList("https://kombify.io, https://admin.kombify.io https://kombify.dev")
	want := []string{"https://kombify.io", "https://admin.kombify.io", "https://kombify.dev"}
	if len(got) != len(want) {
		t.Fatalf("SplitOriginList() len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitOriginList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateCORSOriginsIncludesAllowedFrameOrigins(t *testing.T) {
	t.Setenv(AllowedFrameOriginsEnv, "https://kombify.io https://admin.kombify.io")

	cfg := DefaultConfig()
	cfg.DeploymentMode = ModeSaaS
	cfg.Server.Environment = "production"
	cfg.Server.CORSOrigins = []string{"https://techstack.kombify.io"}

	origins, warnings := cfg.ValidateCORSOrigins()
	if len(warnings) != 0 {
		t.Fatalf("ValidateCORSOrigins() warnings = %#v, want none", warnings)
	}

	for _, want := range []string{
		"https://techstack.kombify.io",
		"https://kombify.io",
		"https://app.kombify.io",
		"https://admin.kombify.io",
		"https://kombify.dev",
		"https://app.kombify.dev",
	} {
		if !containsString(origins, want) {
			t.Fatalf("ValidateCORSOrigins() missing %q in %#v", want, origins)
		}
	}
}

func TestValidateCORSOriginsClearsProductionWildcardButKeepsFrameOrigins(t *testing.T) {
	t.Setenv(AllowedFrameOriginsEnv, "https://kombify.space")

	cfg := DefaultConfig()
	cfg.Server.Environment = "production"
	cfg.Server.CORSOrigins = []string{"*"}

	origins, warnings := cfg.ValidateCORSOrigins()
	if len(warnings) == 0 {
		t.Fatal("ValidateCORSOrigins() expected wildcard warning")
	}
	if len(origins) != 1 || origins[0] != "https://kombify.space" {
		t.Fatalf("ValidateCORSOrigins() origins = %#v, want allowed frame origin only", origins)
	}
}

func TestInferredSaaSFrameOriginsByHost(t *testing.T) {
	got := InferredSaaSFrameOrigins("techstack.kombify.io:443", ModeSaaS)
	if len(got) != 2 || got[0] != "https://kombify.io" || got[1] != "https://app.kombify.io" {
		t.Fatalf("InferredSaaSFrameOrigins(io) = %#v", got)
	}

	got = InferredSaaSFrameOrigins("techstack.kombify.dev", ModeSaaS)
	if len(got) != 2 || got[0] != "https://kombify.dev" || got[1] != "https://app.kombify.dev" {
		t.Fatalf("InferredSaaSFrameOrigins(dev) = %#v", got)
	}

	if got := InferredSaaSFrameOrigins("techstack.example.com", ModeSelfHosted); len(got) != 0 {
		t.Fatalf("InferredSaaSFrameOrigins(self-hosted) = %#v, want empty", got)
	}
}

func TestPublicOriginFromEnvSanitizesHostedOrigin(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.kombify.io/")
	t.Setenv("APP_URL", "https://ignored.example")

	if got, want := PublicOriginFromEnv(), "https://techstack.kombify.io"; got != want {
		t.Fatalf("PublicOriginFromEnv() = %q, want %q", got, want)
	}
}

// TestLoadFromFile tests loading configuration from a YAML file.
func TestLoadFromFile(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir := t.TempDir()

	t.Run("valid config file", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, "valid_config.yaml")
		configContent := `
server:
  listen_addr: ":8080"
  grpc_addr: ":8081"
  environment: staging
database:
  path: "/custom/path/db.sqlite"
logging:
  level: debug
  format: text
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to create test config file: %v", err)
		}

		cfg := DefaultConfig()
		if err := cfg.loadFromFile(configPath); err != nil {
			t.Fatalf("loadFromFile() error = %v", err)
		}

		if cfg.Server.ListenAddr != ":8080" {
			t.Errorf("ListenAddr = %v, want %v", cfg.Server.ListenAddr, ":8080")
		}
		if cfg.Server.GRPCAddr != ":8081" {
			t.Errorf("GRPCAddr = %v, want %v", cfg.Server.GRPCAddr, ":8081")
		}
		if cfg.Server.Environment != "staging" {
			t.Errorf("Environment = %v, want %v", cfg.Server.Environment, "staging")
		}
		if cfg.Database.Path != "/custom/path/db.sqlite" {
			t.Errorf("Database.Path = %v, want %v", cfg.Database.Path, "/custom/path/db.sqlite")
		}
		if cfg.Logging.Level != "debug" {
			t.Errorf("Logging.Level = %v, want %v", cfg.Logging.Level, "debug")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		cfg := DefaultConfig()
		err := cfg.loadFromFile(filepath.Join(tmpDir, "non_existent.yaml"))
		if err == nil {
			t.Error("loadFromFile() expected error for non-existent file, got nil")
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, "invalid_config.yaml")
		invalidContent := `
server:
  listen_addr: ":8080"
  invalid_yaml: [
`
		if err := os.WriteFile(configPath, []byte(invalidContent), 0644); err != nil {
			t.Fatalf("Failed to create test config file: %v", err)
		}

		cfg := DefaultConfig()
		err := cfg.loadFromFile(configPath)
		if err == nil {
			t.Error("loadFromFile() expected error for invalid YAML, got nil")
		}
	})
}

// TestLoad tests the main Load function with file and env overrides.
func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("load with explicit path", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, "config.yaml")
		configContent := `
server:
  listen_addr: ":9000"
  environment: testing
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to create test config file: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		if cfg.Server.ListenAddr != ":9000" {
			t.Errorf("ListenAddr = %v, want %v", cfg.Server.ListenAddr, ":9000")
		}
	})

	t.Run("load with non-existent explicit path", func(t *testing.T) {
		_, err := Load(filepath.Join(tmpDir, "does_not_exist.yaml"))
		if err == nil {
			t.Error("Load() expected error for non-existent file, got nil")
		}
	})

	t.Run("load with empty path uses defaults", func(t *testing.T) {
		// Clear env vars that might affect test
		envVars := []string{
			"TECHSTACK_LISTEN_ADDR",
			"TECHSTACK_GRPC_ADDR",
			"TECHSTACK_ENV",
		}
		for _, v := range envVars {
			os.Unsetenv(v)
		}

		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		// Should use defaults since no config file exists
		if cfg.Server.ListenAddr != ":5260" {
			t.Errorf("ListenAddr = %v, want %v", cfg.Server.ListenAddr, ":5260")
		}
	})

	t.Run("env vars override file config", func(t *testing.T) {
		configPath := filepath.Join(tmpDir, "override_config.yaml")
		configContent := `
server:
  listen_addr: ":9000"
  environment: testing
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to create test config file: %v", err)
		}

		// Set env var to override
		os.Setenv("TECHSTACK_LISTEN_ADDR", ":7777")
		defer os.Unsetenv("TECHSTACK_LISTEN_ADDR")

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}

		// Env var should override file
		if cfg.Server.ListenAddr != ":7777" {
			t.Errorf("ListenAddr = %v, want %v (env override)", cfg.Server.ListenAddr, ":7777")
		}
	})
}

// TestEnvStr tests the envStr helper function.
func TestEnvStr(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		current  string
		expected string
	}{
		{
			name:     "env var set",
			envValue: "new-value",
			current:  "old-value",
			expected: "new-value",
		},
		{
			name:     "env var empty",
			envValue: "",
			current:  "old-value",
			expected: "old-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ENV_STR"
			if tt.envValue != "" {
				os.Setenv(key, tt.envValue)
				defer os.Unsetenv(key)
			} else {
				os.Unsetenv(key)
			}

			result := envStr(key, tt.current)
			if result != tt.expected {
				t.Errorf("envStr(%q, %q) = %q, want %q", tt.envValue, tt.current, result, tt.expected)
			}
		})
	}
}

// TestLoadFromEnvSimulationProviders removed (cleanup plan 2026-04-27 phase 3.1):
// simulation providers, KombiSim env vars, and VM_ENGINE_URL no longer exist.

// TestLoadFromEnvCorsOrigins tests env var loading for CORS origins.
func TestLoadFromEnvCorsOrigins(t *testing.T) {
	defer os.Unsetenv("TECHSTACK_CORS_ORIGINS")

	t.Run("cors origins from env", func(t *testing.T) {
		os.Setenv("TECHSTACK_CORS_ORIGINS", "http://app1.com,http://app2.com")
		defer os.Unsetenv("TECHSTACK_CORS_ORIGINS")

		cfg := DefaultConfig()
		if err := cfg.loadFromEnv(); err != nil {
			t.Fatalf("loadFromEnv() error = %v", err)
		}

		if len(cfg.Server.CORSOrigins) != 2 {
			t.Errorf("CORSOrigins count = %d, want 2", len(cfg.Server.CORSOrigins))
		}
		if cfg.Server.CORSOrigins[0] != "http://app1.com" {
			t.Errorf("CORSOrigins[0] = %v, want %v", cfg.Server.CORSOrigins[0], "http://app1.com")
		}
	})
}
