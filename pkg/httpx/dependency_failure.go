package httpx

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"

	"github.com/kombifyio/techstack/pkg/secrets"
)

const dependencyFailureErrorMaxBytes = 240

// DependencyFailure names why a read of a backing dependency (database,
// authorization policy) failed. A handler that answers "inventory is
// unavailable" keeps that fail-closed answer; this records the cause the answer
// hides, so the Sentry event and the log line can tell an outage, a database
// restart, an exhausted pool and a departed client apart.
type DependencyFailure struct {
	Operation string
	Class     string
	Message   string
}

type dependencyFailureContextKey struct{}

// DependencyFailureClass maps an error to a short, value-free class:
// "canceled" (the caller's context ended), "deadline_exceeded", "sqlstate_<code>"
// (a PostgreSQL error code, for example 57P01 when the server shuts a session
// down during a restart or deploy, or 53300 when the server has no free
// connection), "connection" (the transport broke) or "other".
func DependencyFailureClass(err error) string {
	var state interface{ SQLState() string }
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.As(err, &state) && state.SQLState() != "":
		return "sqlstate_" + strings.ToLower(state.SQLState())
	case errors.Is(err, driver.ErrBadConn), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "connection"
	}
	return "other"
}

// RecordDependencyFailure logs why a dependency read failed and attaches the
// cause to the request, so the 5xx report that follows names it. It never
// changes the response.
func RecordDependencyFailure(e *Event, operation string, err error) {
	if e == nil || e.Request == nil || err == nil {
		return
	}
	message := truncateSentryDetails(secrets.Redact(err.Error()))
	if len(message) > dependencyFailureErrorMaxBytes {
		message = message[:dependencyFailureErrorMaxBytes]
	}
	failure := DependencyFailure{Operation: operation, Class: DependencyFailureClass(err), Message: message}
	e.Request = e.Request.WithContext(context.WithValue(e.Request.Context(), dependencyFailureContextKey{}, failure))
	slog.WarnContext(e.Request.Context(), "dependency_read_failed",
		"operation", failure.Operation,
		"cause_class", failure.Class,
		"route", e.Request.URL.Path,
		"request_id", e.Request.Header.Get("X-Request-ID"),
		"error", failure.Message,
	)
}

// DependencyFailureFrom returns the failure recorded for the request, if any.
func DependencyFailureFrom(ctx context.Context) (DependencyFailure, bool) {
	if ctx == nil {
		return DependencyFailure{}, false
	}
	failure, ok := ctx.Value(dependencyFailureContextKey{}).(DependencyFailure)
	return failure, ok
}

// ClientGone reports whether the caller ended the request before it was
// answered. A 5xx written to a client that has already left is not a server
// fault, so it is not reported to Sentry.
func ClientGone(ctx context.Context) bool {
	return ctx != nil && errors.Is(ctx.Err(), context.Canceled)
}
