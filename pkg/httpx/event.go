// Package httpx provides a PocketBase-free HTTP request/response foundation
// for the kombify-TechStack control plane.
//
// It is a deliberate drop-in replacement for PocketBase's router primitives
// (github.com/pocketbase/pocketbase/core.RequestEvent and the underlying
// tools/router.Event + tools/router.Router). The goal is that existing leaf
// route handler bodies barely change when migrated off PocketBase:
//
//	// before (PocketBase)
//	func (h handler) get(e *core.RequestEvent) error {
//	    id := e.Request.PathValue("id")
//	    return api.PBSuccess(e, 200, data)
//	}
//
//	// after (httpx)
//	func (h handler) get(e *httpx.Event) error {
//	    id := e.Request.PathValue("id")
//	    return httpx.Success(e, 200, data)
//	}
//
// The Event method set mirrors the subset of core.RequestEvent that the
// TechStack routes actually use. See pkg/httpx/doc.go-style notes inline.
//
// Routing is backed by the standard library net/http.ServeMux with Go 1.22+
// method+path pattern matching (e.g. "GET /api/v1/agents/{id}"), so
// e.Request.PathValue("id") works exactly as it does under PocketBase.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/pkg/identity"
)

// headerContentType is the canonical Content-Type header key.
const headerContentType = "Content-Type"

// DefaultMaxMemory mirrors PocketBase's multipart parse budget (32mb).
const DefaultMaxMemory = 32 << 20

// ErrUnsupportedContentType is returned by BindBody when the request
// Content-Type is not one of the supported binders. Mirrors the PocketBase
// router error so callers that branch on it keep working.
var ErrUnsupportedContentType = errors.New("unsupported Content-Type")

// ErrInvalidRedirectStatusCode is returned by Redirect for out-of-range codes.
var ErrInvalidRedirectStatusCode = errors.New("invalid redirect status code")

// HandlerFunc is the leaf route handler signature. It mirrors the PocketBase
// route action signature func(e *core.RequestEvent) error, so handler bodies
// migrate with only the parameter type changed.
type HandlerFunc func(e *Event) error

// MiddlewareFunc is the middleware signature. It mirrors PocketBase's
// router middleware (func(e *core.RequestEvent) error): a middleware inspects
// the Event, optionally short-circuits by returning before calling e.Next(),
// or calls e.Next() to invoke the rest of the chain.
type MiddlewareFunc func(e *Event) error

// Event is the per-request handler event. It is the httpx analogue of
// PocketBase's *core.RequestEvent.
//
// The Response and Request fields are always set. Everything the TechStack
// leaf handlers touch is reachable from those two fields plus the helper
// methods below.
type Event struct {
	// Response is the underlying http.ResponseWriter.
	//
	// Used directly for header mutation (e.Response.Header().Set(...)),
	// passed to http.Error(e.Response, ...) and to srv.ServeHTTP(e.Response,
	// e.Request) in the v2 / vm_leases reverse-proxy forwarders.
	Response http.ResponseWriter

	// Request is the underlying *http.Request — by far the most used member.
	//
	// All of the following keep working unchanged because Request is the
	// stdlib type:
	//   e.Request.Context()
	//   e.Request.PathValue("id")   // Go 1.22 ServeMux path params
	//   e.Request.URL.Query()
	//   e.Request.Header.Get(...)
	//   e.Request.Body
	//   e.Request.Method
	//   e.Request.RemoteAddr
	//   e.Request.Cookie(...)
	Request *http.Request

	// Auth is the authenticated principal for the request, or nil when
	// unauthenticated. It is the drop-in replacement for core.RequestEvent.Auth
	// (*core.Record) and exposes the same method surface the routes use:
	// .Id, .IsSuperuser(), .GetString(key). It is populated from the
	// Postgres-era identity (identity.FromContext) by the edge-identity and
	// v2-session middleware.
	Auth *Principal

	// data is the per-request key/value store backing Set/Get, mirroring
	// router.Event's store. Lazily initialized.
	data map[string]any

	// chain / index drive Next(): the remaining middleware to run followed by
	// the terminal route handler. See Router.ServeHTTP.
	chain []MiddlewareFunc
	index int
	final HandlerFunc

	// responseWritten records that a status line has already gone out, so
	// renderError can honour the contract its own doc comment states: convert a
	// returned error into an envelope only "when nothing has been written yet".
	// Without it, making Error return a non-nil sentinel would append a second
	// envelope to each of the ~435 handlers that legitimately end in
	// `return httpx.Error(...)`.
	responseWritten bool
}

// ResponseWritten reports whether a status line has already been sent.
func (e *Event) ResponseWritten() bool { return e != nil && e.responseWritten }

// markResponseWritten is called by every helper that writes a status line.
func (e *Event) markResponseWritten() {
	if e != nil {
		e.responseWritten = true
	}
}

