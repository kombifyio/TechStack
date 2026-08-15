package httpx

import (
	"context"
	"net/http"
	"time"
)

// ServerConfig configures the httpx HTTP server. It is a plain value type with
// no dependency on pkg/config, keeping pkg/httpx self-contained; the caller maps
// its config.ServerConfig onto these fields.
type ServerConfig struct {
	// Addr is the listen address, e.g. ":5260".
	Addr string

	// ReadTimeout bounds reading the entire request including body.
	// Zero uses DefaultReadTimeout.
	ReadTimeout time.Duration

	// ReadHeaderTimeout bounds reading request headers.
	// Zero uses DefaultReadHeaderTimeout.
	ReadHeaderTimeout time.Duration

	// WriteTimeout bounds writing the response.
	// Zero uses DefaultWriteTimeout.
	WriteTimeout time.Duration

	// IdleTimeout bounds keep-alive idle connections.
	// Zero uses DefaultIdleTimeout.
	IdleTimeout time.Duration

	// MaxHeaderBytes bounds request header size. Zero uses the net/http default.
	MaxHeaderBytes int

	// ShutdownTimeout bounds graceful shutdown drain time.
	// Zero uses DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
}

// Server defaults. Conservative production-safe values; override via ServerConfig.
const (
	DefaultReadTimeout       = 30 * time.Second
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultWriteTimeout      = 60 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultShutdownTimeout   = 15 * time.Second
)

// Server owns the *http.Server built from a Router, applies the configured
// timeouts, and provides graceful shutdown. It is the PB-free replacement for
// PocketBase serving the embedded mux.
type Server struct {
	httpServer      *http.Server
	router          *Router
	shutdownTimeout time.Duration
}

// NewServer constructs a Server. The Router is compiled to a *http.ServeMux via
// router.BuildMux and installed as the http.Server handler. Middleware bound on
// the router/groups is applied per-route through the Event chain.
func NewServer(router *Router, cfg ServerConfig) *Server {
	readTimeout := cfg.ReadTimeout
	if readTimeout == 0 {
		readTimeout = DefaultReadTimeout
	}
	readHeaderTimeout := cfg.ReadHeaderTimeout
	if readHeaderTimeout == 0 {
		readHeaderTimeout = DefaultReadHeaderTimeout
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = DefaultWriteTimeout
	}
	idleTimeout := cfg.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = DefaultIdleTimeout
	}
	shutdownTimeout := cfg.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router.BuildMux(),
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}

	return &Server{
		httpServer:      srv,
		router:          router,
		shutdownTimeout: shutdownTimeout,
	}
}

// Router returns the underlying Router.
func (s *Server) Router() *Router { return s.router }

// HTTPServer exposes the underlying *http.Server for advanced configuration
// (TLS, BaseContext, ConnState, etc.) before Start.
func (s *Server) HTTPServer() *http.Server { return s.httpServer }

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.httpServer.Addr }

// Start begins serving and blocks until the server stops. It returns nil on a
// clean shutdown (http.ErrServerClosed) and the underlying error otherwise.
func (s *Server) Start() error {
	err := s.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// StartTLS is the TLS variant of Start.
func (s *Server) StartTLS(certFile, keyFile string) error {
	err := s.httpServer.ListenAndServeTLS(certFile, keyFile)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown gracefully drains in-flight requests, bounded by the configured
// shutdown timeout (or the provided ctx, whichever fires first).
func (s *Server) Shutdown(ctx context.Context) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, s.shutdownTimeout)
	defer cancel()
	return s.httpServer.Shutdown(shutdownCtx)
}

// Close immediately closes the server without draining. Prefer Shutdown.
func (s *Server) Close() error {
	return s.httpServer.Close()
}
