package datasubject

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	contract "github.com/kombifyio/techstack/internal/selfhostcontracts/datasubject"
)

const testSecret = "s3cret-fixture-for-datasubject-tests"

func post(t *testing.T, h http.Handler, caller string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	binding, err := servicecall.NewRequestBinding("POST", Path, body)
	if err != nil {
		t.Fatal(err)
	}
	token, err := servicecall.IssueBoundToken(servicecall.Config{ServiceName: caller, Secret: testSecret, Scope: Scope}, ServiceName, binding, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", Path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func request(t *testing.T, kind contract.Kind, subject string) []byte {
	t.Helper()
	now := time.Now().UTC()
	b, err := json.Marshal(contract.Request{
		RequestID: "6f1c1f4e-2b8a-4a55-9f0e-3b7d2f9a1c10", Kind: kind, SubjectRef: subject,
		RequestedAt: now, Deadline: now.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func handlerFor(t *testing.T, exec Executor) (http.Handler, ed25519.PublicKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Config{Enabled: true, Secret: testSecret, SigningKey: key, Executor: exec}), pub
}

// Only Gateway may deliver a request: another service holding the same secret
// is refused before the executor runs.
func TestNonGatewayCallerIsRefused(t *testing.T) {
	h, _ := handlerFor(t, &StoreExecutor{})
	rec := post(t, h, "cloud", request(t, contract.KindErase, SubjectRefFor("auth0|someone")))
	if rec.Code == http.StatusOK {
		t.Fatalf("non-gateway caller was served: %s", rec.Body)
	}
}

// A raw e-mail address is not a subject: refused before any data is touched.
func TestEmailSubjectRefIsRefused(t *testing.T) {
	h, _ := handlerFor(t, &StoreExecutor{})
	if rec := post(t, h, "gateway", request(t, contract.KindErase, "person@example.com")); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
}

// Unconfigured, the route stays closed.
func TestUnconfiguredRouteIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(Config{Enabled: true, Secret: testSecret}).ServeHTTP(rec, httptest.NewRequest("POST", Path, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
}
