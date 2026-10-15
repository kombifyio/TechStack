//go:build guardqualification

package guardrelease

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
)

// A qualification build admits exactly the run it was linked for: that workflow
// file, branch and commit, dispatched on a GitHub-hosted runner. The release
// workflow, another branch or commit, another trigger and a malformed link-time
// pin are all refused.
func TestQualificationIdentityAdmitsOnlyItsOwnRun(t *testing.T) {
	const (
		workflow = "kombifyio/TechStack/.github/workflows/guard-qualification.yml@refs/heads/guard-qual-h"
		commit   = "0123456789abcdef0123456789abcdef01234567"
	)
	pinned, err := QualificationIdentity(workflow, commit)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newCertificateIdentity(pinned)
	if err != nil {
		t.Fatal(err)
	}
	signer := "https://github.com/" + workflow
	good := certificate.Summary{
		SubjectAlternativeName: signer,
		Extensions: certificate.Extensions{
			Issuer: GitHubOIDCIssuer, BuildSignerURI: signer, BuildTrigger: "workflow_dispatch", RunnerEnvironment: "github-hosted",
			SourceRepositoryURI: releaseRepositoryURI, SourceRepositoryOwnerURI: "https://github.com/kombifyio",
			SourceRepositoryRef: "refs/heads/guard-qual-h", SourceRepositoryDigest: commit,
		},
	}
	with := func(mutate func(*certificate.Summary)) certificate.Summary {
		c := good
		mutate(&c)
		return c
	}
	admitted := func(c certificate.Summary) bool {
		return identity.Verify(c) == nil && checkBuildTrigger(pinned.BuildTriggers, c.BuildTrigger) == nil
	}
	cases := map[string]struct {
		cert certificate.Summary
		ok   bool
	}{
		"the pinned run": {cert: good, ok: true},
		"the release workflow": {cert: with(func(c *certificate.Summary) {
			c.SubjectAlternativeName = "https://github.com/kombifyio/TechStack/.github/workflows/guard-release.yml@refs/heads/main"
			c.BuildSignerURI, c.SourceRepositoryRef = c.SubjectAlternativeName, "refs/heads/main"
		})},
		"another commit":       {cert: with(func(c *certificate.Summary) { c.SourceRepositoryDigest = "89abcdef0123456789abcdef0123456789abcdef" })},
		"another branch":       {cert: with(func(c *certificate.Summary) { c.SourceRepositoryRef = "refs/heads/attacker" })},
		"a push trigger":       {cert: with(func(c *certificate.Summary) { c.BuildTrigger = "push" })},
		"a self-hosted runner": {cert: with(func(c *certificate.Summary) { c.RunnerEnvironment = "self-hosted" })},
		"a fork":               {cert: with(func(c *certificate.Summary) { c.SourceRepositoryURI = "https://github.com/attacker/kombify-Techstack" })},
	}
	for name, tc := range cases {
		if got := admitted(tc.cert); got != tc.ok {
			t.Errorf("%s: admitted=%v, want %v", name, got, tc.ok)
		}
	}
	for name, bad := range map[string][2]string{
		"another workflow": {"kombifyio/TechStack/.github/workflows/guard-release.yml@refs/heads/main", commit},
		"a tag ref":        {"kombifyio/TechStack/.github/workflows/guard-qualification.yml@refs/tags/v1", commit},
		"a fork":           {"attacker/kombify-Techstack/.github/workflows/guard-qualification.yml@refs/heads/x", commit},
		"no ref":           {"kombifyio/TechStack/.github/workflows/guard-qualification.yml", commit},
		"a short commit":   {workflow, "0123456"},
		"no commit":        {workflow, ""},
	} {
		if _, err := QualificationIdentity(bad[0], bad[1]); err == nil {
			t.Errorf("%s: a malformed pin was accepted", name)
		}
	}
}
