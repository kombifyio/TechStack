// Package providers wires identity providers (Pocket ID, Auth0, generic OIDC)
// into a uniform [Provider] interface that bridges into the shared
// [github.com/kombifyio/go-common/oidcclient] verifier via
// [Provider.ToOIDCClient] / [Registry.ToOIDCClientRegistry].
//
// Provider configuration lives in Postgres (`identity_providers` table) and is
// loaded into a [Registry] at runtime as part of the transitional lean-core
// auth surface.
package providers

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	commonoidc "github.com/kombifyio/go-common/oidcclient"
	"github.com/kombifyio/techstack/pkg/config"
)

// Kind enumerates the supported provider flavors. The kind drives default
// discovery URL composition; once a provider is constructed all kinds share
// the same OIDC verification path.
type Kind string

const (
	KindPocketID Kind = "pocketid"
	KindAuth0    Kind = "auth0"
	KindGeneric  Kind = "generic"
)

// Errors returned by Registry / Provider.
var (
	ErrUnknownProvider = errors.New("providers: unknown provider")
	ErrInvalidConfig   = errors.New("providers: invalid configuration")
)

// Config describes a single identity provider entry.
type Config struct {
	ID               string // logical id (per org/tenant), e.g. "primary", "auth0-saas"
	Kind             Kind
	Issuer           string   // required
	Audience         string   // optional; defaults to ClientID
	ClientID         string   // required for auth code login
	ClientSecret     string   // optional for public clients
	AuthorizationURL string   // optional; derived from issuer when empty
	TokenURL         string   // optional; derived from issuer when empty
	JWKSURL          string   // optional; derived from issuer when empty
	Scopes           []string // optional; defaults to openid/profile/email
}

// Provider is a verified-OIDC bridge for a single identity provider config.
// Verification itself is delegated to go-common/oidcclient via
// [Provider.ToOIDCClient]; this type only carries configuration.
type Provider struct {
	cfg Config
}

// ID returns the logical provider id.
func (p *Provider) ID() string { return p.cfg.ID }

// Kind returns the provider flavor.
func (p *Provider) Kind() Kind { return p.cfg.Kind }

// Issuer returns the provider issuer URL.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// ClientID returns the OAuth2 client id used for auth-code login.
func (p *Provider) ClientID() string { return p.cfg.ClientID }

// ClientSecret returns the OAuth2 client secret, if configured.
func (p *Provider) ClientSecret() string { return p.cfg.ClientSecret }

// AuthorizationURL returns the provider authorization endpoint.
func (p *Provider) AuthorizationURL() string { return p.cfg.AuthorizationURL }

// TokenURL returns the provider token endpoint.
func (p *Provider) TokenURL() string { return p.cfg.TokenURL }

// AuthCodeURL constructs the user-agent redirect URL for starting an auth-code flow.
func (p *Provider) AuthCodeURL(redirectURI, state string) string {
	params := url.Values{}
	params.Set("client_id", p.cfg.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(p.cfg.Scopes, " "))
	if state != "" {
		params.Set("state", state)
	}
	return p.cfg.AuthorizationURL + "?" + params.Encode()
}

// Verify is removed in favor of [Provider.ToOIDCClient] +
// [github.com/kombifyio/go-common/oidcclient.Provider.Verify].

// ToOIDCClient converts the TechStack provider into the shared go-common
// provider shape used by authflow/authlocal.
func (p *Provider) ToOIDCClient() (*commonoidc.Provider, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: provider is nil", ErrInvalidConfig)
	}
	kind, err := toOIDCClientKind(p.cfg.Kind)
	if err != nil {
		return nil, err
	}
	return commonoidc.NewProvider(commonoidc.ProviderConfig{
		ID:               p.cfg.ID,
		Kind:             kind,
		Issuer:           p.cfg.Issuer,
		Audience:         p.cfg.Audience,
		ClientID:         p.cfg.ClientID,
		ClientSecret:     p.cfg.ClientSecret,
		AuthorizationURL: p.cfg.AuthorizationURL,
		TokenURL:         p.cfg.TokenURL,
		JWKSURL:          p.cfg.JWKSURL,
		Scopes:           append([]string(nil), p.cfg.Scopes...),
	})
}

