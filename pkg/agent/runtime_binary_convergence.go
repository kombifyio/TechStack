package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/privatechannel"
)

const maxTechstackRuntimeBinaryBytes int64 = 512 << 20

const (
	// runtimeBinarySliceBytes must stay well below the point at which the
	// control plane's platform proxy resets a streamed response (observed:
	// 2.2-5.2 MB of a 77.7 MB artifact).
	runtimeBinarySliceBytes int64 = 2 << 20
	// runtimeBinarySliceAttempts bounds the retries for one slice. The offset
	// never advances on failure, so a link that cannot carry a slice at all
	// surfaces as an error instead of spinning.
	runtimeBinarySliceAttempts = 3
)

type TechstackRuntimeConvergenceConfig struct {
	URL, AgentToken, RuntimeAgentID, TenantID string
	AgentPath, OperationsPath                 string
	HTTPClient                                *http.Client
	PrivateLANHTTPOrigin                      string
}

type TechstackRuntimeConvergenceResult struct {
	AgentUpdated      bool
	OperationsUpdated bool
	SHA256            string
}

// EnsureTechstackRuntime converges both privileged node-side Techstack
// executables to the exact binary served by the authenticated control plane.
// The caller must restart the Agent when AgentUpdated is true.
func (executor *StackKitExecutor) EnsureTechstackRuntime(ctx context.Context, cfg TechstackRuntimeConvergenceConfig) (TechstackRuntimeConvergenceResult, error) {
	var result TechstackRuntimeConvergenceResult
	if executor == nil {
		return result, fmt.Errorf("StackKits executor is required")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	for label, path := range map[string]string{"agent": cfg.AgentPath, "operations": cfg.OperationsPath} {
		if !filepath.IsAbs(filepath.Clean(path)) {
			return result, fmt.Errorf("Techstack %s executable path must be absolute", label)
		}
	}
	parsedURL, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || parsedURL.Hostname() == "" || (parsedURL.Scheme != "https" && !(parsedURL.Scheme == "http" && isLoopbackHost(parsedURL.Hostname())) && !privatechannel.MatchesLANOrigin(cfg.URL, cfg.PrivateLANHTTPOrigin)) {
		return result, fmt.Errorf("Techstack runtime URL must use HTTPS or loopback HTTP")
	}
	localDigest, localErr := digestRuntimeExecutable(cfg.AgentPath)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	stage, expected, notModified, err := fetchRuntimeArtifact(ctx, client, cfg, localDigest)
	if err != nil {
		return result, err
	}
	if notModified {
		if localErr != nil {
			return result, fmt.Errorf("control plane reports current Techstack binary but local executable is invalid: %w", localErr)
		}
		result.SHA256 = localDigest
		updated, err := convergeRuntimeExecutable(cfg.AgentPath, cfg.OperationsPath, localDigest)
		result.OperationsUpdated = updated
		return result, err
	}
	defer os.Remove(stage)
	if err := os.Rename(stage, cfg.AgentPath); err != nil {
		return result, fmt.Errorf("activate Techstack Agent executable: %w", err)
	}
	result.AgentUpdated = localDigest != expected
	result.SHA256 = expected
	updated, err := convergeRuntimeExecutable(cfg.AgentPath, cfg.OperationsPath, expected)
	result.OperationsUpdated = updated
	return result, err
}

// fetchRuntimeArtifact pulls the exact control-plane executable into a staging
// file next to the target and returns its verified digest.
//
// The artifact is requested in slices rather than as one response. The control
// plane's platform proxy resets the origin connection a few megabytes into a
// large response — observed on 2026-08-12 as "write: connection reset by peer"
// after 2.2 MB of a 77.7 MB body — so a single-response download could never
// finish, and every Agent that needed an update retried forever without ever
// converging. Each slice is buffered whole before it is committed to the
// staging file, so a slice the proxy cuts short is retried without corrupting
// the running digest.
//
// A control plane that ignores Range answers 200 with the full body; that path
// still works and is simply the single-slice case.
func fetchRuntimeArtifact(
	ctx context.Context,
	client *http.Client,
	cfg TechstackRuntimeConvergenceConfig,
	localDigest string,
) (string, string, bool, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.AgentPath), 0755); err != nil {
		return "", "", false, err
	}
	stage, err := os.CreateTemp(filepath.Dir(cfg.AgentPath), ".techstack-runtime-*")
	if err != nil {
		return "", "", false, err
	}
	stagePath := stage.Name()
	abort := func(cause error) (string, string, bool, error) {
		_ = stage.Close()
		_ = os.Remove(stagePath)
		return "", "", false, cause
	}

	hash := sha256.New()
	expected := ""
	var offset, total int64
	for {
		slice, sliceErr := readRuntimeArtifactSlice(ctx, client, cfg, localDigest, offset)
		if sliceErr != nil {
			return abort(sliceErr)
		}
		if slice.notModified {
			_ = stage.Close()
			_ = os.Remove(stagePath)
			return "", "", true, nil
		}
		if expected == "" {
			expected = slice.digest
		} else if slice.digest != expected {
			// The control plane was redeployed mid-download. Restarting is the
			// only correct answer: the slices no longer describe one artifact.
			return abort(fmt.Errorf("Techstack runtime changed while downloading"))
		}
		if _, writeErr := stage.Write(slice.body); writeErr != nil {
			return abort(writeErr)
		}
		hash.Write(slice.body)
		offset += int64(len(slice.body))
		if offset > maxTechstackRuntimeBinaryBytes {
			return abort(fmt.Errorf("Techstack runtime exceeds size limit"))
		}
		total = slice.total
		if total <= 0 || offset >= total {
			break
		}
	}
	if total > 0 && offset != total {
		return abort(fmt.Errorf("Techstack runtime is incomplete: %d of %d bytes", offset, total))
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return abort(fmt.Errorf("Techstack runtime checksum mismatch"))
	}
	if err := stage.Chmod(0755); err != nil {
		return abort(err)
	}
	if err := stage.Close(); err != nil {
		_ = os.Remove(stagePath)
		return "", "", false, err
	}
	return stagePath, expected, false, nil
}

