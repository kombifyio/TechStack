package httpx

import (
	"net/http"
	"strings"
)

// Route is a single registered route. BindFunc attaches route-scoped
// middleware that runs after the inherited group/router middleware and before
// the handler. It mirrors PocketBase's router.Route[T].BindFunc, returning the
// Route for chaining.
type Route struct {
	method      string
	path        string
	handler     HandlerFunc
	middlewares []MiddlewareFunc
}

// BindFunc attaches route-scoped middleware and returns the Route for chaining.
func (r *Route) BindFunc(mw ...MiddlewareFunc) *Route {
	r.middlewares = append(r.middlewares, mw...)
	return r
}

// RouterGroup is a route sub-tree sharing a path prefix and a middleware stack.
// It is the httpx analogue of PocketBase's router.RouterGroup[T].
//
// Middleware bound on a group via BindFunc applies to every route registered on
// that group and on any nested group, executed in registration order before the
// route handler. This matches PocketBase semantics.
//
// The type is named RouterGroup (not Group) so that Router can embed it as a
// field whose name does not collide with the promoted Group(prefix) method —
// exactly the layout PocketBase uses.
type RouterGroup struct {
	prefix      string
	middlewares []MiddlewareFunc
	router      *Router
}

// Group creates a nested sub-group with the given prefix appended to the
// current prefix. The child inherits the parent's middleware.
func (g *RouterGroup) Group(prefix string) *RouterGroup {
	child := &RouterGroup{
		prefix:      joinPath(g.prefix, prefix),
		middlewares: append([]MiddlewareFunc(nil), g.middlewares...),
		router:      g.router,
	}
	return child
}

// BindFunc appends middleware to this group's stack and returns the group for
// chaining. Mirrors router.RouterGroup[T].BindFunc.
func (g *RouterGroup) BindFunc(mw ...MiddlewareFunc) *RouterGroup {
	g.middlewares = append(g.middlewares, mw...)
	return g
}

// Route registers a handler for an explicit method+path. Lower-level entry
// point used by the GET/POST/... helpers. Mirrors router.RouterGroup[T].Route.
func (g *RouterGroup) Route(method, path string, handler HandlerFunc) *Route {
	rt := &Route{
		method:      strings.ToUpper(method),
		path:        joinPath(g.prefix, path),
		handler:     handler,
		middlewares: append([]MiddlewareFunc(nil), g.middlewares...),
	}
	g.router.routes = append(g.router.routes, rt)
	return rt
}

// GET registers a GET handler.
func (g *RouterGroup) GET(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodGet, path, handler)
}

// POST registers a POST handler.
func (g *RouterGroup) POST(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodPost, path, handler)
}

// PUT registers a PUT handler.
func (g *RouterGroup) PUT(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodPut, path, handler)
}

// DELETE registers a DELETE handler.
func (g *RouterGroup) DELETE(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodDelete, path, handler)
}

// PATCH registers a PATCH handler.
func (g *RouterGroup) PATCH(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodPatch, path, handler)
}

// HEAD registers a HEAD handler.
func (g *RouterGroup) HEAD(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodHead, path, handler)
}

// OPTIONS registers an OPTIONS handler.
func (g *RouterGroup) OPTIONS(path string, handler HandlerFunc) *Route {
	return g.Route(http.MethodOptions, path, handler)
}

// Any registers a handler for all methods on the given path (no method token in
// the ServeMux pattern). Mirrors router.RouterGroup[T].Any.
func (g *RouterGroup) Any(path string, handler HandlerFunc) *Route {
	return g.Route("", path, handler)
}

// Router is the top-level HTTP router. It embeds the root *RouterGroup, so
// router.GET(...), router.BindFunc(...), router.Group(...) all work directly —
// matching the PocketBase Router[T] surface (se.Router.GET / se.Router.BindFunc
// / se.Router.Group).
//
// Build a net/http handler with BuildMux (or hand the Router straight to
// NewServer, which calls it).
type Router struct {
	*RouterGroup

	routes []*Route
	// notFound is invoked when no route matches. Defaults to a standardized
	// 404 envelope.
	notFound HandlerFunc
	// methodNotAllowed is invoked when the path matches but the method does
	// not. Defaults to a standardized 405 envelope.
	methodNotAllowed HandlerFunc
}

// NewRouter creates an empty Router with a root group.
func NewRouter() *Router {
	r := &Router{}
	r.RouterGroup = &RouterGroup{router: r}
	r.notFound = defaultNotFound
	r.methodNotAllowed = defaultMethodNotAllowed
	return r
}

