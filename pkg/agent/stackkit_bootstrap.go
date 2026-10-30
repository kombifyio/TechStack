package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	maxStackKitRuntimeBundleBytes   int64 = 4 << 30
	stackKitRuntimeBundleDigestFile       = ".techstack-bundle-sha256"
	legacyTarRegularFileType              = byte(0) // NUL is the legacy regular-file type flag.
)

type StackKitRuntimeBootstrapConfig struct {
	URL, AgentToken, RuntimeAgentID, TenantID string
	HTTPClient                                *http.Client
	// StageDir is where a downloaded bundle is written before installation;
	// empty selects the system temp directory.
	StageDir string
}

type StackKitRuntimeConvergenceResult struct {
	Version string
	SHA256  string
}

// EnsureRuntime converges an enrolled Agent to the exact bundle currently
// published by its authenticated Techstack control plane. A locally valid but
// older release is not sufficient evidence that the Agent is current.
func (executor *StackKitExecutor) EnsureRuntime(ctx context.Context, cfg StackKitRuntimeBootstrapConfig) error {
	_, err := executor.EnsureRuntimeEvidence(ctx, cfg)
	return err
}

// EnsureRuntimeEvidence converges the runtime and returns the exact published
// StackKits artifact identity that was revalidated after activation.
func (executor *StackKitExecutor) EnsureRuntimeEvidence(ctx context.Context, cfg StackKitRuntimeBootstrapConfig) (StackKitRuntimeConvergenceResult, error) {
	if executor == nil {
		return StackKitRuntimeConvergenceResult{}, fmt.Errorf("StackKits executor is required")
	}
	installed, _ := executor.InstalledRuntimeBundleDigest()
	bundle, err := FetchStackKitRuntimeBundle(ctx, cfg, installed)
	if err != nil {
		return StackKitRuntimeConvergenceResult{}, err
	}
	if bundle.NotModified {
		return executor.ConfirmInstalledRuntime()
	}
	defer func() { _ = os.Remove(bundle.Path) }()
	return executor.InstallRuntimeBundle(bundle.Path, bundle.SHA256)
}

// StackKitRuntimeBundle is a downloaded, digest-checked StackKits runtime
// bundle waiting to be installed.
type StackKitRuntimeBundle struct {
	// NotModified is true when the control plane reports the installed bundle
	// as current; Path and SHA256 are empty then.
	NotModified bool
	Path        string
	SHA256      string
}

// stackKitReleaseDownloadAttempts bounds how often a gateway answer (502, 503,
// 504) to the release download is retried before the command fails.
const stackKitReleaseDownloadAttempts = 3

// stackKitReleaseDownloadRetryDelay is the base pause between those attempts;
// the n-th retry waits n times as long.
var stackKitReleaseDownloadRetryDelay = 2 * time.Second

func stackKitReleaseGatewayStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// FetchStackKitRuntimeBundle downloads the bundle the authenticated control
// plane currently publishes into cfg.StageDir (the system temp directory when
// empty). It needs the Guard's credential and never installs anything, so an
// unprivileged Guard can run it and hand the file to the privileged executor.
func FetchStackKitRuntimeBundle(ctx context.Context, cfg StackKitRuntimeBootstrapConfig, installedDigest string) (StackKitRuntimeBundle, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return StackKitRuntimeBundle{}, fmt.Errorf("StackKits release URL is required")
	}
	parsedURL, err := url.Parse(cfg.URL)
	if err != nil || parsedURL.Hostname() == "" || (parsedURL.Scheme != "https" && !(parsedURL.Scheme == "http" && isLoopbackHost(parsedURL.Hostname()))) {
		return StackKitRuntimeBundle{}, fmt.Errorf("StackKits release URL must use HTTPS or loopback HTTP")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	var response *http.Response
	for attempt := 1; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, nil)
		if err != nil {
			return StackKitRuntimeBundle{}, err
		}
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AgentToken))
		request.Header.Set("X-Kombify-Runtime-Agent-ID", strings.TrimSpace(cfg.RuntimeAgentID))
		request.Header.Set("X-Kombify-Tenant-ID", strings.TrimSpace(cfg.TenantID))
		if len(installedDigest) == sha256.Size*2 {
			request.Header.Set("If-None-Match", `"`+installedDigest+`"`)
		}
		response, err = client.Do(request)
		if err != nil {
			return StackKitRuntimeBundle{}, err
		}
		// A server-directed refusal belongs to the durable outer convergence
		// schedule. Retrying here can consume a full body before Retry-After.
		if retryAfter := ArtifactRetryAfter(response.Header.Get("Retry-After"), time.Now()); stackKitReleaseGatewayStatus(response.StatusCode) && retryAfter > 0 {
			_ = response.Body.Close()
			return StackKitRuntimeBundle{}, &ArtifactHTTPError{
				Operation: "StackKits release download", StatusCode: response.StatusCode, RetryAfter: retryAfter,
			}
		}
		if !stackKitReleaseGatewayStatus(response.StatusCode) || attempt >= stackKitReleaseDownloadAttempts {
			break
		}
		// A gateway answer (the control plane restarting behind its proxy) is
		// transient: close it unread (a stalled error body must not hold the
		// retry for the client timeout) and ask again after a short pause.
		_ = response.Body.Close()
		timer := time.NewTimer(stackKitReleaseDownloadRetryDelay * time.Duration(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return StackKitRuntimeBundle{}, ctx.Err()
		case <-timer.C:
		}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return StackKitRuntimeBundle{NotModified: true}, nil
	}
	if response.StatusCode != http.StatusOK {
		return StackKitRuntimeBundle{}, &ArtifactHTTPError{Operation: "StackKits release download", StatusCode: response.StatusCode, RetryAfter: ArtifactRetryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	expected := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Kombify-Artifact-SHA256")))
	if len(expected) != sha256.Size*2 {
		return StackKitRuntimeBundle{}, fmt.Errorf("StackKits release checksum is missing")
	}
	tmp, err := os.CreateTemp(cfg.StageDir, "techstack-stackkit-release-*.tar.gz")
	if err != nil {
		return StackKitRuntimeBundle{}, err
	}
	tmpPath := tmp.Name()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(response.Body, maxStackKitRuntimeBundleBytes+1))
	closeErr := tmp.Close()
	fail := func(cause error) (StackKitRuntimeBundle, error) {
		_ = os.Remove(tmpPath)
		return StackKitRuntimeBundle{}, cause
	}
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	if written > maxStackKitRuntimeBundleBytes {
		return fail(fmt.Errorf("StackKits release bundle exceeds size limit"))
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return fail(fmt.Errorf("StackKits release bundle checksum mismatch"))
	}
	return StackKitRuntimeBundle{Path: tmpPath, SHA256: expected}, nil
}

// RuntimeRoot is the directory the StackKits runtime is installed into.
func (executor *StackKitExecutor) RuntimeRoot() string {
	if executor == nil || !executor.Available() {
		return ""
	}
	return filepath.Dir(filepath.Clean(executor.pinPath))
}

// InstalledRuntimeBundleDigest returns the digest of the installed StackKits
// bundle, which names the bundle the control plane may answer 304 for.
func (executor *StackKitExecutor) InstalledRuntimeBundleDigest() (string, error) {
	if executor == nil || !executor.Available() {
		return "", fmt.Errorf("StackKits runtime paths are not configured")
	}
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), hostSecurityHelpTimeout)
	defer cancel()
	unlock, err := executor.lockRuntime(ctx, false)
	if err != nil {
		return "", err
	}
	defer unlock()
	return readInstalledStackKitBundleDigest(filepath.Dir(filepath.Clean(executor.pinPath)))
}

