package stacks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestRequireStackAuthAcceptsSignedEdgeIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "api-key:techstack"}))
	e := &httpx.Event{
		Request:  req,
		Response: httptest.NewRecorder(),
	}

	got, err := requireStackAuth(e)
	if err != nil {
		t.Fatalf("requireStackAuth returned error: %v", err)
	}
	if got != "api-key:techstack" {
		t.Fatalf("requireStackAuth returned %q, want edge identity user ID", got)
	}
}

func TestRequireStackAuthRejectsMissingIdentity(t *testing.T) {
	rec := httptest.NewRecorder()
	e := &httpx.Event{
		Request:  httptest.NewRequest("GET", "/api/v1/stacks", nil),
		Response: rec,
	}

	got, err := requireStackAuth(e)
	if err == nil {
		t.Fatal("requireStackAuth must return an error once it has written the refusal")
	}
	if got != "" {
		t.Fatalf("requireStackAuth returned %q, want empty user ID", got)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("requireStackAuth status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestJobStatsRequiresAuthentication(t *testing.T) {
	rec := httptest.NewRecorder()
	e := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stats", nil),
		Response: rec,
	}

	if err := (crudRouteHandlers{}).jobStats(e); err == nil {
		t.Fatal("jobStats returned nil after rejecting an unauthenticated request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("jobStats status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	authedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stats", nil)
	authedRequest = authedRequest.WithContext(identity.NewContext(authedRequest.Context(), &identity.Identity{UserID: "owner-1"}))
	authedRecorder := httptest.NewRecorder()
	if err := (crudRouteHandlers{}).jobStats(&httpx.Event{Request: authedRequest, Response: authedRecorder}); err != nil {
		t.Fatalf("authenticated jobStats: %v", err)
	}
	if authedRecorder.Code != http.StatusOK {
		t.Fatalf("authenticated jobStats status = %d, want %d", authedRecorder.Code, http.StatusOK)
	}
}

// Regression (kombify-Techstack-7kf0): an operator who signed in directly
// through Auth0 has no projected kombify Cloud profile yet. The automatic cloud
// owner bootstrap must accept the email the Gateway signed as verified, and
// must still fail closed when the signed identity does not assert it.
func TestAutomaticCloudOwnerBootstrapUsesEdgeVerifiedEmail(t *testing.T) {
	autoCloudRequest := func() normalizedCreateStackRequest {
		return normalizedCreateStackRequest{
			Name:       "Direct Login Owner",
			Mode:       "easy",
			UserConfig: map[string]interface{}{"name": "direct-login-owner"},
			Options: map[string]interface{}{
				"owner_bootstrap_mode": ownerBootstrapModeAuto,
				"owner_source":         ownerSourceCloud,
			},
		}
	}
	eventWith := func(id *identity.Identity) *httpx.Event {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
		req = req.WithContext(identity.NewContext(context.Background(), id))
		return &httpx.Event{Request: req, Response: httptest.NewRecorder()}
	}

	verified := ownerBootstrapContextFromRequest(eventWith(&identity.Identity{
		UserID: "auth0|direct", Email: "Direct.Owner@example.com", EmailVerified: true,
	}))
	resolved, denial := resolveCreateOwnerBootstrap(autoCloudRequest(), verified)
	if denial != nil {
		t.Fatalf("verified edge email denied: %s (%s)", denial.Message, denial.ReasonCode)
	}
	bootstrap, ok := ownerBootstrapFromRequest(resolved)
	if !ok || bootstrap.Source != ownerSourceCloud || bootstrap.Email != "Direct.Owner@example.com" || bootstrap.Username != "direct-owner" {
		t.Fatalf("unexpected resolved bootstrap: %+v", bootstrap)
	}

	unverified := ownerBootstrapContextFromRequest(eventWith(&identity.Identity{
		UserID: "auth0|direct", Email: "Direct.Owner@example.com",
	}))
	if unverified.EdgeVerifiedEmail != "" {
		t.Fatalf("unverified identity produced edge verified email %q", unverified.EdgeVerifiedEmail)
	}
	if _, denial := resolveCreateOwnerBootstrap(autoCloudRequest(), unverified); denial == nil || denial.ReasonCode != reasonCloudProfileEmailMissing {
		t.Fatalf("unverified edge email: denial = %+v, want %s", denial, reasonCloudProfileEmailMissing)
	}

	// A projected Cloud profile keeps precedence over the edge identity.
	verified.CurrentProfile = &cloudLinkIdentity{Email: "profile@example.com", EmailVerified: true, DisplayName: "Profile"}
	resolved, denial = resolveCreateOwnerBootstrap(autoCloudRequest(), verified)
	if denial != nil {
		t.Fatalf("verified profile denied: %s", denial.Message)
	}
	if bootstrap, _ := ownerBootstrapFromRequest(resolved); bootstrap.Email != "profile@example.com" {
		t.Fatalf("profile email not preferred: %+v", bootstrap)
	}
}
