package notifications

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A burst of changes for one user reaches the engine as the leading hint plus a
// single trailing hint, signed with the hint scope; a change after a quiet
// window is announced at once again.
func TestHintAnnouncerCoalescesBurstIntoLeadingAndOneTrailingHint(t *testing.T) {
	type hintRequest struct {
		scope  any
		user   string
		domain string
	}
	received := make(chan hintRequest, 16)
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ User, Domain string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/notifications/hints" {
			t.Errorf("unexpected engine call %s %s", r.Method, r.URL.Path)
		}
		received <- hintRequest{scope: verifyToken(t, r.Header.Get("Authorization"))["scope"], user: body.User, domain: body.Domain}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer engine.Close()

	announcer := newHintAnnouncer(newTestEngine(engine.URL).Hint, nil, 80*time.Millisecond)
	for i := 0; i < 20; i++ {
		announcer.Announce("auth0|owner-1")
		announcer.Announce("owner-client@clients")
		announcer.Announce("")
	}

	next := func(what string) hintRequest {
		t.Helper()
		select {
		case hint := <-received:
			return hint
		case <-time.After(2 * time.Second):
			t.Fatalf("%s hint never reached the engine", what)
			return hintRequest{}
		}
	}
	leading := next("leading")
	if leading.scope != "notifications:hint" || leading.user != "auth0|owner-1" || leading.domain != "homelab.inventory" {
		t.Fatalf("leading hint = %+v", leading)
	}
	next("trailing")
	select {
	case extra := <-received:
		t.Fatalf("burst produced an extra hint for %q", extra.user)
	case <-time.After(400 * time.Millisecond):
	}

	announcer.Announce("auth0|owner-1")
	next("post-quiet")
}
