package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/security"

	pbmigration "github.com/kombifyio/techstack/internal/pocketbase_migration"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

func newCloudLinkTestApp(t *testing.T) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	if err := pbmigration.EnsureSaaSAuthCollections(app); err != nil {
		t.Fatalf("ensure auth collections: %v", err)
	}
	return app
}

func createCloudLinkTestUser(t *testing.T, app *tests.TestApp, email string) string {
	t.Helper()
	collection, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatalf("find users collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.SetEmail(email)
	record.SetPassword(security.RandomString(20))
	if err := app.Save(record); err != nil {
		t.Fatalf("save user: %v", err)
	}
	return record.Id
}

func authedCloudLinkEvent(method, path, userID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return &httpx.Event{
		Request:  req,
		Response: rec,
		Auth:     &httpx.Principal{Id: userID},
	}, rec
}

func TestConsumeCloudLinkState_SingleUse(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	userID := createCloudLinkTestUser(t, app, "operator@example.com")

	state := "test-state-token"
	if err := storeCloudLinkState(app, userID, state, "test-verifier", time.Now().UTC().Add(cloudLinkStateTTL)); err != nil {
		t.Fatalf("store state: %v", err)
	}

	gotUser, gotVerifier, err := consumeCloudLinkState(app, state)
	if err != nil {
		t.Fatalf("first consume failed: %v", err)
	}
	if gotUser != userID || gotVerifier != "test-verifier" {
		t.Fatalf("consume returned user=%q verifier=%q, want user=%q verifier=test-verifier", gotUser, gotVerifier, userID)
	}

	if _, _, replayErr := consumeCloudLinkState(app, state); replayErr == nil {
		t.Fatal("replayed state must be rejected")
	}
}

func TestConsumeCloudLinkState_Expired(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	userID := createCloudLinkTestUser(t, app, "operator@example.com")

	state := "expired-state-token"
	if err := storeCloudLinkState(app, userID, state, "test-verifier", time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("store state: %v", err)
	}

	if _, _, err := consumeCloudLinkState(app, state); err == nil {
		t.Fatal("expired state must be rejected")
	}
}

func TestUpsertCloudLinkForUser_LinksToGivenUserAndRelinks(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	userID := createCloudLinkTestUser(t, app, "operator@example.com")

	if err := upsertCloudLinkForUser(app, userID, &oidcUserInfo{
		Sub:           "auth0|subject-1",
		Email:         "linked@example.com",
		EmailVerified: true,
		Name:          "Linked Owner",
	}); err != nil {
		t.Fatalf("upsert link: %v", err)
	}

	record := findCloudLinkRecord(app, userID)
	if record == nil {
		t.Fatal("expected cloud link record")
	}
	if record.GetString("user") != userID {
		t.Fatalf("link user = %q, want %q (must attach to the initiating user, never create one)", record.GetString("user"), userID)
	}
	if !record.GetBool("email_verified") {
		t.Fatal("expected email_verified to be persisted")
	}

	// Relink with a different cloud identity updates the same record
	// ((user, provider) is unique).
	if err := upsertCloudLinkForUser(app, userID, &oidcUserInfo{
		Sub:           "auth0|subject-2",
		Email:         "relinked@example.com",
		EmailVerified: true,
		Name:          "Relinked Owner",
	}); err != nil {
		t.Fatalf("relink: %v", err)
	}
	relinked := findCloudLinkRecord(app, userID)
	if relinked == nil || relinked.Id != record.Id {
		t.Fatalf("relink must update the existing record, got %+v", relinked)
	}
	if relinked.GetString("external_id") != "auth0|subject-2" || relinked.GetString("external_email") != "relinked@example.com" {
		t.Fatalf("relink did not update identity fields: %+v", relinked)
	}
}

func TestHandleCloudLinkStart_NotConfigured(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	defer app.Cleanup()
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "")
	t.Setenv("AUTH0_CLIENT_ID", "")

	e, rec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", "operator-1")
	if handlerErr := handleCloudLinkStart(app, config.ModeSelfHosted)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), reasonCloudOIDCNotConfigured) {
		t.Fatalf("expected reason_code %q in body: %s", reasonCloudOIDCNotConfigured, rec.Body.String())
	}
}

