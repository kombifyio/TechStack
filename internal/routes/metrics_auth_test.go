package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestRequireMetricsAccessAcceptsSignedEdgeAdmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "api-key:metrics-scraper",
		Roles:  []string{"admin"},
	}))
	e := &httpx.Event{
		Request:  req,
		Response: httptest.NewRecorder(),
	}

	if err := requireMetricsAccess(e); err != nil {
		t.Fatalf("requireMetricsAccess returned error for signed edge admin: %v", err)
	}
}

func TestRequireMetricsAccessRejectsSignedEdgeNonAdmin(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "api-key:tenant-user",
		Roles:  []string{"operator"},
	}))
	e := &httpx.Event{
		Request:  req,
		Response: rec,
	}

	// This asserted nil and so codified the bypass: the guard wrote 403, the
	// handler saw no error and served /metrics anyway. A rejection must be
	// reported as an error, not only as a recorder status.
	if err := requireMetricsAccess(e); err == nil {
		t.Fatalf("requireMetricsAccess must return an error for a non-admin, so the handler stops")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("requireMetricsAccess status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
