// Package artifactdelivery enforces the hosting boundary for file downloads.
package artifactdelivery

import (
	"net/http"
	"os"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RequireAllowed rejects file delivery on Render before an artifact is opened,
// an ETag or Range is evaluated, or a local fallback is selected. Render's
// provider-owned placement markers are sufficient individually; there is no
// configuration override that enables downloads on that platform.
func RequireAllowed(e *httpx.Event) error {
	for _, name := range []string{
		"RENDER", "RENDER_SERVICE_ID", "RENDER_INSTANCE_ID",
		"RENDER_EXTERNAL_HOSTNAME", "RENDER_EXTERNAL_URL",
	} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			e.Response.Header().Set("Cache-Control", "no-store")
			return httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
				"Downloads are unavailable from this host", map[string]any{
					"reason_code": "render_downloads_forbidden",
					"retryable":   false,
				})
		}
	}
	return nil
}
