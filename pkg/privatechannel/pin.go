package privatechannel

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PinPrefix is curl's --pinnedpubkey spelling, so the installer can pass the
// pin to curl unchanged.
const PinPrefix = "sha256//"

// ErrPinMismatch means the bridge presented a key other than the pinned one.
var ErrPinMismatch = errors.New("private LAN bridge presented an unpinned public key")

// PinOf returns the SHA-256 pin of a certificate's subject public key.
func PinOf(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return PinPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

// ParsePin returns the 32-byte digest of a well-formed pin.
func ParsePin(pin string) ([]byte, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(pin), PinPrefix)
	if !ok {
		return nil, errors.New("private LAN pin must start with " + PinPrefix)
	}
	digest, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(digest) != sha256.Size {
		return nil, errors.New("private LAN pin must be a base64 SHA-256 digest")
	}
	return digest, nil
}

// PinnedTLSConfig trusts exactly the certificate whose public key matches pin.
// The chain is not checked against any CA: the bridge is self-signed and the
// pin, delivered out of band, is the whole trust anchor.
func PinnedTLSConfig(pin string) (*tls.Config, error) {
	want, err := ParsePin(pin)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection pins the key instead of a CA chain.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrPinMismatch
			}
			got := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return ErrPinMismatch
			}
			return nil
		},
	}, nil
}

// PinnedTransport sends requests for the bridge origin over a TLS connection
// pinned to pin, with no proxy, and every other request through base. A
// clear-text request to the bridge host is refused.
func PinnedTransport(base http.RoundTripper, origin, pin string) (http.RoundTripper, error) {
	normalized, err := NormalizeLANOrigin(origin)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := PinnedTLSConfig(pin)
	if err != nil {
		return nil, err
	}
	if existing, ok := base.(*lanTransport); ok {
		base = existing.base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	host := strings.TrimPrefix(normalized, "https://")
	return &lanTransport{
		host: host,
		pinned: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:     tlsConfig,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConns:        8,
		},
		base: base,
	}, nil
}

type lanTransport struct {
	host   string
	pinned *http.Transport
	base   http.RoundTripper
}

func (t *lanTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || !t.matches(req.URL) {
		return t.base.RoundTrip(req)
	}
	if req.URL.Scheme != "https" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("private LAN bridge %s accepts only pinned HTTPS", t.host)
	}
	return t.pinned.RoundTrip(req)
}

func (t *lanTransport) matches(u *url.URL) bool {
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return net.JoinHostPort(ip.String(), port) == t.host
}

var installMu sync.Mutex

// InstallProcessPin makes http.DefaultTransport, the base of every Guard
// control-plane client, reach the bridge only through the pinned key. An
// empty origin and pin leave the process unchanged.
func InstallProcessPin(origin, pin string) error {
	if strings.TrimSpace(origin) == "" && strings.TrimSpace(pin) == "" {
		return nil
	}
	installMu.Lock()
	defer installMu.Unlock()
	transport, err := PinnedTransport(http.DefaultTransport, origin, pin)
	if err != nil {
		return err
	}
	http.DefaultTransport = transport
	return nil
}
