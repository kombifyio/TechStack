package guardexec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardrelease"
)

// candidateInstallBinding reuses the command-binding payload, pin and expiry
// checks at runtime.install. Public runtime installs retain their existing path.
func candidateInstallBinding(params RuntimeInstallParams, pins []guardbinding.PinnedKey, now time.Time) *Denial {
	if params.CandidateIntent == nil || params.CandidateAdmission == nil || len(pins) == 0 {
		return deny(CodeBindingMismatch, "internal candidate install needs a pinned worker admission")
	}
	intent, b := params.CandidateIntent, params.CandidateAdmission
	if intent.Candidate.Validate() != nil || intent.BundleSHA256 != params.SHA256 || b.TenantID == "" || b.WorkerID == "" || b.ServerID == "" || b.WorkerGeneration <= 0 || b.WorkerGeneration != b.CurrentGeneration {
		return deny(CodeBindingMismatch, "candidate installation identity is incomplete")
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return deny(CodeBindingMismatch, "candidate intent cannot be encoded")
	}
	if d := b.Validate(raw, OpRuntimeInstall, guardbinding.Expectation{}, now); d != nil {
		return deny(CodeBindingMismatch, "%s", d.Message)
	}
	if d := b.VerifySignature(pins); d != nil {
		return deny(CodeBindingMismatch, "%s", d.Message)
	}
	return nil
}

func (s *Server) verifyCandidateInstall(params RuntimeInstallParams) *Denial {
	if s.Pins == nil || s.Replay == nil {
		return deny(CodeUnavailable, "candidate install needs pinned worker authority and durable replay protection")
	}
	pins, err := s.Pins()
	if err != nil {
		return deny(CodeUnavailable, "candidate binding pins cannot be read")
	}
	if d := candidateInstallBinding(params, pins, s.now()); d != nil {
		return d
	}
	return nil
}

func (s *Server) admitCandidateInstall(params RuntimeInstallParams) *Denial {
	if d := s.verifyCandidateInstall(params); d != nil {
		return d
	}
	fresh, err := s.Replay.Claim(params.CandidateAdmission.CommandID)
	if err != nil {
		return deny(CodeUnavailable, "candidate install admission cannot be recorded")
	}
	if !fresh {
		return deny(CodeReplayed, "candidate install admission was already used")
	}
	return nil
}

// Root-only records remember the admission instant, so normal installed
// operation after restart does not renew or extend the original authority.
// The existing signature is reverified at that recorded instant; current floor
// revocations still apply. No daemon-provided receipt or trust override is read.
type installedCandidate struct {
	SchemaVersion string               `json:"schemaVersion"`
	AcceptedAt    time.Time            `json:"acceptedAt"`
	Manifest      json.RawMessage      `json:"manifest"`
	Signature     json.RawMessage      `json:"signature"`
	Params        RuntimeInstallParams `json:"params"`
}

func (h *Host) candidatePath(pin stackkitrelease.Pin, bundleSHA256 string) string {
	return filepath.Join(h.cfg.StateDir, "candidate-admissions", pin.IndexSHA256+"-"+pin.ArchiveSHA256+"-"+bundleSHA256+".json")
}

