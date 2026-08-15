package config

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestDeploymentMode_IsValid(t *testing.T) {
	tests := []struct {
		mode DeploymentMode
		want bool
	}{
		{ModeSelfHosted, true},
		{ModeSaaS, true},
		{"self-hosted", true},
		{"saas", true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.mode.IsValid(); got != tt.want {
			t.Errorf("DeploymentMode(%q).IsValid() = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestDeploymentMode_IsSaaS(t *testing.T) {
	if !ModeSaaS.IsSaaS() {
		t.Error("ModeSaaS.IsSaaS() should be true")
	}
	if ModeSelfHosted.IsSaaS() {
		t.Error("ModeSelfHosted.IsSaaS() should be false")
	}
}

func TestDeploymentMode_IsSelfHosted(t *testing.T) {
	if !ModeSelfHosted.IsSelfHosted() {
		t.Error("ModeSelfHosted.IsSelfHosted() should be true")
	}
	if ModeSaaS.IsSelfHosted() {
		t.Error("ModeSaaS.IsSelfHosted() should be false")
	}
}

func TestDefaultConfig_DeploymentMode(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Edition != EditionSelfHostOSS {
		t.Errorf("DefaultConfig().Edition = %q, want %q", cfg.Edition, EditionSelfHostOSS)
	}
	if cfg.DeploymentMode != ModeSelfHosted {
		t.Errorf("DefaultConfig().DeploymentMode = %q, want %q", cfg.DeploymentMode, ModeSelfHosted)
	}
}

func TestLoadFromEnv_LegacyDeploymentMode(t *testing.T) {
	tests := []struct {
		env         string
		wantEdition Edition
		wantMode    DeploymentMode
		hostedGate  bool
	}{
		{"saas", EditionSaaSStandalone, ModeSaaS, true},
		{"self-hosted", EditionSelfHostOSS, ModeSelfHosted, false},
		{"invalid", EditionSelfHostOSS, ModeSelfHosted, false}, // invalid legacy value falls back to default
		{"", EditionSelfHostOSS, ModeSelfHosted, false},        // empty uses default
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("KOMBIFY_EDITION", "")
			t.Setenv("TECHSTACK_AUTH_MODE", "")
			t.Setenv("DEPLOYMENT_MODE", tt.env)
			if tt.hostedGate {
				enableHostedRuntimeGate(t, "legacy-saas-key")
			}
			cfg := DefaultConfig()
			if err := cfg.loadFromEnv(); err != nil {
				t.Fatalf("loadFromEnv() error = %v", err)
			}
			if cfg.Edition != tt.wantEdition {
				t.Errorf("With DEPLOYMENT_MODE=%q, got Edition=%q, want %q", tt.env, cfg.Edition, tt.wantEdition)
			}
			if cfg.DeploymentMode != tt.wantMode {
				t.Errorf("With DEPLOYMENT_MODE=%q, got DeploymentMode=%q, want %q", tt.env, cfg.DeploymentMode, tt.wantMode)
			}
		})
	}
}

func TestLoadFromEnv_Edition(t *testing.T) {
	tests := []struct {
		env        string
		wantMode   DeploymentMode
		hostedGate bool
	}{
		{string(EditionSelfHostOSS), ModeSelfHosted, false},
		{string(EditionPreview), ModeSaaS, true},
		{string(EditionSaaSStandalone), ModeSaaS, true},
		{string(EditionSaaSEmbedded), ModeSaaS, true},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("KOMBIFY_EDITION", tt.env)
			t.Setenv("DEPLOYMENT_MODE", "")
			if tt.hostedGate {
				enableHostedRuntimeGate(t, tt.env+"-key")
			}

			cfg := DefaultConfig()
			if err := cfg.loadFromEnv(); err != nil {
				t.Fatalf("loadFromEnv() error = %v", err)
			}
			if cfg.Edition != Edition(tt.env) {
				t.Errorf("Edition = %q, want %q", cfg.Edition, tt.env)
			}
			if cfg.DeploymentMode != tt.wantMode {
				t.Errorf("DeploymentMode = %q, want %q", cfg.DeploymentMode, tt.wantMode)
			}
		})
	}
}

