package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// The supported runtime has one canonical local owner and Auth0 cloud users.
// Retired PocketBase operator credentials are never recreated or reset.
func RegisterRuntimeSystemAccountRoutes(r *httpx.Router) {
	r.POST("/api/v1/system-accounts/{role}/reset", func(e *httpx.Event) error {
		if _, err := requireAuth(e); err != nil {
			return err
		}
		return httpx.Error(e, http.StatusGone, ksapi.ErrCodeUnavailable,
			"PocketBase operator accounts are retired", map[string]any{
				"reason_code": "system_account_retired", "retryable": false,
				"user_guidance": "Use Auth0 account recovery for cloud login or the canonical local owner recovery flow.",
			})
	})
}

// RegisterSystemAccountRoutes preserves the old signature without retaining
// its retired password and wallet mutation path.
// RegisterSystemAccountRefusal answers the reset route of a deployment that
// keeps credentials in the kombify Wallet. The system accounts are the
// PocketBase operator accounts of one Techstack instance (superuser, admin,
// developer), not kombify accounts and not Wallet owners, so there is nothing
// to reset there and nowhere to store a new password.
func RegisterSystemAccountRefusal(r *httpx.Router) {
	r.POST("/api/v1/system-accounts/{role}/reset", refuseSystemAccountReset)
}

func refuseSystemAccountReset(e *httpx.Event) error {
	if _, err := requireAuth(e); err != nil {
		return err
	}
	return kombifywallet.SelfHostedOnly(
		"System accounts are not available here",
		"This deployment keeps credentials in the kombify Wallet. The system accounts are operator accounts of a Techstack instance, not kombify accounts, so nothing was reset.",
	).Write(e)
}
