package main

import (
	"net/http"

	"github.com/kombifyio/techstack/pkg/auth/testidentity"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
)

// testIdentityAdmission runs after the request identity and its tenant are
// resolved. A test identity acts only in a test tenant, and only a test
// identity acts in one (D-2026-10-22-02). It holds on every channel and for
// every route a signed-in person reaches; the tenant is the projected one,
// whether it came from the edge envelope or the browser session.
func testIdentityAdmission(e *httpx.Event) error {
	id := identity.FromContext(e.Request.Context())
	if id == nil || !id.IsAuthenticated() {
		return e.Next()
	}
	if err := testidentity.Admit(id.UserID, id.OrgID); err != nil {
		logger.Default().Warn("test_identity_tenant_refused",
			"test_subject", testidentity.IsTestSubject(id.UserID), "test_tenant", testidentity.IsTestTenant(id.OrgID))
		return testIdentityRefusal(err)
	}
	return e.Next()
}

func testIdentityRefusal(err error) error {
	return httpx.NewAPIError(http.StatusForbidden, "test_identity_tenant_refused", err.Error(), nil)
}
