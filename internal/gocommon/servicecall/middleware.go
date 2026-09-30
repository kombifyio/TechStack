package servicecall

import (
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
)

// Middleware validates X-Kombify-Service-Auth, stores the verified Caller
// in context and promotes OnBehalfOf to identity.Identity so downstream
// handlers use identity.FromContext() uniformly.
//
// Behavior:
//   - cfg.Enabled == false          → pass-through (for self-hosted builds)
//   - no X-Kombify-Service-Auth      → pass-through (the route may still be
//     reachable via the edge; edgeauth.Middleware handles that path)
//   - token present but invalid/exp  → 401
//   - audience mismatch              → 401
//   - caller not in AllowedCallers   → 403
//   - valid                          → Caller in ctx, identity in ctx
//
// Use RequireServiceAuth for routes that must ONLY be callable service-to-service.
func Middleware(cfg Config) func(next http.Handler) http.Handler {
	expectedAud := "kombify-" + cfg.ServiceName
	allowSet := buildAllowSet(cfg.AllowedCallers)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			token := strings.TrimSpace(r.Header.Get(HeaderServiceAuth))
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			claims, err := VerifyToken(token, cfg.Secret, cfg.SecretNext)
			if err != nil {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", err.Error())
				return
			}
			// A bound token is not a generic service credential. Only the
			// strict receiver verifies its method, target and body restriction.
			if claims.RequestBinding != nil {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", "bound_token_requires_bound_route")
				return
			}
			if claims.Aud != expectedAud {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_invalid", "wrong_audience")
				return
			}
			if len(allowSet) == 0 {
				respondAuthError(w, http.StatusForbidden, "service_auth_forbidden", "caller_allowlist_empty")
				return
			}
			if _, ok := allowSet[strings.ToLower(claims.Svc)]; !ok {
				respondAuthError(w, http.StatusForbidden, "service_auth_forbidden", "caller_not_allowed")
				return
			}

			caller := &Caller{
				Service:    claims.Svc,
				Scope:      claims.Scope,
				OnBehalfOf: claims.OnBehalfOf,
				RequestID:  claims.RequestID,
				IssuedAt:   time.Unix(claims.Iat, 0),
				ExpiresAt:  time.Unix(claims.Exp, 0),
			}
			ctx := NewContext(r.Context(), caller)

			if obo := claims.OnBehalfOf; obo != nil && obo.Sub != "" {
				ctx = identity.NewContext(ctx, &identity.Identity{
					UserID: obo.Sub,
					OrgID:  obo.OrgID,
					Email:  obo.Email,
					Roles:  obo.Roles,
				})
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireServiceAuth is a stricter variant of Middleware that returns 401
// when the service-auth header is missing. Use on routes that must not be
// reachable via the public edge.
func RequireServiceAuth(cfg Config) func(next http.Handler) http.Handler {
	inner := Middleware(cfg)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				inner(next).ServeHTTP(w, r)
				return
			}
			if strings.TrimSpace(r.Header.Get(HeaderServiceAuth)) == "" {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_required", "missing_service_auth_header")
				return
			}
			inner(next).ServeHTTP(w, r)
		})
	}
}

// RequireScope requires a verified service caller with the exact scope.
// Compose it inside RequireServiceAuth on routes that need scoped authority.
// Scopes are case-sensitive ASCII tokens separated by a single space.
func RequireScope(required string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := FromContext(r.Context())
			if caller == nil {
				respondAuthError(w, http.StatusUnauthorized, "service_auth_required", "missing_service_auth_context")
				return
			}
			if !hasScope(caller.Scope, required) {
				respondAuthError(w, http.StatusForbidden, "service_auth_forbidden", "required_scope_missing")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func hasScope(claim, required string) bool {
	if !validScopeToken(required) || claim == "" {
		return false
	}

	found := false
	for _, scope := range strings.Split(claim, " ") {
		if !validScopeToken(scope) {
			return false
		}
		if scope == required {
			found = true
		}
	}
	return found
}

func validScopeToken(scope string) bool {
	if scope == "" {
		return false
	}
	for i := 0; i < len(scope); i++ {
		c := scope[i]
		// OAuth scope-token grammar (RFC 6749 section 3.3). Punctuation is
		// literal: an asterisk never matches another named capability.
		if c < 0x21 || c == 0x22 || c == 0x5c || c > 0x7e {
			return false
		}
	}
	return true
}

func buildAllowSet(list []string) map[string]struct{} {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(list))
	for _, c := range list {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" {
			out[c] = struct{}{}
		}
	}
	return out
}

func respondAuthError(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `","reason":"` + reason + `"}`))
}