// On SaaS the Cloud account already is the identity: cloud-link start answers
// with a structured denial instead of failing with 500, and stores no state.
func TestHandleCloudLinkStart_SaaSDeniesWithGuidance(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	userID := createCloudLinkTestUser(t, app, "operator@example.com")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "https://cloud.example.test")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "test-client")

	e, rec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", userID)
	if handlerErr := handleCloudLinkStart(app, config.ModeSaaS)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	var envelope struct {
		Error struct {
			Details struct {
				ErrorCode    string         `json:"error_code"`
				Retryable    bool           `json:"retryable"`
				UserGuidance map[string]any `json:"user_guidance"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if rec.Code != http.StatusConflict || envelope.Error.Details.ErrorCode != cloudLinkUnavailableErrorCode ||
		envelope.Error.Details.Retryable || envelope.Error.Details.UserGuidance == nil {
		t.Fatalf("want structured cloud_link_unavailable denial, got %d %s", rec.Code, rec.Body.String())
	}
	if records, _ := app.FindAllRecords(cloudLinkStatesCollection); len(records) != 0 {
		t.Fatalf("SaaS denial stored %d cloud-link states", len(records))
	}
}

func TestHandleCloudLinkStart_ReturnsAuthorizationURL(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	userID := createCloudLinkTestUser(t, app, "operator@example.com")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "https://cloud.example.test")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "test-client")

	e, rec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", userID)
	if handlerErr := handleCloudLinkStart(app, config.ModeSelfHosted)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data struct {
			AuthorizationURL string `json:"authorization_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	payload := envelope.Data
	parsed, parseErr := url.Parse(payload.AuthorizationURL)
	if parseErr != nil {
		t.Fatalf("parse authorization_url: %v", parseErr)
	}
	if !strings.HasPrefix(payload.AuthorizationURL, "https://cloud.example.test/authorize?") {
		t.Fatalf("authorization_url = %q, want issuer /authorize", payload.AuthorizationURL)
	}
	query := parsed.Query()
	if query.Get("client_id") != "test-client" ||
		query.Get("code_challenge_method") != "S256" ||
		query.Get("code_challenge") == "" ||
		query.Get("state") == "" ||
		!strings.HasSuffix(query.Get("redirect_uri"), cloudLinkCallbackPath) {
		t.Fatalf("authorization_url missing PKCE params: %q", payload.AuthorizationURL)
	}

	// The state must be persisted single-use for the returned URL's state param.
	if _, _, consumeErr := consumeCloudLinkState(app, query.Get("state")); consumeErr != nil {
		t.Fatalf("stored state not consumable: %v", consumeErr)
	}
}

func TestHandleCloudLinkCallback_UnknownStateRedirectsError(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()

	req := httptest.NewRequest(http.MethodGet, cloudLinkCallbackPath+"?state=unknown&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: cloudLinkBindingCookie, Value: "unknown"})
	rec := httptest.NewRecorder()
	e := &httpx.Event{Request: req, Response: rec}

	if handlerErr := handleCloudLinkCallback(app, config.ModeSelfHosted)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, cloudLinkCompletePath+"#") || !strings.Contains(location, "status=error") || !strings.Contains(location, "state_expired") {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

// A cloud-link callback completes only in the browser that started the flow:
// a victim who opens an attacker-started authorization URL must not link the
// victim's cloud identity to the attacker's local account (login CSRF).
func TestHandleCloudLinkCallback_LinksOnlyInTheInitiatingBrowser(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	attackerID := createCloudLinkTestUser(t, app, "attacker@example.com")

	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"victim-access","token_type":"Bearer"}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"sub":"auth0|victim","email":"victim@example.com","email_verified":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", issuer.URL)
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "test-client")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET", "")

	start, startRec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", attackerID)
	if err := handleCloudLinkStart(app, config.ModeSelfHosted)(start); err != nil || startRec.Code != http.StatusOK {
		t.Fatalf("start: err=%v status=%d body=%s", err, startRec.Code, startRec.Body.String())
	}
	var envelope struct {
		Data struct {
			AuthorizationURL string `json:"authorization_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(startRec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	authorizationURL, err := url.Parse(envelope.Data.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	state := authorizationURL.Query().Get("state")
	callbackURL := cloudLinkCallbackPath + "?" + url.Values{"state": {state}, "code": {"victim-code"}}.Encode()

	callback := func(cookies []*http.Cookie) string {
		req := httptest.NewRequest(http.MethodGet, callbackURL, nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		if err := handleCloudLinkCallback(app, config.ModeSelfHosted)(&httpx.Event{Request: req, Response: rec}); err != nil {
			t.Fatalf("callback: %v", err)
		}
		return rec.Header().Get("Location")
	}

	// The victim's browser never received the binding cookie.
	if location := callback(nil); !strings.Contains(location, "status=error") {
		t.Fatalf("callback from another browser must fail, got %q", location)
	}
	if findCloudLinkRecord(app, attackerID) != nil {
		t.Fatal("callback from another browser linked a cloud identity")
	}

	// The initiating browser still completes the link with the same state.
	if location := callback(startRec.Result().Cookies()); !strings.Contains(location, "status=ok") {
		t.Fatalf("callback from the initiating browser must link, got %q", location)
	}
	if findCloudLinkRecord(app, attackerID) == nil {
		t.Fatal("callback from the initiating browser did not link")
	}
}
