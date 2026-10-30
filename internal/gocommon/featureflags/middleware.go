package featureflags

import (
	"net/http"
)

// RequireFeature returns HTTP middleware that blocks requests when a feature
// flag is disabled. The check function should return true if the feature is enabled.
// This is a generic middleware usable by any kombify Go tool.
func RequireFeature(flagName string, isEnabled func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isEnabled(flagName) {
				http.Error(w, `{"error":"feature '`+flagName+`' is disabled"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireFeatureFunc returns a HandlerFunc wrapper (for stdlib mux patterns).
func RequireFeatureFunc(flagName string, isEnabled func(string) bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isEnabled(flagName) {
			http.Error(w, `{"error":"feature '`+flagName+`' is disabled"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
