package privatechannel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
)

const certificateFile = "tls.pem"

// LoadOrCreateCertificate returns the bridge's persistent self-signed
// certificate and its pin, kept in the private custody directory dir (owner
// only, with an explicit private ACL on Windows). The key is created once and
// kept across restarts and address changes, so paired Guards keep matching
// it. A file that exists but does not parse is an error, never silently
// replaced: a new key would lock out every Guard paired to the old pin.
func LoadOrCreateCertificate(dir string) (tls.Certificate, string, error) {
	root, err := privatefs.Open(dir)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("private LAN bridge key custody: %w", err)
	}
	defer root.Close()
	path := filepath.Join(dir, certificateFile)
	data, err := privatefs.ReadFile(root, certificateFile)
	switch {
	case err == nil:
		return parseCertificateFile(path, data)
	case !errors.Is(err, os.ErrNotExist):
		return tls.Certificate{}, "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "kombify Techstack private LAN bridge"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	data = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...)
	if err := privatefs.Replace(root, certificateFile, data, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	return parseCertificateFile(path, data)
}

func parseCertificateFile(path string, data []byte) (tls.Certificate, string, error) {
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("private LAN bridge certificate %s is unreadable; remove it and re-pair the LAN servers: %w", path, err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert.Leaf = leaf
	return cert, PinOf(leaf), nil
}
