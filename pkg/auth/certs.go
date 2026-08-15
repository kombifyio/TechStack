// Package auth provides mTLS certificate management for agent authentication.
package auth

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CertManager handles certificate generation and validation for mTLS.
type CertManager struct {
	caKey   *ecdsa.PrivateKey
	caCert  *x509.Certificate
	caDir   string
	revoked map[string]time.Time // serial -> revocation time
	mu      sync.RWMutex
}

// CertConfig holds certificate configuration.
type CertConfig struct {
	CommonName   string
	Organization string
	ValidDays    int
}

// GeneratedCert contains a generated certificate and key.
type GeneratedCert struct {
	CertPEM     []byte
	KeyPEM      []byte
	Serial      string
	Fingerprint string
	ExpiresAt   time.Time
}

// NewCertManager creates a new certificate manager.
// If CA doesn't exist, it will be generated.
func NewCertManager(caDir string) (*CertManager, error) {
	cm := &CertManager{caDir: caDir, revoked: make(map[string]time.Time)}

	// Ensure CA directory exists
	if err := os.MkdirAll(caDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create CA directory: %w", err)
	}

	caKeyPath := filepath.Join(caDir, "ca.key")
	caCertPath := filepath.Join(caDir, "ca.crt")

	// Check if CA exists
	if _, err := os.Stat(caKeyPath); os.IsNotExist(err) {
		// Generate new CA
		if err := cm.generateCA(); err != nil {
			return nil, fmt.Errorf("failed to generate CA: %w", err)
		}
	} else {
		// Load existing CA
		if err := cm.loadCA(caKeyPath, caCertPath); err != nil {
			return nil, fmt.Errorf("failed to load CA: %w", err)
		}
	}

	// Load revocation list
	if err := cm.LoadRevokedList(); err != nil {
		return nil, fmt.Errorf("failed to load revocation list: %w", err)
	}

	return cm, nil
}

// generateCA creates a new Certificate Authority.
func (cm *CertManager) generateCA() error {
	// Generate ECDSA key (P-256 for broad compatibility)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate CA key: %w", err)
	}

	// Create CA certificate template
	serialNumber, err := generateSerial()
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"kombifyTechstack"},
			CommonName:   "kombifyTechstack Root CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
	}

	// Self-sign the CA certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create CA certificate: %w", err)
	}

	// Parse the certificate back
	caCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Save CA key
	keyDER, err := marshalECPrivateKey(privateKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})
	if err := os.WriteFile(filepath.Join(cm.caDir, "ca.key"), keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to save CA key: %w", err)
	}

	// Save CA certificate (0600 for secure permissions)
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
	if err := os.WriteFile(filepath.Join(cm.caDir, "ca.crt"), certPEM, 0600); err != nil {
		return fmt.Errorf("failed to save CA certificate: %w", err)
	}

	cm.caKey = privateKey
	cm.caCert = caCert

	return nil
}

// loadCA loads an existing CA from disk.
func (cm *CertManager) loadCA(keyPath, certPath string) error {
	// Load key
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read CA key: %w", err)
	}

	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return fmt.Errorf("failed to decode CA key PEM")
	}

	privateKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CA key: %w", err)
	}

	// Load certificate
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	block, _ = pem.Decode(certPEM)
	if block == nil {
		return fmt.Errorf("failed to decode CA certificate PEM")
	}

	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	cm.caKey = privateKey
	cm.caCert = caCert

	return nil
}

// defaultCertOrg is the certificate Organization used for agent certs that are
// not bound to a specific tenant. pickTenantFromCert (grpcserver) reads the
// first non-empty Organization as the tenant, so tenant-scoped agent certs put
// the tenant id here instead.
const defaultCertOrg = "kombifyTechstack"

// GenerateAgentCert creates a client certificate for an agent under the default
// (non-tenant) organization. Kept for callers and tests that predate
// tenant-scoped issuance; new issuance paths should use GenerateAgentCertForTenant.
func (cm *CertManager) GenerateAgentCert(agentID string, validDays int) (*GeneratedCert, error) {
	return cm.GenerateAgentCertForTenant(agentID, defaultCertOrg, validDays)
}