type runtimeArtifactSlice struct {
	body        []byte
	digest      string
	total       int64
	notModified bool
}

// readRuntimeArtifactSlice fetches one bounded slice, retrying a transfer the
// platform cut short. Retries are bounded and never advance the offset, so a
// permanently failing link surfaces as an error instead of an endless loop.
func readRuntimeArtifactSlice(
	ctx context.Context,
	client *http.Client,
	cfg TechstackRuntimeConvergenceConfig,
	localDigest string,
	offset int64,
) (runtimeArtifactSlice, error) {
	var lastErr error
	for attempt := 0; attempt < runtimeBinarySliceAttempts; attempt++ {
		slice, err := requestRuntimeArtifactSlice(ctx, client, cfg, localDigest, offset)
		if err == nil {
			return slice, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return runtimeArtifactSlice{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return runtimeArtifactSlice{}, lastErr
}

func requestRuntimeArtifactSlice(
	ctx context.Context,
	client *http.Client,
	cfg TechstackRuntimeConvergenceConfig,
	localDigest string,
	offset int64,
) (runtimeArtifactSlice, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, nil)
	if err != nil {
		return runtimeArtifactSlice{}, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AgentToken))
	request.Header.Set("X-Kombify-Runtime-Agent-ID", strings.TrimSpace(cfg.RuntimeAgentID))
	request.Header.Set("X-Kombify-Tenant-ID", strings.TrimSpace(cfg.TenantID))
	if len(localDigest) == sha256.Size*2 {
		request.Header.Set("If-None-Match", `"`+localDigest+`"`)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+runtimeBinarySliceBytes-1))

	response, err := client.Do(request)
	if err != nil {
		return runtimeArtifactSlice{}, err
	}
	defer func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotModified {
		return runtimeArtifactSlice{notModified: true}, nil
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return runtimeArtifactSlice{}, fmt.Errorf("Techstack runtime download returned HTTP %d", response.StatusCode)
	}
	digest := strings.ToLower(strings.TrimSpace(response.Header.Get("X-Kombify-Artifact-SHA256")))
	if len(digest) != sha256.Size*2 {
		return runtimeArtifactSlice{}, fmt.Errorf("Techstack runtime checksum is missing")
	}
	// A control plane that ignores Range restarts at byte 0; accepting its body
	// at a non-zero offset would splice the artifact together wrongly.
	if response.StatusCode == http.StatusOK && offset != 0 {
		return runtimeArtifactSlice{}, fmt.Errorf("Techstack runtime does not support resuming at %d bytes", offset)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxTechstackRuntimeBinaryBytes+1))
	if err != nil {
		return runtimeArtifactSlice{}, err
	}
	if len(body) == 0 {
		return runtimeArtifactSlice{}, fmt.Errorf("Techstack runtime slice at %d bytes is empty", offset)
	}
	slice := runtimeArtifactSlice{body: body, digest: digest}
	if response.StatusCode == http.StatusPartialContent {
		total, parseErr := totalFromContentRange(response.Header.Get("Content-Range"))
		if parseErr != nil {
			return runtimeArtifactSlice{}, parseErr
		}
		slice.total = total
	}
	return slice, nil
}

