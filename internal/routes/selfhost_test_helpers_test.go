package routes

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func stackLifecycleRouteTestEvent(method, target, body, ownerID, orgID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "test-add-server-request")
	if ownerID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID, OrgID: orgID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func seedControlPlaneStack(t *testing.T, mem *controlplane.MemoryStore, tenantID, ownerID, stackID string) {
	t.Helper()
	if _, err := mem.CreateStack(context.Background(), controlplane.CreateStackRequest{ID: stackID, TenantID: tenantID, OwnerSubjectID: ownerID, Name: stackID, Status: "pending"}); err != nil {
		t.Fatalf("seed control-plane stack: %v", err)
	}
}