// GenerateAgentCertForTenant creates a client certificate whose CommonName is
// the agent id and whose Organization is the tenant id. The gRPC Register
// handler binds identity from exactly these fields (CN == agent_id,
// Organization == tenant), so this is the issuance path the enrollment flow
// must use. An empty tenantID falls back to the default organization.
func (cm *CertManager) GenerateAgentCertForTenant(agentID, tenantID string, validDays int) (*GeneratedCert, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	org := tenantID
	if org == "" {
		org = defaultCertOrg
	}
	if validDays <= 0 {
		validDays = 365 // Default 1 year
	}
	template := &x509.Certificate{
		Subject: pkix.Name{
			Organization: []string{org},
			CommonName:   agentID,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(0, 0, validDays),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return cm.signLeaf(template)
}

// GenerateServerCert creates the Core's gRPC server certificate. commonName is
// the primary hostname; sans are the additional hostnames/IPs agents will dial
// (each parsed as an IP address, else treated as a DNS name). The agent verifies
// this cert against the CA and its dial target, so every reachable gRPC address
// must appear in commonName or sans.
func (cm *CertManager) GenerateServerCert(commonName string, sans []string, validDays int) (*GeneratedCert, error) {
	if commonName == "" {
		return nil, fmt.Errorf("common name is required")
	}
	if validDays <= 0 {
		validDays = 365
	}
	template := &x509.Certificate{
		Subject: pkix.Name{
			Organization: []string{defaultCertOrg},
			CommonName:   commonName,
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(0, 0, validDays),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, san := range sans {
		san = strings.TrimSpace(san)
		if san == "" {
			continue
		}
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}
	if ip := net.ParseIP(commonName); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
	} else {
		template.DNSNames = append(template.DNSNames, commonName)
	}
	return cm.signLeaf(template)
}

// signLeaf fills the serial, generates a P-256 key, signs the template with the
// CA, and returns the PEM bundle. The template's SerialNumber is set here so
// callers cannot accidentally reuse one.
func (cm *CertManager) signLeaf(template *x509.Certificate) (*GeneratedCert, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate leaf key: %w", err)
	}
	serialNumber, err := generateSerial()
	if err != nil {
		return nil, err
	}
	template.SerialNumber = serialNumber
	template.BasicConstraintsValid = true

	certDER, err := x509.CreateCertificate(rand.Reader, template, cm.caCert, &privateKey.PublicKey, cm.caKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := marshalECPrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated certificate: %w", err)
	}
	fingerprint := fmt.Sprintf("%x", cert.Raw[:20])

	return &GeneratedCert{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Serial:      serialNumber.String(),
		Fingerprint: fingerprint,
		ExpiresAt:   template.NotAfter,
	}, nil
}

// CACertPEM returns the CA certificate in PEM format.
func (cm *CertManager) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cm.caCert.Raw,
	})
}

// VerifyAgentCert verifies that a certificate was signed by the CA and is not revoked.
func (cm *CertManager) VerifyAgentCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Verify signature
	if err := cert.CheckSignatureFrom(cm.caCert); err != nil {
		return nil, fmt.Errorf("certificate signature verification failed: %w", err)
	}

	// Check expiration
	if time.Now().After(cert.NotAfter) {
		return nil, fmt.Errorf("certificate has expired")
	}

	// Check revocation
	if cm.IsRevoked(cert.SerialNumber.String()) {
		return nil, fmt.Errorf("certificate has been revoked")
	}

	return cert, nil
}

// RevokeCert revokes a certificate by serial number.
func (cm *CertManager) RevokeCert(serial string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.revoked[serial] = time.Now()

	// Persist to file
	return cm.saveRevokedList()
}

// IsRevoked checks if a certificate serial is revoked.
func (cm *CertManager) IsRevoked(serial string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	_, revoked := cm.revoked[serial]
	return revoked
}

// GetRevokedSerials returns all revoked certificate serials.
func (cm *CertManager) GetRevokedSerials() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	serials := make([]string, 0, len(cm.revoked))
	for serial := range cm.revoked {
		serials = append(serials, serial)
	}
	return serials
}

// LoadRevokedList loads the revocation list from disk.
func (cm *CertManager) LoadRevokedList() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	revokedPath := filepath.Join(cm.caDir, "revoked.txt")
	file, err := os.Open(revokedPath)
	if os.IsNotExist(err) {
		return nil // No revoked certs yet
	}
	if err != nil {
		return fmt.Errorf("failed to open revoked list: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		serial := parts[0]
		var revokedAt time.Time
		if len(parts) > 1 {
			revokedAt, _ = time.Parse(time.RFC3339, parts[1])
		}
		if revokedAt.IsZero() {
			revokedAt = time.Now()
		}
		cm.revoked[serial] = revokedAt
	}

	return scanner.Err()
}

// saveRevokedList persists the revocation list to disk.
func (cm *CertManager) saveRevokedList() error {
	revokedPath := filepath.Join(cm.caDir, "revoked.txt")

	file, err := os.Create(revokedPath)
	if err != nil {
		return fmt.Errorf("failed to create revoked list: %w", err)
	}
	defer file.Close()

	file.WriteString("# kombifyTechstack Certificate Revocation List\n")
	file.WriteString("# Format: serial,revoked_at\n")
	for serial, revokedAt := range cm.revoked {
		fmt.Fprintf(file, "%s,%s\n", serial, revokedAt.Format(time.RFC3339))
	}

	return nil
}

func generateSerial() (*big.Int, error) {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, serialNumberLimit)
}

func marshalECPrivateKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal EC private key: %w", err)
	}
	return der, nil
}
