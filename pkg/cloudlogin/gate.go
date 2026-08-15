// Package cloudlogin is a thin compatibility shim around the shared
// [goCloudlogin] package in kombify-go-common.
//
// Migration history: this package was the original donor of cloudlogin
// (lifted 2026-05-03 as part of the kombify auth standardization). It now
// re-exports the upstream surface so existing TechStack import paths
// (`pkg/cloudlogin`) keep working without behavioral change.
//
// New code SHOULD import `github.com/kombifyio/go-common/cloudlogin`
// directly. This shim will be removed once all call sites have been
// migrated; track the cleanup in Beads.
package cloudlogin

import (
	goCloudlogin "github.com/kombifyio/go-common/cloudlogin"

	"github.com/kombifyio/techstack/pkg/config"
)

// FeatureKey identifies the enrollment claim feature this gate consumes.
const FeatureKey = goCloudlogin.FeatureKey

// DefaultAudience preserves TechStack's historic audience constant
// ("kombify-techstack:selfhosted-cloud-login") instead of the more generic
// kombify default. Existing enrollment tokens continue to verify because
// OptionsFromEnv stamps this value into ExpectedAudience when the env
// override is missing.
const DefaultAudience = "kombify-techstack:selfhosted-cloud-login"

// Type aliases — Options and Result are upstream structs.
type (
	Options = goCloudlogin.Options
	Result  = goCloudlogin.Result
)

// Evaluate is re-exported as a function value so callers that previously
// took a function reference (e.g. for tests) continue to compile.
var Evaluate = goCloudlogin.Evaluate

// OptionsFromEnv builds gate options from the documented TECHSTACK_* env
// variables and overlays the deployment mode booleans expected by the
// upstream evaluator. The signature is preserved from the donor so call
// sites in `cmd/techstack` and `internal/routes/auth` need no changes.
func OptionsFromEnv(mode config.DeploymentMode) Options {
	opts := goCloudlogin.OptionsFromEnv("TECHSTACK")
	opts.IsSaaS = mode.IsSaaS()
	opts.IsSelfHosted = mode.IsSelfHosted()
	if opts.ExpectedAudience == "" {
		opts.ExpectedAudience = DefaultAudience
	}
	return opts
}
