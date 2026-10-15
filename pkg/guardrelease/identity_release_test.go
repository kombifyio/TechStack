//go:build !guardqualification

package guardrelease

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
)

// A default build verifies only the release workflow. A certificate of a
// qualification-workflow run, signed by the same public-good Sigstore and
// otherwise well formed, is refused: no production Guard trusts a harness run.
func TestDefaultBuildRefusesAQualificationRunSigner(t *testing.T) {
	pinned, err := pinnedIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newCertificateIdentity(pinned)
	if err != nil {
		t.Fatal(err)
	}
	cert := func(workflow, ref string) certificate.Summary {
		signer := "https://github.com/kombifyio/TechStack/.github/workflows/" + workflow + "@" + ref
		return certificate.Summary{
			SubjectAlternativeName: signer,
			Extensions: certificate.Extensions{
				Issuer: GitHubOIDCIssuer, BuildSignerURI: signer, BuildTrigger: "workflow_dispatch", RunnerEnvironment: "github-hosted",
				SourceRepositoryURI: releaseRepositoryURI, SourceRepositoryOwnerURI: "https://github.com/kombifyio",
				SourceRepositoryRef: ref, SourceRepositoryDigest: "0123456789abcdef0123456789abcdef01234567",
			},
		}
	}
	if err := identity.Verify(cert("guard-release.yml", "refs/heads/main")); err != nil {
		t.Fatalf("the release workflow was refused: %v", err)
	}
	if err := identity.Verify(cert("guard-qualification.yml", "refs/heads/main")); err == nil {
		t.Fatal("a guard-qualification run was admitted by a default build")
	}
}
