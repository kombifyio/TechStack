package middleware

import (
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

// RequestIDMiddleware injects a unique X-Request-ID into every request.
// If the client already provides one it is used (truncated to 128 chars);
// otherwise a new UUID v4 is generated.
//
// Register as a global httpx middleware:
//
//	router.BindFunc(middleware.RequestIDMiddleware)
func RequestIDMiddleware(e *httpx.Event) error {
	rid := e.Request.Header.Get(requestIDHeader)
	if rid == "" {
		rid = uuid.NewString()
	} else if len(rid) > 128 {
		rid = rid[:128]
	}
	// Set on request header so downstream handlers can read it
	e.Request.Header.Set(requestIDHeader, rid)
	// Set on response header so clients can correlate
	e.Response.Header().Set(requestIDHeader, rid)
	return e.Next()
}
