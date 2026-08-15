// Package auth provides mTLS certificate management testing.
package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestNewCertManager_CreatesNewCA tests CA generation when directory is empty.
func TestNewCertManager_CreatesNewCA(t *testing.T) {
	tmpDir := t.TempDir()
	caDir := filepath.Join(tmpDir, "ca")

	cm, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("NewCertManager failed: %v", err)
	}

	// Verify CA files exist
	if _, err := os.Stat(filepath.Join(caDir, "ca.key")); err != nil {
		t.Errorf("CA key not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(caDir, "ca.crt")); err != nil {
		t.Errorf("CA certificate not created: %v", err)
	}

	// Verify CA is valid
	if cm.caCert == nil {
		t.Error("CA certificate not loaded")
	}
	if cm.caKey == nil {
		t.Error("CA key not loaded")
	}
	if !cm.caCert.IsCA {
		t.Error("Certificate is not marked as CA")
	}
	if cm.caCert.Subject.CommonName != "kombifyTechstack Root CA" {
		t.Errorf("Unexpected CA CommonName: %s", cm.caCert.Subject.CommonName)
	}
}

// TestNewCertManager_LoadsExistingCA tests loading an existing CA.
func TestNewCertManager_LoadsExistingCA(t *testing.T) {
	tmpDir := t.TempDir()
	caDir := filepath.Join(tmpDir, "ca")

	// Create CA first time
	cm1, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("First NewCertManager failed: %v", err)
	}
	originalSerial := cm1.caCert.SerialNumber.String()

	// Load CA second time
	cm2, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("Second NewCertManager failed: %v", err)
	}

	// Verify same CA was loaded
	if cm2.caCert.SerialNumber.String() != originalSerial {
		t.Error("Different CA was loaded instead of existing one")
	}
}

// TestNewCertManager_InvalidDirectory tests error handling for invalid directories.
func TestNewCertManager_InvalidDirectory(t *testing.T) {
	// Try to create CA in a file (not directory)
	tmpFile, err := os.CreateTemp("", "test-file")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	_, err = NewCertManager(tmpFile.Name())
	if err == nil {
		t.Error("Expected error when creating CA in file path")
	}
}

// TestNewCertManager_LoadsRevokedList tests that revocation list is loaded on init.
func TestNewCertManager_LoadsRevokedList(t *testing.T) {
	tmpDir := t.TempDir()
	caDir := filepath.Join(tmpDir, "ca")

	// Create CA first
	cm1, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("First NewCertManager failed: %v", err)
	}

	// Revoke a cert
	testSerial := "12345678901234567890"
	if err := cm1.RevokeCert(testSerial); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	// Create new manager - should load revoked list
	cm2, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("Second NewCertManager failed: %v", err)
	}

	if !cm2.IsRevoked(testSerial) {
		t.Error("Revoked serial was not loaded from disk")
	}
}

// TestGenerateAgentCert tests basic agent certificate generation.
func TestGenerateAgentCert(t *testing.T) {
	cm := setupTestCertManager(t)

	agentID := "test-agent-001"
	cert, err := cm.GenerateAgentCert(agentID, 365)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	// Verify certificate fields
	if len(cert.CertPEM) == 0 {
		t.Error("CertPEM is empty")
	}
	if len(cert.KeyPEM) == 0 {
		t.Error("KeyPEM is empty")
	}
	if cert.Serial == "" {
		t.Error("Serial is empty")
	}
	if cert.Fingerprint == "" {
		t.Error("Fingerprint is empty")
	}
	if cert.ExpiresAt.Before(time.Now()) {
		t.Error("Certificate already expired")
	}

	// Parse and verify the certificate
	block, _ := pem.Decode(cert.CertPEM)
	if block == nil {
		t.Fatal("Failed to decode certificate PEM")
	}

	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	if x509Cert.Subject.CommonName != agentID {
		t.Errorf("Expected CommonName %s, got %s", agentID, x509Cert.Subject.CommonName)
	}
	if len(x509Cert.ExtKeyUsage) == 0 || x509Cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Error("Certificate missing ClientAuth ExtKeyUsage")
	}
}

