package specv2

import "testing"

// A managed Cloud stack authors under a stand-in zone; only a route whose host
// is its own serviceRef in that zone returns to the platform address.
func TestRebaseAuthoredRoutesKeepsPlatformAddressing(t *testing.T) {
	route := func(service, host string) map[string]map[string]any {
		return map[string]map[string]any{"vault": {"vault-initial-https": map[string]any{"serviceRef": service, "host": host}}}
	}
	routes := route("vault", "vault."+authoringInstallZone)
	if err := rebaseAuthoredRoutes(routes, authoringInstallZone, platformAddressBase); err != nil {
		t.Fatalf("rebase: %v", err)
	}
	if host := routes["vault"]["vault-initial-https"].(map[string]any)["host"]; host != "vault.kombify.me" {
		t.Fatalf("host = %v, want vault.kombify.me", host)
	}
	for name, host := range map[string]string{
		"other zone":         "vault.example.com",
		"bare zone":          authoringInstallZone,
		"nested label":       "a.vault." + authoringInstallZone,
		"another service":    "id." + authoringInstallZone,
		"shared parent only": "vault.kombify.me",
	} {
		if err := rebaseAuthoredRoutes(route("vault", host), authoringInstallZone, platformAddressBase); err == nil {
			t.Errorf("%s: host %q was rebased, want refusal", name, host)
		}
	}
}
