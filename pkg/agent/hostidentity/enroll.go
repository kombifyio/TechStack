package hostidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// EnrollmentView is the part of an enrollment response the identity protocol
// needs. The complete response is persisted verbatim for the rest of the Guard.
type EnrollmentView struct {
	RuntimeAgentID string
	AgentToken     string
	TenantID       string
	HeartbeatURL   string
	Endpoints      Endpoints
	// Accepted is nil when the response did not say.
	Accepted *bool
	// PrivateLANOrigin and PrivateLANPin name the private-LAN bridge this
	// enrollment was redeemed through, and the key pin it presented.
	PrivateLANOrigin string
	PrivateLANPin    string
}

type enrollmentFields struct {
	RuntimeAgentID    string `json:"runtime_agent_id"`
	WorkerID          string `json:"worker_id"`
	AgentToken        string `json:"agent_token"`
	TenantID          string `json:"tenant_id"`
	HeartbeatURL      string `json:"heartbeat_url"`
	IdentityTokenURL  string `json:"identity_token_url"`
	IdentityBindURL   string `json:"identity_bind_url"`
	IdentityRotateURL string `json:"identity_rotate_url"`
	Accepted          *bool  `json:"accepted"`
	PrivateLANOrigin  string `json:"private_lan_origin"`
	PrivateLANPin     string `json:"private_lan_pin"`
	ChannelBootstrap  struct {
		TenantID          string `json:"tenant_id"`
		HeartbeatURL      string `json:"heartbeat_url"`
		IdentityTokenURL  string `json:"identity_token_url"`
		IdentityBindURL   string `json:"identity_bind_url"`
		IdentityRotateURL string `json:"identity_rotate_url"`
	} `json:"channel_bootstrap"`
}