// ConfirmInstalledRuntime revalidates the installed runtime after the control
// plane reported it current.
func (executor *StackKitExecutor) ConfirmInstalledRuntime() (StackKitRuntimeConvergenceResult, error) {
	if executor == nil {
		return StackKitRuntimeConvergenceResult{}, fmt.Errorf("StackKits executor is required")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), hostSecurityHelpTimeout)
	defer cancel()
	unlock, err := executor.lockRuntime(ctx, false)
	if err != nil {
		return StackKitRuntimeConvergenceResult{}, err
	}
	defer unlock()
	if err := executor.validate(); err != nil {
		return StackKitRuntimeConvergenceResult{}, fmt.Errorf("control plane reports current StackKits bundle but local runtime is invalid: %w", err)
	}
	return executor.currentStackKitRuntimeEvidence()
}

// InstallRuntimeBundle installs a downloaded bundle whose sha256 the caller
// has already established, then revalidates the pinned release.
func (executor *StackKitExecutor) InstallRuntimeBundle(bundlePath, digest string) (StackKitRuntimeConvergenceResult, error) {
	return executor.InstallRuntimeBundleChecked(bundlePath, digest, nil)
}

// InstallRuntimeBundleChecked installs a bundle after check approved its
// extracted tree. The privileged executor uses it to require that the
// executables about to run as root are the ones a signed release names.
func (executor *StackKitExecutor) InstallRuntimeBundleChecked(bundlePath, digest string, check func(stageDir string) error) (StackKitRuntimeConvergenceResult, error) {
	return executor.InstallRuntimeBundleCheckedContext(context.Background(), bundlePath, digest, check)
}