// New builds a Provider from a [Config]. JWKSURL defaults to
// `<issuer>/.well-known/jwks.json` when not explicitly set; that is the
// convention for both Pocket ID and Auth0.
func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.ID) == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidConfig)
	}
	switch cfg.Kind {
	case KindPocketID, KindAuth0, KindGeneric:
	default:
		return nil, fmt.Errorf("%w: unsupported kind %q", ErrInvalidConfig, cfg.Kind)
	}
	issuer := strings.TrimSpace(cfg.Issuer)
	if cfg.Kind == KindAuth0 {
		issuer = config.NormalizeCloudAuthIssuer(issuer)
		issuer = auth0OIDCIssuer(issuer)
	}
	if issuer == "" {
		return nil, fmt.Errorf("%w: issuer is required", ErrInvalidConfig)
	}
	endpointBase := strings.TrimRight(issuer, "/")
	clientID := strings.TrimSpace(cfg.ClientID)
	if clientID == "" {
		return nil, fmt.Errorf("%w: client_id is required", ErrInvalidConfig)
	}
	audience := strings.TrimSpace(cfg.Audience)
	if audience == "" {
		audience = clientID
	}
	authorizationURL := strings.TrimSpace(cfg.AuthorizationURL)
	if authorizationURL == "" {
		authorizationURL = endpointBase + "/authorize"
	}
	tokenURL := strings.TrimSpace(cfg.TokenURL)
	if tokenURL == "" {
		tokenURL = endpointBase + "/oauth/token"
	}
	jwks := strings.TrimSpace(cfg.JWKSURL)
	if jwks == "" {
		jwks = endpointBase + "/.well-known/jwks.json"
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	return &Provider{cfg: Config{
		ID:               cfg.ID,
		Kind:             cfg.Kind,
		Issuer:           issuer,
		Audience:         audience,
		ClientID:         clientID,
		ClientSecret:     cfg.ClientSecret,
		AuthorizationURL: authorizationURL,
		TokenURL:         tokenURL,
		JWKSURL:          jwks,
		Scopes:           scopes,
	}}, nil
}

func auth0OIDCIssuer(issuer string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(issuer), "/")
	if trimmed == "" {
		return ""
	}
	return trimmed + "/"
}

// Registry holds providers indexed by ID. Safe for concurrent reads after
// construction; write methods take an internal mutex.
type Registry struct {
	mu    sync.RWMutex
	items map[string]*Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{items: map[string]*Provider{}} }

// Add registers (or replaces) a provider.
func (r *Registry) Add(p *Provider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[p.ID()] = p
}

// Get returns the provider with the given id.
func (r *Registry) Get(id string) (*Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.items[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, id)
	}
	return p, nil
}

// Len returns the number of registered providers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

// IDs returns the registered provider ids in arbitrary order.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.items))
	for k := range r.items {
		out = append(out, k)
	}
	return out
}

// ToOIDCClientRegistry converts the TechStack registry into the shared
// go-common registry shape used by authflow/authlocal.
func (r *Registry) ToOIDCClientRegistry() (*commonoidc.Registry, error) {
	if r == nil {
		return nil, nil
	}
	out := commonoidc.NewRegistry()
	for _, id := range r.IDs() {
		provider, err := r.Get(id)
		if err != nil {
			return nil, err
		}
		converted, err := provider.ToOIDCClient()
		if err != nil {
			return nil, err
		}
		out.Add(converted)
	}
	return out, nil
}

func toOIDCClientKind(kind Kind) (commonoidc.Kind, error) {
	switch kind {
	case KindPocketID:
		return commonoidc.KindPocketID, nil
	case KindAuth0:
		return commonoidc.KindAuth0, nil
	case KindGeneric:
		return commonoidc.KindGeneric, nil
	default:
		return "", fmt.Errorf("%w: unsupported kind %q", ErrInvalidConfig, kind)
	}
}
