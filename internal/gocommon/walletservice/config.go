package walletservice

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/internal/httpsecurity"
)

const (
	defaultTimeout       = 15 * time.Second
	defaultBrokerTimeout = 45 * time.Second
)

// reasonNotConfigured is the DisabledReason of a client given no settings at all.
const reasonNotConfigured = "not_configured"

var serviceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Config is everything a product needs to use the Wallet's product-service
// routes (`/v1/wallet/service/*` on the Gateway). Origin, ServiceID,
// ServiceSecret (or SigningKey) and RecipientGeneration are required together.
// RecipientKey is required only for Deliver and Mint, which open service-sealed
// values. An invalid supplied key disables the client.
type Config struct {
	// Origin is the Gateway origin, for example https://api.kombify.io. It must
	// be https (or http on a loopback IP for local development) and carry no
	// user info, path, query or fragment.
	Origin string
	// ServiceID is the product's service id at the Wallet: `paperwork`, `ai`,
	// `techstack`, `simulate` or `cloud`. The service token's `svc`, the lease
	// actor `service:<id>`, the transport key id and the transport audience
	// `urn:kombify:wallet:service:<id>` all derive from it.
	ServiceID string
	// ServiceSecret is the product's own Wallet service-auth secret
	// (`SERVICE_AUTH_SECRET_<PRODUCT>` at the Gateway). The platform's shared
	// `SERVICE_AUTH_SECRET` never admits these routes and must not be passed;
	// this package never reads the environment.
	ServiceSecret string
	// SigningKey and SigningKeyID are the product's per-caller Ed25519 identity
	// (`servicecall.LoadEd25519FromEnv`). When set, the service token is signed
	// EdDSA under SigningKeyID and ServiceSecret is not used; the Gateway's
	// public set for this caller must carry the key. Same claims and header as
	// the HS256 token.
	SigningKey   ed25519.PrivateKey
	SigningKeyID string
	// RecipientKey is the product's private P-256 JWK (JSON). The Wallet Store
	// seals every delivered value to its public half, enrolled in
	// WALLET_SERVICE_RECIPIENTS. Required for Deliver and Mint; omit it when the
	// service only transports node-sealed envelopes or uses other operations.
	RecipientKey string
	// RecipientGeneration is the generation the Wallet pins for that key (a
	// positive decimal).
	RecipientGeneration string
	// CaptureKeyThumbprint optionally pins the store's capture key. A capture
	// key with another thumbprint is refused before anything is sealed to it.
	// Without a pin the key is trusted as delivered over the authenticated call.
	CaptureKeyThumbprint string

	// HTTPClient carries the calls (transport, proxy, TLS). Its redirects are
	// always disabled so the service token cannot travel to another host. Zero
	// means a default client.
	HTTPClient *http.Client
	// Timeout bounds a call; zero means 15 seconds. BrokerTimeout bounds a
	// brokered call, which waits for the upstream (30 seconds at the Wallet);
	// zero means 45 seconds.
	Timeout       time.Duration
	BrokerTimeout time.Duration
	// Now is a test hook; zero means time.Now.
	Now func() time.Time
}

// Client talks to the Wallet product-service routes. It is immutable after New
// and safe for concurrent use. The zero value, a nil *Client and Disabled() are
// disabled: every call returns an ErrUnavailable error without touching the
// network.
type Client struct {
	disabled      string
	origin        string
	serviceID     string
	secret        string
	signingKey    ed25519.PrivateKey
	signingKeyID  string
	audience      string
	pin           string
	recipient     *keyPair
	http          *http.Client
	timeout       time.Duration
	brokerTimeout time.Duration
	now           func() time.Time
}

// New builds the client. It never fails: an incomplete or invalid configuration
// yields a disabled client whose DisabledReason names what is wrong (a short
// token, never a value), so a caller can log it once and keep failing closed.
func New(cfg Config) *Client {
	c := &Client{}
	if reason := c.configure(cfg); reason != "" {
		return &Client{disabled: reason}
	}
	return c
}

