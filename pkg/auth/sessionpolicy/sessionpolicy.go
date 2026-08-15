// Package sessionpolicy owns TechStack browser-session policy.
package sessionpolicy

import (
	"time"

	"github.com/kombifyio/go-common/authsession"
)

// BrowserSessionLifetime is the product-level lifetime for TechStack browser
// sessions. It intentionally overrides the shared authsession default because
// deployment workflows can run longer than the shared short-lived default.
const BrowserSessionLifetime = 7 * 24 * time.Hour

// BrowserSessionConfig returns the shared authsession config with TechStack's
// product lifetime applied.
func BrowserSessionConfig(audience string, secret []byte) authsession.Config {
	return authsession.Config{
		Audience: audience,
		Secret:   secret,
		Lifetime: BrowserSessionLifetime,
	}
}
