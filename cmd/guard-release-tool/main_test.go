package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// A valid release-workflow certificate from another commit must not qualify a
// Golden Image's requested binary source. The normal policy still admits a
// manifest refreshed from that later commit and preserves legacy verification.
func TestArtifactSourceAdmissionPreservesManifestRefresh(t *testing.T) {
	source, later := strings.Repeat("a", 40), strings.Repeat("b", 40)
	admit := func(expected, actual string) error {
		t.Helper()
		options, err := artifactVerificationOptions(expected)
		if err != nil {
			return err
		}
		identity := options.Identity
		if expected == "" {
			identity = guardrelease.DefaultIdentity
		}
		san, err := verify.NewSANMatcher("", identity.SANRegexp)
		if err != nil {
			t.Fatal(err)
		}
		issuer, err := verify.NewIssuerMatcher(identity.Issuer, "")
		if err != nil {
			t.Fatal(err)
		}
		policy, err := verify.NewCertificateIdentity(san, issuer, identity.Extensions)
		if err != nil {
			t.Fatal(err)
		}
		return policy.Verify(certificate.Summary{
			SubjectAlternativeName: "https://github.com/kombifyio/TechStack/.github/workflows/guard-release.yml@refs/heads/main",
			Extensions: certificate.Extensions{
				Issuer:            guardrelease.GitHubOIDCIssuer,
				BuildSignerURI:    "https://github.com/kombifyio/TechStack/.github/workflows/guard-release.yml@refs/heads/main",
				RunnerEnvironment: "github-hosted", BuildTrigger: "workflow_dispatch",
				SourceRepositoryURI:      "https://github.com/kombifyio/TechStack",
				SourceRepositoryOwnerURI: "https://github.com/kombifyio",
				SourceRepositoryRef:      "refs/heads/main", SourceRepositoryDigest: actual,
			},
		})
	}
	if err := admit(source, source); err != nil {
		t.Fatalf("the requested binary source was refused: %v", err)
	}
	for _, actual := range []string{later, ""} {
		if err := admit(source, actual); err == nil {
			t.Fatal("an artifact without the requested certificate source was admitted")
		}
	}
	if err := admit("", later); err != nil {
		t.Fatalf("the ordinary refresh/legacy policy refused a later release source: %v", err)
	}
	for _, malformed := range []string{"", "abc123", strings.ToUpper(source), source + "\n"} {
		if _, err := run("verify", t.TempDir(), "--expected-source", malformed); !errors.Is(err, errInvalidExpectedSource) {
			t.Fatalf("malformed expected source was not refused at input admission: %v", err)
		}
	}
	// The explicit publication-refresh clock cannot admit future issuance or
	// escape today's minimum/revocation policy. Runtime defaults remain real time.
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	m := guardrelease.Manifest{SchemaVersion: guardrelease.ManifestSchema, Version: "0.50.0", MinimumAdmittedVersion: "0.0.0",
		Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: digest, Size: 5}},
		IssuedAt:  now.Add(-72 * time.Hour), ExpiresAt: now.Add(-48 * time.Hour)}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	policy := []byte(`{"minimumAdmittedVersion":"0.0.0","revokedVersions":[],"revokedDigests":[]}`)
	options, err := publishedRefreshOptions(raw, policy, now)
	if err != nil || options.Now == nil || !options.Now().Equal(m.IssuedAt) {
		t.Fatalf("historical refresh clock was not admitted: %v", err)
	}
	for _, deniedPolicy := range []string{
		`{"minimumAdmittedVersion":"0.51.0","revokedVersions":[],"revokedDigests":[]}`,
		`{"minimumAdmittedVersion":"0.0.0","revokedVersions":["0.50.0"],"revokedDigests":[]}`,
		`{"minimumAdmittedVersion":"0.0.0","revokedVersions":[],"revokedDigests":["` + digest + `"]}`,
	} {
		if _, err := publishedRefreshOptions(raw, []byte(deniedPolicy), now); err == nil {
			t.Fatal("refresh bypassed the current release policy")
		}
	}
	m.IssuedAt, m.ExpiresAt = now.Add(time.Hour), now.Add(2*time.Hour)
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishedRefreshOptions(raw, policy, now); err == nil {
		t.Fatal("future-issued publication was admitted for refresh")
	}
}

func TestHistoricalReuseRequiresCurrentPolicyWithoutExpiryWaiver(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	manifest := guardrelease.Manifest{SchemaVersion: guardrelease.ManifestSchema, Version: "0.39.5", MinimumAdmittedVersion: "0.0.0",
		Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: digest, Size: 5}},
		IssuedAt:  now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	policy := []byte(`{"minimumAdmittedVersion":"0.0.0","revokedVersions":[],"revokedDigests":[]}`)
	if _, err := currentPolicyVerificationOptions(raw, policy, now); err != nil {
		t.Fatalf("fresh allowed release refused: %v", err)
	}
	for _, deniedPolicy := range []string{
		`{"minimumAdmittedVersion":"0.39.6","revokedVersions":[],"revokedDigests":[]}`,
		`{"minimumAdmittedVersion":"0.0.0","revokedVersions":["0.39.5"],"revokedDigests":[]}`,
		`{"minimumAdmittedVersion":"0.0.0","revokedVersions":[],"revokedDigests":["` + digest + `"]}`,
	} {
		if _, err := currentPolicyVerificationOptions(raw, []byte(deniedPolicy), now); err == nil {
			t.Fatal("reuse ignored the current minimum or revocation policy")
		}
	}
	manifest.IssuedAt, manifest.ExpiresAt = now.Add(-72*time.Hour), now.Add(-48*time.Hour)
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := currentPolicyVerificationOptions(raw, policy, now); err == nil {
		t.Fatal("historical reuse waived expired manifest admission")
	}
	if _, err := publishedRefreshOptions(raw, policy, now); err != nil {
		t.Fatalf("explicit publication refresh lost its existing clock: %v", err)
	}
	if _, err := run("verify", t.TempDir(), "--require-current-policy", "--refresh-published"); err == nil {
		t.Fatal("reuse accepted the publication-refresh clock flag")
	}
}
