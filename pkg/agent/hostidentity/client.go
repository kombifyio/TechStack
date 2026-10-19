package hostidentity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// Mode is how requests to the control plane are authenticated.
type Mode int

const (
	// ModeLegacy sends the bearer credential alone. It is used until a host key
	// is bound and against control planes that predate host identity.
	ModeLegacy Mode = iota
	// ModeHostKey sends a key-bound access token plus a proof per request.
	ModeHostKey
)

var (
	// ErrRevoked means the control plane revoked this identity.
	ErrRevoked = errors.New("kombify Guard identity was revoked by the control plane")
	// ErrRecoveryNeeded means the binding cannot complete without re-pairing.
	ErrRecoveryNeeded = errors.New("kombify Guard needs a new pairing capability to recover its identity")
	// ErrAlreadyBound means the Guard holds a confirmed binding; it is not
	// enrolled again until the server is detached.
	ErrAlreadyBound = errors.New("kombify Guard is already bound to this server; detach it before pairing it again")
	// ErrDowngradeRefused means a Guard that holds a bound host key was told
	// that host identity is unsupported. That answer is an outage or an attack
	// (a path that strips the identity endpoints), never a reason to fall back to
	// the bearer credential, so the Guard keeps waiting instead.
	ErrDowngradeRefused = errors.New("kombify Guard refuses to fall back to its bearer credential: the control plane reports no host identity for a key-bound server")
)

// APIError is a structured denial from the control plane. It never carries a
// response body beyond the bounded error envelope.
type APIError struct {
	Status    int
	Code      string
	Reason    string
	Retryable bool
	// ClockOffset is the server Date minus the local clock when it was sent.
	ClockOffset time.Duration
}

func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("control plane answered HTTP %d", e.Status)}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	return strings.Join(parts, ": ")
}

// Endpoints are the control-plane URLs of the identity protocol.
type Endpoints struct {
	TokenURL  string
	BindURL   string
	RotateURL string
}

// ClientConfig configures a Client.
type ClientConfig struct {
	Manager    *Manager
	Endpoints  Endpoints
	AgentToken string
	// TenantID is sent as the tenant hint the credential lookup needs.
	TenantID string
	// HTTPClient carries the identity calls themselves; it must not use the
	// Client's own Transport. A default with a short timeout and no redirects
	// is used when nil.
	HTTPClient *http.Client
	Logger     *slog.Logger
	// Probe is set when the control plane did not advertise identity endpoints
	// (an enrollment that predates them): a plain 401 or a missing endpoint then
	// means "host identity unsupported here", not "credential rejected".
	Probe bool
	// RotateAfter overrides DefaultRotateAfter.
	RotateAfter time.Duration
	Now         func() time.Time
}

// Client proves possession of the Guard identity key to the control plane.
type Client struct {
	cfg    ClientConfig
	log    *slog.Logger
	origin string

	exchangeMu sync.Mutex
	mu         sync.Mutex
	mode       Mode
	token      string
	expires    time.Time
	// pendingApproval is set while a key bind waits for the owner; Maintain then
	// looks again at pendingApprovalRetry instead of its slow cadence.
	pendingApproval atomic.Bool
}