func TestBootDeploymentModeFromEnv(t *testing.T) {
	tests := []struct {
		name           string
		edition        string
		deploymentMode string
		wantMode       DeploymentMode
		wantErr        bool
	}{
		{name: "default is self-hosted", wantMode: ModeSelfHosted},
		{name: "selfhost edition", edition: string(EditionSelfHostOSS), wantMode: ModeSelfHosted},
		{name: "saas edition resolves without the hosted gate", edition: string(EditionSaaSStandalone), wantMode: ModeSaaS},
		{name: "preview edition is saas", edition: string(EditionPreview), wantMode: ModeSaaS},
		{name: "legacy deployment mode", deploymentMode: "saas", wantMode: ModeSaaS},
		{name: "invalid edition fails closed", edition: "managed", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KOMBIFY_EDITION", tt.edition)
			t.Setenv("DEPLOYMENT_MODE", tt.deploymentMode)
			disableHostedRuntimeGate(t)

			mode, err := BootDeploymentModeFromEnv()
			if tt.wantErr {
				if err == nil {
					t.Fatal("BootDeploymentModeFromEnv() expected invalid edition error")
				}
				return
			}
			if err != nil {
				t.Fatalf("BootDeploymentModeFromEnv() error = %v", err)
			}
			if mode != tt.wantMode {
				t.Errorf("BootDeploymentModeFromEnv() = %q, want %q", mode, tt.wantMode)
			}
		})
	}
}

func TestLoadFromEnv_SaaSEditionRequiresHostedRuntimeGate(t *testing.T) {
	t.Run("explicit_saas_denied_for_public_build", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", string(EditionSaaSStandalone))
		t.Setenv("DEPLOYMENT_MODE", "")
		disableHostedRuntimeGate(t)

		cfg := DefaultConfig()
		err := cfg.loadFromEnv()
		if err == nil {
			t.Fatal("loadFromEnv() expected hosted build gate error")
		}
		if !strings.Contains(err.Error(), "requires a private Kombify hosted build") {
			t.Fatalf("loadFromEnv() error = %v, want hosted build gate", err)
		}
	})

	t.Run("legacy_saas_denied_for_public_build", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", "")
		t.Setenv("DEPLOYMENT_MODE", string(ModeSaaS))
		disableHostedRuntimeGate(t)

		cfg := DefaultConfig()
		err := cfg.loadFromEnv()
		if err == nil {
			t.Fatal("loadFromEnv() expected hosted build gate error")
		}
		if !strings.Contains(err.Error(), "requires a private Kombify hosted build") {
			t.Fatalf("loadFromEnv() error = %v, want hosted build gate", err)
		}
	})

	t.Run("hosted_build_still_requires_runtime_key", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", string(EditionPreview))
		t.Setenv("DEPLOYMENT_MODE", "")
		setHostedRuntimeKeyHash(t, "preview-secret")

		cfg := DefaultConfig()
		err := cfg.loadFromEnv()
		if err == nil {
			t.Fatal("loadFromEnv() expected runtime key error")
		}
		if !strings.Contains(err.Error(), hostedRuntimeKeyEnv) {
			t.Fatalf("loadFromEnv() error = %v, want missing runtime key", err)
		}
	})

	t.Run("hosted_runtime_key_must_match_build_hash", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", string(EditionPreview))
		t.Setenv("DEPLOYMENT_MODE", "")
		setHostedRuntimeKeyHash(t, "preview-secret")
		t.Setenv(hostedRuntimeKeyEnv, "wrong-secret")

		cfg := DefaultConfig()
		err := cfg.loadFromEnv()
		if err == nil {
			t.Fatal("loadFromEnv() expected runtime proof mismatch")
		}
		if !strings.Contains(err.Error(), "failed private Kombify hosted runtime proof") {
			t.Fatalf("loadFromEnv() error = %v, want runtime proof mismatch", err)
		}
	})
}

