package routes

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// sessionReauthSkew tolerates a session whose sign-in lies slightly in the
// future because of clock drift between the identity provider and this host.
const sessionReauthSkew = 30 * time.Second

// Machine-readable reasons of a refusal that a fresh sign-in resolves. The
// browser starts a step-up only for these (or for a bare 403 of an older
// server); a structured denial such as the demo restriction names its own
// error_code, which no new sign-in can change.
const (
	serverAccessStepUpReason = "server_access_step_up_required"
	walletRevealStepUpReason = "wallet_reveal_step_up_required"
)

var (
	errSessionReauthAbsent        = errors.New("session carries no re-authentication for this purpose")
	errSessionReauthOtherSubject  = errors.New("session re-authentication belongs to another subject")
	errSessionReauthOtherResource = errors.New("session re-authentication is bound to another resource")
	errSessionReauthStale         = errors.New("session re-authentication is outside the fresh window")
)

// verifySessionReauth accepts the session's own re-authentication: the login
// flow binds a purpose and a resource into the session only after a completed
// interactive sign-in that the identity provider reports as fresh
// (kombify-go-common authflow, prompt=login and max_age=0 with a checked
// auth_time). Session renewal keeps the original authentication time, so a
// renewed session never becomes fresher than its sign-in.
func verifySessionReauth(ctx context.Context, now time.Time, ownerID, purpose, resourceID string) error {
	claims, err := authsession.ClaimsFrom(ctx)
	if err != nil || claims.ReauthPurpose != purpose {
		return errSessionReauthAbsent
	}
	if claims.Subject != ownerID {
		return errSessionReauthOtherSubject
	}
	if claims.ReauthResource != resourceID {
		return errSessionReauthOtherResource
	}
	age := now.UTC().Sub(time.Unix(claims.AuthenticatedAt, 0).UTC())
	if claims.AuthenticatedAt <= 0 || age > walletReauthWindow || age < -sessionReauthSkew {
		return errSessionReauthStale
	}
	return nil
}

// stepUpRequired answers a refusal that a fresh sign-in resolves, with the
// machine-readable reason the browser keys its step-up on.
func stepUpRequired(e *httpx.Event, message, reason string) error {
	return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, message, map[string]any{
		"reason": reason, "retryable": true,
	})
}
