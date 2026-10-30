//go:build kombify_full_local

// Package observability is the Full Local build of the Sentry wiring (build
// tag kombify_full_local, DATA-ARCHITECTURE.md section 5, D-2026-10-21-12): the
// same API, but the Sentry SDK is not linked, so the binary has no error
// reporting client, no DSN handling and nothing to send. Tracing helpers
// (tracing.go) and the logger (logging.go) are unchanged; they have no
// network exporter.
package observability

import (
	"context"
	"errors"
	"net/http"
)

// MustInit does nothing and returns a no-op shutdown.
func MustInit(Config) func() { return func() {} }

// Middleware returns next unchanged.
func Middleware(next http.Handler) http.Handler { return next }

// CaptureErr returns err unchanged; nothing is reported.
func CaptureErr(_ context.Context, err error) error { return err }

// ErrObservabilityDisabled reports that error reporting is disabled.
var ErrObservabilityDisabled = errors.New("observability: compiled out of this Full Local build")

// IsEnabled is always false.
func IsEnabled() bool { return false }