// Next invokes the next handler in the middleware chain.
//
// It mirrors core.RequestEvent.Next(): middleware calls e.Next() to continue;
// not calling it short-circuits the request. In a leaf route handler there is
// nothing after it, so Next() returns nil.
func (e *Event) Next() error {
	if e.index < len(e.chain) {
		mw := e.chain[e.index]
		e.index++
		return mw(e)
	}
	if e.final != nil {
		f := e.final
		e.final = nil
		return f(e)
	}
	return nil
}

// Store
// -------------------------------------------------------------------

// Get retrieves a single value from the per-request data store.
// Mirrors core.RequestEvent.Get. Returns nil if absent.
func (e *Event) Get(key string) any {
	if e.data == nil {
		return nil
	}
	return e.data[key]
}

// Set saves a single value into the per-request data store.
// Mirrors core.RequestEvent.Set.
func (e *Event) Set(key string, value any) {
	if e.data == nil {
		e.data = make(map[string]any, 4)
	}
	e.data[key] = value
}

// Response writers
// -------------------------------------------------------------------

func (e *Event) setResponseHeaderIfEmpty(key, value string) {
	header := e.Response.Header()
	if header.Get(key) == "" {
		header.Set(key, value)
	}
}

// JSON writes a JSON response with the given status code.
//
// This is the single concrete envelope sink: every TechStack route emits its
// response body through the PB* / httpx response helpers, which ultimately call
// e.JSON. Mirrors core.RequestEvent.JSON semantics (Content-Type defaulting +
// status header + json encode). The PocketBase "?fields=" picker is
// intentionally NOT reproduced; no TechStack route relies on it.
func (e *Event) JSON(status int, data any) error {
	e.setResponseHeaderIfEmpty(headerContentType, "application/json")
	e.Response.WriteHeader(status)
	e.markResponseWritten()
	return json.NewEncoder(e.Response).Encode(data)
}

// String writes a plain-text response. Mirrors core.RequestEvent.String.
// Used by workers.go.
func (e *Event) String(status int, data string) error {
	e.setResponseHeaderIfEmpty(headerContentType, "text/plain; charset=utf-8")
	e.Response.WriteHeader(status)
	e.markResponseWritten()
	_, err := e.Response.Write([]byte(data))
	return err
}

// HTML writes an HTML response. Mirrors core.RequestEvent.HTML.
func (e *Event) HTML(status int, data string) error {
	e.setResponseHeaderIfEmpty(headerContentType, "text/html; charset=utf-8")
	e.Response.WriteHeader(status)
	e.markResponseWritten()
	_, err := e.Response.Write([]byte(data))
	return err
}

// Blob writes a raw byte-slice response with an explicit content type.
// Mirrors core.RequestEvent.Blob. Used by stacks/import_export.go for exports.
func (e *Event) Blob(status int, contentType string, b []byte) error {
	e.setResponseHeaderIfEmpty(headerContentType, contentType)
	e.Response.WriteHeader(status)
	e.markResponseWritten()
	_, err := e.Response.Write(b)
	return err
}

// MarkResponseStreamed records that the handler writes the status line and
// body itself instead of going through JSON/String/Blob — the case for a large
// artifact that must never be buffered in memory (see
// internal/routes/workers_binary.go). Without it renderError still believes
// nothing was written and appends an error envelope to an already-committed
// response, which the runtime reports as "superfluous response.WriteHeader".
func (e *Event) MarkResponseStreamed() { e.markResponseWritten() }

// NoContent writes a body-less response (e.g. 204). Mirrors
// core.RequestEvent.NoContent.
func (e *Event) NoContent(status int) error {
	e.Response.WriteHeader(status)
	return nil
}

// Redirect writes a redirect response. The status code must be 300–399.
// Mirrors core.RequestEvent.Redirect. Used by auth/oidc.go.
func (e *Event) Redirect(status int, url string) error {
	if status < 300 || status > 399 {
		return ErrInvalidRedirectStatusCode
	}
	e.Response.Header().Set("Location", url)
	e.Response.WriteHeader(status)
	return nil
}

// Cookie writes a Set-Cookie header. Convenience wrapper over http.SetCookie
// for parity with the patterns used in auth flows.
func (e *Event) SetCookie(cookie *http.Cookie) {
	http.SetCookie(e.Response, cookie)
}

// Real IP
// -------------------------------------------------------------------

