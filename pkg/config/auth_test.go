package config

import "testing"

func TestNormalizeCloudAuthIssuerUsesLoginKombifyCustomDomain(t *testing.T) {
	for _, raw := range []string{
		"kombify.eu.auth0.com",
		"https://kombify.eu.auth0.com/",
	} {
		t.Run(raw, func(t *testing.T) {
			if got := NormalizeCloudAuthIssuer(raw); got != DefaultCloudAuthIssuer {
				t.Fatalf("NormalizeCloudAuthIssuer(%q) = %q, want %q", raw, got, DefaultCloudAuthIssuer)
			}
		})
	}
}

func TestNormalizeCloudAuthIssuerKeepsCustomIssuer(t *testing.T) {
	if got, want := NormalizeCloudAuthIssuer("login.example.com/"), "https://login.example.com"; got != want {
		t.Fatalf("NormalizeCloudAuthIssuer() = %q, want %q", got, want)
	}
}
