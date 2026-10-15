package guardrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
)

const (
	testSigner  = "https://github.com/kombifyio/TechStack/.github/workflows/guard-release.yml@refs/heads/main"
	testForeign = "https://github.com/attacker/fork/.github/workflows/guard-release.yml@refs/heads/main"
)

type release struct {
	sigstore *ca.VirtualSigstore
	verifier *Verifier
	dir      string
	now      time.Time
}

func newRelease(t *testing.T) *release {
	t.Helper()
	sigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// The virtual Sigstore issues certificates with a subject and an issuer only,
	// so the pinned extensions are exercised by TestDefaultIdentity... below.
	identity := Identity{Issuer: GitHubOIDCIssuer, SANRegexp: DefaultIdentity.SANRegexp}
	verifier, err := NewVerifier(Options{material: sigstore, Identity: identity, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &release{sigstore: sigstore, verifier: verifier, dir: t.TempDir(), now: now}
}

func (r *release) artifact(t *testing.T, content string) (path string, digest string) {
	t.Helper()
	path = filepath.Join(r.dir, "techstack-"+digestOf(content)[:8])
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, digestOf(content)
}

func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (r *release) manifest(t *testing.T, mutate func(*Manifest), artifactContent string) []byte {
	t.Helper()
	manifest := Manifest{
		SchemaVersion: ManifestSchema, Version: "0.50.0", MinimumAdmittedVersion: "0.48.0",
		Artifacts: []Artifact{{OS: "linux", Arch: "amd64", SHA256: digestOf(artifactContent), Size: int64(len(artifactContent))}},
		IssuedAt:  r.now.Add(-time.Hour), ExpiresAt: r.now.Add(24 * time.Hour),
	}
	if mutate != nil {
		mutate(&manifest)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Only an artifact the signed manifest names, signed by the pinned release
// workflow, inside the manifest's freshness and version policy, is admitted.
// Every other combination is refused before anything is staged.
func TestArtifactAdmissionRefusesEverythingButTheSignedFreshAdmissibleRelease(t *testing.T) {
	const good = "guard-binary-0.50.0"
	cases := []struct {
		name      string
		manifest  func(*Manifest)
		signer    string // signer of the artifact
		content   string // bytes on disk
		mSigner   string // signer of the manifest
		installed string
		code      string
	}{
		{name: "admitted", signer: testSigner, mSigner: testSigner, content: good},
		{name: "artifact signed by a foreign workflow", signer: testForeign, mSigner: testSigner, content: good, code: CodeSignatureInvalid},
		{name: "manifest signed by a foreign workflow", signer: testSigner, mSigner: testForeign, content: good, code: CodeSignatureInvalid},
		{name: "tampered artifact bytes", signer: testSigner, mSigner: testSigner, content: good + "x", code: CodeDigestMismatch},
		{name: "revoked version", signer: testSigner, mSigner: testSigner, content: good, code: CodeRevoked,
			manifest: func(m *Manifest) { m.RevokedVersions = []string{"0.50.0"} }},
		{name: "revoked digest", signer: testSigner, mSigner: testSigner, content: good, code: CodeRevoked,
			manifest: func(m *Manifest) { m.RevokedDigests = []string{digestOf(good)} }},
		{name: "below the minimum admitted version", signer: testSigner, mSigner: testSigner, content: good, code: CodeBelowMinimum,
			manifest: func(m *Manifest) { m.Version, m.MinimumAdmittedVersion = "0.50.0", "0.51.0" }},
		{name: "expired manifest", signer: testSigner, mSigner: testSigner, content: good, code: CodeManifestStale,
			manifest: func(m *Manifest) {
				m.IssuedAt, m.ExpiresAt = m.IssuedAt.Add(-72*time.Hour), m.IssuedAt.Add(-48*time.Hour)
			}},
		{name: "downgrade of the installed version", signer: testSigner, mSigner: testSigner, content: good, installed: "0.51.3", code: CodeDowngrade},
		{name: "artifact for another platform", signer: testSigner, mSigner: testSigner, content: good, code: CodeNotInManifest,
			manifest: func(m *Manifest) { m.Artifacts[0].Arch = "arm64" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRelease(t)
			raw := r.manifest(t, tc.manifest, good)
			manifestEntity, err := r.sigstore.Sign(tc.mSigner, GitHubOIDCIssuer, raw)
			if err != nil {
				t.Fatal(err)
			}
			path, _ := r.artifact(t, tc.content)
			artifactEntity, err := r.sigstore.Sign(tc.signer, GitHubOIDCIssuer, []byte(good))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := r.verifier.verifyManifestEntity(raw, manifestEntity)
			if err == nil {
				var evidence Evidence
				evidence, err = r.verifier.verifyArtifactEntity(manifest, "linux", "amd64", path, artifactEntity, tc.installed)
				if err == nil && (evidence.Version != "0.50.0" || evidence.SHA256 != digestOf(good)) {
					t.Fatalf("evidence = %+v", evidence)
				}
			}
			var denial *Denial
			switch {
			case tc.code == "" && err != nil:
				t.Fatalf("a valid release was refused: %v", err)
			case tc.code != "" && !errors.As(err, &denial):
				t.Fatalf("expected %s, got %v", tc.code, err)
			case tc.code != "" && denial.Code != tc.code:
				t.Fatalf("expected %s, got %s", tc.code, denial.Code)
			}
		})
	}
}

// Guards accept a signature only from the release workflow run on main of this
// repository, on a GitHub-hosted runner, started by a dispatch or the schedule.
// A run at a tag, in a fork, on a self-hosted runner or started by a push carries
// a different certificate and is refused even when the workflow file matches.
func TestDefaultIdentityAdmitsOnlyTheMainBranchWorkflowRun(t *testing.T) {
	identity, err := newCertificateIdentity(DefaultIdentity)
	if err != nil {
		t.Fatal(err)
	}
	good := certificate.Summary{
		SubjectAlternativeName: testSigner,
		Extensions: certificate.Extensions{
			Issuer: GitHubOIDCIssuer, BuildSignerURI: testSigner, BuildTrigger: "workflow_dispatch", RunnerEnvironment: "github-hosted",
			SourceRepositoryURI: "https://github.com/kombifyio/TechStack", SourceRepositoryOwnerURI: "https://github.com/kombifyio",
			SourceRepositoryRef: "refs/heads/main",
		},
	}
	with := func(mutate func(*certificate.Summary)) certificate.Summary {
		c := good
		mutate(&c)
		return c
	}
	cases := map[string]struct {
		cert certificate.Summary
		ok   bool
	}{
		"main branch, dispatch": {cert: good, ok: true},
		"a release tag workflow": {cert: with(func(c *certificate.Summary) {
			c.SubjectAlternativeName = "https://github.com/kombifyio/TechStack/.github/workflows/guard-release.yml@refs/tags/guard-v99.0.0"
			c.SourceRepositoryRef, c.BuildSignerURI = "refs/tags/guard-v99.0.0", c.SubjectAlternativeName
		})},
		"another branch":       {cert: with(func(c *certificate.Summary) { c.SourceRepositoryRef = "refs/heads/attacker" })},
		"a fork":               {cert: with(func(c *certificate.Summary) { c.SourceRepositoryURI = "https://github.com/attacker/kombify-Techstack" })},
		"a self-hosted runner": {cert: with(func(c *certificate.Summary) { c.RunnerEnvironment = "self-hosted" })},
		"another workflow on main": {cert: with(func(c *certificate.Summary) {
			c.BuildSignerURI = "https://github.com/kombifyio/TechStack/.github/workflows/other.yml@refs/heads/main"
		})},
		"another certificate issuer": {cert: with(func(c *certificate.Summary) { c.Issuer = "https://accounts.google.com" })},
	}
	for name, tc := range cases {
		err := identity.Verify(tc.cert)
		if err == nil {
			err = checkBuildTrigger(DefaultIdentity.BuildTriggers, tc.cert.BuildTrigger)
		}
		if (err == nil) != tc.ok {
			t.Errorf("%s: admitted=%v err=%v", name, err == nil, err)
		}
	}
	for _, trigger := range []string{"push", "pull_request", "release", ""} {
		if checkBuildTrigger(DefaultIdentity.BuildTriggers, trigger) == nil {
			t.Errorf("trigger %q was admitted", trigger)
		}
	}
	if checkBuildTrigger(DefaultIdentity.BuildTriggers, "schedule") != nil {
		t.Error("the nightly schedule was refused")
	}
}

// A release without a signature bundle is refused, not assumed fine.
func TestUnsignedReleaseEvidenceIsRefused(t *testing.T) {
	if _, err := NewVerifier(Options{}); err != nil {
		t.Fatalf("the pinned trust root does not load: %v", err)
	}
	r := newRelease(t)
	raw := r.manifest(t, nil, "x")
	var denial *Denial
	if _, err := r.verifier.VerifyManifest(raw, nil); !errors.As(err, &denial) || denial.Code != CodeEvidenceMissing {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.verifier.VerifyManifest(raw, []byte("{}")); !errors.As(err, &denial) || denial.Code != CodeSignatureInvalid {
		t.Fatalf("err = %v", err)
	}
}
