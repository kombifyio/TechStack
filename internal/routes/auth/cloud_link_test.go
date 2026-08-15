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
	if handlerErr := handleCloudLinkStart(app)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), reasonCloudOIDCNotConfigured) {
		t.Fatalf("expected reason_code %q in body: %s", reasonCloudOIDCNotConfigured, rec.Body.String())
	}
}

func TestHandleCloudLinkStart_ReturnsAuthorizationURL(t *testing.T) {
	app := newCloudLinkTestApp(t)
	defer app.Cleanup()
	userID := createCloudLinkTestUser(t, app, "operator@example.com")
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "https://cloud.example.test")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "test-client")

	e, rec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", userID)
	if handlerErr := handleCloudLinkStart(app)(e); handlerErr != nil {
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
	rec := httptest.NewRecorder()
	e := &httpx.Event{Request: req, Response: rec}

	if handlerErr := handleCloudLinkCallback(app)(e); handlerErr != nil {
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