// NewClient validates the configuration.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Manager == nil {
		return nil, errors.New("kombify Guard identity manager is required")
	}
	if strings.TrimSpace(cfg.AgentToken) == "" {
		return nil, errors.New("kombify Guard runtime credential is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(cfg.Endpoints.TokenURL))
	if err != nil || parsed.Host == "" {
		return nil, errors.New("kombify Guard identity token URL is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.RotateAfter <= 0 {
		cfg.RotateAfter = DefaultRotateAfter
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{cfg: cfg, log: logger.With("component", "guard-identity"), origin: strings.ToLower(parsed.Scheme + "://" + parsed.Host)}, nil
}

// DeriveEndpoints builds the identity URLs from a worker heartbeat URL for
// enrollments that predate the identity_* fields.
func DeriveEndpoints(heartbeatURL string) Endpoints {
	base := strings.TrimSuffix(strings.TrimSpace(heartbeatURL), "/heartbeat")
	if base == strings.TrimSpace(heartbeatURL) || base == "" {
		return Endpoints{}
	}
	return Endpoints{TokenURL: base + "/identity/token", BindURL: base + "/identity/bind", RotateURL: base + "/identity/rotate"}
}

// UseHostKey switches to key-bound requests without a preceding Establish. It
// is for short-lived helper processes of a Guard whose identity the main
// process already established: the access token is exchanged lazily.
func (c *Client) UseHostKey() error {
	if err := c.cfg.Manager.EnsureKey(); err != nil {
		return err
	}
	c.setMode(ModeHostKey)
	return nil
}

// Mode reports the current authentication mode.
func (c *Client) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

func (c *Client) setMode(mode Mode) {
	c.mu.Lock()
	c.mode = mode
	c.mu.Unlock()
}

// Establish brings the identity to a usable state: it exchanges the credential
// for a key-bound access token, binds the key first when the control plane does
// not know it yet (an upgraded legacy worker), and confirms an unconfirmed
// redemption. Transient failures are returned as ordinary errors for the
// caller to retry; ErrRevoked and ErrRecoveryNeeded are terminal until a new
// pairing. A control plane without host identity leaves the legacy mode.
func (c *Client) Establish(ctx context.Context) (Mode, error) {
	if err := c.cfg.Manager.EnsureKey(); err != nil {
		if errors.Is(err, ErrKeyLost) {
			return ModeLegacy, ErrRecoveryNeeded
		}
		return ModeLegacy, err
	}
	if rotated := c.resolvePendingRotation(ctx); rotated {
		c.log.Info("guard_identity_rotation_completed")
	}
	err := c.exchangeAndStore(ctx)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == guardidentity.CodeProofInvalid && apiErr.Reason == guardidentity.ReasonIdentityUnbound {
		if bindErr := c.bind(ctx); bindErr != nil {
			if c.awaitingApproval(bindErr) {
				return ModeLegacy, nil
			}
			return ModeLegacy, c.classify(bindErr)
		}
		err = c.exchangeAndStore(ctx)
	}
	if err != nil {
		if c.awaitingApproval(err) {
			return ModeLegacy, nil
		}
		return c.modeAfterFailure(err)
	}
	if recordErr := c.cfg.Manager.SetState(StateBound, ""); recordErr != nil {
		c.log.Warn("guard_identity_state_not_persisted", "error", recordErr.Error())
	}
	c.setMode(ModeHostKey)
	return ModeHostKey, nil
}

// awaitingApproval reports a key bind that the owner has not approved yet. The
// Guard is not failing: it keeps reporting on its bearer credential, which is
// observe-only, and looks again shortly (Maintain), so its key binds as soon as
// the owner approves the fingerprint logged here.
func (c *Client) awaitingApproval(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != guardidentity.CodeIdentityUpgradePending {
		return false
	}
	c.setMode(ModeLegacy)
	c.pendingApproval.Store(true)
	fingerprint := c.cfg.Manager.Record().KeyThumbprint
	if apiErr.Reason == guardidentity.ReasonOtherKeyPending {
		c.log.Warn("guard_identity_other_key_pending",
			"key_thumbprint", fingerprint,
			"guidance", "a different key awaits owner approval for this server; if that key is not this server's, reject it in the kombify dashboard and rotate the server credential")
		return true
	}
	c.log.Warn("guard_identity_awaiting_owner_approval",
		"key_thumbprint", fingerprint,
		"guidance", "approve this key fingerprint in the kombify dashboard; until then this server reports but receives no commands")
	return true
}

func (c *Client) modeAfterFailure(err error) (Mode, error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == "" && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusMethodNotAllowed ||
		apiErr.Status == http.StatusNotImplemented || (c.cfg.Probe && apiErr.Status == http.StatusUnauthorized)) {
		if c.cfg.Manager.Record().State == StateBound {
			// Downgrade protection: a key-bound Guard never returns to the bearer
			// credential because the control plane claims not to know identity.
			c.log.Error("guard_identity_downgrade_refused", "status", apiErr.Status,
				"guidance", "this server holds a bound host key; check the path to the control plane (proxy, DNS, origin) and retry. The Guard will not fall back to its bearer credential.")
			return ModeLegacy, ErrDowngradeRefused
		}
		// The control plane predates host identity: keep the bearer credential
		// and let the caller look again later.
		c.log.Warn("guard_identity_unsupported_by_control_plane")
		c.setMode(ModeLegacy)
		return ModeLegacy, nil
	}
	return ModeLegacy, c.classify(err)
}

// reasonCredentialRejected records a bare 401 on the worker credential.
const reasonCredentialRejected = "credential_rejected"

// classify maps a denial onto the terminal conditions and records the state.
func (c *Client) classify(err error) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	manager := c.cfg.Manager
	switch {
	case apiErr.Code == guardidentity.CodeIdentityRevoked:
		_ = manager.SetState(StateRevoked, guardidentity.CodeIdentityRevoked)
		return ErrRevoked
	case apiErr.Code == guardidentity.CodeIdentityAlreadyBound,
		apiErr.Code == guardidentity.CodeProofInvalid && apiErr.Reason == guardidentity.ReasonKeyMismatch:
		_ = manager.SetState(StateRecoveryNeeded, "bound_to_another_key")
		return fmt.Errorf("%w: the control plane holds a different key for this server", ErrRecoveryNeeded)
	case apiErr.Code == "" && apiErr.Status == http.StatusUnauthorized:
		_ = manager.SetState(StateRecoveryNeeded, reasonCredentialRejected)
		return fmt.Errorf("%w: the worker credential was rejected", ErrRecoveryNeeded)
	case apiErr.Reason == guardidentity.ReasonProofClock:
		c.log.Warn("guard_identity_clock_skew",
			"clock_offset", apiErr.ClockOffset.Round(time.Second).String(),
			"guidance", "synchronize the server clock (NTP); proofs are accepted within 60 seconds")
	}
	return err
}

func (c *Client) exchangeAndStore(ctx context.Context) error {
	token, expires, err := c.exchange(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.token, c.expires = token, expires
	c.mu.Unlock()
	return nil
}

func (c *Client) newRequest(ctx context.Context, endpoint string, body []byte, key ed25519.PrivateKey) (*http.Request, error) {
	proof, err := guardidentity.SignProof(key, guardidentity.ProofInput{Method: http.MethodPost, URL: endpoint, Now: c.cfg.Now()})
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.AgentToken))
	req.Header.Set(guardidentity.ProofHeader, proof)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenant := strings.TrimSpace(c.cfg.TenantID); tenant != "" {
		req.Header.Set("X-Kombify-Tenant-ID", tenant)
	}
	return req, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	response, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return payload, nil
	}
	return nil, parseAPIError(response, payload, c.cfg.Now())
}