// SetNotFound overrides the no-route-matched handler.
func (r *Router) SetNotFound(h HandlerFunc) { r.notFound = h }

// SetMethodNotAllowed overrides the wrong-method handler.
func (r *Router) SetMethodNotAllowed(h HandlerFunc) { r.methodNotAllowed = h }

// BuildMux assembles a *http.ServeMux from the registered routes.
//
// Each route is registered under a Go 1.22 method+path pattern
// ("GET /api/v1/agents/{id}"), so r.PathValue("id") works in handlers. The
// route's inherited group middleware plus its route-scoped middleware are
// composed into the per-request chain consumed by Event.Next().
//
// A catch-all "/" pattern is registered for paths with no matching route so the
// standardized 404/405 envelopes are emitted instead of the bare net/http text
// responses.
func (r *Router) BuildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Track which paths carry method-specific routes, and which exact
	// (method,path)/method-less patterns are registered, so we can synthesize
	// per-path 405 handlers and a global 404 catch-all without clobbering real
	// routes.
	methodScopedPaths := make(map[string]bool) // path -> has >=1 method-specific route
	anyMethodPaths := make(map[string]bool)    // path -> has a method-less (Any) route
	rootRegistered := false

	for _, rt := range r.routes {
		rt := rt

		pattern := rt.path
		if rt.method != "" {
			pattern = rt.method + " " + rt.path
			methodScopedPaths[rt.path] = true
		} else {
			anyMethodPaths[rt.path] = true
		}
		if rt.path == "/" {
			rootRegistered = true
		}

		chain := rt.middlewares
		handler := rt.handler

		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			r.dispatch(w, req, chain, handler)
		})
	}

	// For every path that only has method-specific routes, register a
	// method-less handler on that exact path so a request with an unmatched
	// method resolves here (ServeMux prefers the more specific "METHOD /path"
	// for matched methods) and yields a standardized 405 envelope instead of
	// falling through to the 404 catch-all. Verified against net/http ServeMux
	// precedence. Router-level middleware still runs.
	for path := range methodScopedPaths {
		if anyMethodPaths[path] {
			continue // an Any route already handles all methods on this path
		}
		// The method-less 405 fallback registers a bare-path pattern. Two
		// distinct method-scoped routes can have overlapping wildcard paths
		// (e.g. "/backups/{name}/upload-s3" and "/backups/s3/{name}") whose
		// method-less forms conflict in net/http ServeMux and would panic at
		// registration. The method-scoped patterns themselves never conflict
		// (different methods), so it is safe to skip a conflicting 405-fallback:
		// the path keeps its real handlers and only loses the 405-vs-404 nicety
		// for unmatched methods, falling through to the standardized 404.
		func(path string) {
			defer func() { _ = recover() }()
			mux.HandleFunc(path, func(w http.ResponseWriter, req *http.Request) {
				r.dispatch(w, req, r.middlewares, r.methodNotAllowed)
			})
		}(path)
	}

	// Global catch-all so unmatched paths get the standardized 404 envelope and
	// the router-level middleware still runs.
	if !rootRegistered {
		mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
			r.dispatch(w, req, r.middlewares, r.notFound)
		})
	}

	return mux
}

// dispatch builds the Event, composes the middleware chain in front of the leaf
// handler, and runs it. The chain is [middleware..., handler] driven by
// Event.Next(); any error returned bubbles up and is rendered as an envelope.
func (r *Router) dispatch(w http.ResponseWriter, req *http.Request, chain []MiddlewareFunc, handler HandlerFunc) {
	e := &Event{
		Response: w,
		Request:  req,
		Auth:     authFromContext(req.Context()),
		chain:    chain,
		index:    0,
		final:    handler,
	}
	if err := e.Next(); err != nil {
		renderError(e, err)
	}
}

// ServeHTTP lets a Router be used directly as an http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.BuildMux().ServeHTTP(w, req)
}

// joinPath concatenates URL path segments, collapsing duplicate slashes and
// preserving a single leading slash.
func joinPath(base, p string) string {
	switch {
	case base == "" && p == "":
		return ""
	case base == "":
		return ensureLeadingSlash(p)
	case p == "":
		return ensureLeadingSlash(base)
	}
	b := strings.TrimRight(base, "/")
	s := strings.TrimLeft(p, "/")
	if b == "" {
		return "/" + s
	}
	return ensureLeadingSlash(b) + "/" + s
}

func ensureLeadingSlash(p string) string {
	if p == "" || strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}
