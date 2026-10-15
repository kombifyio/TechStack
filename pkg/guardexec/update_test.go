package guardexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
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

// A release without verifiable evidence never reaches the install directory, and
// a path outside the handoff directory is not a release.
func TestUnsignedOrForeignArtifactIsRefusedBeforeStaging(t *testing.T) {
	h := newHost(t, false, func(cfg *UpdaterConfig) {
		verifier, err := guardrelease.NewVerifier(guardrelease.Options{})
		if err != nil {
			t.Fatal(err)
		}
		cfg.Verifier = verifier
	})
	params := h.offer(t, "guard-0.51.0", "0.51.0")
	if _, err := h.updater.Stage(t.Context(), params); !IsDenial(err, guardrelease.CodeEvidenceMissing) {
		t.Fatalf("unsigned artifact err = %v", err)
	}
	params.ManifestFile, params.ManifestBundleFile, params.ArtifactBundleFile = "artifact", "artifact", "artifact"
	if _, err := h.updater.Stage(t.Context(), params); err == nil {
		t.Fatal("a release signed by nobody was staged")
	}
	params.ArtifactFile = "../artifact"
	if _, err := h.updater.Stage(t.Context(), params); !IsDenial(err, CodeInvalidRequest) {
		t.Fatalf("traversal err = %v", err)
	}
	for _, name := range []string{"techstack.next", "techstack-stackkit-operations.next"} {
		if _, err := os.Stat(filepath.Join(h.bin, name)); err == nil {
			t.Fatalf("%s was staged from an unverified release", name)
		}
	}
	if got := h.installed(t); got != "guard-0.50.0" {
		t.Fatalf("installed executable changed to %q", got)
	}
}

