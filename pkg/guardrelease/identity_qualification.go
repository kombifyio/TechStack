//go:build guardqualification

package guardrelease

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
)

// This file exists only in binaries built for the Guard qualification harness
// (scripts/guard-qualification). Production builds never carry it: without the
// guardqualification tag identity_release.go pins the release workflow and
// nothing else.
//
// The harness signs its qualification releases keyless with the public-good
// Sigstore from its own GitHub Actions run, so a Guard under qualification must
// trust that run instead of guard-release.yml. The run is named at link time by
//
//	-X <this package>.qualificationWorkflowRef=$GITHUB_WORKFLOW_REF
//	-X <this package>.qualificationCommit=$GITHUB_SHA
//
// The trusted root, the Fulcio and Rekor requirements and every certificate
// check stay the production ones; only the signer identity differs.

const (
	qualificationRepository   = "kombifyio/TechStack"
	qualificationWorkflowFile = qualificationRepository + "/.github/workflows/guard-qualification.yml"
)

var (
	qualificationWorkflowRef string
	qualificationCommit      string

	qualificationBranchRef = regexp.MustCompile(`^refs/heads/[A-Za-z0-9._/-]{1,200}$`)
	qualificationSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// QualificationIdentity is the signer of one run of the qualification workflow:
// that workflow file, at the branch and commit the run started on, in this
// repository, on a GitHub-hosted runner, started by a manual dispatch. workflowRef
// is GITHUB_WORKFLOW_REF and commit is GITHUB_SHA. Anything malformed is an
// error; there is no permissive reading of a pin.
func QualificationIdentity(workflowRef, commit string) (Identity, error) {
	at := strings.LastIndex(workflowRef, "@")
	if at < 0 {
		at = len(workflowRef)
	}
	path, ref := workflowRef[:at], strings.TrimPrefix(workflowRef[at:], "@")
	if path != qualificationWorkflowFile || !qualificationBranchRef.MatchString(ref) {
		return Identity{}, fmt.Errorf("qualification signer: %q is not the guard-qualification workflow at a branch of %s", workflowRef, qualificationRepository)
	}
	if !qualificationSHA.MatchString(commit) {
		return Identity{}, errors.New("qualification signer: the run commit must be a full lowercase SHA-1")
	}
	signer := "https://github.com/" + workflowRef
	return Identity{
		Issuer:    GitHubOIDCIssuer,
		SANRegexp: "^" + regexp.QuoteMeta(signer) + "$",
		Extensions: certificate.Extensions{
			SourceRepositoryURI:      releaseRepositoryURI,
			SourceRepositoryOwnerURI: "https://github.com/kombifyio",
			SourceRepositoryRef:      ref,
			SourceRepositoryDigest:   commit,
			BuildSignerURI:           signer,
			RunnerEnvironment:        "github-hosted",
		},
		BuildTriggers: []string{"workflow_dispatch"},
	}, nil
}

// pinnedIdentity is the release workflow unless the binary was linked for a
// qualification run. A half-set or malformed link-time pin is an error, so a
// misbuilt qualification binary verifies nothing instead of something weaker.
func pinnedIdentity() (Identity, error) {
	if qualificationWorkflowRef == "" && qualificationCommit == "" {
		return DefaultIdentity, nil
	}
	return QualificationIdentity(qualificationWorkflowRef, qualificationCommit)
}