// configure validates cfg and fills c, or returns the reason it cannot.
func (c *Client) configure(cfg Config) string {
	if reason := missing(cfg); reason != "" {
		return reason
	}
	serviceID, secret, key := strings.TrimSpace(cfg.ServiceID), strings.TrimSpace(cfg.ServiceSecret), strings.TrimSpace(cfg.RecipientKey)
	generation, pin := strings.TrimSpace(cfg.RecipientGeneration), strings.TrimSpace(cfg.CaptureKeyThumbprint)
	origin, originOK := parseOrigin(cfg.Origin)
	var recipient *keyPair
	keyOK := true
	if key != "" {
		recipient, keyOK = parseRecipient(key, serviceID, generation)
	}
	switch {
	case !serviceIDPattern.MatchString(serviceID):
		return "service_id_invalid"
	case !originOK:
		return "origin_invalid"
	case !decimalPattern.MatchString(generation):
		return "recipient_generation_invalid"
	case !keyOK:
		return "recipient_key_invalid"
	case pin != "" && !thumbprintPattern.MatchString(pin):
		return "capture_key_pin_invalid"
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	c.origin, c.serviceID, c.secret, c.pin, c.recipient = origin, serviceID, secret, pin, recipient
	c.signingKey, c.signingKeyID = cfg.SigningKey, cfg.SigningKeyID
	c.audience = "urn:kombify:wallet:service:" + serviceID
	c.http = httpsecurity.WithoutRedirects(httpClient)
	c.timeout, c.brokerTimeout, c.now = orDefault(cfg.Timeout, defaultTimeout), orDefault(cfg.BrokerTimeout, defaultBrokerTimeout), cfg.Now
	if c.now == nil {
		c.now = time.Now
	}
	return ""
}

func orDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// missing names the first required setting that is absent, in the order a
// person fills them in; all four absent is simply not configured.
func missing(cfg Config) string {
	origin, id, secret, key := strings.TrimSpace(cfg.Origin), strings.TrimSpace(cfg.ServiceID), strings.TrimSpace(cfg.ServiceSecret), strings.TrimSpace(cfg.RecipientKey)
	if secret == "" && cfg.SigningKey != nil {
		secret = "ed25519"
	}
	switch {
	case origin == "" && id == "" && secret == "" && key == "":
		return reasonNotConfigured
	case origin == "":
		return "origin_missing"
	case secret == "":
		return "service_secret_missing"
	}
	return ""
}

// Disabled is a client that refuses every call with ErrUnavailable.
func Disabled() *Client { return &Client{disabled: "disabled"} }

// Enabled reports whether authenticated calls can reach the Wallet. Deliver
// and Mint additionally require a configured RecipientKey before any call.
func (c *Client) Enabled() bool { return c != nil && c.disabled == "" && c.http != nil }

// DisabledReason names what is missing or wrong, never a value. Empty when the
// client is enabled.
func (c *Client) DisabledReason() string {
	switch {
	case c == nil:
		return reasonNotConfigured
	case c.disabled != "":
		return c.disabled
	case c.http == nil:
		return reasonNotConfigured
	}
	return ""
}

// RecipientPublicJWK is the public half of the configured recipient key, as it
// is enrolled in the Wallet's WALLET_SERVICE_RECIPIENTS. It is the zero value
// for a disabled client or one without a recipient key.
func (c *Client) RecipientPublicJWK() PublicJWK {
	if !c.Enabled() || c.recipient == nil {
		return PublicJWK{}
	}
	return publicJWKOf(c.recipient.private.PublicKey())
}

func (c *Client) gate() error {
	if !c.Enabled() {
		return errUnavailable(c.DisabledReason())
	}
	return nil
}

func (c *Client) gateRecipient() error {
	if err := c.gate(); err != nil {
		return err
	}
	if c.recipient == nil {
		return errUnavailable("recipient_key_missing")
	}
	return nil
}

// parseOrigin accepts an https origin, or an http origin on a loopback IP for
// local development, without user info, path, query or fragment.
func parseOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", false
	}
	switch u.Scheme {
	case "https":
	case "http":
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return "", false
		}
	default:
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// privateJWK is the recipient key as configured. WebCrypto and node exports add
// key_ops and ext; neither changes the key.
type privateJWK struct {
	PublicJWK
	D      string   `json:"d"`
	Kid    string   `json:"kid,omitempty"`
	Alg    string   `json:"alg,omitempty"`
	Use    string   `json:"use,omitempty"`
	KeyOps []string `json:"key_ops,omitempty"`
	Ext    *bool    `json:"ext,omitempty"`
}

// parseRecipient loads the private P-256 JWK. The key must be a consistent key
// pair; a `kid` it carries must be this service's `service:<id>`. Nothing in a
// failure names key material.
func parseRecipient(raw, serviceID, generation string) (*keyPair, bool) {
	var jwk privateJWK
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&jwk) != nil || jwk.Kty != "EC" || jwk.Crv != curveP256 ||
		(jwk.Kid != "" && jwk.Kid != "service:"+serviceID) || (jwk.Alg != "" && jwk.Alg != keyAlgorithm) || (jwk.Use != "" && jwk.Use != "enc") {
		return nil, false
	}
	d, err := b64.DecodeString(jwk.D)
	if err != nil || len(d) != 32 {
		return nil, false
	}
	private, err := ecdh.P256().NewPrivateKey(d)
	clear(d)
	if err != nil {
		return nil, false
	}
	public, err := jwk.key()
	if err != nil || !public.Equal(private.PublicKey()) {
		return nil, false
	}
	return newKeyPair(private, "service:"+serviceID, generation), true
}

// GenerateRecipientKey creates a fresh recipient key for a product service. The
// private JWK is what Config.RecipientKey takes and belongs in the product's
// secret custody; the public JWK is what the Wallet's recipient manifest
// (WALLET_SERVICE_RECIPIENTS) enrolls, together with the generation.
func GenerateRecipientKey() (privateKey string, public PublicJWK, err error) {
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", PublicJWK{}, err
	}
	public = publicJWKOf(private.PublicKey())
	d := private.Bytes()
	defer clear(d)
	raw, err := json.Marshal(struct {
		PublicJWK
		D string `json:"d"`
	}{public, base64.RawURLEncoding.EncodeToString(d)})
	if err != nil {
		return "", PublicJWK{}, err
	}
	return string(bytes.TrimSpace(raw)), public, nil
}