func (h *Host) rememberCandidate(params RuntimeInstallParams, manifest *guardrelease.Manifest, stage string) error {
	// Recheck root pins and signed intent at the stage/activation boundary, too.
	pins, err := guardbinding.ReadPins(filepath.Join(h.cfg.StateDir, guardbinding.PinFile))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if d := candidateInstallBinding(params, pins, now); d != nil {
		return d
	}
	want, err := manifest.CandidateIntent(h.cfg.Updater.cfg.GOARCH, params.SHA256)
	if err != nil || params.CandidateIntent == nil || *params.CandidateIntent != want {
		return deny(CodeBindingMismatch, "candidate intent differs from authenticated Guard runtime")
	}
	pin, err := stackkitrelease.LoadPin(filepath.Join(stage, "stackkits-release-pin.json"))
	if err != nil {
		return err
	}
	if pin.SchemaVersion != stackkitrelease.CandidatePinSchemaVersion || pin.Candidate == nil || *pin.Candidate != want.Candidate {
		return deny(CodeBindingMismatch, "candidate runtime pin differs from signed provenance")
	}
	raw, err := readBounded(h.cfg.Updater.path(manifestFile), maxManifestFile)
	if err != nil {
		return err
	}
	sig, err := readBounded(h.cfg.Updater.path(manifestBundle), maxBundleFile)
	if err != nil {
		return err
	}
	// Authenticate the exact cached pair at this boundary, not a later parse of
	// an independently replaced manifest. The producer identity stays pinned.
	verifier, err := guardrelease.NewVerifier(guardrelease.Options{Now: func() time.Time { return now }})
	if err != nil {
		return err
	}
	again, err := verifier.VerifyManifest(raw, sig)
	if err != nil {
		return err
	}
	expected, err := again.CandidateIntent(h.cfg.Updater.cfg.GOARCH, params.SHA256)
	if err != nil || expected != want {
		return deny(CodeBindingMismatch, "cached candidate evidence changed before activation")
	}
	path := h.candidatePath(pin, params.SHA256)
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(path, installedCandidate{SchemaVersion: "techstack.installed-preview-candidate/v1", AcceptedAt: now, Manifest: raw, Signature: sig, Params: params}, 0o600)
}

func (h *Host) admitInstalledCandidate(pin stackkitrelease.Pin) error {
	if pin.Candidate == nil || pin.Candidate.Validate() != nil {
		return fmt.Errorf("installed candidate pin has no provenance")
	}
	if pin.SchemaVersion != stackkitrelease.CandidatePinSchemaVersion || filepath.Clean(pin.BinaryPath) != filepath.Join(h.cfg.StackKit.RuntimeRoot(), "bin", "stackkit") {
		return fmt.Errorf("installed candidate pin points outside the root-owned runtime")
	}
	installedBundle, err := agentpkg.InstalledStackKitRuntimeBundleDigestAt(h.cfg.StackKit.RuntimeRoot())
	if err != nil {
		return err
	}
	path := h.candidatePath(pin, installedBundle)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("installed candidate admission is not root-private")
	}
	raw, err := readBounded(path, 3<<20)
	if err != nil {
		return err
	}
	var record installedCandidate
	if err = decodeStrict(raw, &record); err != nil {
		return err
	}
	if record.SchemaVersion != "techstack.installed-preview-candidate/v1" || record.AcceptedAt.IsZero() || record.AcceptedAt.After(time.Now().UTC()) {
		return fmt.Errorf("installed candidate admission is invalid")
	}
	if record.Params.SHA256 != installedBundle {
		return fmt.Errorf("candidate activation marker differs from its root-only admission")
	}
	pins, err := guardbinding.ReadPins(filepath.Join(h.cfg.StateDir, guardbinding.PinFile))
	if err != nil {
		return err
	}
	if d := candidateInstallBinding(record.Params, pins, record.AcceptedAt); d != nil {
		return d
	}
	verifier, err := guardrelease.NewVerifier(guardrelease.Options{Now: func() time.Time { return record.AcceptedAt }})
	if err != nil {
		return err
	}
	manifest, err := verifier.VerifyManifest(record.Manifest, record.Signature)
	if err != nil {
		return err
	}
	want, err := manifest.CandidateIntent(pin.Platform.Arch, record.Params.SHA256)
	if err != nil || record.Params.CandidateIntent == nil || *record.Params.CandidateIntent != want || *pin.Candidate != want.Candidate || pin.Version != want.Version || pin.ArchiveSHA256 != want.ArchiveSHA256 || pin.BinarySHA256 != want.BinarySHA256 || pin.Platform.OS != want.OS {
		return fmt.Errorf("installed candidate differs from root's admitted runtime")
	}
	floor, err := h.cfg.Updater.loadFloor()
	if err != nil {
		return err
	}
	artifact, ok := manifest.Artifact("linux", pin.Platform.Arch)
	if !ok {
		return fmt.Errorf("installed candidate has no signed Guard platform")
	}
	if d := floor.Admit(manifest.Version, artifact.SHA256); d != nil {
		return d
	}
	return nil
}
