// Package servertls provides shared TLS configuration for kombify Go services.
//
// It supports four modes: disabled (plain HTTP), file-based certificates,
// Let's Encrypt autocert, and ephemeral self-signed certificates for
// development. All modes enforce a minimum of TLS 1.2.
package servertls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Mode selects how TLS certificates are sourced.
type Mode string

const (
	// ModeDisabled serves plain HTTP without TLS.
	ModeDisabled Mode = "disabled"
	// ModeFile loads certificates from disk (PEM files).
	ModeFile Mode = "file"
	// ModeAutocert obtains certificates from Let's Encrypt via ACME.
	ModeAutocert Mode = "autocert"
	// ModeSelfSigned generates an ephemeral self-signed certificate (dev only).
	ModeSelfSigned Mode = "self-signed"
)

// Config controls TLS behavior for a server.
type Config struct {
	// Mode selects the certificate source.
	Mode Mode

	// CertFile and KeyFile are PEM paths used in ModeFile.
	CertFile string
	KeyFile  string

	// CAFile is an optional CA bundle for mTLS client verification (ModeFile).
	CAFile string

	// Hosts lists domain names for autocert whitelisting (ModeAutocert).
	Hosts []string

	// MinVersion overrides the minimum TLS version (default: TLS 1.2).
	// Values lower than TLS 1.2 are ignored.
	MinVersion uint16
}

// ServerTLSConfig builds a *tls.Config for use with an HTTP server.
// Returns (nil, nil) when cfg.Mode is ModeDisabled.
func ServerTLSConfig(cfg Config) (*tls.Config, error) {
	switch cfg.Mode {
	case ModeDisabled, "":
		return nil, nil

	case ModeFile:
		return serverTLSFromFile(cfg)

	case ModeAutocert:
		return serverTLSAutocert(cfg)

	case ModeSelfSigned:
		return serverTLSSelfSigned(cfg)

	default:
		return nil, fmt.Errorf("servertls: unknown mode %q", cfg.Mode)
	}
}

// ClientTLSConfig builds a *tls.Config for an mTLS client that presents
// a client certificate and optionally pins a custom CA.
func ClientTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("servertls: load client keypair: %w", err)
	}

	tc := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	if caFile != "" {
		pool, err := loadCAPool(caFile)
		if err != nil {
			return nil, err
		}
		tc.RootCAs = pool
	}

	return tc, nil
}

// DefaultFromEnv reads TLS configuration from environment variables:
//
//	KOMBIFY_TLS_MODE  -> Config.Mode      (default: "disabled")
//	KOMBIFY_TLS_CERT  -> Config.CertFile
//	KOMBIFY_TLS_KEY   -> Config.KeyFile
//	KOMBIFY_TLS_CA    -> Config.CAFile
//	KOMBIFY_TLS_HOSTS -> Config.Hosts     (comma-separated)
func DefaultFromEnv() Config {
	mode := Mode(os.Getenv("KOMBIFY_TLS_MODE"))
	if mode == "" {
		mode = ModeDisabled
	}

	var hosts []string
	if h := os.Getenv("KOMBIFY_TLS_HOSTS"); h != "" {
		for _, s := range strings.Split(h, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				hosts = append(hosts, s)
			}
		}
	}

	return Config{
		Mode:     mode,
		CertFile: os.Getenv("KOMBIFY_TLS_CERT"),
		KeyFile:  os.Getenv("KOMBIFY_TLS_KEY"),
		CAFile:   os.Getenv("KOMBIFY_TLS_CA"),
		Hosts:    hosts,
	}
}

// --- internal helpers ---

func minVersion(cfg Config) uint16 {
	if cfg.MinVersion > tls.VersionTLS12 {
		return cfg.MinVersion
	}
	return tls.VersionTLS12
}

func serverTLSFromFile(cfg Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("servertls: load keypair: %w", err)
	}

	tc := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   minVersion(cfg),
	}

	if cfg.CAFile != "" {
		pool, err := loadCAPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tc.ClientCAs = pool
		tc.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tc, nil
}

func serverTLSAutocert(cfg Config) (*tls.Config, error) {
	if len(cfg.Hosts) == 0 {
		return nil, fmt.Errorf("servertls: autocert requires at least one host")
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.Hosts...),
		Cache:      autocert.DirCache("autocert-cache"),
	}

	tc := m.TLSConfig()
	tc.MinVersion = minVersion(cfg)
	return tc, nil
}

func serverTLSSelfSigned(cfg Config) (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("servertls: generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("servertls: generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now,
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("servertls: create certificate: %w", err)
	}

	cert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}

	tc := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   minVersion(cfg),
	}
	return tc, nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("servertls: read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("servertls: no valid certificates in CA file %s", caFile)
	}
	return pool, nil
}
