// Package logger provides structured logging for kombifyTechstack using slog
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

var globalLogger = Default()

// Logger wraps slog.Logger with kombifyTechstack-specific functionality
type Logger struct {
	*slog.Logger
}

// New creates a new Logger with the specified level and format
func New(level, format string) *Logger {
	var handler slog.Handler

	opts := &slog.HandlerOptions{
		Level:     parseLevel(level),
		AddSource: level == "debug",
	}

	if strings.ToLower(format) == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return &Logger{
		Logger: slog.New(handler),
	}
}

// Default returns a default logger (info level, json format)
func Default() *Logger {
	return New("info", "json")
}

// NewNop returns a logger that discards all output.
// Useful for unit tests.
func NewNop() *Logger {
	h := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})
	return &Logger{Logger: slog.New(h)}
}

// Get returns the package-default logger.
func Get() *Logger {
	return globalLogger
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithComponent returns a new logger with a component attribute
func (l *Logger) WithComponent(name string) *Logger {
	return &Logger{
		Logger: l.With("component", name),
	}
}

// WithRequestID returns a new logger with a request ID attribute
func (l *Logger) WithRequestID(id string) *Logger {
	return &Logger{
		Logger: l.With("request_id", id),
	}
}

// HTTP logs an HTTP request
func (l *Logger) HTTP(ctx context.Context, method, path string, status int, duration string) {
	l.InfoContext(ctx, "http_request",
		"method", method,
		"path", path,
		"status", status,
		"duration", duration,
	)
}

// StartupInfo logs service startup information
func (l *Logger) StartupInfo(service, version, addr string) {
	l.Info("service_starting",
		"service", service,
		"version", version,
		"addr", addr,
	)
}
