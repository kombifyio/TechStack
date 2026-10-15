package routes

import (
	"context"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The deprecated registration must never revive a second password authority,
// including for a signed caller. No database or wallet fixture is involved.
func TestRetiredSystemAccountsCannotResetCredentials(t *testing.T) {
	router := httpx.NewRouter()
	RegisterSystemAccountRoutes(router, nil, nil)
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/system-accounts/admin/reset", nil)
		want := http.StatusUnauthorized
		if authenticated {
			request = request.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "api-key:techstack-admin"}))
			want = http.StatusGone
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("authenticated=%t: status=%d, want=%d", authenticated, response.Code, want)
		}
	}
}
