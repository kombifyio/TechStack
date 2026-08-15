package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSecret = "test-service-secret"

func newTestEngine(baseURL string) *Engine {
	return &Engine{baseURL: strings.TrimRight(baseURL, "/"), secret: testSecret, http: http.DefaultClient}
}

// verifyToken parses a "Bearer <jwt>" header, checks the HS256 signature, and
// returns the decoded claims — proving the engine would accept it.
func verifyToken(t *testing.T, authHeader string) map[string]any {
	t.Helper()
	tok := strings.TrimPrefix(authHeader, "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a 3-part JWT: %q", tok)
	}
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		t.Fatal("HS256 signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

func TestSignToken(t *testing.T) {
	tok, err := newTestEngine("http://x").signToken("notifications:read")
	if err != nil {
		t.Fatal(err)
	}
	claims := verifyToken(t, "Bearer "+tok)
	if claims["iss"] != issuer || claims["aud"] != audience {
		t.Fatalf("iss/aud wrong: %v / %v", claims["iss"], claims["aud"])
	}
	if claims["scope"] != "notifications:read" {
		t.Fatalf("scope wrong: %v", claims["scope"])
	}
	if _, ok := claims["exp"]; !ok {
		t.Fatal("exp missing (engine requires it)")
	}
}

func TestSignToken_NotConfigured(t *testing.T) {
	e := &Engine{secret: ""}
	if e.Configured() {
		t.Fatal("empty secret should not be configured")
	}
	if _, err := e.Feed(context.Background(), "auth0|u", 20); err != ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}

func TestFeed_SignsAndForwardsRecipient(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"feed":[{"id":"f1","subject":"hi"}]}`))
	}))
	defer srv.Close()

	res, err := newTestEngine(srv.URL).Feed(context.Background(), "auth0|user-1", 30)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || !strings.Contains(string(res.Body), `"f1"`) {
		t.Fatalf("unexpected result: %d %s", res.Status, res.Body)
	}
	claims := verifyToken(t, gotAuth)
	if claims["scope"] != "notifications:read" {
		t.Fatalf("feed must use read scope, got %v", claims["scope"])
	}
	if gotPath != "/v1/notifications/feed" {
		t.Fatalf("path wrong: %s", gotPath)
	}
	if !strings.Contains(gotQuery, "auth0_user_id=auth0%7Cuser-1") || !strings.Contains(gotQuery, "limit=30") {
		t.Fatalf("query wrong: %s", gotQuery)
	}
}

func TestMutations_UseWriteScopeAndPaths(t *testing.T) {
	cases := []struct {
		name       string
		call       func(e *Engine) (*Result, error)
		wantMethod string
		wantPath   string
	}{
		{"read", func(e *Engine) (*Result, error) { return e.MarkRead(context.Background(), "auth0|u", "item-9") }, http.MethodPost, "/v1/notifications/feed/item-9/read"},
		{"dismiss", func(e *Engine) (*Result, error) { return e.Dismiss(context.Background(), "auth0|u", "item-9") }, http.MethodPost, "/v1/notifications/feed/item-9/dismiss"},
		{"read-all", func(e *Engine) (*Result, error) { return e.MarkAllRead(context.Background(), "auth0|u") }, http.MethodPost, "/v1/notifications/feed/read-all"},
		{"put-prefs", func(e *Engine) (*Result, error) {
			return e.PutPreferences(context.Background(), "auth0|u", []byte(`{"topics":[]}`))
		}, http.MethodPut, "/v1/notifications/preferences"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotScope string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				parts := strings.Split(tok, ".")
				payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
				var c map[string]any
				_ = json.Unmarshal(payload, &c)
				gotScope, _ = c["scope"].(string)
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer srv.Close()

			if _, err := tc.call(newTestEngine(srv.URL)); err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.wantMethod || gotPath != tc.wantPath {
				t.Fatalf("%s %s, want %s %s", gotMethod, gotPath, tc.wantMethod, tc.wantPath)
			}
			if gotScope != "notifications:write" {
				t.Fatalf("mutation must use write scope, got %q", gotScope)
			}
		})
	}
}