// TestGenerateAgentCert_DefaultValidity tests default validity period.
func TestGenerateAgentCert_DefaultValidity(t *testing.T) {
	cm := setupTestCertManager(t)

	cert, err := cm.GenerateAgentCert("agent-default", 0)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	// Should default to 365 days
	expectedExpiry := time.Now().AddDate(0, 0, 365)
	diff := cert.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Hour || diff > time.Hour {
		t.Errorf("Default validity incorrect: expected ~365 days, got expiry at %v", cert.ExpiresAt)
	}
}

// TestGenerateAgentCert_CustomValidity tests custom validity period.
func TestGenerateAgentCert_CustomValidity(t *testing.T) {
	cm := setupTestCertManager(t)

	validDays := 30
	cert, err := cm.GenerateAgentCert("agent-custom", validDays)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	expectedExpiry := time.Now().AddDate(0, 0, validDays)
	diff := cert.ExpiresAt.Sub(expectedExpiry)
	if diff < -time.Hour || diff > time.Hour {
		t.Errorf("Custom validity incorrect: expected ~%d days, got expiry at %v", validDays, cert.ExpiresAt)
	}
}

// TestGenerateAgentCert_SignedByCA verifies certificate is signed by CA.
func TestGenerateAgentCert_SignedByCA(t *testing.T) {
	cm := setupTestCertManager(t)

	cert, err := cm.GenerateAgentCert("agent-signed", 365)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	block, _ := pem.Decode(cert.CertPEM)
	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	// Verify signature
	if err := x509Cert.CheckSignatureFrom(cm.caCert); err != nil {
		t.Errorf("Certificate not properly signed by CA: %v", err)
	}
}

// TestCACertPEM tests CA certificate PEM export.
func TestCACertPEM(t *testing.T) {
	cm := setupTestCertManager(t)

	caPEM := cm.CACertPEM()
	if len(caPEM) == 0 {
		t.Error("CACertPEM returned empty")
	}

	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("Failed to decode CA PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("Expected PEM type CERTIFICATE, got %s", block.Type)
	}

	// Verify it's actually the CA cert
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("Failed to parse CA certificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("Returned certificate is not CA")
	}
}

// TestVerifyAgentCert_Valid tests verification of a valid certificate.
func TestVerifyAgentCert_Valid(t *testing.T) {
	cm := setupTestCertManager(t)

	cert, err := cm.GenerateAgentCert("agent-valid", 365)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	verified, err := cm.VerifyAgentCert(cert.CertPEM)
	if err != nil {
		t.Errorf("VerifyAgentCert failed for valid cert: %v", err)
	}
	if verified == nil {
		t.Fatal("VerifyAgentCert returned nil certificate")
	}
	if verified.Subject.CommonName != "agent-valid" {
		t.Errorf("Unexpected CommonName: %s", verified.Subject.CommonName)
	}
}

// TestVerifyAgentCert_InvalidSignature tests rejection of cert not signed by CA.
func TestVerifyAgentCert_InvalidSignature(t *testing.T) {
	cm := setupTestCertManager(t)

	// Create a self-signed certificate (not signed by CA)
	privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fake-agent"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(1, 0, 0),
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	_, err := cm.VerifyAgentCert(certPEM)
	if err == nil {
		t.Error("Expected error for certificate not signed by CA")
	}
}

// TestVerifyAgentCert_Expired tests rejection of expired certificate.
func TestVerifyAgentCert_Expired(t *testing.T) {
	cm := setupTestCertManager(t)

	// Generate a certificate that's already expired
	privateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "expired-agent"},
		NotBefore:    time.Now().AddDate(-2, 0, 0),
		NotAfter:     time.Now().AddDate(-1, 0, 0), // Expired 1 year ago
	}
	certDER, _ := x509.CreateCertificate(rand.Reader, template, cm.caCert, &privateKey.PublicKey, cm.caKey)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	_, err := cm.VerifyAgentCert(certPEM)
	if err == nil {
		t.Error("Expected error for expired certificate")
	}
}

