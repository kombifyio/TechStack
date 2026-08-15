package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
)

func prodCfg() *config.Config {
	c := &config.Config{}
	c.Server.Environment = "production"
	return c
}

func devCfg() *config.Config {
	c := &config.Config{}
	c.Server.Environment = "development"
	return c
}

// seedCADir creates a CA in a known dir and returns its cert+key PEMs,
// simulating deployment-supplied TECHSTACK_AGENT_CA_CERT/KEY material.
func seedCADir(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := auth.NewCertManager(dir); err != nil {
		t.Fatalf("seed CA: %v", err)
	}
	caCrt, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatalf("read ca.crt: %v", err)
	}
	caKey, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		t.Fatalf("read ca.key: %v", err)
	}
	return string(caCrt), string(caKey)
}

func TestAgentCAFromEnv_ProductionWithoutCADisabled(t *testing.T) {
	t.Setenv(envAgentCACert, "")
	t.Setenv(envAgentCAKey, "")
	t.Setenv(envSelfManagedCA, "")
	cm, enabled, err := agentCAFromEnv(prodCfg(), t.TempDir())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if enabled || cm != nil {
		t.Fatal("production without a supplied CA must stay disabled (fail-closed)")
	}
}

func TestAgentCAFromEnv_ProductionWithSuppliedCAEnabled(t *testing.T) {
	certPEM, keyPEM := seedCADir(t)
	t.Setenv(envAgentCACert, certPEM)
	t.Setenv(envAgentCAKey, keyPEM)
	cm, enabled, err := agentCAFromEnv(prodCfg(), t.TempDir())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !enabled || cm == nil {
		t.Fatal("production with a supplied CA must enable the self-managed path")
	}
	// The loaded CA must be able to mint + verify an agent cert.
	gc, err := cm.GenerateAgentCertForTenant("agent-1", "tenant-A", 10)
	if err != nil {
		t.Fatalf("mint from supplied CA: %v", err)
	}
	if _, err := cm.VerifyAgentCert(gc.CertPEM); err != nil {
		t.Fatalf("supplied CA rejected its own leaf: %v", err)
	}
}

func TestAgentCAFromEnv_PartialCAErrors(t *testing.T) {
	t.Setenv(envAgentCACert, "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----")
	t.Setenv(envAgentCAKey, "")
	if _, _, err := agentCAFromEnv(prodCfg(), t.TempDir()); err == nil {
		t.Fatal("partial CA config must error")
	}
}

func TestAgentCAFromEnv_DevOptInGenerates(t *testing.T) {
	t.Setenv(envAgentCACert, "")
	t.Setenv(envAgentCAKey, "")
	t.Setenv(envSelfManagedCA, "1")
	cm, enabled, err := agentCAFromEnv(devCfg(), t.TempDir())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !enabled || cm == nil {
		t.Fatal("dev opt-in must generate a CA")
	}
}

func TestAgentCAFromEnv_DevWithoutOptInDisabled(t *testing.T) {
	t.Setenv(envAgentCACert, "")
	t.Setenv(envAgentCAKey, "")
	t.Setenv(envSelfManagedCA, "")
	_, enabled, err := agentCAFromEnv(devCfg(), t.TempDir())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if enabled {
		t.Fatal("dev without opt-in must not flip mTLS on (behavior unchanged)")
	}
}

func TestWriteSelfManagedServerTLS_ProducesLoadableFiles(t *testing.T) {
	dir := t.TempDir()
	cm, err := auth.NewCertManager(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	tlsDir := filepath.Join(dir, "tls")
	certFile, keyFile, caFile, err := writeSelfManagedServerTLS(cm, "techstack.kombify.io", []string{"127.0.0.1", "localhost"}, tlsDir)
	if err != nil {
		t.Fatalf("writeSelfManagedServerTLS: %v", err)
	}
	for _, f := range []string{certFile, keyFile, caFile} {
		if _, statErr := os.Stat(f); statErr != nil {
			t.Errorf("expected file %s: %v", f, statErr)
		}
	}
	// Server cert chains to the written CA (this is what an agent does).
	certPEM, _ := os.ReadFile(certFile)
	caPEM, _ := os.ReadFile(caFile)
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA pool")
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:   "techstack.kombify.io",
	}); err != nil {
		t.Errorf("server cert failed verification: %v", err)
	}
}

func TestGRPCServerCommonNameAndSANs(t *testing.T) {
	t.Setenv(envGRPCSAN, "techstack.kombify.io, 10.0.0.5")
	cn, sans := grpcServerCommonNameAndSANs()
	if cn != "techstack.kombify.io" {
		t.Errorf("cn = %q, want techstack.kombify.io", cn)
	}
	for _, want := range []string{"techstack.kombify.io", "10.0.0.5", "localhost", "127.0.0.1"} {
		if !containsString(sans, want) {
			t.Errorf("SANs %v missing %q", sans, want)
		}
	}

	// No config: defaults only, CN localhost.
	t.Setenv(envGRPCSAN, "")
	cn, sans = grpcServerCommonNameAndSANs()
	if cn != "localhost" {
		t.Errorf("default cn = %q, want localhost", cn)
	}
	if !containsString(sans, "localhost") || !containsString(sans, "127.0.0.1") {
		t.Errorf("default SANs %v missing localhost/127.0.0.1", sans)
	}
}
