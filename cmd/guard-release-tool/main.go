// Command guard-release-tool is the build-time helper of the Guard release
// workflow. It is not shipped.
//
//	guard-release-tool tree-digest <dir>          digest of a StackKits runtime tree
//	guard-release-tool archive-digest <file.tgz>  the same digest of a bundle archive
//	guard-release-tool verify <dir>               verify the release in <dir> the way a Guard does
//
// The digests are the lowercase hex values pkg/guardrelease.TreeDigest defines.
// verify reads release-manifest.json, release-manifest.sigstore.json and, for
// every artifact the manifest lists, techstack-<os>-<arch> with its
// .sigstore.json bundle, and applies the pinned signer identity, the pinned
// Sigstore trusted root and the manifest policy. A release that would be refused
// by a Guard is refused before it is published.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: guard-release-tool tree-digest <dir> | archive-digest <file.tar.gz> | verify <dir> [--expected-source <40-character SHA>] [--require-runtime-bundles] [--require-current-policy | --refresh-published]")
		os.Exit(64)
	}
	output, err := run(os.Args[1], os.Args[2], os.Args[3:]...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "guard-release-tool:", err)
		os.Exit(1)
	}
	if output != "" {
		fmt.Println(output)
	}
}

func run(command, arg string, extra ...string) (string, error) {
	if len(extra) != 0 && command != "verify" {
		return "", fmt.Errorf("%s takes one path", command)
	}
	switch command {
	case "tree-digest":
		return guardrelease.TreeDigest(arg)
	case "archive-digest":
		file, err := os.Open(arg) // #nosec G304 -- an operator-supplied build path.
		if err != nil {
			return "", err
		}
		defer func() { _ = file.Close() }()
		return guardrelease.BundleTreeDigest(file)
	case "verify":
		expectedSource := ""
		requireRuntime := false
		refreshPublished := false
		requireCurrentPolicy := false
		for len(extra) != 0 {
			switch extra[0] {
			case "--expected-source":
				if expectedSource != "" || len(extra) < 2 || !sourceCommitPattern.MatchString(extra[1]) {
					return "", errInvalidExpectedSource
				}
				expectedSource = extra[1]
				extra = extra[2:]
			case "--require-runtime-bundles":
				if requireRuntime {
					return "", errors.New("runtime bundle requirement is duplicated")
				}
				requireRuntime = true
				extra = extra[1:]
			case "--refresh-published":
				if refreshPublished {
					return "", errors.New("published refresh is duplicated")
				}
				refreshPublished = true
				extra = extra[1:]
			case "--require-current-policy":
				if requireCurrentPolicy {
					return "", errors.New("current policy requirement is duplicated")
				}
				requireCurrentPolicy = true
				extra = extra[1:]
			default:
				return "", errInvalidExpectedSource
			}
		}
		if refreshPublished && requireCurrentPolicy {
			return "", errors.New("current-time policy verification cannot use the publication-refresh clock")
		}
		return "", verifyRelease(arg, expectedSource, requireRuntime, refreshPublished, requireCurrentPolicy)
	}
	return "", fmt.Errorf("unknown command %q", command)
}

var sourceCommitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var errInvalidExpectedSource = errors.New("verify accepts only --expected-source followed by a full lowercase 40-character source SHA")

func artifactVerificationOptions(expectedSource string) (guardrelease.Options, error) {
	if expectedSource == "" {
		return guardrelease.Options{}, nil
	}
	if !sourceCommitPattern.MatchString(expectedSource) {
		return guardrelease.Options{}, errInvalidExpectedSource
	}
	identity := guardrelease.DefaultIdentity
	identity.Extensions.SourceRepositoryDigest = expectedSource
	return guardrelease.Options{Identity: identity}, nil
}

// publishedRefreshOptions is only a build-time recovery of an immutable
// publication's identity. Its clock selects the signed issuance window, but
// does not authenticate this pre-parse: VerifyManifest and VerifyArtifact still
// verify every signature afterwards. The new manifest is verified normally.
func publishedRefreshOptions(raw, policyRaw []byte, now time.Time) (guardrelease.Options, error) {
	manifest, err := admitCurrentReleasePolicy(raw, policyRaw)
	if err != nil {
		return guardrelease.Options{}, err
	}
	if manifest.IssuedAt.After(now) {
		return guardrelease.Options{}, errors.New("a future-issued publication cannot be refreshed")
	}
	return guardrelease.Options{Now: func() time.Time { return manifest.IssuedAt }}, nil
}