// InstallRuntimeBundleCheckedContext preserves the installed public artifact
// before the verified replacement can remove its runtime tree.
func (executor *StackKitExecutor) InstallRuntimeBundleCheckedContext(ctx context.Context, bundlePath, digest string, check func(stageDir string) error) (StackKitRuntimeConvergenceResult, error) {
	if executor == nil {
		return StackKitRuntimeConvergenceResult{}, fmt.Errorf("StackKits executor is required")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if !executor.Available() {
		return StackKitRuntimeConvergenceResult{}, fmt.Errorf("StackKits runtime paths are not configured")
	}
	unlock, err := executor.lockRuntime(ctx, true)
	if err != nil {
		return StackKitRuntimeConvergenceResult{}, err
	}
	defer unlock()
	runtimeRoot := filepath.Dir(filepath.Clean(executor.pinPath))
	prepare := func(stage string) error {
		if check != nil {
			if err := check(stage); err != nil {
				return err
			}
		}
		if err := executor.retainInstalledRelease(ctx, stage, check != nil); err != nil {
			return fmt.Errorf("retain installed StackKits public artifact before replacement: %w", err)
		}
		return ctx.Err()
	}
	if err := installStackKitRuntimeBundleChecked(bundlePath, runtimeRoot, digest, prepare, executor.validate); err != nil {
		return StackKitRuntimeConvergenceResult{}, err
	}
	return executor.currentStackKitRuntimeEvidence()
}

func (executor *StackKitExecutor) retainInstalledRelease(ctx context.Context, stage string, authenticated bool) error {
	installed, err := stackkitrelease.LoadPin(executor.pinPath)
	if errors.Is(err, os.ErrNotExist) {
		entries, rootErr := os.ReadDir(executor.RuntimeRoot())
		if errors.Is(rootErr, os.ErrNotExist) || (rootErr == nil && len(entries) == 0) {
			return nil // A fresh installation has no original runtime to retain.
		}
		return fmt.Errorf("existing StackKits runtime has no valid installed release pin")
	}
	if err != nil {
		return err
	}
	if filepath.Clean(installed.BinaryPath) != filepath.Join(executor.RuntimeRoot(), "bin", "stackkit") {
		return fmt.Errorf("installed StackKits pin points outside the runtime root")
	}
	release, err := executor.releaseCache().ResolvePin(executor.pinPath)
	if err != nil {
		return err
	}
	next, err := stackkitrelease.LoadPin(filepath.Join(stage, "stackkits-release-pin.json"))
	if err != nil {
		return err
	}
	if installed.Kit == next.Kit && installed.Version == next.Version && installed.Platform == next.Platform &&
		installed.ArchiveSHA256 == next.ArchiveSHA256 && installed.IndexSHA256 == next.IndexSHA256 && installed.BinarySHA256 == next.BinarySHA256 {
		// Refreshing the same immutable CLI cannot strand a different original.
		return nil
	}
	if !authenticated {
		return fmt.Errorf("replacement CLI requires authenticated staged runtime evidence")
	}
	cache, err := executor.persistentReleaseCache()
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(stage, cache); err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
		return fmt.Errorf("StackKits public artifact cache must be outside the staging tree")
	}
	cliDigest, err := stackKitRegularDigest(release.BinaryPath())
	if err != nil {
		return err
	}
	stagedCLI := filepath.Join(stage, "bin", "stackkit")
	stagedDigest, err := stackKitRegularDigest(stagedCLI)
	if err != nil || stagedDigest != next.BinarySHA256 {
		return fmt.Errorf("staged StackKits CLI differs from its verified pin")
	}
	ctx, cancel := context.WithTimeout(ctx, defaultStackKitCommandTimeout)
	defer cancel()
	if installed.Candidate != nil {
		material, err := executor.installedCandidateSource(installed)
		if err != nil {
			return err
		}
		return retainStackKitSourceCandidate(ctx, stagedCLI, cache, installed, cliDigest, material)
	}
	// Runtime bundles built before the pin named the kit of its own archive
	// label the bootstrap archive with the image's catalog kit. The CLI binds
	// archive, index and CLI digests exactly and verifies attestations, so the
	// kit is only a lookup key: try the pinned kit, then the other product
	// kits, and accept only an exact attested match.
	var refusal error
	for _, kit := range stackKitRetentionKits(installed.Kit) {
		mismatch, err := retainStackKitSourceRelease(ctx, stagedCLI, cache, kit, installed, cliDigest)
		if err == nil {
			return nil
		}
		if !mismatch {
			return err
		}
		if refusal == nil {
			refusal = err
		}
	}
	return refusal
}

// stackKitRetentionProductKits are the product kits whose archive a runtime
// bundle can carry.
var stackKitRetentionProductKits = []string{"basement-kit", "cloud-kit", "modern-homelab"}

func stackKitRetentionKits(pinned string) []string {
	kits := []string{pinned}
	for _, kit := range stackKitRetentionProductKits {
		if kit != pinned {
			kits = append(kits, kit)
		}
	}
	return kits
}

