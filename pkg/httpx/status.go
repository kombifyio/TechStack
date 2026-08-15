package httpx

import (
	"errors"

	"github.com/pocketbase/pocketbase/tools/router"
)

// StatusOf maps a returned handler/middleware error to the HTTP status the
// router renders for it: the carried *APIError or pocketbase *router.ApiError
// status, otherwise 500 (a plain error renders the INTERNAL_ERROR envelope).
func StatusOf(err error) int {
	if err == nil {
		return 0
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	var routerErr *router.ApiError
	if errors.As(err, &routerErr) {
		return routerErr.Status
	}
	return 500
}

// IsServerError reports whether a returned error maps to a 5xx (or unknown)
// status. Client 4xx errors (validation, auth, not-found) are expected control
// flow, so callers can use this to avoid reporting them to error tracking.
func IsServerError(err error) bool {
	if err == nil {
		return false
	}
	status := StatusOf(err)
	return status == 0 || status >= 500
}
