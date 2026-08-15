package auth

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
)

func newTestCM(t *testing.T) *CertManager {
	t.Helper()
	cm, err := NewCertManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	return cm
}

func parseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func TestGenerateAgentCertForTenant_BindsIdentity(t *testing.T) {
	cm := newTestCM(t)
	gc, err := cm.GenerateAgentCertForTenant("agent-1", "tenant-abc", 30)
	if err != nil {
		t.Fatalf("GenerateAgentCertForTenant: %v", err)
	}

	cert := parseLeaf(t, gc.CertPEM)
	// The gRPC Register handler binds CN==agent_id and Organization==tenant.
	if cert.Subject.CommonName != "agent-1" {
		t.Errorf("CN = %q, want agent-1", cert.Subject.CommonName)
	}
	if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != "tenant-abc" {
		t.Errorf("Organization = %v, want [tenant-abc]", cert.Subject.Organization)
	}
	// Must be a client-auth leaf.
	foundClientAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			foundClientAuth = true
		}
	}
	if !foundClientAuth {
		t.Error("agent cert missing ClientAuth EKU")
	}
	// Serial on the bundle must match the cert.
	if gc.Serial != cert.SerialNumber.String() {
		t.Errorf("bundle serial %q != cert serial %q", gc.Serial, cert.SerialNumber.String())
	}
	// The CA must accept its own leaf.
	if _, err := cm.VerifyAgentCert(gc.CertPEM); err != nil {
		t.Errorf("VerifyAgentCert rejected freshly issued cert: %v", err)
	}
}

func TestGenerateAgentCert_DefaultOrgBackCompat(t *testing.T) {
	cm := newTestCM(t)
	gc, err := cm.GenerateAgentCert("legacy-agent", 30)
	if err != nil {
		t.Fatalf("GenerateAgentCert: %v", err)
	}
	cert := parseLeaf(t, gc.CertPEM)
	if cert.Subject.CommonName != "legacy-agent" {
		t.Errorf("CN = %q, want legacy-agent", cert.Subject.CommonName)
	}
	if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != defaultCertOrg {
		t.Errorf("Organization = %v, want [%s]", cert.Subject.Organization, defaultCertOrg)
	}
}

func TestGenerateAgentCertForTenant_RequiresAgentID(t *testing.T) {
	cm := newTestCM(t)
	if _, err := cm.GenerateAgentCertForTenant("", "tenant", 30); err == nil {
		t.Error("expected error for empty agent id")
	}
}

func TestGenerateServerCert_SANsAndServerAuth(t *testing.T) {
	cm := newTestCM(t)
	gc, err := cm.GenerateServerCert("techstack.kombify.io", []string{"127.0.0.1", "localhost", "10.0.0.5"}, 30)
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	cert := parseLeaf(t, gc.CertPEM)

	foundServerAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			foundServerAuth = true
		}
	}
	if !foundServerAuth {
		t.Error("server cert missing ServerAuth EKU")
	}

	// DNS SANs: commonName + "localhost".
	dnsWant := map[string]bool{"techstack.kombify.io": false, "localhost": false}
	for _, d := range cert.DNSNames {
		if _, ok := dnsWant[d]; ok {
			dnsWant[d] = true
		}
	}
	for d, seen := range dnsWant {
		if !seen {
			t.Errorf("missing DNS SAN %q (got %v)", d, cert.DNSNames)
		}
	}

	// IP SANs: 127.0.0.1 and 10.0.0.5.
	ipWant := map[string]bool{"127.0.0.1": false, "10.0.0.5": false}
	for _, ip := range cert.IPAddresses {
		if _, ok := ipWant[ip.String()]; ok {
			ipWant[ip.String()] = true
		}
	}
	for ip, seen := range ipWant {
		if !seen {
			t.Errorf("missing IP SAN %q (got %v)", ip, cert.IPAddresses)
		}
	}

	// The server cert must chain to the CA (agent verifies it as the server).
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cm.CACertPEM()) {
		t.Fatal("failed to build CA pool")
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:   "techstack.kombify.io",
	}); err != nil {
		t.Errorf("server cert failed chain verification: %v", err)
	}
	// IP verification path.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:   "127.0.0.1",
	}); err != nil {
		// DNSName with an IP literal exercises IP SAN matching in crypto/x509.
		if net.ParseIP("127.0.0.1") != nil {
			t.Errorf("server cert failed IP SAN verification: %v", err)
		}
	}
}

func TestGenerateServerCert_RequiresCommonName(t *testing.T) {
	cm := newTestCM(t)
	if _, err := cm.GenerateServerCert("", nil, 30); err == nil {
		t.Error("expected error for empty common name")
	}
}
