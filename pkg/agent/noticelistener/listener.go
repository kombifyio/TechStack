// Package noticelistener is the node-local door of the opt-in `user_notification`
// agent-channel class. A Home Assistant automation posts the text its author
// wrote to a small HTTP listener that only the node-internal docker network or
// loopback can reach; the agent forwards it to Core, which holds the
// notifications-engine credential. The node never holds one.
//
// The listener is enabled by StackKits, the node's custody holder: it writes a
// bearer token (and optionally the address to bind) into a directory next to
// the StackKit access manifest. Without that token nothing listens.
//
//	POST /v1/user-notifications
//	Authorization: Bearer <token>
//	{"automation_id":"waschmaschine","text":"Waschmaschine fertig","audience":"household","member_username":""}
//
// member_username is a string, or the one-element list Home Assistant sends for
// a notify target.
package noticelistener

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/usernotice"
)

const (
	// DirName is the directory StackKits provisions next to the access manifest.
	DirName = "kombify-notify"
	// TokenFile holds the bearer token, mode 0600, written by StackKits.
	TokenFile = "token"
	// ListenFile optionally holds the host:port to bind.
	ListenFile = "listen"
	// DefaultListen is the loopback address used when no listen file exists.
	DefaultListen = "127.0.0.1:18741"
	// Path is the single route.
	Path = "/v1/user-notifications"

	minTokenLength = 32
	maxBodyBytes   = 8 << 10
	forwardTimeout = 20 * time.Second
	defaultPoll    = 30 * time.Second
)

// Forwarder hands one notice to Core over the agent's authenticated channel.
type Forwarder interface {
	Forward(ctx context.Context, notice usernotice.Notice) (usernotice.Result, error)
}

// Config configures the listener supervisor.
type Config struct {
	// Dir is the kombify-notify directory.
	Dir       string
	Forwarder Forwarder
	Logger    *slog.Logger
	// PollInterval is how often the directory is re-read and a failed bind
	// retried; the docker gateway address may appear after the agent starts.
	PollInterval time.Duration
}

// DirFor returns the kombify-notify directory next to the first existing
// access manifest of a comma-separated list, or "" when there is none.
func DirFor(accessManifests string) string {
	for _, candidate := range strings.Split(accessManifests, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		dir := filepath.Join(filepath.Dir(candidate), DirName)
		if info, err := os.Stat(filepath.Dir(candidate)); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// Run supervises the listener until ctx ends. It starts listening when StackKits
// provisioned a token, restarts on a changed token or address, and stops when
// the token disappears.
func Run(ctx context.Context, cfg Config) {
	if cfg.Dir == "" || cfg.Forwarder == nil {
		return
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = defaultPoll
	}
	supervisor := &supervisor{cfg: cfg, log: log}
	defer supervisor.stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		supervisor.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type supervisor struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	server  *http.Server
	address string
	token   string
}

func (s *supervisor) reconcile(ctx context.Context) {
	token, address, err := readSettings(s.cfg.Dir)
	s.mu.Lock()
	running := s.server != nil
	unchanged := running && token == s.token && address == s.address
	s.mu.Unlock()
	switch {
	case errors.Is(err, os.ErrNotExist) || token == "":
		if running {
			s.log.Info("user_notification_listener_stopped", "reason", "token removed")
			s.stop()
		}
		return
	case err != nil:
		s.log.Warn("user_notification_listener_refused", "error", err.Error())
		s.stop()
		return
	case unchanged:
		return
	}
	s.stop()
	s.start(ctx, token, address)
}

func (s *supervisor) start(ctx context.Context, token, address string) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		// The docker gateway address may not exist yet; the next poll retries.
		s.log.Warn("user_notification_listener_bind_failed", "address", address, "error", err.Error())
		return
	}
	server := &http.Server{
		Handler:           handler(token, s.cfg.Forwarder, s.log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      forwardTimeout + 5*time.Second,
		IdleTimeout:       30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	s.mu.Lock()
	s.server, s.address, s.token = server, address, token
	s.mu.Unlock()
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.log.Warn("user_notification_listener_failed", "error", serveErr.Error())
		}
	}()
	s.log.Info("user_notification_listener_started", "address", address)
}

