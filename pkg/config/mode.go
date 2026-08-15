// Package config — deployment mode configuration.
package config

// DeploymentMode determines whether kombifyTechstack runs as a self-hosted instance
// or as part of the kombify SaaS platform.
type DeploymentMode string

const (
	// ModeSelfHosted is the default: single-user, PocketBase auth, no Edge gateway.
	ModeSelfHosted DeploymentMode = "self-hosted"

	// ModeSaaS enables multi-tenant mode: hosted cloud auth via Edge, tenant isolation,
	// SSO bridge, and platform feature flags.
	ModeSaaS DeploymentMode = "saas"
)

// Edition identifies the product lane that owns boot-time behavior.
type Edition string

const (
	// EditionSelfHostOSS is the public, self-hostable OSS lane.
	EditionSelfHostOSS Edition = "selfhost-oss"
	// EditionPreview is the SaaS preview/staging lane.
	EditionPreview Edition = "preview"
	// EditionSaaSStandalone is the hosted TechStack SaaS service lane.
	EditionSaaSStandalone Edition = "saas-standalone"
	// EditionSaaSEmbedded is reserved for a Cloud-owned embedded boot lane.
	EditionSaaSEmbedded Edition = "saas-embedded"
)

// IsValid returns true if the mode is a recognized deployment mode.
func (m DeploymentMode) IsValid() bool {
	return m == ModeSelfHosted || m == ModeSaaS
}

// IsSaaS returns true when running in SaaS mode.
func (m DeploymentMode) IsSaaS() bool {
	return m == ModeSaaS
}

// IsSelfHosted returns true when running in self-hosted mode.
func (m DeploymentMode) IsSelfHosted() bool {
	return m == ModeSelfHosted
}

// IsValid returns true if the edition is a recognized TechStack edition.
func (e Edition) IsValid() bool {
	switch e {
	case EditionSelfHostOSS, EditionPreview, EditionSaaSStandalone, EditionSaaSEmbedded:
		return true
	default:
		return false
	}
}

// DeploymentMode derives the legacy deployment mode from the edition.
func (e Edition) DeploymentMode() DeploymentMode {
	if e == EditionSelfHostOSS {
		return ModeSelfHosted
	}
	return ModeSaaS
}

// IsSaaS returns true when the edition belongs to a hosted Kombify SaaS lane.
func (e Edition) IsSaaS() bool {
	return e.DeploymentMode().IsSaaS()
}

// DefaultEdition returns the safe default for local and self-hosted runs.
func DefaultEdition() Edition {
	return EditionSelfHostOSS
}

// BootDeploymentModeFromEnv resolves the deployment mode from environment
// variables with the same precedence the boot configuration loader applies
// (KOMBIFY_EDITION first, legacy DEPLOYMENT_MODE second, self-host default).
// It performs pure resolution for guard decisions that must not depend on a
// loaded config file; it does not enforce the hosted-build gate, which full
// configuration loading keeps owning.
func BootDeploymentModeFromEnv() (DeploymentMode, error) {
	var c Config
	if err := c.resolveEditionValueFromEnv(); err != nil {
		return "", err
	}
	return c.DeploymentMode, nil
}

// ValidEditionValues returns the accepted KOMBIFY_EDITION values.
func ValidEditionValues() []string {
	return []string{
		string(EditionSelfHostOSS),
		string(EditionPreview),
		string(EditionSaaSStandalone),
		string(EditionSaaSEmbedded),
	}
}
