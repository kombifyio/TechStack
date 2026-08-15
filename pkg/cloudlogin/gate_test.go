package cloudlogin

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
)

func TestOptionsFromEnvAppliesTechStackAudience(t *testing.T) {
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.example.com/")

	opts := OptionsFromEnv(config.ModeSelfHosted)

	if !opts.IsSelfHosted || opts.IsSaaS {
		t.Fatalf("mode flags = selfHosted:%v saas:%v", opts.IsSelfHosted, opts.IsSaaS)
	}
	if opts.PublicOrigin != "https://techstack.example.com" {
		t.Fatalf("PublicOrigin = %q", opts.PublicOrigin)
	}
	if opts.ExpectedAudience != DefaultAudience {
		t.Fatalf("ExpectedAudience = %q, want %q", opts.ExpectedAudience, DefaultAudience)
	}
}

func TestOptionsFromEnvSaaSModeOverridesEnvMode(t *testing.T) {
	t.Setenv("TECHSTACK_DEPLOYMENT_MODE", "self-hosted")

	opts := OptionsFromEnv(config.ModeSaaS)

	if !opts.IsSaaS || opts.IsSelfHosted {
		t.Fatalf("mode flags = selfHosted:%v saas:%v", opts.IsSelfHosted, opts.IsSaaS)
	}
}