// Reusing existing assets still applies today's policy and normal freshness.
// It never selects the issuance clock used only to publish a refreshed manifest.
func currentPolicyVerificationOptions(raw, policyRaw []byte, now time.Time) (guardrelease.Options, error) {
	manifest, err := admitCurrentReleasePolicy(raw, policyRaw)
	if err != nil {
		return guardrelease.Options{}, err
	}
	if err := manifest.Fresh(now); err != nil {
		return guardrelease.Options{}, err
	}
	return guardrelease.Options{}, nil
}

func admitCurrentReleasePolicy(raw, policyRaw []byte) (*guardrelease.Manifest, error) {
	manifest, err := guardrelease.ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	var policy struct {
		MinimumAdmittedVersion string   `json:"minimumAdmittedVersion"`
		RevokedVersions        []string `json:"revokedVersions"`
		RevokedDigests         []string `json:"revokedDigests"`
	}
	decoder := json.NewDecoder(bytes.NewReader(policyRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("the current release policy has trailing data")
	}
	current := *manifest
	current.MinimumAdmittedVersion = policy.MinimumAdmittedVersion
	current.RevokedVersions = policy.RevokedVersions
	current.RevokedDigests = policy.RevokedDigests
	currentRaw, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	if _, err := guardrelease.ParseManifest(currentRaw); err != nil {
		return nil, fmt.Errorf("current release policy: %w", err)
	}
	for _, artifact := range manifest.Artifacts {
		if denial := current.Admit(manifest.Version, artifact.SHA256); denial != nil {
			return nil, denial
		}
	}
	return manifest, nil
}

func verifyRelease(dir, expectedSource string, requireRuntime, refreshPublished, requireCurrentPolicy bool) error {
	artifactOptions, err := artifactVerificationOptions(expectedSource)
	if err != nil {
		return err
	}
	read := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) } // #nosec G304 -- below the build directory.
	raw, err := read("release-manifest.json")
	if err != nil {
		return err
	}
	options := guardrelease.Options{}
	if refreshPublished || requireCurrentPolicy {
		policyRaw, err := os.ReadFile("guard-release-policy.json")
		if err != nil {
			return err
		}
		if requireCurrentPolicy {
			options, err = currentPolicyVerificationOptions(raw, policyRaw, time.Now().UTC())
		} else {
			options, err = publishedRefreshOptions(raw, policyRaw, time.Now().UTC())
		}
		if err != nil {
			return err
		}
	}
	verifier, err := guardrelease.NewVerifier(options)
	if err != nil {
		return err
	}
	bundle, err := read("release-manifest.sigstore.json")
	if err != nil {
		return err
	}
	manifest, err := verifier.VerifyManifest(raw, bundle)
	if err != nil {
		return fmt.Errorf("release manifest: %w", err)
	}
	// Nightly refresh signs the manifest from a later main commit. Only the
	// immutable binary certificates must name the caller's exact source.
	artifactVerifier := verifier
	if expectedSource != "" {
		artifactOptions.Now = options.Now
		artifactVerifier, err = guardrelease.NewVerifier(artifactOptions)
		if err != nil {
			return err
		}
	}
	for _, artifact := range manifest.Artifacts {
		name := fmt.Sprintf("techstack-%s-%s", artifact.OS, artifact.Arch)
		artifactBundle, err := read(name + ".sigstore.json")
		if err != nil {
			return err
		}
		if _, err := artifactVerifier.VerifyArtifact(manifest, artifact.OS, artifact.Arch, filepath.Join(dir, name), artifactBundle, ""); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(os.Stderr, "verified %s %s\n", name, manifest.Version)
	}
	// Historical releases did not publish runtime archives. Keep their binary
	// verification compatible, but never accept a partial pair or ignore a
	// present archive. New publications require both paired payloads.
	present := 0
	for _, arch := range []string{"amd64", "arm64"} {
		name := fmt.Sprintf("stackkit-runtime-linux-%s.tar.gz", arch)
		file, err := os.Open(filepath.Join(dir, name)) // #nosec G304 -- fixed asset below the build directory.
		if errors.Is(err, os.ErrNotExist) && !requireRuntime {
			continue
		}
		if err != nil {
			return err
		}
		present++
		if manifest.StackKits == nil {
			_ = file.Close()
			return errors.New("the release has no signed StackKits runtime")
		}
		runtime, ok := manifest.StackKits.ForArch(arch)
		if !ok {
			_ = file.Close()
			return fmt.Errorf("the release has no signed runtime for %s", arch)
		}
		err = guardrelease.VerifyRuntimeBundle(runtime, file)
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(os.Stderr, "verified %s %s\n", name, manifest.StackKits.Version)
	}
	if present == 1 {
		return errors.New("the release holds only part of its runtime archive pair")
	}
	return nil
}
