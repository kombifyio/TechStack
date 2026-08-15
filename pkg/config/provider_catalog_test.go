package config

import (
	"strings"
	"testing"
)

func TestProviderCatalogDefaultsToFailClosedControlPlane(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ProviderCatalog.Mode != ProviderCatalogControlPlane {
		t.Fatalf("provider catalog mode = %q, want %q", cfg.ProviderCatalog.Mode, ProviderCatalogControlPlane)
	}
}

func TestProviderCatalogAllowsExplicitSelfHostedStaticMode(t *testing.T) {
	t.Setenv(providerCatalogModeEnv, string(ProviderCatalogSelfHostedStatic))
	cfg := DefaultConfig()
	if err := cfg.loadProviderCatalogFromEnv(); err != nil {
		t.Fatalf("loadProviderCatalogFromEnv: %v", err)
	}
	if cfg.ProviderCatalog.Mode != ProviderCatalogSelfHostedStatic {
		t.Fatalf("provider catalog mode = %q, want %q", cfg.ProviderCatalog.Mode, ProviderCatalogSelfHostedStatic)
	}
}

func TestProviderCatalogRejectsStaticModeForSaaS(t *testing.T) {
	t.Setenv(providerCatalogModeEnv, string(ProviderCatalogSelfHostedStatic))
	cfg := DefaultConfig()
	cfg.DeploymentMode = ModeSaaS
	err := cfg.loadProviderCatalogFromEnv()
	if err == nil || !strings.Contains(err.Error(), "allowed only in self-hosted mode") {
		t.Fatalf("loadProviderCatalogFromEnv error = %v, want SaaS rejection", err)
	}
}

func TestProviderCatalogRejectsUnknownMode(t *testing.T) {
	t.Setenv(providerCatalogModeEnv, "fallback")
	err := DefaultConfig().loadProviderCatalogFromEnv()
	if err == nil || !strings.Contains(err.Error(), "invalid "+providerCatalogModeEnv) {
		t.Fatalf("loadProviderCatalogFromEnv error = %v, want invalid-mode error", err)
	}
}
