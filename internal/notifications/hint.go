package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	hintScope  = "notifications:hint"
	hintPath   = "/v1/notifications/hints"
	hintDomain = "homelab.inventory"

	// A burst of changes for one user collapses into the leading hint plus one
	// trailing hint at the end of this window.
	hintCoalesceWindow = 2 * time.Second
	hintTimeout        = 2 * time.Second
	hintMaxInFlight    = 8
	hintMaxUsers       = 4096
	hintMaxSubjectLen  = 256
)

// Hint tells the engine that the user's cached homelab reads may be stale. The
// engine answers 202 at once; a hint is ephemeral, best-effort and coalesced
// again on the edge, so a failure here is never a product failure.
func (e *Engine) Hint(ctx context.Context, ownerSub string) error {
	sub, ok := hintSubject(ownerSub)
	if !ok {
		return ErrRecipientRequired
	}
	body, err := json.Marshal(map[string]string{"user": sub, "domain": hintDomain})
	if err != nil {
		return err
	}
	result, err := e.do(ctx, http.MethodPost, hintPath, hintScope, body)
	if err != nil {
		return err
	}
	if result.Status < 200 || result.Status >= 300 {
		return fmt.Errorf("notifications: engine returned %d for hint", result.Status)
	}
	return nil
}

// hintSubject accepts only a human Auth0 subject. Machine subjects (Auth0
// client-credentials, "<client>@clients") own no Companion session to revalidate.
func hintSubject(ownerSub string) (string, bool) {
	sub := strings.TrimSpace(ownerSub)
	if sub == "" || len(sub) > hintMaxSubjectLen || strings.HasSuffix(sub, "@clients") {
		return "", false
	}
	return sub, true
}

// HintAnnouncer delivers change hints without ever blocking or failing its
// caller. Per user, the first change is announced at once; further changes in
// the coalesce window collapse into one trailing hint. Deliveries in flight and
// tracked users are bounded; beyond that a hint is dropped.
type HintAnnouncer struct {
	send   func(context.Context, string) error
	log    *slog.Logger
	window time.Duration
	slots  chan struct{}

	mu    sync.Mutex
	users map[string]*hintUser
}

type hintUser struct{ trailing bool }

// NewHintAnnouncer returns nil (a valid no-op announcer) while the engine is
// not configured, so unconfigured and self-hosted setups pay nothing.
func NewHintAnnouncer(engine *Engine, logger *slog.Logger) *HintAnnouncer {
	if !engine.Configured() {
		return nil
	}
	return newHintAnnouncer(engine.Hint, logger, hintCoalesceWindow)
}

func newHintAnnouncer(send func(context.Context, string) error, logger *slog.Logger, window time.Duration) *HintAnnouncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &HintAnnouncer{
		send:   send,
		log:    logger,
		window: window,
		slots:  make(chan struct{}, hintMaxInFlight),
		users:  make(map[string]*hintUser),
	}
}

// Announce records that the user's homelab inventory changed. Call it only
// after the change is committed. It is safe on a nil announcer.
func (a *HintAnnouncer) Announce(ownerSub string) {
	if a == nil {
		return
	}
	sub, ok := hintSubject(ownerSub)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if user, open := a.users[sub]; open {
		user.trailing = true
		return
	}
	if len(a.users) >= hintMaxUsers {
		a.log.Debug("notification_hint_dropped", "reason", "tracked users exhausted")
		return
	}
	a.users[sub] = &hintUser{}
	a.deliver(sub)
	time.AfterFunc(a.window, func() { a.closeWindow(sub) })
}

func (a *HintAnnouncer) closeWindow(sub string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	user := a.users[sub]
	if user == nil {
		return
	}
	if !user.trailing {
		delete(a.users, sub)
		return
	}
	user.trailing = false
	a.deliver(sub)
	time.AfterFunc(a.window, func() { a.closeWindow(sub) })
}

// deliver starts one bounded, non-blocking delivery; on overload it drops.
func (a *HintAnnouncer) deliver(sub string) {
	select {
	case a.slots <- struct{}{}:
	default:
		a.log.Debug("notification_hint_dropped", "reason", "deliveries in flight exhausted")
		return
	}
	go func() {
		defer func() { <-a.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), hintTimeout)
		defer cancel()
		if err := a.send(ctx, sub); err != nil {
			a.log.Debug("notification_hint_failed", "error", err)
			return
		}
		a.log.Debug("notification_hint_sent")
	}()
}