func (s *supervisor) stop() {
	s.mu.Lock()
	server := s.server
	s.server, s.address, s.token = nil, "", ""
	s.mu.Unlock()
	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
}

// readSettings returns the bearer token and the address to bind. A missing
// token file is os.ErrNotExist: the integration is off.
func readSettings(dir string) (token, address string, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, TokenFile))
	if err != nil {
		return "", "", err
	}
	token = strings.TrimSpace(string(raw))
	if len(token) < minTokenLength {
		return "", "", fmt.Errorf("the token in %s is shorter than %d characters", TokenFile, minTokenLength)
	}
	address = DefaultListen
	if listen, listenErr := os.ReadFile(filepath.Join(dir, ListenFile)); listenErr == nil {
		if value := strings.TrimSpace(string(listen)); value != "" {
			address = value
		}
	} else if !errors.Is(listenErr, os.ErrNotExist) {
		return "", "", listenErr
	}
	if err := checkInternalAddress(address); err != nil {
		return "", "", err
	}
	return token, address, nil
}

// checkInternalAddress admits only an IP literal that is loopback or private
// (a docker bridge gateway is private). It never admits a wildcard or a public
// address: the listener is a node-internal door, not a network service.
func checkInternalAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address %q is not host:port", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("listen port %q is invalid", portText)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("listen host %q must be an IP address", host)
	}
	if ip.IsUnspecified() || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return fmt.Errorf("listen address %s is not loopback or private", ip)
	}
	return nil
}

type request struct {
	AutomationID   string          `json:"automation_id"`
	Text           string          `json:"text"`
	Audience       string          `json:"audience"`
	MemberUsername memberReference `json:"member_username"`
}

// memberReference is the named member: a plain string, or the one-element list
// Home Assistant sends for a notify target. More than one name is refused, so
// a notice always reaches exactly the member it names.
type memberReference struct {
	name    string
	invalid bool
}

func (m *memberReference) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		m.name = single
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	switch len(list) {
	case 0:
	case 1:
		m.name = list[0]
	default:
		m.invalid = true
	}
	return nil
}

// Handler serves the notice route with the bearer token. It is exported for
// the node tests.
func Handler(token string, forwarder Forwarder) http.Handler {
	return handler(token, forwarder, slog.Default())
}

func handler(token string, forwarder Forwarder, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(Path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
			return
		}
		if !bearerMatches(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		if err != nil || len(body) > maxBodyBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "body_too_large"})
			return
		}
		var req request
		if unmarshalErr := json.Unmarshal(body, &req); unmarshalErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body_invalid"})
			return
		}
		if req.MemberUsername.invalid {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": usernotice.RefusalAudienceInvalid})
			return
		}
		notice := usernotice.Notice{
			AutomationID: req.AutomationID, Text: req.Text, Audience: req.Audience,
			MemberUsername: req.MemberUsername.name, Source: usernotice.SourceHomeAssistant,
		}.Normalize()
		if code := notice.Refusal(); code != "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": code})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), forwardTimeout)
		defer cancel()
		result, err := forwarder.Forward(ctx, notice)
		if err != nil {
			log.Warn("user_notification_forward_failed", "error", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "core_unreachable"})
			return
		}
		writeResult(w, result)
	})
	return mux
}

func writeResult(w http.ResponseWriter, result usernotice.Result) {
	if result.Accepted {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "recipients": result.Recipients})
		return
	}
	switch result.RefusalCode {
	case usernotice.RefusalRateLimited:
		seconds := int(result.RetryAfter.Seconds())
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": result.RefusalCode, "retry_after_seconds": seconds})
	case usernotice.RefusalNotConfigured, usernotice.RefusalNodeUnresolved:
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": result.RefusalCode})
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": result.RefusalCode})
	}
}

func bearerMatches(header, token string) bool {
	scheme, value, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(value)), []byte(token)) == 1
}

func writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