// TestVerifyAgentCert_Revoked tests rejection of revoked certificate.
func TestVerifyAgentCert_Revoked(t *testing.T) {
	cm := setupTestCertManager(t)

	cert, err := cm.GenerateAgentCert("agent-revoked", 365)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	// Revoke the certificate
	if err := cm.RevokeCert(cert.Serial); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	// Verify should now fail
	_, err = cm.VerifyAgentCert(cert.CertPEM)
	if err == nil {
		t.Error("Expected error for revoked certificate")
	}
}

// TestVerifyAgentCert_MalformedPEM tests rejection of malformed PEM.
func TestVerifyAgentCert_MalformedPEM(t *testing.T) {
	cm := setupTestCertManager(t)

	_, err := cm.VerifyAgentCert([]byte("not valid PEM"))
	if err == nil {
		t.Error("Expected error for malformed PEM")
	}

	_, err = cm.VerifyAgentCert(nil)
	if err == nil {
		t.Error("Expected error for nil PEM")
	}
}

// TestRevokeCert tests certificate revocation.
func TestRevokeCert(t *testing.T) {
	cm := setupTestCertManager(t)

	testSerial := "9876543210"

	if cm.IsRevoked(testSerial) {
		t.Error("Serial should not be revoked before RevokeCert")
	}

	if err := cm.RevokeCert(testSerial); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	if !cm.IsRevoked(testSerial) {
		t.Error("Serial should be revoked after RevokeCert")
	}
}

// TestRevokeCert_Persistence tests that revocation persists to disk.
func TestRevokeCert_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	caDir := filepath.Join(tmpDir, "ca")

	cm1, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("NewCertManager failed: %v", err)
	}

	testSerial := "persistence-test-123"
	if err := cm1.RevokeCert(testSerial); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	// Verify file exists
	revokedPath := filepath.Join(caDir, "revoked.txt")
	if _, err := os.Stat(revokedPath); err != nil {
		t.Errorf("Revoked list file not created: %v", err)
	}

	// Create new manager and verify revocation persisted
	cm2, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("Second NewCertManager failed: %v", err)
	}

	if !cm2.IsRevoked(testSerial) {
		t.Error("Revocation did not persist to disk")
	}
}

// TestIsRevoked tests revocation status checking.
func TestIsRevoked(t *testing.T) {
	cm := setupTestCertManager(t)

	serial1 := "serial-1"
	serial2 := "serial-2"

	// Neither should be revoked initially
	if cm.IsRevoked(serial1) || cm.IsRevoked(serial2) {
		t.Error("Serials should not be revoked initially")
	}

	// Revoke one
	cm.RevokeCert(serial1)

	if !cm.IsRevoked(serial1) {
		t.Error("serial1 should be revoked")
	}
	if cm.IsRevoked(serial2) {
		t.Error("serial2 should not be revoked")
	}
}

// TestIsRevoked_Concurrent tests thread-safety of IsRevoked.
func TestIsRevoked_Concurrent(t *testing.T) {
	cm := setupTestCertManager(t)

	testSerial := "concurrent-serial"
	cm.RevokeCert(testSerial)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !cm.IsRevoked(testSerial) {
				t.Error("Concurrent IsRevoked failed")
			}
		}()
	}
	wg.Wait()
}

// TestGetRevokedSerials tests retrieval of all revoked serials.
func TestGetRevokedSerials(t *testing.T) {
	cm := setupTestCertManager(t)

	// Initially empty
	serials := cm.GetRevokedSerials()
	if len(serials) != 0 {
		t.Errorf("Expected 0 revoked serials, got %d", len(serials))
	}

	// Revoke some certs
	cm.RevokeCert("serial-a")
	cm.RevokeCert("serial-b")
	cm.RevokeCert("serial-c")

	serials = cm.GetRevokedSerials()
	if len(serials) != 3 {
		t.Errorf("Expected 3 revoked serials, got %d", len(serials))
	}

	// Verify all serials are present
	serialMap := make(map[string]bool)
	for _, s := range serials {
		serialMap[s] = true
	}
	for _, expected := range []string{"serial-a", "serial-b", "serial-c"} {
		if !serialMap[expected] {
			t.Errorf("Missing expected serial: %s", expected)
		}
	}
}