// A candidate that cannot complete an authenticated round trip is replaced by the
// previous Guard, the host identity is untouched, and the failed candidate is not
// staged again at once. A candidate that confirms stays.
func TestFailedTrialRestoresThePreviousGuardAndKeepsItsIdentity(t *testing.T) {
	h := newHost(t, true, nil)
	identityBefore, _ := os.ReadFile(filepath.Join(h.identity, "identity.key"))

	stage := func(content, version string) (StageResult, string) {
		t.Helper()
		staged, err := h.updater.Stage(t.Context(), h.offer(t, content, version))
		if err != nil {
			t.Fatal(err)
		}
		committed, err := h.updater.Commit(t.Context(), CommitParams{Version: staged.Version, SHA256: staged.SHA256})
		if err != nil {
			t.Fatal(err)
		}
		if got := h.installed(t); got != content {
			t.Fatalf("installed = %q after commit, want %q", got, content)
		}
		return staged, committed.TrialID
	}

	// Trial 1: the candidate never reports.
	_, trialID := stage("guard-0.51.0-broken", "0.51.0")
	if err := h.updater.RunTrial(t.Context(), trialID); err != nil {
		t.Fatal(err)
	}
	if got := h.installed(t); got != "guard-0.50.0" {
		t.Fatalf("a failed candidate was not replaced: %q", got)
	}
	if status := h.updater.Status(); status.State != StateRolledBack || status.Version != "0.50.0" {
		t.Fatalf("status = %+v", status)
	}
	if h.updater.DenyMutations() {
		t.Fatal("a rollback with an admissible fallback denied mutations")
	}
	if _, err := h.updater.Stage(t.Context(), h.offer(t, "guard-0.51.0-broken", "0.51.0")); !IsDenial(err, CodeNotAllowed) {
		t.Fatalf("a failed candidate was staged again: %v", err)
	}

	// Trial 2: a good candidate confirms through the handoff file.
	_, trialID = stage("guard-0.52.0", "0.52.0")
	ready, _ := json.Marshal(TrialReady{TrialID: trialID, Version: "0.52.0", At: time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(h.handoff, HandoffTrialReadyFile), ready, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.updater.RunTrial(t.Context(), trialID); err != nil {
		t.Fatal(err)
	}
	if got := h.installed(t); got != "guard-0.52.0" || h.updater.Status().State != StateCommitted {
		t.Fatalf("a confirmed candidate did not stay: %q %+v", got, h.updater.Status())
	}
	if identityAfter, _ := os.ReadFile(filepath.Join(h.identity, "identity.key")); string(identityAfter) != string(identityBefore) {
		t.Fatal("the host identity changed during update and rollback")
	}
}

// When the previous executable is no longer admissible (here: below the minimum
// the newest verified manifest admits), a failed trial never re-admits it: the
// host denies mutations and reports recovery-needed.
func TestFailedTrialWithoutAdmissibleFallbackDeniesMutations(t *testing.T) {
	h := newHost(t, true, nil)
	manifest, _ := json.Marshal(guardrelease.Manifest{
		SchemaVersion: guardrelease.ManifestSchema, Version: "0.52.0", MinimumAdmittedVersion: "0.51.0",
		Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: digestHex("x"), Size: 1}},
		IssuedAt:  time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err := os.WriteFile(filepath.Join(h.state, manifestFile), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	staged, err := h.updater.Stage(t.Context(), h.offer(t, "guard-0.52.0", "0.52.0"))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := h.updater.Commit(t.Context(), CommitParams{Version: staged.Version, SHA256: staged.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.updater.RunTrial(t.Context(), committed.TrialID); err != nil {
		t.Fatal(err)
	}
	if status := h.updater.Status(); status.State != StateRecoveryNeeded {
		t.Fatalf("status = %+v", status)
	}
	if !h.updater.DenyMutations() {
		t.Fatal("a host without an admissible fallback still admits mutations")
	}
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

// A revocation is permanent on a host. Once a manifest that revokes a release
// was accepted, neither an older manifest that predates the revocation nor a
// newer one that merely omits it re-admits that release.
func TestRevokedReleaseStaysRefusedWhenAnOlderOrSilentManifestIsPresented(t *testing.T) {
	h := newHost(t, false, func(cfg *UpdaterConfig) { cfg.Verifier = trustingVerifier{} })
	now := time.Now().UTC()
	const revoked = "guard-0.51.0"
	manifest := func(issuedAgo time.Duration, revokedVersions ...string) string {
		raw, err := json.Marshal(guardrelease.Manifest{
			SchemaVersion: guardrelease.ManifestSchema, Version: "0.51.0", MinimumAdmittedVersion: "0.48.0", RevokedVersions: revokedVersions,
			Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: digestHex(revoked), Size: int64(len(revoked))}},
			IssuedAt:  now.Add(-issuedAgo), ExpiresAt: now.Add(72 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		name := "manifest-" + digestHex(string(raw))[:8]
		if err := os.WriteFile(filepath.Join(h.handoff, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return name
	}
	if err := os.WriteFile(filepath.Join(h.handoff, "sig"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage := func(manifestName string) error {
		params := h.offer(t, revoked, "0.51.0")
		params.ManifestFile, params.ManifestBundleFile, params.ArtifactBundleFile = manifestName, "sig", "sig"
		_, err := h.updater.Stage(t.Context(), params)
		return err
	}

	older := manifest(3 * time.Hour)
	if err := stage(older); err != nil {
		t.Fatalf("the release was refused before any revocation: %v", err)
	}
	if _, err := h.updater.AuthenticateManifest(manifest(time.Hour, "0.51.0"), "sig"); err != nil {
		t.Fatal(err)
	}
	if err := stage(older); !IsDenial(err, guardrelease.CodeManifestSuperseded) {
		t.Fatalf("an older manifest erased the revocation: %v", err)
	}
	if err := stage(manifest(time.Minute)); !IsDenial(err, guardrelease.CodeRevoked) {
		t.Fatalf("a newer manifest that omits the revocation re-admitted the release: %v", err)
	}
}

func digestHex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
