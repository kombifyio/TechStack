package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/guardrelease"
)

type installedCandidateSource struct {
	archiveKit string
	directory  string
	sbomSHA256 string
}

type candidateSourceCacheProof struct {
	SchemaVersion            string                                `json:"schemaVersion"`
	Kit                      string                                `json:"kit"`
	Version                  string                                `json:"version"`
	Platform                 stackkitrelease.Platform              `json:"platform"`
	IndexSHA256              string                                `json:"indexSha256"`
	ArchiveSHA256            string                                `json:"archiveSha256"`
	CLISHA256                string                                `json:"cliSha256"`
	IndexAttestationSHA256   string                                `json:"indexAttestationSha256"`
	PayloadAttestationSHA256 string                                `json:"payloadAttestationSha256"`
	TrustedRootSHA256        string                                `json:"trustedRootSha256"`
	SBOMSHA256               string                                `json:"sbomSha256"`
	Source                   guardrelease.PreviewCandidateSource   `json:"source"`
	Producer                 guardrelease.PreviewCandidateProducer `json:"producer"`
}

// The existing root admission authenticates the installed tree. Its material
// reader chooses only that runtime's retained bytes; no request, environment or
// downloaded provenance chooses a source path or grants installation authority.
func (executor *StackKitExecutor) installedCandidateSource(pin stackkitrelease.Pin) (installedCandidateSource, error) {
	var material installedCandidateSource
	if pin.SchemaVersion != stackkitrelease.CandidatePinSchemaVersion || pin.Candidate == nil || executor.candidateAdmission == nil {
		return material, fmt.Errorf("candidate source retention requires root installed admission")
	}
	if err := executor.candidateAdmission(pin); err != nil {
		return material, err
	}
	root, err := os.OpenRoot(executor.RuntimeRoot())
	if err != nil {
		return material, err
	}
	defer root.Close()
	info, err := root.Lstat("candidate-source")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return material, fmt.Errorf("installed candidate source directory is not confined")
	}
	const indexName = "stackkits-preview-candidate-index-v1.json"
	for _, subject := range []struct {
		name, digest string
		limit        int64
	}{
		{indexName, pin.Candidate.IndexSHA256, 4 << 20},
		{indexName + ".intoto.jsonl", pin.Candidate.IndexAttestationSHA256, 32 << 20},
		{"payload.intoto.jsonl", pin.Candidate.PayloadAttestationSHA256, 32 << 20},
		{"sigstore-trusted-root.jsonl", pin.Candidate.TrustedRootSHA256, 4 << 20},
	} {
		if err := verifyInstalledCandidateSubject(root, subject.name, subject.digest, subject.limit); err != nil {
			return material, err
		}
	}
	index, err := readInstalledCandidateSubject(root, indexName, 4<<20)
	if err != nil {
		return material, err
	}
	archiveKit, archive, sbom, err := stackkitrelease.CandidateArchive(index, pin)
	if err != nil {
		return material, err
	}
	material.archiveKit, material.sbomSHA256 = archiveKit, sbom
	if err := verifyInstalledCandidateSubject(root, archive, pin.ArchiveSHA256, maxStackKitRuntimeBundleBytes); err != nil {
		return material, err
	}
	if err := verifyInstalledCandidateSubject(root, archive+".spdx.json", material.sbomSHA256, 32<<20); err != nil {
		return material, err
	}
	raw, err := readInstalledCandidateSubject(root, "candidate.provenance.json", 64<<10)
	if err != nil {
		return material, err
	}
	var provenance guardrelease.PreviewCandidateProvenance
	if json.Unmarshal(raw, &provenance) != nil || provenance != *pin.Candidate {
		return material, fmt.Errorf("retained candidate provenance differs from root admission")
	}
	material.directory = filepath.Join(executor.RuntimeRoot(), "candidate-source")
	return material, nil
}

func openInstalledCandidateSubject(root *os.Root, name string, limit int64) (*os.File, os.FileInfo, error) {
	if filepath.Base(name) != name {
		return nil, nil, fmt.Errorf("installed candidate subject is not a filename")
	}
	path := filepath.Join("candidate-source", name)
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > limit {
		return nil, nil, fmt.Errorf("installed candidate subject %s is not bounded and regular", name)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("installed candidate subject changed before opening")
	}
	return file, info, nil
}

