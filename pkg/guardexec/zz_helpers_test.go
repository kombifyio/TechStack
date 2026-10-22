package guardexec

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"context"
	"path/filepath"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"encoding/hex"
	"os"
	"crypto/sha256"
	"sync"
	"testing"
	"time"
)

type fakeSystemd struct {
	mu       sync.Mutex
	restarts int
	started  []string
	props    map[string]string
}

func (s *fakeSystemd) Restart(context.Context, string) error {
	s.mu.Lock()
	s.restarts++
	s.mu.Unlock()
	return nil
}

func (s *fakeSystemd) Show(context.Context, string, ...string) (map[string]string, error) {
	return s.props, nil
}

func (s *fakeSystemd) RunTransient(_ context.Context, unit string, _ []string) error {
	s.mu.Lock()
	s.started = append(s.started, unit)
	s.mu.Unlock()
	return nil
}

type host struct {
	updater  *Updater
	systemd  *fakeSystemd
	bin      string
	handoff  string
	state    string
	identity string
}

func newHost(t *testing.T, allowUnsigned bool, mutate func(*UpdaterConfig)) *host {
	t.Helper()
	root := t.TempDir()
	h := &host{systemd: &fakeSystemd{}, bin: filepath.Join(root, "bin"), handoff: filepath.Join(root, "handoff"), state: filepath.Join(root, "exec-state"), identity: filepath.Join(root, "guard-state")}
	for _, dir := range []string{h.bin, h.handoff, h.identity} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"techstack", "techstack-stackkit-operations"} {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte("guard-0.50.0"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.identity, "identity.key"), []byte("host-private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := UpdaterConfig{
		StateDir: h.state, HandoffDir: h.handoff, AgentPath: filepath.Join(h.bin, "techstack"), OperationsPath: filepath.Join(h.bin, "techstack-stackkit-operations"),
		CurrentVersion: "0.50.0", GOOS: "linux", GOARCH: "amd64", AllowUnsigned: allowUnsigned, Systemd: h.systemd,
		TrialBudget: 150 * time.Millisecond, TrialPoll: 10 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	updater, err := NewUpdater(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.updater = updater
	return h
}

func (h *host) offer(t *testing.T, content, version string) StageParams {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.handoff, "artifact"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return StageParams{ArtifactFile: "artifact", Version: version}
}

func (h *host) installed(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.bin, "techstack"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// trustingVerifier accepts any well-formed manifest as if it were signed; the
// test is about what the updater remembers, not about signatures.
type trustingVerifier struct{}

func (trustingVerifier) VerifyManifest(raw, _ []byte) (*guardrelease.Manifest, error) {
	return guardrelease.ParseManifest(raw)
}

func (trustingVerifier) VerifyArtifact(manifest *guardrelease.Manifest, goos, goarch, path string, _ []byte, _ string) (guardrelease.Evidence, error) {
	entry, _ := manifest.Artifact(goos, goarch)
	digest, err := fileDigest(path)
	if err != nil || digest != entry.SHA256 {
		return guardrelease.Evidence{}, &guardrelease.Denial{Code: guardrelease.CodeDigestMismatch, Message: "digest"}
	}
	if denial := manifest.Admit(manifest.Version, digest); denial != nil {
		return guardrelease.Evidence{}, denial
	}
	return guardrelease.Evidence{Version: manifest.Version, SHA256: digest, Size: entry.Size}, nil
}

func digestHex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

