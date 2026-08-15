package routes

import (
	"net/http"
	"runtime"

	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	// goroutineDumpLimit bounds the response. A wedged control plane has a few
	// hundred goroutines; anything past this is noise, and an unbounded dump
	// would be a denial-of-service surface of its own.
	goroutineDumpLimit = 4 << 20
	diagnosticsRoute   = "/api/v1/internal/diagnostics/goroutines"
)

// RegisterRuntimeDiagnosticsRoutes wires the operator-only goroutine dump.
//
// A job that stops reporting is indistinguishable from a job that is working:
// the row says running, the logs stop, and no error is ever written. Every
// other signal the control plane offers is a projection of that same silence.
// A stack dump is the only thing that says where the process actually is, and
// without it a wedged rollout can only be guessed at from the outside.
//
// It reuses the demo-automation shared secret rather than inventing a new
// credential, and it exposes goroutines only -- no heap, no CPU profile, no
// arbitrary pprof surface.
func RegisterRuntimeDiagnosticsRoutes(r *httpx.Router) {
	r.GET(diagnosticsRoute, handleGoroutineDump)
}

func handleGoroutineDump(e *httpx.Event) error {
	// authorizeDemoAutomation writes its own rejection envelope and returns a
	// nil error when that write succeeds, so the error alone does not say
	// whether the caller was admitted. The tenant id does: it is empty on every
	// rejection. Checking only the error let a refused caller keep reading.
	tenantID, err := authorizeDemoAutomation(e, "diagnostics")
	if err != nil || tenantID == "" {
		return err
	}
	buf := make([]byte, goroutineDumpLimit)
	n := runtime.Stack(buf, true)
	e.Response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	e.Response.Header().Set("Cache-Control", "no-store")
	e.Response.WriteHeader(http.StatusOK)
	_, writeErr := e.Response.Write(buf[:n])
	return writeErr
}
