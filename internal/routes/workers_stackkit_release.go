package routes

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const stackKitReleaseBundleEnv = "TECHSTACK_STACKKIT_RELEASE_BUNDLE"

func currentStackKitReleaseBundle() (agentBinaryArtifact, error) {
	path := strings.TrimSpace(os.Getenv(stackKitReleaseBundleEnv))
	if path == "" {
		return agentBinaryArtifact{}, fmt.Errorf("%s is not configured", stackKitReleaseBundleEnv)
	}
	return agentBinaryArtifactForPath(path, managedRuntimeOSLinux, "amd64")
}

func (h workerRouteHandlers) agentStackKitRelease(e *httpx.Event) error {
	requestedOS := strings.ToLower(strings.TrimSpace(e.Request.PathValue("os")))
	requestedArch := normalizeAgentBinaryArch(e.Request.PathValue("arch"))
	if requestedOS != managedRuntimeOSLinux || requestedArch == "" {
		return httpx.BadRequest(e, "Unsupported StackKits runtime platform", nil)
	}
	rawToken := bearerToken(e.Request)
	if rawToken == "" {
		return httpx.Unauthorized(e, "A valid agent download capability is required")
	}
	now := time.Now().UTC()
	authorization, authorized, err := h.authorizeAgentBinary(e, rawToken, now)
	if err != nil || !authorized {
		return err
	}
	artifactProvider := h.stackKitReleaseArtifact
	if artifactProvider == nil {
		artifactProvider = currentStackKitReleaseBundle
	}
	artifact, err := artifactProvider()
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits release is temporarily unavailable", nil)
	}
	if artifact.os != requestedOS || artifact.arch != requestedArch {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The deployed control plane does not publish StackKits for the requested platform", nil)
	}
	etag := `"` + artifact.sha256 + `"`
	e.Response.Header().Set("ETag", etag)
	e.Response.Header().Set(agentBinaryChecksumHeader, artifact.sha256)
	if strings.TrimSpace(e.Request.Header.Get("If-None-Match")) == etag {
		e.Response.Header().Set("Cache-Control", "private, no-store")
		e.Response.Header().Set("Vary", "Authorization")
		e.Response.WriteHeader(http.StatusNotModified)
		return nil
	}
	guard := h.binaryDownloadGuard
	if guard == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Artifact download guard is unavailable", nil)
	}
	release, allowed := guard.acquire("stackkit:"+authorization.tokenHash, authorization.resetAt, now, artifact.size)
	if !allowed {
		return httpx.Error(e, http.StatusTooManyRequests, ksapi.ErrCodeRateLimited, "StackKits release download is rate limited", nil)
	}
	defer release()
	file, err := os.Open(artifact.path)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits release is temporarily unavailable", nil)
	}
	defer file.Close()
	e.Response.Header().Set("Content-Type", "application/gzip")
	e.Response.Header().Set("Cache-Control", "private, no-store")
	e.Response.Header().Set("Vary", "Authorization")
	_ = http.NewResponseController(e.Response).SetWriteDeadline(time.Now().Add(agentBinaryWriteTimeout))
	// Same streaming contract as the Agent binary: the handler owns the status
	// line, so a transfer the platform cuts short is logged rather than turned
	// into a second response envelope, and only the bytes that reached the wire
	// are charged to the token budget.
	e.MarkResponseStreamed()
	counter := &agentBinaryTransferCounter{ResponseWriter: e.Response}
	started := time.Now()
	http.ServeContent(counter, e.Request, "techstack-stackkit-runtime.tar.gz", artifact.modTime, file)
	guard.record("stackkit:"+authorization.tokenHash, counter.written)
	logAgentBinaryTransfer(e, artifact, counter, started, nil)
	return nil
}