func readInstalledCandidateSubject(root *os.Root, name string, limit int64) ([]byte, error) {
	file, info, err := openInstalledCandidateSubject(root, name, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || int64(len(raw)) != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("installed candidate subject changed while reading")
	}
	return raw, nil
}

func verifyInstalledCandidateSubject(root *os.Root, name, digest string, limit int64) error {
	if !stackKitSHA256HexPattern.MatchString(digest) {
		return fmt.Errorf("installed candidate subject has no admitted digest")
	}
	file, info, err := openInstalledCandidateSubject(root, name, limit)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, info.Size()+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || n != info.Size() || !after.ModTime().Equal(info.ModTime()) || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("installed candidate subject %s differs from root admission", name)
	}
	return nil
}

// Native candidate proof stays discriminated. It cannot satisfy a public
// sourceRelease receipt and does not authenticate an Actions artifact ID.
func retainStackKitSourceCandidate(ctx context.Context, stagedCLI, cache string, installed stackkitrelease.Pin, cliDigest string, material installedCandidateSource) error {
	process := newStackKitProcess(ctx, stagedCLI, "--no-log", "kit", "prepare-source-generation", "--cache-only",
		"--source-kind", "preview-candidate", "--source-candidate-dir", material.directory,
		"--release-cache", cache, "--source-kit", material.archiveKit, "--source-release", installed.Version,
		"--source-os", installed.Platform.OS, "--source-arch", installed.Platform.Arch,
		"--source-archive-sha256", installed.ArchiveSHA256, "--source-index-sha256", installed.IndexSHA256,
		"--source-cli-sha256", cliDigest, "--json") // #nosec G204 -- exact admitted staged CLI and root-derived retained source only.
	process.Env = stackKitChildEnvironment(os.Environ())
	stdout, stderr := newBoundedStackKitBuffer(maxStackKitCommandOutputBytes), newBoundedStackKitBuffer(maxStackKitCommandOutputBytes)
	process.Stdout, process.Stderr = stdout, stderr
	if err := process.Run(); err != nil {
		return fmt.Errorf("candidate source retention failed: %w: %s", err, errorString(fmt.Errorf("%s", stderr.String())))
	}
	var result struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Prepared        bool                      `json:"prepared"`
			SourceKind      string                    `json:"sourceKind"`
			CLISHA256       string                    `json:"cliSha256"`
			SourceRelease   json.RawMessage           `json:"sourceRelease"`
			SourceCandidate candidateSourceCacheProof `json:"sourceCandidate"`
		} `json:"data"`
	}
	if stdout.truncated || json.Unmarshal(stdout.buffer.Bytes(), &result) != nil {
		return fmt.Errorf("candidate source retention returned invalid evidence")
	}
	proof, expected := result.Data.SourceCandidate, installed.Candidate
	if expected == nil || result.SchemaVersion != stackKitCommandResultVersion || result.Command != "stackkit kit prepare-source-generation" || result.Status != "success" ||
		!result.Data.Prepared || result.Data.SourceKind != "preview-candidate" || result.Data.CLISHA256 != cliDigest || len(result.Data.SourceRelease) != 0 ||
		proof.SchemaVersion != "stackkit.preview-source-cache/v1" || proof.Kit != material.archiveKit || proof.Version != strings.TrimPrefix(installed.Version, "v") || proof.Platform != installed.Platform ||
		proof.IndexSHA256 != installed.IndexSHA256 || proof.ArchiveSHA256 != installed.ArchiveSHA256 || proof.CLISHA256 != cliDigest ||
		proof.IndexAttestationSHA256 != expected.IndexAttestationSHA256 || proof.PayloadAttestationSHA256 != expected.PayloadAttestationSHA256 ||
		proof.TrustedRootSHA256 != expected.TrustedRootSHA256 || proof.SBOMSHA256 != material.sbomSHA256 || proof.Source != expected.Source || proof.Producer != expected.Producer {
		return fmt.Errorf("candidate source retention differs from independently admitted installed provenance")
	}
	return nil
}
