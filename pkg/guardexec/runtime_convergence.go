package guardexec

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/guardrelease"
)

const runtimeCandidateFile = "stackkit-runtime-candidate.tar.gz"
const runtimeCandidateRecord = "stackkit-runtime-candidate.json"

type runtimeBoundary struct {
	describe  func(context.Context) (RuntimeDescription, error)
	preflight func(context.Context, RuntimeInstallParams) (RuntimeAdmission, error)
	install   func(context.Context, RuntimeInstallParams) (RuntimeEvidence, error)
}

type runtimeRetry struct {
	After time.Time
	Cause error
}

func (e *runtimeRetry) Error() string {
	return fmt.Sprintf("runtime retry after %s: %v", e.After.UTC().Format(time.RFC3339), e.Cause)
}
func (e *runtimeRetry) Unwrap() error { return e.Cause }

// One record/file per Guard. No credential, raw peer identity or admission token
// is persisted. Fresh signed policy and worker admission precede every attempt.
type runtimeCandidate struct {
	Schema      string    `json:"schema"`
	Scope       string    `json:"scope"`
	Identity    string    `json:"identity,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	Failures    int       `json:"failures"`
	NextAttempt time.Time `json:"next_attempt"`
	Outcome     string    `json:"outcome"`
}

type runtimeConvergence struct{ mu sync.Mutex }

func runtimeBackoff(failures int) time.Duration {
	delay := time.Minute
	for i := 1; i < failures && delay < 15*time.Minute; i++ {
		delay *= 2
	}
	return min(delay, 15*time.Minute)
}

func (c *runtimeConvergence) converge(ctx context.Context, cfg agentpkg.StackKitRuntimeBootstrapConfig, b runtimeBoundary, clock func() time.Time) (agentpkg.StackKitRuntimeConvergenceResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero agentpkg.StackKitRuntimeConvergenceResult
	if clock == nil {
		clock = time.Now
	}
	now := clock().UTC()
	if cfg.StageDir == "" {
		return zero, errors.New("runtime convergence needs its private handoff directory")
	}
	if err := os.MkdirAll(cfg.StageDir, 0700); err != nil {
		return zero, err
	}
	recordPath := filepath.Join(cfg.StageDir, runtimeCandidateRecord)
	candidatePath := filepath.Join(cfg.StageDir, runtimeCandidateFile)
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.URL+"\x00"+cfg.TenantID+"\x00"+cfg.RuntimeAgentID)))
	state := runtimeCandidate{Schema: "techstack.runtime-candidate/v1", Scope: scope}
	if raw, err := readBounded(recordPath, 16<<10); err == nil {
		if err := decodeStrict(raw, &state); err != nil || state.Schema != "techstack.runtime-candidate/v1" || state.Failures < 0 || state.Failures > 32 {
			return zero, errors.New("runtime candidate record is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	clear := func() error {
		for _, path := range []string{candidatePath, recordPath} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		state = runtimeCandidate{Schema: "techstack.runtime-candidate/v1", Scope: scope}
		return nil
	}
	if state.Scope != scope {
		if err := clear(); err != nil {
			return zero, err
		}
	}
	fail := func(cause error) (agentpkg.StackKitRuntimeConvergenceResult, error) {
		state.Failures = min(state.Failures+1, 32)
		delay := runtimeBackoff(state.Failures)
		var status *agentpkg.ArtifactHTTPError
		if errors.As(cause, &status) {
			delay = max(delay, status.RetryAfter)
		}
		// Start the retry window when the failed operation returned. Describe,
		// signed evidence and an HTTP refusal can consume minutes after entry.
		state.NextAttempt = clock().UTC().Add(min(delay, 24*time.Hour))
		state.Outcome = "refused_or_unknown"
		if err := writeJSONAtomic(recordPath, state, 0600); err != nil {
			return zero, fmt.Errorf("%w; persist runtime retry: %v", cause, err)
		}
		return zero, &runtimeRetry{After: state.NextAttempt, Cause: cause}
	}
	// Marker/describe failures are not an empty cache hint. An acknowledged or
	// unknown install is read independently before any possible second effect.
	description, err := b.describe(ctx)
	if err != nil {
		return fail(err)
	}
	if !description.Available {
		return fail(errors.New("StackKits runtime paths are not configured"))
	}
	possibleCommitted := description.Valid && state.SHA256 != "" && description.InstalledDigest == state.SHA256
	if now.Before(state.NextAttempt) && !possibleCommitted {
		return zero, &runtimeRetry{After: state.NextAttempt, Cause: errors.New("runtime candidate remains in bounded backoff")}
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	signed, err := fetchReleaseEvidence(ctx, client, agentpkg.TechstackRuntimeConvergenceConfig{URL: cfg.URL, AgentToken: cfg.AgentToken, RuntimeAgentID: cfg.RuntimeAgentID, TenantID: cfg.TenantID})
	if err != nil {
		return fail(err)
	}
	params := RuntimeInstallParams{}
	if signed != nil {
		manifest, err := base64.StdEncoding.DecodeString(signed.Manifest)
		if err != nil {
			return fail(errors.New("runtime manifest is not base64"))
		}
		for name, raw := range map[string][]byte{"runtime-manifest.json": manifest, "runtime-manifest.sigstore.json": signed.ManifestBundle} {
			if err := writeHandoffFile(cfg.StageDir, name, raw); err != nil {
				return fail(err)
			}
		}
		params.ManifestFile, params.ManifestBundleFile = "runtime-manifest.json", "runtime-manifest.sigstore.json"
		params.CandidateIntent, params.CandidateAdmission = signed.CandidateIntent, signed.CandidateAdmission
		if params.CandidateIntent != nil {
			params.SHA256 = params.CandidateIntent.BundleSHA256
		}
	}
	admission, err := b.preflight(ctx, params)
	if err != nil {
		return fail(err)
	}
	if admission.InstalledCurrent && description.Valid {
		if err := clear(); err != nil {
			return zero, err
		}
		return agentpkg.StackKitRuntimeConvergenceResult{Version: description.Version, SHA256: description.ArchiveSHA256}, nil
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(scope+"\x00"+admission.IdentitySHA256)))
	if state.Identity != "" && state.Identity != identity {
		if err := clear(); err != nil {
			return zero, err
		}
	}
	state.Identity = identity
	verify := func(path string) error {
		if !admission.Signed {
			return errors.New("unsigned runtime bytes cannot be a retained verified candidate")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxRuntimeBundle {
			return errors.New("runtime candidate is not a bounded regular file")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		return guardrelease.VerifyRuntimeBundle(admission.Runtime, file)
	}
	if state.SHA256 != "" {
		digest, err := fileDigest(candidatePath)
		if err != nil || digest != state.SHA256 {
			if err := clear(); err != nil {
				return zero, err
			}
			return fail(errors.New("retained runtime candidate digest changed"))
		}
		if !admission.Signed {
			if err := clear(); err != nil {
				return zero, err
			}
			return fail(errors.New("unverified cached runtime is not reusable"))
		}
		if err := verify(candidatePath); err != nil {
			if cleanup := clear(); cleanup != nil {
				return zero, cleanup
			}
			return fail(err)
		}
	} else {
		bundle, err := agentpkg.FetchStackKitRuntimeBundle(ctx, cfg, description.InstalledDigest)
		if err != nil {
			return fail(err)
		}
		if bundle.NotModified {
			if !admission.Signed && description.Valid {
				if err := clear(); err != nil {
					return zero, err
				}
				return agentpkg.StackKitRuntimeConvergenceResult{Version: description.Version, SHA256: description.ArchiveSHA256}, nil
			}
			return fail(errors.New("control plane reports current runtime without matching authenticated installed tree"))
		}
		if admission.Signed {
			if err := verify(bundle.Path); err != nil {
				_ = os.Remove(bundle.Path)
				return fail(err)
			}
		}
		if params.CandidateIntent != nil && params.CandidateIntent.BundleSHA256 != bundle.SHA256 {
			_ = os.Remove(bundle.Path)
			return fail(errors.New("runtime body differs from worker-bound candidate intent"))
		}
		if err := os.Rename(bundle.Path, candidatePath); err != nil {
			_ = os.Remove(bundle.Path)
			return fail(err)
		}
		state.SHA256 = bundle.SHA256
		if err := writeJSONAtomic(recordPath, state, 0600); err != nil {
			return zero, err
		}
	}
	params.BundleFile, params.SHA256 = runtimeCandidateFile, state.SHA256
	// A durable dispatch fence precedes the one bounded install. Every later
	// attempt needs fresh authority and independent installed-state readback.
	state.Failures = min(state.Failures+1, 32)
	state.NextAttempt = clock().UTC().Add(runtimeBackoff(state.Failures))
	state.Outcome = "install_dispatched"
	if err := writeJSONAtomic(recordPath, state, 0600); err != nil {
		return zero, err
	}
	_, installErr := b.install(ctx, params)
	after, readErr := b.describe(ctx)
	if readErr == nil && after.Valid && after.InstalledDigest == state.SHA256 {
		if err := clear(); err != nil {
			return zero, err
		}
		return agentpkg.StackKitRuntimeConvergenceResult{Version: after.Version, SHA256: after.ArchiveSHA256}, nil
	}
	if installErr == nil {
		installErr = errors.New("runtime install has no independent committed readback")
	}
	if readErr != nil {
		installErr = fmt.Errorf("%w; runtime outcome readback: %v", installErr, readErr)
	}
	state.Outcome = "refused_or_unknown"
	if !admission.Signed {
		if err := os.Remove(candidatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return zero, err
		}
		state.SHA256 = ""
	}
	if err := writeJSONAtomic(recordPath, state, 0600); err != nil {
		return zero, fmt.Errorf("%w; persist outcome: %v", installErr, err)
	}
	return zero, &runtimeRetry{After: state.NextAttempt, Cause: installErr}
}
