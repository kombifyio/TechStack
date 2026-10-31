package routes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"golang.org/x/mod/semver"
)

const artifactIndexPathEnv = "TECHSTACK_ARTIFACT_INDEX_PATH"
const artifactIndexDigestEnv = "TECHSTACK_ARTIFACT_INDEX_SHA256"
const artifactIndexDigestFileEnv = "TECHSTACK_ARTIFACT_INDEX_SHA256_FILE"
const artifactIndexMaxBytes = 16 << 10

// artifactDeliveryIndex is deployment configuration, never caller input. Its
// digest pins the exact reviewed private release inventory mirrored to R2.
type artifactDeliveryIndex struct {
	seed           *guardrelease.Manifest
	Schema         string                  `json:"schema"`
	ReleaseVersion string                  `json:"release_version"`
	ManifestSHA256 string                  `json:"manifest_sha256"`
	Artifacts      []artifactDeliveryEntry `json:"artifacts"`
}

type artifactDeliveryEntry struct {
	Kind     string `json:"kind"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
}

type artifactAdmission struct {
	Schema string `json:"schema"`
	artifactDeliveryEntry
	ReleaseVersion string `json:"release_version"`
	ManifestSHA256 string `json:"manifest_sha256"`
	IndexSHA256    string `json:"index_sha256"`
	ObjectKey      string `json:"object_key"`
}

func artifactHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func readArtifactDeliveryIndex() (*artifactDeliveryIndex, string, error) {
	path, want := strings.TrimSpace(os.Getenv(artifactIndexPathEnv)), strings.TrimSpace(os.Getenv(artifactIndexDigestEnv))
	if digestFile := strings.TrimSpace(os.Getenv(artifactIndexDigestFileEnv)); digestFile != "" {
		bound, err := readBoundedFile(digestFile, 65)
		if err != nil {
			return nil, "", fmt.Errorf("artifact index digest configuration: %w", err)
		}
		fromImage := strings.TrimSpace(string(bound))
		if !artifactHexDigest(fromImage) || (want != "" && want != fromImage) {
			return nil, "", fmt.Errorf("artifact index digest configuration differs from image")
		}
		want = fromImage
	}
	if path == "" || !artifactHexDigest(want) {
		return nil, "", fmt.Errorf("artifact index configuration is unavailable")
	}
	raw, err := readBoundedFile(path, artifactIndexMaxBytes)
	if err != nil {
		return nil, "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if digest != want {
		return nil, "", fmt.Errorf("artifact index digest mismatch")
	}
	var index artifactDeliveryIndex
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return nil, "", err
	}
	if decoder.Decode(new(any)) != io.EOF || index.Schema != "techstack.artifact-index/v1" ||
		!semver.IsValid("v"+index.ReleaseVersion) || !artifactHexDigest(index.ManifestSHA256) ||
		len(index.Artifacts) == 0 || len(index.Artifacts) > 4 {
		return nil, "", fmt.Errorf("invalid artifact index")
	}
	seen := map[string]bool{}
	for _, entry := range index.Artifacts {
		name := ""
		switch entry.Kind {
		case "guard_binary":
			name = "techstack-" + entry.OS + "-" + entry.Arch
		case "stackkit_runtime":
			name = "stackkit-runtime-" + entry.OS + "-" + entry.Arch + ".tar.gz"
		}
		key := entry.Kind + "/" + entry.OS + "/" + entry.Arch
		if name == "" || entry.Filename != name || entry.OS != "linux" ||
			(entry.Arch != "amd64" && entry.Arch != "arm64") || !artifactHexDigest(entry.SHA256) ||
			entry.Size <= 0 || entry.Size > 4<<30 || seen[key] {
			return nil, "", fmt.Errorf("invalid artifact entry")
		}
		seen[key] = true
	}
	return &index, digest, nil
}

// The immutable index pins the signed image seed, not the periodically renewed
// signature/policy envelope. A newer verified same-artifact policy remains live.
func loadArtifactDeliveryIndex() (*artifactDeliveryIndex, string, error) {
	index, digest, err := readArtifactDeliveryIndex()
	if err != nil {
		return nil, "", err
	}
	dir := guardReleaseDir()
	if dir == "" {
		return nil, "", fmt.Errorf("the signed release manifest is unavailable")
	}
	seedPair, _, err := readVerifiedGuardManifest(dir, guardReleaseSeedManifestFile, guardReleaseSeedManifestSig)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(seedPair.raw)) != index.ManifestSHA256 {
		return nil, "", fmt.Errorf("artifact index seed manifest mismatch")
	}
	seed := seedPair.manifest
	if seed.Version != index.ReleaseVersion {
		return nil, "", fmt.Errorf("artifact index seed version mismatch")
	}
	index.seed = seed
	served, _, err := currentGuardManifest(dir)
	if err != nil || served.manifest.Version != index.ReleaseVersion {
		return nil, "", fmt.Errorf("artifact index does not match the current signed release manifest")
	}
	if served.manifest.IssuedAt.Before(seed.IssuedAt) || droppedPolicy(seed, served.manifest) != "" {
		return nil, "", fmt.Errorf("current signed release policy weakens the immutable seed")
	}
	for _, entry := range index.Artifacts {
		if entry.Kind == "guard_binary" {
			binary, ok := served.manifest.Artifact(entry.OS, entry.Arch)
			original, anchored := seed.Artifact(entry.OS, entry.Arch)
			if !ok || !anchored || original.SHA256 != entry.SHA256 || original.Size != entry.Size || binary.SHA256 != entry.SHA256 || binary.Size != entry.Size {
				return nil, "", fmt.Errorf("artifact index binary does not match the signed manifest")
			}
		} else if seed.StackKits == nil {
			return nil, "", fmt.Errorf("artifact index has no signed runtime identity")
		} else if _, ok := seed.StackKits.ForArch(entry.Arch); !ok {
			return nil, "", fmt.Errorf("artifact index runtime platform is unsigned")
		}
	}
	return index, digest, nil
}

// admittedArtifactBytes mirrors the edge's single-byte-range contract. A
// nonmatching If-Range requires the complete representation, never a fragment.
func admittedArtifactBytes(request *http.Request, size int64, etag string) (int64, bool) {
	value := strings.TrimSpace(request.Header.Get("Range"))
	if value == "" || (request.Header.Get("If-Range") != "" && request.Header.Get("If-Range") != etag) {
		return size, true
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, false
	}
	start, end, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
	if !ok {
		return 0, false
	}
	if start == "" {
		length, err := strconv.ParseInt(end, 10, 64)
		if err != nil || length <= 0 {
			return 0, false
		}
		return min(length, size), true
	}
	first, err := strconv.ParseInt(start, 10, 64)
	if err != nil || first < 0 || first >= size {
		return 0, false
	}
	last := size - 1
	if end != "" {
		last, err = strconv.ParseInt(end, 10, 64)
		if err != nil || last < first {
			return 0, false
		}
		last = min(last, size-1)
	}
	return last - first + 1, true
}

// indexedGuardBinaryArtifact describes the Guard binary a managed node
// installs. The image carries only the signed release metadata: the bytes are
// delivered from R2 under the digest of the verified artifact index (Render
// download prohibition), so that index, not a local file, names them.
func indexedGuardBinaryArtifact(goos, arch string) (agentBinaryArtifact, error) {
	index, _, err := loadArtifactDeliveryIndex()
	if err != nil {
		return agentBinaryArtifact{}, err
	}
	entry, ok := index.artifact("guard_binary", goos, arch)
	if !ok {
		return agentBinaryArtifact{}, fmt.Errorf("the artifact index has no %s/%s Guard binary", goos, arch)
	}
	return agentBinaryArtifact{os: entry.OS, arch: entry.Arch, size: entry.Size, sha256: entry.SHA256}, nil
}

func (index *artifactDeliveryIndex) artifact(kind, goos, arch string) (artifactDeliveryEntry, bool) {
	for _, entry := range index.Artifacts {
		if entry.Kind == kind && entry.OS == goos && entry.Arch == arch {
			return entry, true
		}
	}
	return artifactDeliveryEntry{}, false
}

// admitArtifact authenticates the original request and returns only bounded
// metadata. There is no local file, redirect, Range body, or hosting override.
// The Cloudflare delivery worker alone resolves ObjectKey in its private R2
// binding; a caller cannot turn this response into a Render download.
func (h workerRouteHandlers) admitArtifact(e *httpx.Event, kind string) error {
	goos := strings.ToLower(strings.TrimSpace(e.Request.PathValue("os")))
	arch := normalizeAgentBinaryArch(e.Request.PathValue("arch"))
	if goos != "linux" || (arch != "amd64" && arch != "arm64") {
		return httpx.BadRequest(e, "Unsupported artifact platform", nil)
	}
	rawToken := workerRequestCredential(e.Request)
	if rawToken == "" {
		return httpx.Unauthorized(e, "A valid agent download capability is required")
	}
	now := time.Now().UTC()
	authorization, authorized, err := h.authorizeAgentBinary(e, rawToken, now)
	if err != nil || !authorized {
		return err
	}
	index, indexDigest, err := loadArtifactDeliveryIndex()
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Artifact delivery admission is unavailable", nil)
	}
	entry, ok := index.artifact(kind, goos, arch)
	if !ok {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The admitted release has no artifact for this platform", nil)
	}
	served, _, err := currentGuardManifest(guardReleaseDir())
	if err != nil || !now.Before(served.manifest.ExpiresAt) || now.Before(served.manifest.IssuedAt) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The signed artifact policy is unavailable", nil)
	}
	policyBinary, hasBinary := served.manifest.Artifact(goos, arch)
	if !hasBinary || served.manifest.Admit(index.ReleaseVersion, policyBinary.SHA256) != nil || served.manifest.Admit(index.ReleaseVersion, entry.SHA256) != nil {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "The signed artifact policy refuses this release", nil)
	}
	if kind == "stackkit_runtime" {
		anchor, _ := json.Marshal(index.seed.StackKits)
		current, _ := json.Marshal(served.manifest.StackKits)
		if !bytes.Equal(anchor, current) {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The signed runtime differs from the delivery catalog", nil)
		}
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	e.Response.Header().Set("Vary", "Authorization")
	e.Response.Header().Set("ETag", `"`+entry.SHA256+`"`)
	e.Response.Header().Set(agentBinaryChecksumHeader, entry.SHA256)
	e.Response.Header().Set(agentBinaryVersionHeader, index.ReleaseVersion)
	if strings.TrimSpace(e.Request.Header.Get("If-None-Match")) == `"`+entry.SHA256+`"` {
		e.Response.WriteHeader(http.StatusNotModified)
		return nil
	}
	charge, validRange := admittedArtifactBytes(e.Request, entry.Size, `"`+entry.SHA256+`"`)
	if !validRange {
		e.Response.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", entry.Size))
		return httpx.Error(e, http.StatusRequestedRangeNotSatisfiable, ksapi.ErrCodeBadRequest, "A satisfiable single byte range is required", nil)
	}
	if h.binaryDownloadGuard == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Artifact admission budget is unavailable", nil)
	}
	budgetKey := kind + ":" + authorization.tokenHash
	release, allowed := h.binaryDownloadGuard.acquire(budgetKey, authorization.resetAt, now, entry.Size)
	if !allowed {
		e.Response.Header().Set("Retry-After", "900")
		return httpx.Error(e, http.StatusTooManyRequests, ksapi.ErrCodeRateLimited, "Artifact delivery admission is rate limited", nil)
	}
	defer release()
	// Charge an admitted transfer at its full bound: an acknowledgement loss
	// must not grant an unbounded fresh R2 body on the next request.
	h.binaryDownloadGuard.record(budgetKey, charge)
	return httpx.Success(e, http.StatusOK, artifactAdmission{
		Schema: "techstack.artifact-admission/v1", artifactDeliveryEntry: entry,
		ReleaseVersion: index.ReleaseVersion, ManifestSHA256: index.ManifestSHA256, IndexSHA256: indexDigest,
		ObjectKey: "techstack/guard-v" + index.ReleaseVersion + "/sha256/" + entry.SHA256 + "/" + entry.Filename,
	})
}
