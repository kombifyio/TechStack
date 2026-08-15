package providers

import (
	"strings"
	"testing"

	commonoidc "github.com/kombifyio/go-common/oidcclient"
)

func TestNewValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing id", Config{Kind: KindPocketID, Issuer: "https://i", ClientID: "a"}},
		{"missing issuer", Config{ID: "p", Kind: KindPocketID, ClientID: "a"}},
		{"missing client id", Config{ID: "p", Kind: KindPocketID, Issuer: "https://i"}},
		{"unsupported kind", Config{ID: "p", Kind: "saml", Issuer: "https://i", ClientID: "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.cfg); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestNewDerivesJWKSURL(t *testing.T) {
	p, err := New(Config{ID: "primary", Kind: KindPocketID, Issuer: "https://id.example/", ClientID: "techstack"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "primary" || p.Kind() != KindPocketID {
		t.Fatalf("unexpected: %+v", p)
	}
	if got := p.Issuer(); got != "https://id.example/" {
		t.Fatalf("issuer: %q", got)
	}
	if got := p.AuthorizationURL(); got != "https://id.example/authorize" {
		t.Fatalf("authorization url: %q", got)
	}
	if got := p.TokenURL(); got != "https://id.example/oauth/token" {
		t.Fatalf("token url: %q", got)
	}
}

func TestNewNormalizesLegacyKombifyAuth0Issuer(t *testing.T) {
	p, err := New(Config{ID: "primary", Kind: KindAuth0, Issuer: "https://kombify.eu.auth0.com/", ClientID: "techstack"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Issuer(), "https://login.kombify.io/"; got != want {
		t.Fatalf("issuer = %q, want %q", got, want)
	}
	if got, want := p.AuthorizationURL(), "https://login.kombify.io/authorize"; got != want {
		t.Fatalf("authorization url = %q, want %q", got, want)
	}
}

func TestNewPreservesAuth0IssuerSlashForTokenVerification(t *testing.T) {
	p, err := New(Config{ID: "primary", Kind: KindAuth0, Issuer: "https://login.kombify.io", ClientID: "techstack"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Issuer(), "https://login.kombify.io/"; got != want {
		t.Fatalf("issuer = %q, want %q", got, want)
	}
	converted, err := p.ToOIDCClient()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := converted.Issuer(), "https://login.kombify.io/"; got != want {
		t.Fatalf("converted issuer = %q, want %q", got, want)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if r.Len() != 0 {
		t.Fatalf("len=%d", r.Len())
	}
	if _, err := r.Get("missing"); err == nil {
		t.Fatal("expected ErrUnknownProvider")
	}
	p, err := New(Config{ID: "p1", Kind: KindAuth0, Issuer: "https://t.eu.auth0.com", ClientID: "techstack"})
	if err != nil {
		t.Fatal(err)
	}
	r.Add(p)
	got, err := r.Get("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != "p1" {
		t.Fatalf("unexpected id %q", got.ID())
	}
	if r.Len() != 1 {
		t.Fatalf("len=%d", r.Len())
	}
}

func TestAuthCodeURL(t *testing.T) {
	p, err := New(Config{ID: "p1", Kind: KindAuth0, Issuer: "https://t.eu.auth0.com", ClientID: "cid"})
	if err != nil {
		t.Fatal(err)
	}
	got := p.AuthCodeURL("http://localhost/callback", "state-1")
	if !strings.Contains(got, "client_id=cid") || !strings.Contains(got, "state=state-1") {
		t.Fatalf("auth code url: %q", got)
	}
}

func TestProviderToOIDCClient(t *testing.T) {
	p, err := New(Config{
		ID:               "primary",
		Kind:             KindGeneric,
		Issuer:           "https://id.example",
		Audience:         "aud-1",
		ClientID:         "cid-1",
		ClientSecret:     "secret-1",
		AuthorizationURL: "https://id.example/auth",
		TokenURL:         "https://id.example/token",
		JWKSURL:          "https://id.example/jwks",
		Scopes:           []string{"openid", "email", "groups"},
	})
	if err != nil {
		t.Fatal(err)
	}
	converted, err := p.ToOIDCClient()
	if err != nil {
		t.Fatal(err)
	}
	if converted.ID() != "primary" {
		t.Fatalf("id=%q", converted.ID())
	}
	if converted.Kind() != commonoidc.KindGeneric {
		t.Fatalf("kind=%q", converted.Kind())
	}
	if converted.ClientID() != "cid-1" {
		t.Fatalf("client_id=%q", converted.ClientID())
	}
	if converted.ClientSecret() != "secret-1" {
		t.Fatalf("client_secret=%q", converted.ClientSecret())
	}
	if got := converted.Scopes(); len(got) != 3 || got[2] != "groups" {
		t.Fatalf("scopes=%v", got)
	}
}

func TestRegistryToOIDCClientRegistry(t *testing.T) {
	r := NewRegistry()
	p, err := New(Config{ID: "p1", Kind: KindAuth0, Issuer: "https://login.kombify.io", ClientID: "cid"})
	if err != nil {
		t.Fatal(err)
	}
	r.Add(p)
	converted, err := r.ToOIDCClientRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if converted.Len() != 1 {
		t.Fatalf("len=%d", converted.Len())
	}
	got, err := converted.Get("p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind() != commonoidc.KindAuth0 {
		t.Fatalf("kind=%q", got.Kind())
	}
}

func TestProviderToOIDCClientRejectsUnsupportedKind(t *testing.T) {
	p := &Provider{cfg: Config{ID: "broken", Kind: Kind("saml"), Issuer: "https://id.example", ClientID: "cid"}}
	if _, err := p.ToOIDCClient(); err == nil {
		t.Fatal("expected error")
	}
}
