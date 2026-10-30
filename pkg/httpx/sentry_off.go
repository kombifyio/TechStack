//go:build kombify_full_local

package httpx

// The Full Local build (tag kombify_full_local) has no error-reporting client:
// nothing is captured and no Sentry SDK is linked.
func captureInternalServerError(*Event, string, any) {}