// retainStackKitSourceRelease reports mismatch when the CLI refused only
// because the published release of kit carries a different archive.
func retainStackKitSourceRelease(ctx context.Context, stagedCLI, cache, kit string, installed stackkitrelease.Pin, cliDigest string) (bool, error) {
	process := newStackKitProcess(ctx, stagedCLI, "--no-log", "kit", "prepare-source-generation", "--cache-only", "--release-cache", cache,
		"--source-kit", kit, "--source-release", installed.Version,
		"--source-os", installed.Platform.OS, "--source-arch", installed.Platform.Arch,
		"--source-archive-sha256", installed.ArchiveSHA256, "--source-index-sha256", installed.IndexSHA256,
		"--source-cli-sha256", cliDigest, "--json") // #nosec G204 -- fixed CLI beneath the authenticated private stage; arguments bind the installed pin and host cache.
	process.Env = stackKitChildEnvironment(os.Environ())
	stdout, stderr := newBoundedStackKitBuffer(maxStackKitCommandOutputBytes), newBoundedStackKitBuffer(maxStackKitCommandOutputBytes)
	process.Stdout, process.Stderr = stdout, stderr
	if err := process.Run(); err != nil {
		return strings.Contains(stderr.String(), "published source release differs from the installed admission"),
			fmt.Errorf("public artifact retention failed: %w: %s", err, errorString(fmt.Errorf("%s", stderr.String())))
	}
	var result struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			Prepared      bool                    `json:"prepared"`
			SourceRelease stackkitrelease.Receipt `json:"sourceRelease"`
			CLISHA256     string                  `json:"cliSha256"`
		} `json:"data"`
	}
	if stdout.truncated || json.Unmarshal(stdout.buffer.Bytes(), &result) != nil {
		return false, fmt.Errorf("public artifact retention returned invalid evidence")
	}
	proof := result.Data.SourceRelease
	if result.SchemaVersion != stackKitCommandResultVersion || result.Command != "stackkit kit prepare-source-generation" || result.Status != "success" ||
		!result.Data.Prepared || result.Data.CLISHA256 != cliDigest || proof.SchemaVersion != stackkitrelease.ReceiptSchemaVersion ||
		proof.Kit != kit || proof.Version != installed.Version || proof.Platform != installed.Platform ||
		proof.ArchiveSHA256 != installed.ArchiveSHA256 || proof.IndexSHA256 != installed.IndexSHA256 ||
		!stackKitSHA256HexPattern.MatchString(proof.AttestationSHA256) || !stackKitSHA256HexPattern.MatchString(proof.IndexAttestationSHA256) ||
		!stackKitSHA256HexPattern.MatchString(proof.TrustedRootSHA256) {
		return false, fmt.Errorf("public artifact retention does not bind the installed StackKits artifact and CLI")
	}
	return false, nil
}

var stackKitSHA256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func stackKitRegularDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512<<20 {
		return "", fmt.Errorf("StackKits CLI must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("StackKits CLI changed before hashing")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, info.Size()+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || written != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return "", fmt.Errorf("StackKits CLI changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// persistentReleaseCache is host configuration, independent of child env and
// outside both trees that runtime replacement may delete. Refuse symlink routes.
func (executor *StackKitExecutor) persistentReleaseCache() (string, error) {
	if !filepath.IsAbs(executor.cacheRoot) {
		return "", fmt.Errorf("StackKits public artifact cache must be absolute")
	}
	cache := filepath.Clean(executor.cacheRoot)
	root, err := filepath.Abs(executor.RuntimeRoot())
	if err != nil {
		return "", err
	}
	if !outsideReplacedStackKitRuntime(root, cache) {
		// Guards laid out before the persistent cache existed keep the cache
		// inside the runtime tree, and executor.env is written only at layout
		// time. Their runtime could never converge again, so they retain into
		// the host default the current layout configures.
		if runtime.GOOS != "linux" || !outsideReplacedStackKitRuntime(root, defaultStackKitPersistentReleaseCache) {
			return "", fmt.Errorf("StackKits public artifact cache must be outside the replaced runtime")
		}
		cache = defaultStackKitPersistentReleaseCache
	}
	if err := stackKitTrustedDirectoryPath(cache); err != nil {
		return "", err
	}
	return cache, nil
}

func outsideReplacedStackKitRuntime(root, cache string) bool {
	for _, replaced := range []string{root, root + ".previous"} {
		rel, err := filepath.Rel(replaced, cache)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)) {
			return false
		}
	}
	return true
}

func stackKitTrustedDirectoryPath(leaf string) error {
	protectedChild := false
	for path := leaf; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("StackKits host path must use only directories without symlinks")
		}
		if err == nil && !(path != leaf && trustedStackKitTempAncestor(path, info)) {
			anchor, openErr := os.OpenRoot(path)
			if openErr != nil {
				return openErr
			}
			ownerErr := checkStackKitRuntimeAncestor(anchor, path != leaf && filepath.Dir(path) == path && protectedChild)
			_ = anchor.Close()
			if ownerErr != nil {
				return fmt.Errorf("StackKits host path ancestor %s is writable by another principal: %w", path, ownerErr)
			}
		}
		protectedChild = err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return nil
}

