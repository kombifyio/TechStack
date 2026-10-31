package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

const agentBinaryChecksumHeader = "X-Kombify-Artifact-SHA256"
const agentBinaryRuntimeAgentIDHeader = "X-Kombify-Runtime-Agent-ID"

const (
	agentBinaryMaxAttemptsPerToken = 2
	agentBinaryDefaultTokenWindow  = 15 * time.Minute
	agentBinaryMaxConcurrent       = 2
	agentBinaryMaxTrackedTokens    = 2048
	agentBinaryWriteTimeout        = 5 * time.Minute
)

type agentBinaryArtifact struct {
	path    string
	os      string
	arch    string
	size    int64
	sha256  string
	modTime time.Time
}

type agentBinaryAuthorization struct {
	tokenHash string
	resetAt   time.Time
	runtime   *runtimeAgentAuthContext
}

var (
	runningAgentBinaryOnce     sync.Once
	runningAgentBinaryArtifact agentBinaryArtifact
	runningAgentBinaryErr      error
)

// agentBinaryDownloadAttempt meters a token window in BYTES, not requests.
//
// Counting requests made the artifact undownloadable the moment it had to be
// fetched in slices: Render's proxy resets the origin connection a few MB into
// a ~78 MB response (proven 2026-08-12: "write: connection reset by peer" after
// 2.2 MB), so a converging Agent must issue many Range requests, and a
// two-request ceiling denied it on the third slice. A byte budget expresses the
// same intent — roughly two full artifacts per token window — without caring
// how many responses it took.
type agentBinaryDownloadAttempt struct {
	bytesServed int64
	resetAt     time.Time
}

type agentBinaryDownloadGuard struct {
	mu       sync.Mutex
	attempts map[string]agentBinaryDownloadAttempt
	slots    chan struct{}
}

func newAgentBinaryDownloadGuard() *agentBinaryDownloadGuard {
	return &agentBinaryDownloadGuard{
		attempts: make(map[string]agentBinaryDownloadAttempt),
		slots:    make(chan struct{}, agentBinaryMaxConcurrent),
	}
}

// acquire admits one transfer when the token still has budget left. budgetBytes
// is the artifact size; the window allows agentBinaryMaxAttemptsPerToken times
// that many bytes, so a single-response download and a sliced one cost the
// same. Served bytes are booked afterwards through record, because how much a
// response actually carries is only known once it has been written.
func (g *agentBinaryDownloadGuard) acquire(tokenHash string, tokenExpiresAt, now time.Time, budgetBytes int64) (func(), bool) {
	if g == nil || tokenHash == "" || budgetBytes <= 0 {
		return nil, false
	}

	select {
	case g.slots <- struct{}{}:
	default:
		return nil, false
	}

	g.mu.Lock()
	for key, candidate := range g.attempts {
		if !now.Before(candidate.resetAt) {
			delete(g.attempts, key)
		}
	}
	attempt := g.attempts[tokenHash]
	if attempt.resetAt.IsZero() || !now.Before(attempt.resetAt) {
		if !tokenExpiresAt.After(now) {
			tokenExpiresAt = now.Add(agentBinaryDefaultTokenWindow)
		}
		if len(g.attempts) >= agentBinaryMaxTrackedTokens {
			evictNextExpiringAgentBinaryDownloadAttempt(g.attempts)
		}
		attempt = agentBinaryDownloadAttempt{resetAt: tokenExpiresAt}
	}
	if attempt.bytesServed >= budgetBytes*agentBinaryMaxAttemptsPerToken {
		g.mu.Unlock()
		<-g.slots
		return nil, false
	}
	g.attempts[tokenHash] = attempt
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { <-g.slots })
	}, true
}

// record books the bytes a completed transfer actually served. A truncated
// transfer books only what reached the wire, so a connection the platform reset
// does not spend the budget an Agent needs to converge.
func (g *agentBinaryDownloadGuard) record(tokenHash string, served int64) {
	if g == nil || tokenHash == "" || served <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	attempt, tracked := g.attempts[tokenHash]
	if !tracked {
		return
	}
	attempt.bytesServed += served
	g.attempts[tokenHash] = attempt
}

func evictNextExpiringAgentBinaryDownloadAttempt(attempts map[string]agentBinaryDownloadAttempt) {
	candidateKey := ""
	var candidateResetAt time.Time
	for key, attempt := range attempts {
		if candidateKey == "" || attempt.resetAt.Before(candidateResetAt) ||
			(attempt.resetAt.Equal(candidateResetAt) && key < candidateKey) {
			candidateKey = key
			candidateResetAt = attempt.resetAt
		}
	}
	if candidateKey != "" {
		delete(attempts, candidateKey)
	}
}

