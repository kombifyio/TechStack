package v2

import (
	"net/http"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// RegisterHTTPX mounts the V2 Server's HTTP routes onto the httpx router.
//
// Why an adapter: the V2 surface is intentionally written against net/http so
// it can be tested and hosted standalone. The control-plane process serves
// through httpx, so we forward every V2 route through the httpx router by
// invoking the underlying http.Handler.
//
// Keep the route table in routeTable() in sync with the routes registered on
// Server.Routes.
func RegisterHTTPX(r *httpx.Router, srv *Server) {
	if r == nil || srv == nil {
		return
	}
	handler := srv.Routes()

	forward := func(e *httpx.Event) error {
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	}

	register := func(method, path string) {
		switch method {
		case http.MethodGet:
			r.GET(path, forward)
		case http.MethodPost:
			r.POST(path, forward)
		case http.MethodPut:
			r.PUT(path, forward)
		case http.MethodPatch:
			r.PATCH(path, forward)
		case http.MethodDelete:
			r.DELETE(path, forward)
		}
	}

	for _, rt := range srv.routeTable() {
		register(rt.method, rt.path)
	}
}

// v2Route describes a single V2 route exposed through the httpx router.
type v2Route struct {
	method string
	path   string
}

// routeTable returns the canonical V2 route table for the configured server.
// Optional routes (whoami, etc.) are only included when their dependencies
// are wired in via [Option] values. Keep this in sync with the routes
// registered on the http.ServeMux returned by [Server.Routes].
func (s *Server) routeTable() []v2Route {
	routes := []v2Route{
		{method: http.MethodGet, path: "/api/v2/health"},
	}
	if s.sessions != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v2/whoami"})
	}
	if s.auth.Providers != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v2/auth/providers"})
	}
	if s.auth.Login != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v2/auth/login"})
	}
	if s.auth.Callback != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v2/auth/callback"})
	}
	if s.auth.Logout != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v2/auth/logout"})
	}
	if s.auth.Methods != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v1/auth/methods"})
	}
	if s.auth.LocalLogin != nil {
		routes = append(routes, v2Route{method: http.MethodPost, path: "/api/v1/auth/login"})
	}
	if s.auth.LocalLogout != nil {
		routes = append(routes, v2Route{method: http.MethodPost, path: "/api/v1/auth/logout"})
	}
	if s.auth.BreakGlassClaim != nil {
		routes = append(routes, v2Route{method: http.MethodPost, path: "/api/v1/auth/breakglass/claim"})
	}
	if s.auth.BreakGlassReveal != nil {
		routes = append(routes, v2Route{method: http.MethodGet, path: "/api/v1/auth/breakglass/reveal"})
	}
	return routes
}