// RealIP returns the client IP, preferring the first X-Forwarded-For entry and
// falling back to the request RemoteAddr (host portion). The TechStack edge
// terminates TLS and sets X-Forwarded-For; this mirrors how trust/helpers.go,
// workers.go and system_accounts.go derive the IP manually today.
func (e *Event) RealIP() string {
	if xff := e.Request.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xrip := strings.TrimSpace(e.Request.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	addr := e.Request.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}

// Binders
// -------------------------------------------------------------------

// BindBody unmarshals the request body into dst.
//
// dst must be a struct pointer or map[string]any. Mirrors
// core.RequestEvent.BindBody for the content types the TechStack routes use:
//   - application/json
//   - multipart/form-data, application/x-www-form-urlencoded (best-effort)
//
// A zero-length body is a no-op (returns nil), matching PocketBase. XML binding
// is intentionally dropped (no route uses it).
func (e *Event) BindBody(dst any) error {
	if e.Request.ContentLength == 0 {
		return nil
	}

	contentType := e.Request.Header.Get(headerContentType)

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		return json.NewDecoder(e.Request.Body).Decode(dst)

	case strings.HasPrefix(contentType, "multipart/form-data"):
		if err := e.Request.ParseMultipartForm(DefaultMaxMemory); err != nil {
			return err
		}
		return bindFormValues(e.Request.Form, dst)

	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		if err := e.Request.ParseForm(); err != nil {
			return err
		}
		return bindFormValues(e.Request.Form, dst)

	default:
		// Default to JSON decoding for unspecified content types so callers
		// that omit Content-Type but send JSON still work (a common API client
		// behavior). If it is genuinely not JSON the decode error surfaces.
		if contentType == "" {
			return json.NewDecoder(e.Request.Body).Decode(dst)
		}
		return ErrUnsupportedContentType
	}
}

// BindJSON is an explicit JSON-only binder alias for BindBody, kept for parity
// with call sites that used core.RequestEvent.BindJSON.
func (e *Event) BindJSON(dst any) error {
	if e.Request.Body == nil {
		return nil
	}
	return json.NewDecoder(e.Request.Body).Decode(dst)
}

// Principal
// -------------------------------------------------------------------

// Principal is the authenticated request actor. It is the PB-free replacement
// for *core.Record in the e.Auth position and exposes the exact method surface
// the TechStack routes consume:
//
//	e.Auth != nil
//	e.Auth.Id
//	e.Auth.IsSuperuser()
//	e.Auth.GetString("role" | "org_id" | "email" | "username" | ...)
//
// It wraps identity.Identity (the Postgres-era principal populated by the edge
// and v2-session middleware) plus an arbitrary attribute bag for fields that do
// not map onto Identity's typed members.
type Principal struct {
	// Id is the stable subject identifier (Auth0 sub / user id).
	// Exported as "Id" (not "ID") to match the core.Record.Id field name so
	// migrated call sites read identically.
	Id string

	// Identity is the underlying edge-injected identity, or nil.
	Identity *identity.Identity

	// superuser marks platform-admin principals.
	superuser bool

	// attrs holds additional string claims addressable via GetString
	// (e.g. "username", "name", "displayName") that are not first-class on
	// identity.Identity.
	attrs map[string]string
}

// NewPrincipal builds a Principal from an identity.Identity. Returns nil when
// id is nil or unauthenticated, so the common `if e.Auth != nil` guard behaves
// exactly as it did with core.Record.
func NewPrincipal(id *identity.Identity) *Principal {
	if id == nil || !id.IsAuthenticated() {
		return nil
	}
	return &Principal{
		Id:        id.UserID,
		Identity:  id,
		superuser: id.HasRole("admin") || id.HasRole("superuser"),
	}
}

// IsSuperuser reports whether the principal has platform-admin authority.
// Mirrors core.Record.IsSuperuser().
func (p *Principal) IsSuperuser() bool {
	return p != nil && p.superuser
}

// SetSuperuser overrides the superuser flag (used by auth middleware that
// applies its own admin-role policy via pbRecordHasAdminRole-equivalent logic).
func (p *Principal) SetSuperuser(v bool) {
	if p != nil {
		p.superuser = v
	}
}

// GetString returns the string value for a claim key, mirroring
// core.Record.GetString. Known identity fields are resolved first; otherwise
// the attribute bag is consulted. Unknown keys return "".
func (p *Principal) GetString(key string) string {
	if p == nil {
		return ""
	}
	switch key {
	case "id":
		return p.Id
	case "email":
		if p.Identity != nil {
			return p.Identity.Email
		}
	case "org_id", "orgId", "organization":
		if p.Identity != nil {
			return p.Identity.OrgID
		}
	case "tier":
		if p.Identity != nil {
			return p.Identity.Tier
		}
	case "role":
		if p.Identity != nil && len(p.Identity.Roles) > 0 {
			return p.Identity.Roles[0]
		}
	}
	if p.attrs != nil {
		return p.attrs[key]
	}
	return ""
}

// Set stores an arbitrary string claim addressable via GetString.
func (p *Principal) Set(key, value string) {
	if p == nil {
		return
	}
	if p.attrs == nil {
		p.attrs = make(map[string]string, 4)
	}
	p.attrs[key] = value
}

// HasRole reports whether the principal carries the given role (case-insensitive).
func (p *Principal) HasRole(role string) bool {
	return p != nil && p.Identity != nil && p.Identity.HasRole(role)
}

// authFromContext derives a Principal from the request context identity.
func authFromContext(ctx context.Context) *Principal {
	return NewPrincipal(identity.FromContext(ctx))
}