// currentAgentBinaryArtifact describes the exact executable backing this
// control-plane process. Metadata is calculated once because a deployed
// executable is immutable for the lifetime of the process; the client still
// verifies every downloaded byte against the returned SHA-256.
func currentAgentBinaryArtifact() (agentBinaryArtifact, error) {
	runningAgentBinaryOnce.Do(func() {
		if dir := guardReleaseDir(); dir != "" {
			runningAgentBinaryArtifact, runningAgentBinaryErr = indexedGuardBinaryArtifact(managedRuntimeOSLinux, "amd64")
			return
		}
		if path := strings.TrimSpace(os.Getenv("TECHSTACK_AGENT_BINARY_LINUX_AMD64")); path != "" {
			runningAgentBinaryArtifact, runningAgentBinaryErr = agentBinaryArtifactForPath(path, managedRuntimeOSLinux, "amd64")
			return
		}
		if runtime.GOOS != managedRuntimeOSLinux {
			runningAgentBinaryErr = fmt.Errorf("running binary is not a Linux artifact")
			return
		}
		path, err := os.Executable()
		if err != nil {
			runningAgentBinaryErr = err
			return
		}
		runningAgentBinaryArtifact, runningAgentBinaryErr = agentBinaryArtifactForPath(path, runtime.GOOS, runtime.GOARCH)
	})
	return runningAgentBinaryArtifact, runningAgentBinaryErr
}

// CurrentAgentBinarySHA256 returns the digest of the immutable executable
// served to managed Agents. The Operations installer copies these exact bytes,
// so rollout Inventory must use this value instead of a second release claim.
func CurrentAgentBinarySHA256() (string, error) {
	artifact, err := currentAgentBinaryArtifact()
	if err != nil {
		return "", err
	}
	if artifact.sha256 == "" {
		return "", errors.New("current agent binary has no SHA-256")
	}
	return "sha256:" + artifact.sha256, nil
}

func agentBinaryArtifactForPath(path, artifactOS, artifactArch string) (agentBinaryArtifact, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return agentBinaryArtifact{}, err
	}
	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return agentBinaryArtifact{}, err
	}
	// #nosec G703 -- the operator-selected artifact path is canonicalized once at process startup.
	file, err := os.Open(canonicalPath)
	if err != nil {
		return agentBinaryArtifact{}, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return agentBinaryArtifact{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return agentBinaryArtifact{}, fmt.Errorf("agent artifact must be a non-empty regular file")
	}

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return agentBinaryArtifact{}, err
	}

	return agentBinaryArtifact{
		path:    canonicalPath,
		os:      strings.ToLower(strings.TrimSpace(artifactOS)),
		arch:    normalizeAgentBinaryArch(artifactArch),
		size:    info.Size(),
		sha256:  hex.EncodeToString(digest.Sum(nil)),
		modTime: info.ModTime().UTC(),
	}, nil
}

// agentInstallPins returns, per Linux architecture, the SHA-256 of the exact
// Guard binary agentBinary serves, so the enrollment command the dashboard
// shows carries TECHSTACK_BINARY_SHA256 and install.sh refuses any other bytes
// (threat model F4). With a signed release an architecture is pinned only when
// its file matches the signed manifest entry; an empty set means no trusted
// digest is known and the dashboard must not offer a command.
func (h workerRouteHandlers) agentInstallPins(e *httpx.Event) error {
	if ownerID, err := requireWorkerOwner(e); err != nil || ownerID == "" {
		return err
	}
	index, _, err := loadArtifactDeliveryIndex()
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Artifact delivery admission is unavailable", nil)
	}
	current, _, err := currentGuardManifest(guardReleaseDir())
	if err != nil || current.manifest.Fresh(time.Now().UTC()) != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The signed artifact policy is unavailable", nil)
	}
	pins := map[string]string{}
	version := index.ReleaseVersion
	for _, entry := range index.Artifacts {
		if entry.Kind == "guard_binary" {
			pins[entry.Arch] = entry.SHA256
		}
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{
		"os":      managedRuntimeOSLinux,
		"version": version,
		"pins":    pins,
	})
}

// agentBinary admits delivery at the Cloudflare/R2 edge; it never serves a file.
func (h workerRouteHandlers) agentBinary(e *httpx.Event) error {
	return h.admitArtifact(e, "guard_binary")
}

// agentBinaryTransferCounter records how much of the artifact actually reached
// the wire. Unwrap keeps http.NewResponseController and http.Flusher working
// through the wrapper.
type agentBinaryTransferCounter struct {
	http.ResponseWriter
	status  int
	written int64
	// writeErr keeps the first write failure. http.ServeContent discards the
	// copy error, and without it a truncated transfer reports how far it got
	// but never why it stopped.
	writeErr error
}