func TestLoadFromEnv_InvalidEditionFails(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "managed")

	cfg := DefaultConfig()
	if err := cfg.loadFromEnv(); err == nil {
		t.Fatal("loadFromEnv() expected invalid KOMBIFY_EDITION error")
	}
}

func TestAuthModeDoesNotPromoteEdition(t *testing.T) {
	t.Run("auth_mode_saas_keeps_default_selfhost_edition", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", "")
		t.Setenv("DEPLOYMENT_MODE", "")
		t.Setenv("TECHSTACK_AUTH_MODE", "saas")

		cfg := DefaultConfig()
		if err := cfg.loadFromEnv(); err != nil {
			t.Fatalf("loadFromEnv() error = %v", err)
		}
		if cfg.Edition != EditionSelfHostOSS {
			t.Errorf("With TECHSTACK_AUTH_MODE=saas, got Edition=%q, want %q", cfg.Edition, EditionSelfHostOSS)
		}
		if cfg.DeploymentMode != ModeSelfHosted {
			t.Errorf("With TECHSTACK_AUTH_MODE=saas, got DeploymentMode=%q, want %q", cfg.DeploymentMode, ModeSelfHosted)
		}
	})

	t.Run("auth_mode_cloud_keeps_default_selfhost_edition", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", "")
		t.Setenv("DEPLOYMENT_MODE", "")
		t.Setenv("TECHSTACK_AUTH_MODE", "cloud")

		cfg := DefaultConfig()
		if err := cfg.loadFromEnv(); err != nil {
			t.Fatalf("loadFromEnv() error = %v", err)
		}
		if cfg.Edition != EditionSelfHostOSS {
			t.Errorf("With TECHSTACK_AUTH_MODE=cloud, got Edition=%q, want %q", cfg.Edition, EditionSelfHostOSS)
		}
		if cfg.DeploymentMode != ModeSelfHosted {
			t.Errorf("With TECHSTACK_AUTH_MODE=cloud, got DeploymentMode=%q, want %q", cfg.DeploymentMode, ModeSelfHosted)
		}
	})

	t.Run("explicit_deployment_mode_not_overridden", func(t *testing.T) {
		t.Setenv("KOMBIFY_EDITION", "")
		t.Setenv("DEPLOYMENT_MODE", "self-hosted")
		t.Setenv("TECHSTACK_AUTH_MODE", "saas")

		cfg := DefaultConfig()
		if err := cfg.loadFromEnv(); err != nil {
			t.Fatalf("loadFromEnv() error = %v", err)
		}
		if cfg.Edition != EditionSelfHostOSS {
			t.Errorf("With DEPLOYMENT_MODE=self-hosted + TECHSTACK_AUTH_MODE=saas, got Edition=%q, want %q", cfg.Edition, EditionSelfHostOSS)
		}
		if cfg.DeploymentMode != ModeSelfHosted {
			t.Errorf("With DEPLOYMENT_MODE=self-hosted + TECHSTACK_AUTH_MODE=saas, got DeploymentMode=%q, want %q", cfg.DeploymentMode, ModeSelfHosted)
		}
	})
}

func enableHostedRuntimeGate(t *testing.T, key string) {
	t.Helper()
	setHostedRuntimeKeyHash(t, key)
	t.Setenv(hostedRuntimeKeyEnv, key)
}

func disableHostedRuntimeGate(t *testing.T) {
	t.Helper()
	previousHash := hostedRuntimeKeySHA256
	hostedRuntimeKeySHA256 = ""
	t.Cleanup(func() {
		hostedRuntimeKeySHA256 = previousHash
	})
}

func setHostedRuntimeKeyHash(t *testing.T, key string) {
	t.Helper()
	previousHash := hostedRuntimeKeySHA256
	sum := sha256.Sum256([]byte(key))
	hostedRuntimeKeySHA256 = hex.EncodeToString(sum[:])
	t.Cleanup(func() {
		hostedRuntimeKeySHA256 = previousHash
	})
}
