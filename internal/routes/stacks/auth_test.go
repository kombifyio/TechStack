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