// TestLoadRevokedList_EmptyFile tests loading empty revocation file.
func TestLoadRevokedList_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	caDir := filepath.Join(tmpDir, "ca")
	os.MkdirAll(caDir, 0700)

	// Create empty revoked file
	os.WriteFile(filepath.Join(caDir, "revoked.txt"), []byte(""), 0600)

	cm, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("NewCertManager failed: %v", err)
	}

	if len(cm.GetRevokedSerials()) != 0 {
		t.Error("Expected no revoked serials from empty file")
	}
}

// TestLoadRevokedList_WithComments tests that comments are ignored.
func TestLoadRevokedList_WithComments(t *testing.T) {
	tmpDir := t.TempDir()
	caDir := filepath.Join(tmpDir, "ca")

	// Create CA first
	cm1, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("NewCertManager failed: %v", err)
	}

	// Write a revoked.txt with comments and blank lines
	content := `# This is a comment
serial-1,2024-01-01T00:00:00Z

# Another comment
serial-2,2024-01-02T00:00:00Z
`
	os.WriteFile(filepath.Join(caDir, "revoked.txt"), []byte(content), 0600)

	// Reload
	cm2, err := NewCertManager(caDir)
	if err != nil {
		t.Fatalf("Second NewCertManager failed: %v", err)
	}

	serials := cm2.GetRevokedSerials()
	if len(serials) != 2 {
		t.Errorf("Expected 2 revoked serials, got %d: %v", len(serials), serials)
	}

	// Ensure we're not comparing the same manager
	_ = cm1 // silence unused warning
}

// TestCertManager_FullLifecycle tests complete certificate lifecycle.
func TestCertManager_FullLifecycle(t *testing.T) {
	cm := setupTestCertManager(t)

	// 1. Generate agent certificate
	agentID := "lifecycle-agent"
	cert, err := cm.GenerateAgentCert(agentID, 365)
	if err != nil {
		t.Fatalf("GenerateAgentCert failed: %v", err)
	}

	// 2. Verify certificate is valid
	verified, err := cm.VerifyAgentCert(cert.CertPEM)
	if err != nil {
		t.Fatalf("Initial verification failed: %v", err)
	}
	if verified.Subject.CommonName != agentID {
		t.Errorf("Unexpected CommonName: %s", verified.Subject.CommonName)
	}

	// 3. Revoke certificate
	if err := cm.RevokeCert(cert.Serial); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	// 4. Verify certificate is now rejected
	_, err = cm.VerifyAgentCert(cert.CertPEM)
	if err == nil {
		t.Error("Revoked certificate should not verify")
	}

	// 5. Verify revocation is in list
	if !cm.IsRevoked(cert.Serial) {
		t.Error("Certificate should be in revoked list")
	}

	serials := cm.GetRevokedSerials()
	found := false
	for _, s := range serials {
		if s == cert.Serial {
			found = true
			break
		}
	}
	if !found {
		t.Error("Serial not found in GetRevokedSerials")
	}
}

// TestMultipleAgentCerts tests generating multiple agent certificates.
func TestMultipleAgentCerts(t *testing.T) {
	cm := setupTestCertManager(t)

	var certs []*GeneratedCert
	for i := 0; i < 10; i++ {
		cert, err := cm.GenerateAgentCert("agent-%d", 365)
		if err != nil {
			t.Fatalf("GenerateAgentCert %d failed: %v", i, err)
		}
		certs = append(certs, cert)
	}

	// Verify all have unique serials
	serials := make(map[string]bool)
	for _, cert := range certs {
		if serials[cert.Serial] {
			t.Error("Duplicate serial found")
		}
		serials[cert.Serial] = true
	}

	// Verify all can be verified
	for i, cert := range certs {
		if _, err := cm.VerifyAgentCert(cert.CertPEM); err != nil {
			t.Errorf("Certificate %d verification failed: %v", i, err)
		}
	}
}

// setupTestCertManager creates a CertManager with a temp directory for testing.
func setupTestCertManager(t *testing.T) *CertManager {
	t.Helper()
	tmpDir := t.TempDir()
	cm, err := NewCertManager(filepath.Join(tmpDir, "ca"))
	if err != nil {
		t.Fatalf("Failed to create test CertManager: %v", err)
	}
	return cm
}
