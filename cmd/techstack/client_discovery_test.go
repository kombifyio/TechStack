package main

import (
	"testing"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/instance"
	"github.com/kombifyio/techstack/pkg/v2/auth/providers"
)

func TestTechstackClientConnectionProfileConfigDefaultsOSSWithoutPublicOriginToLocal(t *testing.T) {
	clearClientDiscoveryEnv(t)
	cfg := config.DefaultConfig()
	profileCfg := techstackClientConnectionProfileConfig(cfg, instance.Identity{ID: clientDiscoveryTestIdentity})
	if profileCfg.DeploymentMode != clientDiscoveryModeLocal || profileCfg.BaseURL != "" || profileCfg.InstanceID != clientDiscoveryTestIdentity {
		t.Fatalf("profile config = %#v", profileCfg)
	}
}

func TestTechstackClientConnectionProfileConfigRequiresExplicitSelfHostedNativeOIDC(t *testing.T) {
	clearClientDiscoveryEnv(t)
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "https://techstack.home.example/")
	t.Setenv(clientDiscoveryIssuerEnv, "https://id.home.example/")
	t.Setenv(clientDiscoveryClientIDEnv, "techstack-native-public")
	t.Setenv(clientDiscoveryAudienceEnv, "https://techstack.home.example/api")
	t.Setenv(clientDiscoveryScopesEnv, "openid, profile offline_access")
	t.Setenv(clientDiscoveryFlowEnv, "authorization_code_pkce")

	profileCfg := techstackClientConnectionProfileConfig(config.DefaultConfig(), instance.Identity{ID: clientDiscoveryTestIdentity})
	if profileCfg.DeploymentMode != clientDiscoveryModeSelfHost || profileCfg.BaseURL != "https://techstack.home.example" {
		t.Fatalf("profile config = %#v", profileCfg)
	}
	if profileCfg.OIDCIssuer != "https://id.home.example/" || profileCfg.OIDCClientID != "techstack-native-public" || profileCfg.OIDCFlow != "authorization_code_pkce" {
		t.Fatalf("OIDC config = %#v", profileCfg)
	}
	if len(profileCfg.OIDCScopes) != 3 || profileCfg.OIDCScopes[2] != "offline_access" {
		t.Fatalf("scopes = %#v", profileCfg.OIDCScopes)
	}
}

func TestTechstackClientConnectionProfileConfigLeavesSaaSForCloudOrigin(t *testing.T) {
	clearClientDiscoveryEnv(t)
	t.Setenv(clientDiscoveryModeEnv, clientDiscoveryModeLocal)
	t.Setenv(clientDiscoveryBaseURLEnv, "http://127.0.0.1:5260")
	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSaaS
	profileCfg := techstackClientConnectionProfileConfig(cfg, instance.Identity{ID: clientDiscoveryTestIdentity})
	if profileCfg.DeploymentMode != "" {
		t.Fatalf("SaaS TechStack must not publish a competing Cloud profile: %#v", profileCfg)
	}
}

func TestTechstackClientConnectionProfileConfigPreservesInvalidRemoteOriginForFailClosedValidation(t *testing.T) {
	clearClientDiscoveryEnv(t)
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "not-a-public-url")
	profileCfg := techstackClientConnectionProfileConfig(config.DefaultConfig(), instance.Identity{ID: clientDiscoveryTestIdentity})
	if profileCfg.DeploymentMode != clientDiscoveryModeSelfHost || profileCfg.BaseURL != "not-a-public-url" {
		t.Fatalf("invalid configured remote origin must not silently downgrade to local: %#v", profileCfg)
	}
}

func TestTechstackClientPairingRouteConfigProjectsTenantAndTLSBindingWithoutInventingStore(t *testing.T) {
	clearClientDiscoveryEnv(t)
	t.Setenv(clientTLSFingerprintEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	profile := techstackClientConnectionProfileConfig(config.DefaultConfig(), instance.Identity{ID: clientDiscoveryTestIdentity})
	pairing := techstackClientPairingRouteConfig(profile, &v2Boot{defaultTenant: "tenant-home"})
	if pairing.Profile.InstanceID != clientDiscoveryTestIdentity || pairing.DefaultTenantID != "tenant-home" {
		t.Fatalf("pairing config = %#v", pairing)
	}
	if pairing.TLSFingerprintSHA256 != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("TLS fingerprint = %q", pairing.TLSFingerprintSHA256)
	}
	if pairing.Store != nil {
		t.Fatal("pairing store must remain unavailable until Postgres is wired")
	}
}

func TestClientPairingOIDCAuthorityRequiresMatchingConfiguredIssuer(t *testing.T) {
	registry := providers.NewRegistry()
	provider, err := providers.New(providers.Config{
		ID: "primary", Kind: providers.KindGeneric,
		Issuer: "https://id.home.example/oidc/", ClientID: "techstack-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.Add(provider)
	profile := routes.ClientConnectionProfileConfig{OIDCIssuer: "https://id.home.example/oidc"}
	if !clientPairingOIDCAuthorityMatches(profile, registry) {
		t.Fatal("matching issuer with trailing-slash normalization was rejected")
	}
	profile.OIDCIssuer = "https://different.example/oidc"
	if clientPairingOIDCAuthorityMatches(profile, registry) {
		t.Fatal("pairing accepted an OIDC issuer not present in the runtime registry")
	}
}

const clientDiscoveryTestIdentity = "11111111-2222-4333-8444-555555555555"

func clearClientDiscoveryEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		clientDiscoveryModeEnv,
		clientDiscoveryBaseURLEnv,
		clientDiscoveryIssuerEnv,
		clientDiscoveryClientIDEnv,
		clientDiscoveryAudienceEnv,
		clientDiscoveryScopesEnv,
		clientDiscoveryFlowEnv,
		clientTLSFingerprintEnv,
		"TECHSTACK_PUBLIC_ORIGIN",
		"PUBLIC_ORIGIN",
		"APP_PUBLIC_ORIGIN",
		"APP_URL",
	} {
		t.Setenv(key, "")
	}
}