func (w *agentBinaryTransferCounter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *agentBinaryTransferCounter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

func (w *agentBinaryTransferCounter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logAgentBinaryTransfer makes fleet self-update failures visible. A truncated
// transfer is otherwise indistinguishable from a healthy one in the request
// log: the status line is already 200 by the time the copy dies, so only the
// byte count proves the Agent never received a verifiable artifact.
func logAgentBinaryTransfer(
	e *httpx.Event,
	artifact agentBinaryArtifact,
	counter *agentBinaryTransferCounter,
	started time.Time,
	deadlineErr error,
) {
	expected := artifact.size
	if rangeHeader := strings.TrimSpace(e.Request.Header.Get("Range")); rangeHeader != "" {
		// A partial request is complete on its own terms; the byte count is
		// only comparable to the full artifact for an unranged transfer.
		expected = counter.written
	}
	complete := counter.written >= expected
	fields := []any{
		"runtime_agent_id", strings.TrimSpace(e.Request.Header.Get(agentBinaryRuntimeAgentIDHeader)),
		"artifact_sha256", artifact.sha256,
		"artifact_bytes", artifact.size,
		"written_bytes", counter.written,
		"status", counter.status,
		"range", strings.TrimSpace(e.Request.Header.Get("Range")),
		"duration_ms", time.Since(started).Milliseconds(),
		"write_deadline_supported", deadlineErr == nil,
		"proto", e.Request.Proto,
	}
	if counter.writeErr != nil {
		fields = append(fields, "write_error", counter.writeErr.Error())
	}
	if complete {
		logger.Get().Info("agent_binary_download_completed", fields...)
		return
	}
	logger.Get().Warn("agent_binary_download_truncated", fields...)
}

// authorizeAgentBinary accepts either the one-time pairing capability used by
// a new enrollment or the exact persistent credential of an already enrolled
// runtime agent. Runtime credentials are never searched globally: the caller
// must supply the non-secret tenant and runtime-agent identity, and the normal
// worker authentication path proves the stored token hash and placement.
func (h workerRouteHandlers) authorizeAgentBinary(
	e *httpx.Event,
	rawToken string,
	now time.Time,
) (agentBinaryAuthorization, bool, error) {
	runtimeAgentID := strings.TrimSpace(e.Request.Header.Get(agentBinaryRuntimeAgentIDHeader))
	tenantID := runtimeAgentTenantIDFromRequest(e)
	if runtimeAgentID != "" || tenantID != "" {
		if runtimeAgentID == "" || tenantID == "" {
			return agentBinaryAuthorization{}, false, httpx.Unauthorized(e, "A valid agent download capability is required")
		}
		authCtx, authenticated := h.authenticateRuntimeAgent(e, runtimeAgentID, workerInventoryRequest{
			TenantID:       tenantID,
			RuntimeAgentID: runtimeAgentID,
		})
		if !authenticated {
			return agentBinaryAuthorization{}, false, nil
		}
		tokenHash := workerauth.SHA256Hex(rawToken)
		if authCtx.IdentityBinding == controlplane.IdentityBindingHostKey {
			// Access tokens rotate every few minutes; the download budget
			// belongs to the worker, not to one short-lived token.
			tokenHash = workerauth.SHA256Hex("guard-worker\x00" + tenantID + "\x00" + runtimeAgentID)
		}
		return agentBinaryAuthorization{
			tokenHash: tokenHash,
			resetAt:   now.Add(agentBinaryDefaultTokenWindow),
			runtime:   &authCtx,
		}, true, nil
	}

	if _, parseErr := pairingtoken.Parse(rawToken); parseErr == nil {
		pairingToken, tokenHash, err := h.resolveStorePairingToken(e, rawToken)
		if err != nil || pairingToken == nil {
			return agentBinaryAuthorization{}, false, err
		}
		resetAt := now.Add(agentBinaryDefaultTokenWindow)
		if pairingToken.ExpiresAt != nil {
			resetAt = pairingToken.ExpiresAt.UTC()
		}
		return agentBinaryAuthorization{tokenHash: tokenHash, resetAt: resetAt}, true, nil
	}
	// A malformed locator must be indistinguishable from an invalid one:
	// naming the download-capability scheme here would hand probing clients a
	// token-format oracle (contract: TestPairingConsumersStopAfterMalformedLocatorRejection).
	return agentBinaryAuthorization{}, false, httpx.Unauthorized(e, "Invalid or expired token")
}

func normalizeAgentBinaryArch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case managedRuntimeArchAMD64, "x86_64":
		return managedRuntimeArchAMD64
	case "arm64", "aarch64":
		return "arm64"
	case "arm", "armv7", "armv7l":
		return "arm"
	default:
		return ""
	}
}

// workerRequestCredential returns the credential of an artifact download: the
// bearer capability of an installer or legacy agent, or the key-bound access
// token of a Guard whose proof authenticateRuntimeAgent verifies.
func workerRequestCredential(r *http.Request) string {
	if token := bearerToken(r); token != "" {
		return token
	}
	if scheme, credentials := authorizationScheme(r); scheme == strings.ToLower(guardidentity.TokenType) {
		return credentials
	}
	return ""
}
