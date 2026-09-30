package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/pkg/auth/sessionpolicy"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/v2"
	"github.com/kombifyio/techstack/pkg/v2/auth/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// The production route adapter and a real disposable PostgreSQL exercise the
// one-use ticket transition. CI provides the embedded PostgreSQL binaries.
func TestNativeCloudLoginHandoffBoundary(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(localdb.EnvEmbeddedPostgresDir, filepath.Join(dataDir, "postgres"))
	t.Setenv(localdb.EnvEmbeddedPostgresPort, "")
	t.Setenv(localdb.EnvEmbeddedPostgresBundleDir, "")
	pg, err := localdb.StartEmbeddedPostgres(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Stop(); err != nil {
			t.Error(err)
		}
	})
	pgConfig, err := pgx.ParseConfig(pg.DSN())
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*pgConfig)
	t.Cleanup(func() { _ = database.Close() })
	migration, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "129_native_login_handoffs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}

	cfg := sessionpolicy.BrowserSessionConfig("techstack-test", []byte("0123456789abcdef0123456789abcdef"))
	mgr, err := session.NewManager(session.Config(cfg))
	if err != nil {
		t.Fatal(err)
	}
	browser, err := sessionpolicy.NewBrowser(cfg, mgr, "techstack_session", true)
	if err != nil {
		t.Fatal(err)
	}
	identity := authsession.Claims{Subject: "auth0|user", TenantID: "org_test", Email: "user@example.test"}
	store := &stubAuthStore{membership: &controlplane.Membership{TenantID: identity.TenantID,
		UserID: identity.Subject, SubjectID: identity.Subject, ProviderKey: "cloud", Status: "active"}}
	h := &nativeLogin{db: database, browser: browser, memberships: store, origin: "https://techstack.kombify.io"}
	srv := v2.NewServer(v2.WithSession(mgr), v2.WithAuthHandlers(v2.AuthHandlers{
		NativeStart: http.HandlerFunc(h.start), NativeHandoff: http.HandlerFunc(h.handoff), NativeRedeem: http.HandlerFunc(h.redeem),
	}))
	router := httpx.NewRouter()
	v2.RegisterHTTPX(router, srv)
	public := router.BuildMux()

	verifier, err := nativeRandom()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	post := func(path string, body any) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		public.ServeHTTP(w, req)
		return w
	}
	start := post("/api/v2/auth/native/start", map[string]any{"verifier_challenge": challenge, "callback_port": 63690})
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	var started map[string]string
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	loginURL, err := url.Parse(started["login_url"])
	if err != nil {
		t.Fatal(err)
	}
	returnTo := loginURL.Query().Get("return_to")
	if !strings.HasPrefix(returnTo, "/api/v2/auth/native/handoff?") {
		t.Fatalf("unsafe return path: %s", returnTo)
	}

	initialToken, err := mgr.Issue(identity)
	if err != nil {
		t.Fatal(err)
	}
	loginCookies := httptest.NewRecorder()
	http.SetCookie(loginCookies, &http.Cookie{Name: "techstack_session", Value: initialToken,
		Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	browser.StampLogin(loginCookies.Header())
	req := httptest.NewRequest(http.MethodGet, returnTo, nil)
	for _, c := range loginCookies.Result().Cookies() {
		req.AddCookie(c)
	}
	original, err := browser.VerifyTransfer(req)
	if err != nil {
		t.Fatal(err)
	}
	handoff := httptest.NewRecorder()
	public.ServeHTTP(handoff, req)
	if handoff.Code != http.StatusSeeOther {
		t.Fatalf("handoff: %d %s", handoff.Code, handoff.Body.String())
	}
	callback, err := url.Parse(handoff.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if callback.Scheme != "http" || callback.Host != "127.0.0.1:63690" || callback.Path != nativeCallbackPath ||
		callback.Query().Get("state") != started["state"] {
		t.Fatalf("unsafe callback: %s", callback)
	}
	ticket := callback.Query().Get("ticket")
	wrong := post("/api/v2/auth/native/redeem", map[string]string{"ticket": ticket,
		"verifier": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, 32)), "state": started["state"]})
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong verifier: %d", wrong.Code)
	}
	redeemed := post("/api/v2/auth/native/redeem", map[string]string{"ticket": ticket, "verifier": verifier, "state": started["state"]})
	if redeemed.Code != http.StatusOK {
		t.Fatalf("redeem: %d %s", redeemed.Code, redeemed.Body.String())
	}
	whoReq := httptest.NewRequest(http.MethodGet, "/api/v2/whoami", nil)
	for _, c := range redeemed.Result().Cookies() {
		whoReq.AddCookie(c)
	}
	transferred, err := browser.VerifyTransfer(whoReq)
	if err != nil || transferred.Claims.Subject != identity.Subject || transferred.Claims.TenantID != identity.TenantID ||
		transferred.SessionExpiry > original.SessionExpiry || transferred.OriginExpiry > original.OriginExpiry {
		t.Fatalf("transferred session identity or cap changed: %+v, %v", transferred, err)
	}
	who := httptest.NewRecorder()
	public.ServeHTTP(who, whoReq)
	if who.Code != http.StatusOK || !strings.Contains(who.Body.String(), identity.Subject) {
		t.Fatalf("whoami: %d %s", who.Code, who.Body.String())
	}
	replay := post("/api/v2/auth/native/redeem", map[string]string{"ticket": ticket, "verifier": verifier, "state": started["state"]})
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay: %d", replay.Code)
	}
}
