package config

import (
	"fmt"
	"os"
	"strings"
)

const providerCatalogModeEnv = "TECHSTACK_PROVIDER_CATALOG_MODE"

// ProviderCatalogMode selects the authority for managed-runtime provider
// availability. Hosted lanes always use the control-plane catalog.
type ProviderCatalogMode string

const (
	// ProviderCatalogControlPlane requires the authoritative Postgres catalog
	// and fails closed when it cannot be queried.
	ProviderCatalogControlPlane ProviderCatalogMode = "control_plane"
	// ProviderCatalogSelfHostedStatic opts a self-hosted operator into the
	// compiled provider set without a platform catalog dependency.
	ProviderCatalogSelfHostedStatic ProviderCatalogMode = "self_hosted_static"
)

// ProviderCatalogConfig controls provider availability authority.
type ProviderCatalogConfig struct {
	Mode ProviderCatalogMode `yaml:"mode"`
}

// DefaultProviderCatalogConfig returns the fail-closed authoritative mode.
func DefaultProviderCatalogConfig() ProviderCatalogConfig {
	return ProviderCatalogConfig{Mode: ProviderCatalogControlPlane}
}

// IsValid reports whether mode names an admitted catalog authority.
func (m ProviderCatalogMode) IsValid() bool {
	return m == ProviderCatalogControlPlane || m == ProviderCatalogSelfHostedStatic
}

func (c *Config) loadProviderCatalogFromEnv() error {
	if c == nil {
		return fmt.Errorf("provider catalog config is required")
	}
	if raw := strings.TrimSpace(os.Getenv(providerCatalogModeEnv)); raw != "" {
		c.ProviderCatalog.Mode = ProviderCatalogMode(raw)
	}
	if strings.TrimSpace(string(c.ProviderCatalog.Mode)) == "" {
		c.ProviderCatalog = DefaultProviderCatalogConfig()
	}
	if !c.ProviderCatalog.Mode.IsValid() {
		return fmt.Errorf("invalid %s %q (valid: %s, %s)", providerCatalogModeEnv, c.ProviderCatalog.Mode, ProviderCatalogControlPlane, ProviderCatalogSelfHostedStatic)
	}
	if c.ProviderCatalog.Mode == ProviderCatalogSelfHostedStatic && !c.DeploymentMode.IsSelfHosted() {
		return fmt.Errorf("%s=%s is allowed only in self-hosted mode", providerCatalogModeEnv, ProviderCatalogSelfHostedStatic)
	}
	return nil
}
