// Package httpsecurity applies the shared credential transport boundary.
package httpsecurity

import "net/http"

// WithoutRedirects preserves a caller's transport, jar and timeout without
// mutating its client. Redirect responses stay available to the owning API.
func WithoutRedirects(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &copy
}
