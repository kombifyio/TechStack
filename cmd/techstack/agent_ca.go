package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
)

// Self-managed agent-CA wiring. When operator-supplied TLS files
// (TECHSTACK_TLS_CERT/KEY/CA) are absent, TechStack can stand up its own agent
// CA and gRPC server cert so kombify Guard mTLS works without hand-provisioned
// certificates:
//
//   - Production: the CA MUST be supplied by the approved deployment secret
//     source as PEM
//     (TECHSTACK_AGENT_CA_CERT / TECHSTACK_AGENT_CA_KEY) so it is persistent
//     across restarts — a regenerated CA would orphan every issued agent cert.
//     Without it, agent gRPC stays disabled (fail-closed, unchanged behavior).
//   - Dev / self-hosted: opt in with TECHSTACK_AGENT_CA_SELF_MANAGED=1 to
//     generate and persist a CA under the data dir. Opt-in so we never silently
//     flip an insecure local gRPC listener to mTLS.
//
// Agents dialing the Core verify its server cert against the CA and their dial
// target, so every reachable gRPC address must be listed in TECHSTACK_GRPC_SAN
// (comma-separated hostnames/IPs); localhost/127.0.0.1 are always included.

const (
	envAgentCACert   = "TECHSTACK_AGENT_CA_CERT"
	envAgentCAKey    = "TECHSTACK_AGENT_CA_KEY"
	envGRPCSAN       = "TECHSTACK_GRPC_SAN"
	envSelfManagedCA = "TECHSTACK_AGENT_CA_SELF_MANAGED"
)

// agentCAFromEnv builds a self-managed CertManager, or reports that the
// self-managed path is not enabled. The returned bool is true only when a CA
// is available to use.
func agentCAFromEnv(cfg *config.Config, dataDir string) (*auth.CertManager, bool, error) {
	caCertPEM := strings.TrimSpace(os.Getenv(envAgentCACert))
	caKeyPEM := strings.TrimSpace(os.Getenv(envAgentCAKey))
	caDir := filepath.Join(dataDir, "agent-ca")

	switch {
	case caCertPEM != "" && caKeyPEM != "":
		// Persistent CA supplied by the deployment secret source: materialize it
		// to the CA dir so the existing loader picks it up, then load.
		if err := os.MkdirAll(caDir, 0o700); err != nil {
			return nil, false, fmt.Errorf("agent-ca: create dir: %w", err)
		}
		// #nosec G703 -- caDir is a fixed "agent-ca" subdir of the operator-set
		// data dir; the filenames are constants, not user input.
		if err := os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte(ensureTrailingNewline(caCertPEM)), 0o600); err != nil {
			return nil, false, fmt.Errorf("agent-ca: write ca.crt: %w", err)
		}
		// #nosec G703 -- caDir is a fixed "agent-ca" subdir of the operator-set
		// data dir; the filenames are constants, not user input.
		if err := os.WriteFile(filepath.Join(caDir, "ca.key"), []byte(ensureTrailingNewline(caKeyPEM)), 0o600); err != nil {
			return nil, false, fmt.Errorf("agent-ca: write ca.key: %w", err)
		}
		cm, err := auth.NewCertManager(caDir)
		if err != nil {
			return nil, false, fmt.Errorf("agent-ca: load supplied CA: %w", err)
		}
		return cm, true, nil

	case caCertPEM != "" || caKeyPEM != "":
		// Partial config is an operator error — fail loudly, never half-open.
		return nil, false, fmt.Errorf("agent-ca: both %s and %s must be set (got only one)", envAgentCACert, envAgentCAKey)

	case cfg != nil && cfg.IsProduction():
		// Production without a supplied CA: never generate an ephemeral one (it
		// would orphan issued certs on restart). Stay disabled, fail-closed.
		return nil, false, nil

	case envBoolDefault(envSelfManagedCA, false):
		// Dev / self-hosted, explicitly opted in: generate (or load an
		// already-persisted) CA under the data dir. Opt-in so we never
		// silently flip an insecure local gRPC listener to mTLS.
		cm, err := auth.NewCertManager(caDir)
		if err != nil {
			return nil, false, fmt.Errorf("agent-ca: generate dev CA: %w", err)
		}
		return cm, true, nil

	default:
		// No CA supplied and no opt-in: behavior unchanged (gRPC mTLS off).
		return nil, false, nil
	}
}

// writeSelfManagedServerTLS mints a gRPC server cert from the CA and writes the
// cert, key, and CA cert to files under dir, returning their paths for
// grpcserver.New (which loads TLS from files).
func writeSelfManagedServerTLS(cm *auth.CertManager, commonName string, sans []string, dir string) (certFile, keyFile, caFile string, err error) {
	if cm == nil {
		return "", "", "", fmt.Errorf("agent-ca: cert manager is required")
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return "", "", "", fmt.Errorf("agent-ca: create tls dir: %w", mkErr)
	}
	gc, err := cm.GenerateServerCert(commonName, sans, 397) // ~13 months
	if err != nil {
		return "", "", "", fmt.Errorf("agent-ca: mint server cert: %w", err)
	}

	certFile = filepath.Join(dir, "server.crt")
	keyFile = filepath.Join(dir, "server.key")
	caFile = filepath.Join(dir, "ca.crt")
	// #nosec G703 -- dir is a fixed subdir of the operator-set data dir; the
	// filenames are constants, not user input.
	if wErr := os.WriteFile(certFile, gc.CertPEM, 0o600); wErr != nil {
		return "", "", "", fmt.Errorf("agent-ca: write server.crt: %w", wErr)
	}
	// #nosec G703 -- see above.
	if wErr := os.WriteFile(keyFile, gc.KeyPEM, 0o600); wErr != nil {
		return "", "", "", fmt.Errorf("agent-ca: write server.key: %w", wErr)
	}
	// #nosec G703 -- see above.
	if wErr := os.WriteFile(caFile, cm.CACertPEM(), 0o600); wErr != nil {
		return "", "", "", fmt.Errorf("agent-ca: write ca.crt: %w", wErr)
	}
	return certFile, keyFile, caFile, nil
}

// grpcServerCommonNameAndSANs resolves the server cert identity. commonName is
// the first configured SAN (or localhost); the SAN set always includes
// localhost and 127.0.0.1 for in-host/dev dialing.
func grpcServerCommonNameAndSANs() (commonName string, sans []string) {
	raw := strings.TrimSpace(os.Getenv(envGRPCSAN))
	var configured []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			configured = append(configured, p)
		}
	}
	sans = append(sans, configured...)
	for _, def := range []string{"localhost", "127.0.0.1"} {
		if !containsString(sans, def) {
			sans = append(sans, def)
		}
	}
	commonName = "localhost"
	if len(configured) > 0 {
		commonName = configured[0]
	}
	return commonName, sans
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