// Socket-activated Guard executors are separate processes. The sibling lock
// survives runtime renames and protects the selected CLI through completion.
func (executor *StackKitExecutor) lockRuntime(ctx context.Context, exclusive bool) (func(), error) {
	if !executor.Available() {
		return nil, fmt.Errorf("StackKits runtime paths are not configured")
	}
	parent := filepath.Dir(executor.RuntimeRoot())
	if err := stackKitTrustedDirectoryPath(parent); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkStackKitRuntimeAncestor(root, false); err != nil {
		return nil, err
	}
	name := "." + filepath.Base(executor.RuntimeRoot()) + ".runtime.lock"
	file, err := privatefs.OpenFile(root, name, os.O_CREATE|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		file, err = privatefs.OpenFile(root, name, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		locked, err := tryStackKitRuntimeLock(file, exclusive)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() { releaseStackKitRuntimeLock(file) }, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (executor *StackKitExecutor) currentStackKitRuntimeEvidence() (StackKitRuntimeConvergenceResult, error) {
	release, err := executor.releaseCache().ResolvePin(executor.pinPath)
	if err != nil {
		return StackKitRuntimeConvergenceResult{}, err
	}
	receipt := release.Receipt()
	return StackKitRuntimeConvergenceResult{Version: receipt.Version, SHA256: receipt.ArchiveSHA256}, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// installStackKitRuntimeBundleChecked extracts the bundle into a staging
// directory, lets check inspect it, and only then activates it.
func installStackKitRuntimeBundleChecked(bundlePath, targetRoot, bundleDigest string, check func(stageDir string) error, validate func() error) error {
	parent := filepath.Dir(targetRoot)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".stackkit-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	file, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == ".stackkit" {
			continue
		}
		prefix := ".stackkit" + string(filepath.Separator)
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("unsafe StackKits bundle path %q", header.Name)
		}
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("unsafe StackKits bundle path %q", header.Name)
		}
		destination := filepath.Join(stage, rel)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0755); err != nil {
				return err
			}
		case tar.TypeReg, legacyTarRegularFileType:
			if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
				return err
			}
			mode := header.FileInfo().Mode().Perm() & 0755
			out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, io.LimitReader(reader, header.Size+1))
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported StackKits bundle entry %q", header.Name)
		}
	}
	if check != nil {
		if err := check(stage); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(stage, stackKitRuntimeBundleDigestFile), []byte(bundleDigest+"\n"), 0600); err != nil {
		return err
	}
	backup := targetRoot + ".previous"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(targetRoot); err == nil {
		if err := os.Rename(targetRoot, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, targetRoot); err != nil {
		_ = os.Rename(backup, targetRoot)
		return err
	}
	if validate != nil {
		if err := validate(); err != nil {
			_ = os.RemoveAll(targetRoot)
			_ = os.Rename(backup, targetRoot)
			return fmt.Errorf("validate installed StackKits runtime: %w", err)
		}
	}
	_ = os.RemoveAll(backup)
	return nil
}

// InstalledStackKitRuntimeBundleDigestAt reads the installer's existing
// root-owned activation marker without acquiring another executor lock. The
// privileged admission loader calls it while its caller holds that lock.
func InstalledStackKitRuntimeBundleDigestAt(targetRoot string) (string, error) {
	return readInstalledStackKitBundleDigest(targetRoot)
}

func readInstalledStackKitBundleDigest(targetRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(targetRoot, stackKitRuntimeBundleDigestFile))
	if err != nil {
		return "", err
	}
	digest := strings.ToLower(strings.TrimSpace(string(data)))
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("installed StackKits bundle digest is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("installed StackKits bundle digest is invalid")
	}
	return digest, nil
}