// totalFromContentRange reads the artifact length out of a `bytes a-b/total`
// header. The length is what tells the caller when the artifact is complete, so
// an unparseable header is an error rather than an assumed single slice.
func totalFromContentRange(header string) (int64, error) {
	_, sizePart, found := strings.Cut(strings.TrimSpace(header), "/")
	if !found {
		return 0, fmt.Errorf("Techstack runtime range header %q has no artifact size", header)
	}
	total, err := strconv.ParseInt(strings.TrimSpace(sizePart), 10, 64)
	if err != nil || total <= 0 {
		return 0, fmt.Errorf("Techstack runtime range header %q has no usable artifact size", header)
	}
	return total, nil
}

func downloadRuntimeExecutable(source io.Reader, targetPath, expected string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return "", err
	}
	stage, err := os.CreateTemp(filepath.Dir(targetPath), ".techstack-runtime-*")
	if err != nil {
		return "", err
	}
	stagePath := stage.Name()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(stage, hash), io.LimitReader(source, maxTechstackRuntimeBinaryBytes+1))
	if copyErr == nil && written > maxTechstackRuntimeBinaryBytes {
		copyErr = fmt.Errorf("Techstack runtime exceeds size limit")
	}
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != expected {
		copyErr = fmt.Errorf("Techstack runtime checksum mismatch")
	}
	if copyErr == nil {
		copyErr = stage.Chmod(0755)
	}
	closeErr := stage.Close()
	if copyErr != nil {
		_ = os.Remove(stagePath)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(stagePath)
		return "", closeErr
	}
	return stagePath, nil
}

func convergeRuntimeExecutable(sourcePath, targetPath, expected string) (bool, error) {
	if digest, err := digestRuntimeExecutable(targetPath); err == nil && digest == expected {
		return false, nil
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return false, err
	}
	defer source.Close()
	stage, err := downloadRuntimeExecutable(source, targetPath, expected)
	if err != nil {
		return false, err
	}
	defer os.Remove(stage)
	if err := os.Rename(stage, targetPath); err != nil {
		return false, fmt.Errorf("activate Techstack operations executable: %w", err)
	}
	return true, nil
}

func digestRuntimeExecutable(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxTechstackRuntimeBinaryBytes {
		return "", fmt.Errorf("Techstack runtime executable is not a bounded regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
