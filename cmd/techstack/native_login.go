package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/pkg/auth/sessionpolicy"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/v2"
)

const nativeCallbackPath = "/oauth/callback"

type nativeLogin struct {
	db          *sql.DB
	browser     *sessionpolicy.Browser
	memberships controlplane.AuthStore
	origin      string
}

func newNativeLoginHandlers(boot *v2Boot, origin string) v2.AuthHandlers {
	if boot == nil || !boot.saasMode || boot.db == nil || boot.db.DB == nil ||
		boot.browserSessions == nil || boot.authStore == nil || origin != "https://techstack.kombify.io" {
		return v2.AuthHandlers{}
	}
	h := &nativeLogin{db: boot.db.DB, browser: boot.browserSessions, memberships: boot.authStore, origin: origin}
	return v2.AuthHandlers{
		NativeStart:   http.HandlerFunc(h.start),
		NativeHandoff: http.HandlerFunc(h.handoff),
		NativeRedeem:  http.HandlerFunc(h.redeem),
	}
}

func nativeRandom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func nativeHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (h *nativeLogin) admit(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if origin := r.Header.Get("Origin"); origin != "" && origin != h.origin {
		http.Error(w, "foreign origin", http.StatusForbidden)
		return false
	}
	if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

func (h *nativeLogin) start(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	var input struct {
		VerifierChallenge string `json:"verifier_challenge"`
		CallbackPort      int    `json:"callback_port"`
	}
	if r.ContentLength > 4096 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil ||
		input.CallbackPort < 63690 || input.CallbackPort > 63694 {
		http.Error(w, "invalid native login request", http.StatusBadRequest)
		return
	}
	challengeBytes, err := base64.RawURLEncoding.DecodeString(input.VerifierChallenge)
	if err != nil || len(challengeBytes) != sha256.Size || len(input.VerifierChallenge) != 43 {
		http.Error(w, "invalid verifier challenge", http.StatusBadRequest)
		return
	}
	id, err := nativeRandom()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	state, err := nativeRandom()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	clientKey := nativeHash(middleware.RequestRateLimitKey(r))
	// The ordinary public mutation limiter applies as well; this additional
	// per-client cap bounds the pre-auth rows that can remain active at once.
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM native_login_handoffs WHERE id IN
		(SELECT id FROM native_login_handoffs WHERE expires_at < now() LIMIT 100)`); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	defer tx.Rollback()
	// Serialize admission across replicas. The global ceiling also bounds
	// anonymous row growth if upstream forwarding headers are spoofed.
	if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(708471290)`); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var globalActive int
	if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM native_login_handoffs
		WHERE expires_at>now()`).Scan(&globalActive); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if globalActive >= 10000 {
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	// This second lock makes the per-client active cap exact as well.
	if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, clientKey); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var active int
	if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM native_login_handoffs
		WHERE client_key_hash=$1 AND expires_at>now() AND consumed_at IS NULL`, clientKey).Scan(&active); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if active >= 8 {
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO native_login_handoffs
		(id,client_key_hash,verifier_hash,callback_port,callback_state,expires_at)
		VALUES ($1,$2,$3,$4,$5,now()+interval '5 minutes')`,
		id, clientKey, hex.EncodeToString(challengeBytes), input.CallbackPort, state)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	returnTo := "/api/v2/auth/native/handoff?challenge=" + url.QueryEscape(id)
	loginURL := h.origin + "/api/v2/auth/login?return_to=" + url.QueryEscape(returnTo)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"login_url": loginURL, "state": state})
}

func (h *nativeLogin) handoff(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	id := r.URL.Query().Get("challenge")
	if len(id) != 43 {
		http.Error(w, "invalid login", http.StatusBadRequest)
		return
	}
	transfer, err := h.browser.VerifyTransfer(r)
	if err != nil {
		http.Error(w, "browser login required", http.StatusUnauthorized)
		return
	}
	if !h.activeMembership(r, transfer.Claims) {
		http.Error(w, "membership required", http.StatusForbidden)
		return
	}
	ticket, err := nativeRandom()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	claimsJSON, err := json.Marshal(transfer.Claims)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var port int
	var state string
	err = h.db.QueryRowContext(r.Context(), `UPDATE native_login_handoffs SET
		ticket_hash=$2, claims_json=$3::jsonb, session_expires_at=$4, origin_expires_at=$5
		WHERE id=$1 AND expires_at>now() AND ticket_hash IS NULL AND consumed_at IS NULL
		RETURNING callback_port, callback_state`, id, nativeHash(ticket), string(claimsJSON),
		transfer.SessionExpiry, transfer.OriginExpiry).Scan(&port, &state)
	if err != nil {
		http.Error(w, "login attempt expired", http.StatusGone)
		return
	}
	callback := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: nativeCallbackPath}
	q := callback.Query()
	q.Set("ticket", ticket)
	q.Set("state", state)
	callback.RawQuery = q.Encode()
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	http.Redirect(w, r, callback.String(), http.StatusSeeOther)
}

func (h *nativeLogin) redeem(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	var input struct {
		Ticket   string `json:"ticket"`
		Verifier string `json:"verifier"`
		State    string `json:"state"`
	}
	if r.ContentLength > 4096 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil ||
		len(input.Ticket) != 43 || len(input.State) != 43 || len(input.Verifier) < 43 || len(input.Verifier) > 128 {
		http.Error(w, "invalid exchange", http.StatusBadRequest)
		return
	}
	verifierBytes, err := base64.RawURLEncoding.DecodeString(input.Verifier)
	if err != nil || len(verifierBytes) < 32 {
		http.Error(w, "invalid exchange", http.StatusBadRequest)
		return
	}
	verifierSum := sha256.Sum256([]byte(input.Verifier))
	var claimsJSON []byte
	var sessionExpiry, originExpiry int64
	err = h.db.QueryRowContext(r.Context(), `UPDATE native_login_handoffs SET consumed_at=now()
		WHERE ticket_hash=$1 AND verifier_hash=$2 AND callback_state=$3
		AND expires_at>now() AND consumed_at IS NULL AND claims_json IS NOT NULL
		RETURNING claims_json,session_expires_at,origin_expires_at`,
		nativeHash(input.Ticket), hex.EncodeToString(verifierSum[:]), input.State,
	).Scan(&claimsJSON, &sessionExpiry, &originExpiry)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "exchange denied", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var claims authsession.Claims
	if json.Unmarshal(claimsJSON, &claims) != nil || !h.activeMembership(r, claims) {
		http.Error(w, "membership required", http.StatusForbidden)
		return
	}
	transfer := sessionpolicy.Transfer{Claims: claims, SessionExpiry: sessionExpiry, OriginExpiry: originExpiry}
	if err := h.browser.IssueTransfer(w, transfer); err != nil {
		http.Error(w, "browser session expired", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"subject": claims.Subject, "tenantId": claims.TenantID})
}

func (h *nativeLogin) activeMembership(r *http.Request, claims authsession.Claims) bool {
	if claims.Subject == "" || claims.TenantID == "" || h.memberships == nil {
		return false
	}
	m, err := h.memberships.GetMembership(r.Context(), claims.TenantID, claims.Subject)
	return err == nil && m != nil && m.Status == "active" && m.TenantID == claims.TenantID &&
		m.UserID == claims.Subject && m.ProviderKey == "cloud" && m.SubjectID == claims.Subject
}
