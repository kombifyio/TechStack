package httpx

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig configures the CORS middleware. It mirrors the subset of
// PocketBase's apis.CORSConfig that the TechStack control plane uses: an
// explicit origin allowlist (with optional "*"), method/header allowlists,
// credentials, exposed headers, and a preflight max-age.
type CORSConfig struct {
	// AllowOrigins is the set of allowed origins. "*" allows any origin.
	AllowOrigins []string
	// AllowMethods is sent in Access-Control-Allow-Methods on preflight.
	AllowMethods []string
	// AllowHeaders is sent in Access-Control-Allow-Headers on preflight.
	AllowHeaders []string
	// ExposeHeaders is sent in Access-Control-Expose-Headers on simple requests.
	ExposeHeaders []string
	// AllowCredentials sets Access-Control-Allow-Credentials: true when matched.
	AllowCredentials bool
	// MaxAge sets Access-Control-Max-Age (seconds) on preflight when > 0.
	MaxAge int
}

// CORS returns an httpx middleware enforcing the given CORS policy.
//
// Behavior matches PocketBase apis.CORS for the configuration the control
// plane uses: requests without an Origin pass through (OPTIONS short-circuits
// with 204); an allowed Origin is reflected with the credentials/expose
// headers; a preflight OPTIONS with an allowed Origin returns 204 with the
// method/header/max-age preflight headers; a disallowed Origin passes the
// request through unannotated (the browser then blocks the response).
func CORS(config CORSConfig) MiddlewareFunc {
	allowMethods := strings.Join(config.AllowMethods, ",")
	allowHeaders := strings.Join(config.AllowHeaders, ",")
	exposeHeaders := strings.Join(config.ExposeHeaders, ",")
	maxAge := "0"
	if config.MaxAge > 0 {
		maxAge = strconv.Itoa(config.MaxAge)
	}

	return func(e *Event) error {
		req := e.Request
		res := e.Response
		origin := req.Header.Get("Origin")

		res.Header().Add("Vary", "Origin")

		preflight := req.Method == http.MethodOptions

		if origin == "" {
			if !preflight {
				return e.Next()
			}
			return e.NoContent(http.StatusNoContent)
		}

		allowOrigin := ""
		for _, o := range config.AllowOrigins {
			if o == "*" || o == origin {
				allowOrigin = o
				break
			}
		}

		if allowOrigin == "" {
			if !preflight {
				return e.Next()
			}
			return e.NoContent(http.StatusNoContent)
		}

		res.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		if config.AllowCredentials {
			res.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		if !preflight {
			if exposeHeaders != "" {
				res.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
			}
			return e.Next()
		}

		// Preflight request.
		res.Header().Add("Vary", "Access-Control-Request-Method")
		res.Header().Add("Vary", "Access-Control-Request-Headers")
		res.Header().Set("Access-Control-Allow-Methods", allowMethods)
		if allowHeaders != "" {
			res.Header().Set("Access-Control-Allow-Headers", allowHeaders)
		} else if h := req.Header.Get("Access-Control-Request-Headers"); h != "" {
			res.Header().Set("Access-Control-Allow-Headers", h)
		}
		if config.MaxAge != 0 {
			res.Header().Set("Access-Control-Max-Age", maxAge)
		}
		return e.NoContent(http.StatusNoContent)
	}
}