func firstOf(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// ParseEnrollment reads an enrollment response or file, with or without the
// {"data": ...} envelope. Identity URLs are derived from the heartbeat URL for
// enrollments that predate them.
func ParseEnrollment(raw []byte) (EnrollmentView, error) {
	var top struct {
		enrollmentFields
		Data enrollmentFields `json:"data"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return EnrollmentView{}, errors.New("enrollment response is not valid JSON")
	}
	d, t := top.Data, top.enrollmentFields
	view := EnrollmentView{
		RuntimeAgentID:   firstOf(d.RuntimeAgentID, d.WorkerID, t.RuntimeAgentID, t.WorkerID),
		AgentToken:       firstOf(d.AgentToken, t.AgentToken),
		TenantID:         firstOf(d.TenantID, d.ChannelBootstrap.TenantID, t.TenantID, t.ChannelBootstrap.TenantID),
		HeartbeatURL:     firstOf(d.HeartbeatURL, d.ChannelBootstrap.HeartbeatURL, t.HeartbeatURL, t.ChannelBootstrap.HeartbeatURL),
		Accepted:         d.Accepted,
		PrivateLANOrigin: firstOf(d.PrivateLANOrigin, t.PrivateLANOrigin),
		PrivateLANPin:    firstOf(d.PrivateLANPin, t.PrivateLANPin),
	}
	if view.Accepted == nil {
		view.Accepted = t.Accepted
	}
	view.Endpoints = Endpoints{
		TokenURL:  firstOf(d.IdentityTokenURL, d.ChannelBootstrap.IdentityTokenURL, t.IdentityTokenURL, t.ChannelBootstrap.IdentityTokenURL),
		BindURL:   firstOf(d.IdentityBindURL, d.ChannelBootstrap.IdentityBindURL, t.IdentityBindURL, t.ChannelBootstrap.IdentityBindURL),
		RotateURL: firstOf(d.IdentityRotateURL, d.ChannelBootstrap.IdentityRotateURL, t.IdentityRotateURL, t.ChannelBootstrap.IdentityRotateURL),
	}
	if view.Endpoints.TokenURL == "" {
		view.Endpoints = DeriveEndpoints(view.HeartbeatURL)
	}
	return view, nil
}

// LocateEnrollment picks the enrollment file to read: the configured path, the
// copy in the state directory written when the configured path was read-only,
// or the newer of the two when both exist.
func LocateEnrollment(configured, stateDir string) string {
	fallback := ""
	if stateDir != "" {
		fallback = filepath.Join(stateDir, EnrollmentFileName)
	}
	primaryInfo, primaryErr := os.Stat(configured)
	fallbackInfo, fallbackErr := os.Stat(fallback)
	switch {
	case configured != "" && primaryErr == nil && (fallback == "" || fallbackErr != nil):
		return configured
	case fallback != "" && fallbackErr == nil && (configured == "" || primaryErr != nil):
		return fallback
	case primaryErr == nil && fallbackErr == nil:
		if fallbackInfo.ModTime().After(primaryInfo.ModTime()) {
			return fallback
		}
		return configured
	}
	return configured
}

// EnrollOptions configures one redemption of a pairing capability.
type EnrollOptions struct {
	Manager   *Manager
	Bootstrap Bootstrap
	// EnrollmentPath is where the enrollment response is persisted (0600).
	EnrollmentPath string
	// HTTPClient performs the redemption and identity calls without the
	// identity transport; a default with no redirects is used when nil.
	HTTPClient *http.Client
	Logger     *slog.Logger
	// Backoff lists the waits between retries of transient failures.
	Backoff []time.Duration
	// ResiduePaths overrides CloudInitResiduePaths (tests).
	ResiduePaths []string
}

// EnrollResult reports a completed enrollment.
type EnrollResult struct {
	Accepted       bool
	Mode           Mode
	EnrollmentPath string
}

var defaultEnrollBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// Enroll redeems the pairing capability with a host-bound key and confirms the
// binding. The attempt id and the capability are persisted before the network
// call, the response is persisted atomically before anything else happens, and
// a repeated run resumes at the step it reached: a lost response is repeated
// with the same attempt, a persisted response goes straight to confirmation.
// After the binding is confirmed the capability is deleted locally.
// enrollmentPresent reports whether the enrollment file that carries a bound
// Guard's credential exists. A bound record without it cannot authenticate and
// takes the recovery path instead.
func (m *Manager) enrollmentLocation(configured string) (string, bool) {
	primary, primaryErr := os.Stat(configured)
	if leaf, inside := m.custodyLeaf(configured); inside {
		primary, primaryErr = m.root.Stat(leaf)
	}
	fallback, fallbackErr := m.root.Stat(EnrollmentFileName)
	if fallbackErr == nil && (configured == "" || primaryErr != nil || fallback.ModTime().After(primary.ModTime())) {
		return filepath.Join(m.Dir(), EnrollmentFileName), true
	}
	return configured, primaryErr == nil
}

func (m *Manager) custodyLeaf(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	directory, err := filepath.Abs(m.Dir())
	if err != nil || filepath.Dir(absolute) != directory {
		return "", false
	}
	return filepath.Base(absolute), true
}

// OpenEnrollment resolves the existing primary/fallback policy against admitted
// state custody. Explicit enrollment files outside that directory remain
// operator-owned; state fallback credentials never reopen the state pathname.
func (m *Manager) OpenEnrollment(configured string) (*os.File, error) {
	path, present := m.enrollmentLocation(configured)
	if !present {
		return nil, &os.PathError{Op: "open enrollment", Path: path, Err: os.ErrNotExist}
	}
	if leaf, inside := m.custodyLeaf(path); inside {
		return privatefs.OpenFile(m.root, leaf, os.O_RDONLY, 0)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	directory, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return directory.Open(filepath.Base(absolute))
}

func (m *Manager) readEnrollment(configured string) []byte {
	file, err := m.OpenEnrollment(configured)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	return data
}

func Enroll(ctx context.Context, opt EnrollOptions) (EnrollResult, error) {
	var result EnrollResult
	m := opt.Manager
	logger := opt.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	logger = logger.With("component", "guard-enroll")
	if m == nil || strings.TrimSpace(opt.Bootstrap.Token) == "" || strings.TrimSpace(opt.Bootstrap.Server) == "" {
		return result, errors.New("a control-plane URL and a pairing capability are required")
	}
	if err := m.EnsureKey(); err != nil && !errors.Is(err, ErrKeyLost) {
		return result, err
	}
	httpClient := opt.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	backoff := opt.Backoff
	if backoff == nil {
		backoff = defaultEnrollBackoff
	}

	previous, _ := m.LoadBootstrap()
	record := m.Record()
	_, enrollmentPresent := m.enrollmentLocation(opt.EnrollmentPath)
	if record.State == StateBound && enrollmentPresent {
		// A confirmed binding is never replaced or degraded by another
		// redemption: a rejected replay would otherwise mark a healthy Guard
		// recovery-needed (finding Q-10). Re-pairing a bound server goes
		// through detach, which revokes it first.
		return result, ErrAlreadyBound
	}
	attempt := record.AttemptID
	resume := record.State == StateRedeeming && attempt != "" && (previous == nil || previous.Token == opt.Bootstrap.Token)
	if !resume {
		var err error
		if attempt, err = m.BeginAttempt(); err != nil {
			return result, err
		}
	}
	if err := m.SaveBootstrap(opt.Bootstrap); err != nil {
		return result, err
	}

	var raw []byte
	if resume && record.EnrollmentAttemptID == attempt {
		raw = m.readEnrollment(opt.EnrollmentPath)
	}
	if raw == nil {
		var err error
		if raw, err = redeem(ctx, httpClient, m, opt.Bootstrap, attempt, backoff, logger); err != nil {
			if errors.Is(err, ErrRecoveryNeeded) {
				// Definitively rejected or expired capabilities cannot resume.
				// Transient transport failures retain them for the same attempt.
				removeEnrollmentBootstrap(m, opt, logger)
			}
			return result, err
		}
		if err := checkLANPin(raw, opt.Bootstrap); err != nil {
			// The capability is spent either way; retrying it on every
			// start would only repeat the refusal.
			_ = m.SetState(StateRecoveryNeeded, "private_lan_pin_mismatch")
			removeEnrollmentBootstrap(m, opt, logger)
			return result, fmt.Errorf("%w: %v", ErrRecoveryNeeded, err)
		}
		path, err := m.persistEnrollment(opt.EnrollmentPath, raw)
		if err != nil {
			return result, err
		}
		result.EnrollmentPath = path
		if err := m.MarkEnrollmentPersisted(attempt); err != nil {
			return result, err
		}
	} else {
		result.EnrollmentPath, _ = m.enrollmentLocation(opt.EnrollmentPath)
	}

	view, err := ParseEnrollment(raw)
	if err != nil || view.AgentToken == "" || view.RuntimeAgentID == "" || view.Accepted == nil {
		return result, errors.New("the control plane returned an invalid enrollment response")
	}
	result.Accepted = *view.Accepted

	client, err := NewClient(ClientConfig{
		Manager: m, Endpoints: view.Endpoints, AgentToken: view.AgentToken, TenantID: view.TenantID,
		HTTPClient: httpClient, Logger: logger,
	})
	if err != nil {
		return result, err
	}
	mode, err := confirm(ctx, client, backoff, logger)
	if err != nil {
		return result, err
	}
	result.Mode = mode
	if mode != ModeHostKey {
		// A control plane without host identity consumed the capability and
		// issued a bearer credential; the agent keeps running on it.
		_ = m.SetState(StateUnbound, "control_plane_without_host_identity")
	}
	removeEnrollmentBootstrap(m, opt, logger)
	return result, nil
}

func removeEnrollmentBootstrap(m *Manager, opt EnrollOptions, logger *slog.Logger) {
	if err := m.discardBootstrapCapability(opt.Bootstrap); err != nil {
		logger.Warn("guard_bootstrap_capability_not_removed", "error", err.Error())
	}
	paths := opt.ResiduePaths
	if paths == nil {
		paths = CloudInitResiduePaths
	}
	if redacted := RedactBootstrapResidue(opt.Bootstrap.Token, paths); redacted > 0 {
		logger.Info("guard_bootstrap_residue_redacted", "files", redacted)
	}
}

func confirm(ctx context.Context, client *Client, backoff []time.Duration, logger *slog.Logger) (Mode, error) {
	var lastErr error
	for attempt := 0; attempt <= len(backoff); attempt++ {
		mode, err := client.Establish(ctx)
		if err == nil {
			return mode, nil
		}
		if errors.Is(err, ErrRevoked) || errors.Is(err, ErrRecoveryNeeded) {
			return mode, err
		}
		lastErr = err
		if attempt == len(backoff) {
			break
		}
		logger.Warn("guard_enrollment_confirmation_retrying", "error", err.Error(), "retry_in", backoff[attempt].String())
		if sleepErr := sleep(ctx, backoff[attempt]); sleepErr != nil {
			return ModeLegacy, sleepErr
		}
	}
	return ModeLegacy, fmt.Errorf("could not confirm the enrollment with the control plane: %w", lastErr)
}

// checkLANPin refuses an enrollment whose bridge pin is not the one the user
// pasted. The response arrived over a connection pinned to that key, so a
// mismatch means the control plane would re-pin the Guard to a key the user
// never saw; it is never persisted.
func checkLANPin(raw []byte, b Bootstrap) error {
	view, err := ParseEnrollment(raw)
	if err != nil {
		return err
	}
	if view.PrivateLANPin != strings.TrimSpace(b.LANPin) {
		return errors.New("the control plane answered with a private LAN pin other than the one in the install command")
	}
	return nil
}

func redeem(ctx context.Context, client *http.Client, m *Manager, b Bootstrap, attempt string, backoff []time.Duration, logger *slog.Logger) ([]byte, error) {
	key := m.Key()
	body := map[string]any{
		"token": strings.TrimSpace(b.Token), "hostname": b.Hostname, "os": b.OS, "arch": b.Arch,
		"guard_public_key": guardidentity.PublicJWK(key), "attempt_id": attempt,
	}
	for name, value := range b.Extra {
		if _, reserved := body[name]; !reserved {
			body[name] = value
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(b.Server), "/") + "/api/v1/workers/register"
	var lastErr error
	for try := 0; try <= len(backoff); try++ {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Content-Type", "application/json")
		// Proof of possession of the host key, bound to this capability, so a
		// stolen capability cannot enrol a key its thief does not hold.
		proof, proofErr := guardidentity.SignProof(key, guardidentity.ProofInput{
			Method: http.MethodPost, URL: endpoint, AccessToken: strings.TrimSpace(b.Token),
		})
		if proofErr != nil {
			return nil, proofErr
		}
		req.Header.Set(guardidentity.ProofHeader, proof)
		response, doErr := client.Do(req)
		if doErr == nil {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			switch {
			case readErr != nil:
				doErr = readErr
			case response.StatusCode >= 200 && response.StatusCode < 300:
				return data, nil
			case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
				_ = m.SetState(StateRecoveryNeeded, "pairing_capability_rejected")
				return nil, fmt.Errorf("%w: the pairing capability was rejected (HTTP %d); create a new one in the dashboard", ErrRecoveryNeeded, response.StatusCode)
			case response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests:
				return nil, fmt.Errorf("the control plane rejected the enrollment (HTTP %d)", response.StatusCode)
			default:
				doErr = fmt.Errorf("the control plane answered HTTP %d", response.StatusCode)
			}
		}
		lastErr = doErr
		if try == len(backoff) {
			break
		}
		logger.Warn("guard_enrollment_redemption_retrying", "error", doErr.Error(), "retry_in", backoff[try].String())
		if sleepErr := sleep(ctx, backoff[try]); sleepErr != nil {
			return nil, sleepErr
		}
	}
	return nil, fmt.Errorf("could not redeem the pairing capability: %w", lastErr)
}

// persistEnrollment stores the response with owner-only permissions. When the
// configured location is read-only (a sandboxed service) the copy in the state
// directory is written instead; LocateEnrollment prefers the newer file.
func (m *Manager) persistEnrollment(configured string, raw []byte) (string, error) {
	if leaf, inside := m.custodyLeaf(configured); inside {
		if err := privatefs.Replace(m.root, leaf, raw, 0o600); err != nil {
			return "", err
		}
		return configured, nil
	}
	if configured != "" {
		_ = os.MkdirAll(filepath.Dir(configured), 0o750)
		if err := WriteFileAtomic(configured, raw, 0o600); err == nil {
			return configured, nil
		}
	}
	fallback := filepath.Join(m.Dir(), EnrollmentFileName)
	if err := privatefs.Replace(m.root, EnrollmentFileName, raw, 0o600); err != nil {
		return "", fmt.Errorf("persist the enrollment response: %w", err)
	}
	return fallback, nil
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
