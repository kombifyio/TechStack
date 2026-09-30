package servicecall

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
)

// RequireBoundServiceAuth admits only a machine service token whose signed
// scope and request binding match the exact HTTP message received. The body is
// read once under MaxBodyBytes and restored unchanged for the handler.
func RequireBoundServiceAuth(cfg BoundRequestConfig) func(http.Handler) http.Handler {
	expectedAudience := "kombify-" + cfg.ServiceName
	allowedCallers := buildAllowSet(cfg.AllowedCallers)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !boundRouteConfigured(cfg, allowedCallers) {
				respondAuthError(w, http.StatusServiceUnavailable, "service_auth_unavailable", "bound_route_not_configured")
				return
			}

			token := strings.TrimSpace(r.Header.Get(HeaderServiceAuth))
			if token == "" {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_required", "missing_service_auth_header")
				return
			}
			claims, err := VerifyToken(token, cfg.Secret, cfg.SecretNext)
			if err != nil {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", err.Error())
				return
			}
			if claims.Aud != expectedAudience {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", "wrong_audience")
				return
			}
			if _, ok := allowedCallers[strings.ToLower(claims.Svc)]; !ok {
				respondAuthError(w, http.StatusForbidden, "service_auth_forbidden", "caller_not_allowed")
				return
			}
			if claims.Scope != cfg.RequiredScope {
				respondAuthError(w, http.StatusForbidden, "service_auth_forbidden", "required_scope_missing")
				return
			}
			binding := claims.RequestBinding
			if binding == nil || claims.OnBehalfOf != nil || !validRequestBinding(*binding) ||
				binding.Method != r.Method || binding.RequestURI != exactRequestURI(r) {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", "request_binding_mismatch")
				return
			}

			body, err := readBoundedBody(r.Body, cfg.MaxBodyBytes)
			if err != nil {
				if err == errBoundBodyTooLarge {
					respondAuthError(w, http.StatusRequestEntityTooLarge, "service_request_too_large", "body_limit_exceeded")
				} else {
					respondAuthError(w, http.StatusBadRequest, "service_request_invalid", "body_read_failed")
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			digest := fmt.Sprintf("%x", sha256.Sum256(body))
			if !hmac.Equal([]byte(binding.BodySHA256), []byte(digest)) {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", "request_binding_mismatch")
				return
			}

			caller := &Caller{
				Service:   claims.Svc,
				Scope:     claims.Scope,
				RequestID: claims.RequestID,
				IssuedAt:  time.Unix(claims.Iat, 0),
				ExpiresAt: time.Unix(claims.Exp, 0),
			}
			// A strict machine route must not inherit user authority from an
			// outer middleware chain either. Attribution stays in Caller.
			ctx := identity.NewContext(r.Context(), nil)
			next.ServeHTTP(w, r.WithContext(NewContext(ctx, caller)))
		})
	}
}

var errBoundBodyTooLarge = fmt.Errorf("servicecall: request body too large")

func boundRouteConfigured(cfg BoundRequestConfig, allowedCallers map[string]struct{}) bool {
	return cfg.Enabled && strings.TrimSpace(cfg.ServiceName) != "" &&
		(strings.TrimSpace(cfg.Secret) != "" || strings.TrimSpace(cfg.SecretNext) != "") &&
		len(allowedCallers) > 0 && validScopeToken(cfg.RequiredScope) && cfg.MaxBodyBytes > 0
}

func exactRequestURI(r *http.Request) string {
	if r.RequestURI != "" {
		return r.RequestURI
	}
	return r.URL.RequestURI()
}

func readBoundedBody(body io.ReadCloser, limit int64) ([]byte, error) {
	if body == nil {
		return []byte{}, nil
	}
	defer body.Close()
	readLimit := limit
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	limited := io.LimitReader(body, readLimit)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errBoundBodyTooLarge
	}
	return raw, nil
}

func validRequestBinding(binding RequestBinding) bool {
	if binding.Method == "" || binding.Method != strings.ToUpper(binding.Method) || !validHTTPToken(binding.Method) {
		return false
	}
	if !strings.HasPrefix(binding.RequestURI, "/") || strings.Contains(binding.RequestURI, "#") {
		return false
	}
	parsed, err := url.ParseRequestURI(binding.RequestURI)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return false
	}
	if len(binding.BodySHA256) != sha256.Size*2 {
		return false
	}
	for _, c := range binding.BodySHA256 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validHTTPToken(value string) bool {
	const punctuation = "!#$%&'*+-.^_`|~"
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && !strings.ContainsRune(punctuation, c) {
			return false
		}
	}
	return value != ""
}
