package serviceauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
)

// Boundary: the environment decides signing and verification, and a malformed
// key fails closed instead of leaving the shared HS256 secret in charge.
func TestEnvDrivesEd25519AndMalformedKeyFailsClosed(t *testing.T) {
	private, public, err := servicecall.GenerateSigningKey("techstack", "ts-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicecall.EnvEd25519PrivateKey, private)
	t.Setenv(servicecall.EnvEd25519PublicKeys, public)
	t.Setenv(servicecall.EnvRequireEd25519, "true")
	reset()
	t.Cleanup(reset)

	base := servicecall.Config{ServiceName: "techstack", Target: "techstack", Secret: "shared", Enabled: true,
		AllowedCallers: []string{"techstack"}}
	cfg, err := Apply(base)
	if err != nil {
		t.Fatal(err)
	}
	reach := func(token string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(servicecall.HeaderServiceAuth, token)
		servicecall.RequireServiceAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)
		return rec.Code
	}

	edToken, err := servicecall.IssueToken(cfg, "techstack", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if code := reach(edToken); code != http.StatusOK {
		t.Fatalf("EdDSA token refused: %d", code)
	}
	hsToken, err := servicecall.IssueToken(base, "techstack", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if code := reach(hsToken); code == http.StatusOK {
		t.Fatal("HS256 token accepted although Ed25519 is required")
	}

	t.Setenv(servicecall.EnvEd25519PrivateKey, "ts-1=not-a-key")
	reset()
	if Check() == nil {
		t.Fatal("malformed key accepted")
	}
	if _, err := Apply(base); err == nil {
		t.Fatal("malformed key fell back to HS256")
	}
}

// Boundary: an Ed25519 public set alone lets a receiver verify without the
// shared secret, and with neither configured every gate fails closed.
func TestEd25519AloneIsEnoughAndNeitherFailsClosed(t *testing.T) {
	private, public, err := servicecall.GenerateSigningKey("techstack", "ts-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicecall.EnvEd25519PrivateKey, "")
	t.Setenv(servicecall.EnvEd25519PublicKeys, "")
	t.Setenv(servicecall.EnvRequireEd25519, "")
	reset()
	t.Cleanup(reset)
	if CanVerify("") || CanSign("") {
		t.Fatal("neither secret nor key must fail closed")
	}

	t.Setenv(servicecall.EnvEd25519PrivateKey, private)
	t.Setenv(servicecall.EnvEd25519PublicKeys, public)
	reset()
	if !CanVerify("") || !CanSign("") {
		t.Fatal("Ed25519 key material alone must be enough")
	}
	cfg, err := Apply(servicecall.Config{ServiceName: "techstack", Target: "techstack", Enabled: true,
		AllowedCallers: []string{"techstack"}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := servicecall.IssueToken(cfg, "techstack", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	servicecall.RequireServiceAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("EdDSA token without secret = %d", rec.Code)
	}
}

// Boundary: a hand-built claim set (Gateway Connect tokens carry a jti and an
// owner identity) is signed EdDSA under the process key and accepted by a
// receiver bound to that key; without a key nothing is signed.
func TestSignEdDSAIsAcceptedByTheKeyBoundReceiver(t *testing.T) {
	private, public, err := servicecall.GenerateSigningKey("techstack", "ts-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(servicecall.EnvEd25519PrivateKey, "")
	t.Setenv(servicecall.EnvEd25519PublicKeys, "")
	reset()
	t.Cleanup(reset)
	if _, ok, err := SignEdDSA([]byte(`{}`)); ok || err != nil {
		t.Fatalf("signed without a key: ok=%v err=%v", ok, err)
	}

	t.Setenv(servicecall.EnvEd25519PrivateKey, private)
	t.Setenv(servicecall.EnvEd25519PublicKeys, public)
	reset()
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{
		"iss": "kombify-techstack", "aud": "kombify-gateway", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
		"svc": "techstack", "scope": "connect:techstack-nodes:read", "jti": "0123456789abcdef0123",
	})
	token, ok, err := SignEdDSA(payload)
	if err != nil || !ok {
		t.Fatalf("sign: ok=%v err=%v", ok, err)
	}
	cfg, err := Apply(servicecall.Config{ServiceName: "gateway", Target: "gateway", Enabled: true, AllowedCallers: []string{"techstack"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	servicecall.RequireServiceAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("EdDSA hand-built token = %d", rec.Code)
	}
}