func parseAPIError(response *http.Response, payload []byte, now time.Time) *APIError {
	apiErr := &APIError{Status: response.StatusCode}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Reason    string `json:"reason"`
				Retryable bool   `json:"retryable"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) == nil && strings.HasPrefix(envelope.Error.Code, "guard_") {
		apiErr.Code, apiErr.Reason, apiErr.Retryable = envelope.Error.Code, envelope.Error.Details.Reason, envelope.Error.Details.Retryable
	}
	if date, err := http.ParseTime(response.Header.Get("Date")); err == nil {
		apiErr.ClockOffset = date.Sub(now)
	}
	return apiErr
}

func (c *Client) exchange(ctx context.Context) (string, time.Time, error) {
	req, err := c.newRequest(ctx, c.cfg.Endpoints.TokenURL, nil, c.cfg.Manager.Key())
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := c.do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	var envelope struct {
		Data struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &envelope) != nil || envelope.Data.AccessToken == "" || envelope.Data.ExpiresIn <= 0 {
		return "", time.Time{}, errors.New("control plane returned an unusable access token response")
	}
	return envelope.Data.AccessToken, c.cfg.Now().Add(time.Duration(envelope.Data.ExpiresIn) * time.Second), nil
}

// bind registers the identity key of an upgraded legacy worker, authenticated
// by the current bearer credential.
func (c *Client) bind(ctx context.Context) error {
	rec := c.cfg.Manager.Record()
	attempt := rec.AttemptID
	if attempt == "" {
		var err error
		if attempt, err = c.cfg.Manager.BeginAttempt(); err != nil {
			return err
		}
		if err := c.cfg.Manager.SetState(StateUnbound, ""); err != nil {
			return err
		}
	}
	key := c.cfg.Manager.Key()
	body, err := json.Marshal(map[string]any{"public_key": guardidentity.PublicJWK(key), "attempt_id": attempt})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, c.cfg.Endpoints.BindURL, body, key)
	if err != nil {
		return err
	}
	if _, err := c.do(req); err != nil {
		return err
	}
	c.log.Info("guard_identity_key_bound")
	return nil
}

// accessToken returns a valid access token, exchanging when it is missing,
// close to expiry or forced. Exchanges are serialized so concurrent requests
// share one.
func (c *Client) accessToken(ctx context.Context, force bool) (string, error) {
	c.exchangeMu.Lock()
	defer c.exchangeMu.Unlock()
	c.mu.Lock()
	if !force && c.token != "" && c.cfg.Now().Before(c.expires.Add(-time.Minute)) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()
	token, expires, err := c.exchange(ctx)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == guardidentity.CodeIdentityRevoked {
			_ = c.cfg.Manager.SetState(StateRevoked, guardidentity.CodeIdentityRevoked)
		}
		return "", err
	}
	c.mu.Lock()
	c.token, c.expires = token, expires
	c.mu.Unlock()
	return token, nil
}

func (c *Client) dropToken() {
	c.mu.Lock()
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
}

// Rotate swaps the identity key. The new key is staged on disk first, the
// request is authenticated by the current key, and the new key proves
// possession with its own proof; the local switch follows the control plane's
// acceptance.
func (c *Client) Rotate(ctx context.Context) error {
	if c.Mode() != ModeHostKey {
		return errors.New("key rotation requires an established host identity")
	}
	if c.cfg.Manager.PendingRotation() != nil {
		// An earlier rotation got no definitive answer. Settle it first: the
		// control plane may already hold the staged key, and submitting it
		// again, or a fresh one, would race a rotation that already happened.
		if c.resolvePendingRotation(ctx) {
			c.log.Info("guard_identity_rotation_completed")
			c.dropToken()
			_, err := c.accessToken(ctx, true)
			return err
		}
		if c.cfg.Manager.PendingRotation() != nil {
			return errors.New("an earlier key rotation is still unresolved; it is retried on the next maintenance pass")
		}
	}
	next, err := c.cfg.Manager.StageRotation()
	if err != nil {
		return err
	}
	if err := c.submitRotation(ctx, next); err != nil {
		return err
	}
	if err := c.cfg.Manager.CommitRotation(); err != nil {
		return err
	}
	c.dropToken()
	_, err = c.accessToken(ctx, true)
	return err
}

func (c *Client) submitRotation(ctx context.Context, next ed25519.PrivateKey) error {
	newProof, err := guardidentity.SignProof(next, guardidentity.ProofInput{Method: http.MethodPost, URL: c.cfg.Endpoints.RotateURL, Now: c.cfg.Now()})
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"new_public_key": guardidentity.PublicJWK(next), "new_key_proof": newProof})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoints.RotateURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if tenant := strings.TrimSpace(c.cfg.TenantID); tenant != "" {
		request.Header.Set("X-Kombify-Tenant-ID", tenant)
	}
	response, err := c.authorizedRoundTrip(c.cfg.HTTPClient.Transport, request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		apiErr := parseAPIError(response, payload, c.cfg.Now())
		// Only a definitive refusal proves the control plane kept the current
		// key. A gateway or server failure may follow an accepted rotation, so
		// the staged key stays for resolvePendingRotation to settle.
		if definitiveRefusal(apiErr) {
			c.cfg.Manager.DiscardRotation()
		}
		return apiErr
	}
	return nil
}

// definitiveRefusal reports a client-error answer that will not change on
// retry. Server errors, 408 and 429 say nothing about what was committed.
func definitiveRefusal(apiErr *APIError) bool {
	return apiErr != nil && apiErr.Status >= http.StatusBadRequest && apiErr.Status < http.StatusInternalServerError &&
		apiErr.Status != http.StatusRequestTimeout && apiErr.Status != http.StatusTooManyRequests
}

// resolvePendingRotation finishes a rotation that was interrupted after the
// control plane accepted it but before the key file switched.
func (c *Client) resolvePendingRotation(ctx context.Context) bool {
	next := c.cfg.Manager.PendingRotation()
	if next == nil {
		return false
	}
	// Try the staged key: if the control plane already rotated, it is accepted.
	proof, err := guardidentity.SignProof(next, guardidentity.ProofInput{Method: http.MethodPost, URL: c.cfg.Endpoints.TokenURL, Now: c.cfg.Now()})
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoints.TokenURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.AgentToken))
	req.Header.Set(guardidentity.ProofHeader, proof)
	if tenant := strings.TrimSpace(c.cfg.TenantID); tenant != "" {
		req.Header.Set("X-Kombify-Tenant-ID", tenant)
	}
	if _, doErr := c.do(req); doErr != nil {
		// Refused: the rotation never completed on the control plane, so the
		// staged key is dropped and the current key stays. Any other failure
		// leaves the staged key, which may be the only one still accepted.
		var apiErr *APIError
		if errors.As(doErr, &apiErr) && definitiveRefusal(apiErr) {
			c.cfg.Manager.DiscardRotation()
		}
		return false
	}
	return c.cfg.Manager.CommitRotation() == nil
}

// MaybeRotate rotates the key when it is older than the rotation interval.
func (c *Client) MaybeRotate(ctx context.Context) error {
	if c.Mode() != ModeHostKey || c.cfg.Manager.KeyAge() < c.cfg.RotateAfter {
		return nil
	}
	c.log.Info("guard_identity_rotating_key", "key_age", c.cfg.Manager.KeyAge().Round(time.Hour).String())
	return c.Rotate(ctx)
}

// Maintain runs until ctx ends. On every tick a Guard still on the bearer
// credential (a control plane that predated host identity) tries to bind its
// key again, and a key-bound Guard rotates a key older than the rotation
// interval.
func (c *Client) Maintain(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	for {
		wait := interval
		if c.pendingApproval.Load() && c.Mode() == ModeLegacy && wait > pendingApprovalRetry {
			wait = pendingApprovalRetry
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if c.Mode() == ModeLegacy {
			c.pendingApproval.Store(false)
			if mode, err := c.Establish(ctx); err != nil {
				c.log.Warn("guard_identity_retry_failed", "error", err.Error())
			} else if mode == ModeHostKey {
				c.log.Info("guard_identity_bound_after_retry")
			}
			continue
		}
		if err := c.MaybeRotate(ctx); err != nil {
			c.log.Warn("guard_identity_rotation_failed", "error", err.Error())
		}
	}
}

// pendingApprovalRetry is how often a Guard whose key awaits the owner looks
// again; approval then reaches the server within about a minute.
const pendingApprovalRetry = time.Minute
