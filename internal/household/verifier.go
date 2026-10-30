package household

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"

	"github.com/kombifyio/techstack/pkg/fulllocal"
)

const verifierTTL = 10 * time.Minute

// OIDCVerifier verifies ID tokens against the published keys (JWKS) of the
// issuer Techstack resolved for a stack. Discovery is fetched from that issuer,
// and the token's own issuer claim must equal it. Verifiers are cached per
// issuer and audience for a short time so a rotated key is picked up.
type OIDCVerifier struct {
	// HTTPClient overrides the default client; tests use it.
	HTTPClient *http.Client
	// AllowPrivateNetworks admits issuers on loopback and private addresses, as
	// a self-hosted installation reaches its own nodes. A hosted control plane
	// leaves it false so a node-reported address cannot aim the fetch inward.
	AllowPrivateNetworks bool

	mu     sync.Mutex
	client *http.Client
	cache  map[string]cachedVerifier
}

type cachedVerifier struct {
	verifier *oidcclient.Verifier
	expires  time.Time
}

// Verify checks signature, issuer, audience and expiry of rawToken.
func (v *OIDCVerifier) Verify(ctx context.Context, issuer, clientID, rawToken string) (*oidcclient.Claims, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" || strings.TrimSpace(clientID) == "" {
		return nil, fmt.Errorf("household: issuer and client id are required")
	}
	verifier, err := v.verifierFor(ctx, issuer, clientID)
	if err != nil {
		return nil, err
	}
	return verifier.Verify(ctx, rawToken)
}

func (v *OIDCVerifier) verifierFor(ctx context.Context, issuer, clientID string) (*oidcclient.Verifier, error) {
	key := issuer + "\x00" + clientID
	v.mu.Lock()
	if cached, ok := v.cache[key]; ok && time.Now().Before(cached.expires) {
		v.mu.Unlock()
		return cached.verifier, nil
	}
	v.mu.Unlock()

	client := v.httpClient()
	doc, err := oidcclient.Discover(ctx, issuer, client)
	if err != nil {
		return nil, err
	}
	// The published keys must live on the issuer's own host.
	issuerURL, _ := url.Parse(issuer)
	jwksURL, jwksErr := url.Parse(doc.JWKSURI)
	if jwksErr != nil || issuerURL == nil || !strings.EqualFold(jwksURL.Host, issuerURL.Host) {
		return nil, fmt.Errorf("household: the issuer's keys are not on the issuer host")
	}
	verifier, err := oidcclient.NewVerifier(oidcclient.VerifierConfig{
		Issuer: doc.Issuer, Audience: clientID, JWKSURL: doc.JWKSURI, HTTPClient: client,
		ClockSkew: time.Minute,
	})
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	if v.cache == nil {
		v.cache = map[string]cachedVerifier{}
	}
	v.cache[key] = cachedVerifier{verifier: verifier, expires: time.Now().Add(verifierTTL)}
	v.mu.Unlock()
	return verifier, nil
}

func (v *OIDCVerifier) httpClient() *http.Client {
	if v.HTTPClient != nil {
		return v.HTTPClient
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.client == nil {
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		if !v.AllowPrivateNetworks {
			dialer.Control = refusePrivateAddress
		}
		v.client = &http.Client{
			Timeout:   10 * time.Second,
			Transport: fulllocal.Transport(&http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second, Proxy: nil}),
		}
	}
	return v.client
}

// refusePrivateAddress rejects the connection at dial time, after name
// resolution, so a hostname cannot resolve its way past the check.
func refusePrivateAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("household: issuer address %s is not a public address", ip)
	}
	return nil
}
