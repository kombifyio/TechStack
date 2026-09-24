package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
)

// v2AuthForceLoginPrompt forwards ?prompt=login from Techstack's login URL onto
// the upstream Auth0 authorize redirect. Silent SSO otherwise reuses a dead
// gateway session and the operator never sees Universal Login.
func v2AuthForceLoginPrompt(next http.Handler) http.Handler {
	if next == nil {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || strings.TrimSpace(r.URL.Query().Get("prompt")) != "login" {
			next.ServeHTTP(w, r)
			return
		}

		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r.Clone(r.Context()))
		location := strings.TrimSpace(recorder.Header().Get("Location"))
		if recorder.Code < http.StatusMultipleChoices ||
			recorder.Code >= http.StatusBadRequest ||
			location == "" {
			copyRecordedHTTPResponse(w, recorder)
			return
		}

		target, err := url.Parse(location)
		if err != nil || !target.IsAbs() || target.Host == "" {
			copyRecordedHTTPResponse(w, recorder)
			return
		}

		query := target.Query()
		query.Set("prompt", "login")
		query.Set("max_age", "0")
		target.RawQuery = query.Encode()

		for key, values := range recorder.Header() {
			if strings.EqualFold(key, "Location") {
				continue
			}
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		http.Redirect(w, r, target.String(), recorder.Code)
	})
}

func copyRecordedHTTPResponse(w http.ResponseWriter, recorder *httptest.ResponseRecorder) {
	for key, values := range recorder.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(recorder.Code)
	_, _ = w.Write(recorder.Body.Bytes())
}
