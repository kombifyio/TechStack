//go:build kombify_full_local

package middleware

import "github.com/kombifyio/techstack/pkg/httpx"

// SentryMiddleware is a pass-through in the Full Local build (tag
// kombify_full_local): there is no error-reporting client to install.
func SentryMiddleware(e *httpx.Event) error { return e.Next() }
