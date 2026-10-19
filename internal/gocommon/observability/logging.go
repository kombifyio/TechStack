package observability

import (
	"io"
	"log/slog"
	"os"
)

// LogConfig configures the shared structured logger for a kombify SaaS Go
// service.
type LogConfig struct {
	// Service is the stable service name (same value as Config.Service).
	Service string
	// Environment and Release mirror the Sentry tags so logs and errors
	// correlate.
	Environment string
	Release     string
	// Level is the minimum level; zero value is slog.LevelInfo.
	Level slog.Level
	// Output defaults to os.Stderr.
	Output io.Writer
}

// NewLogger returns a JSON slog logger that carries service, env and release
// on every record and scrubs values the same way the Sentry hook does:
// attributes whose key names a credential, cookie, body, query or e-mail are
// replaced with "[scrubbed]", and URL-shaped string values lose their query
// string and fragment. Callers add request-scoped fields with Logger.With.
func NewLogger(cfg LogConfig) *slog.Logger {
	out := cfg.Output
	if out == nil {
		out = os.Stderr
	}
	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       cfg.Level,
		ReplaceAttr: scrubLogAttr,
	})
	logger := slog.New(handler)
	attrs := make([]any, 0, 6)
	if cfg.Service != "" {
		attrs = append(attrs, slog.String("service", cfg.Service))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, slog.String("env", cfg.Environment))
	}
	if cfg.Release != "" {
		attrs = append(attrs, slog.String("release", cfg.Release))
	}
	return logger.With(attrs...)
}

func scrubLogAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 {
		switch a.Key {
		case slog.TimeKey, slog.LevelKey, slog.MessageKey, slog.SourceKey, "service", "env", "release":
			return a
		}
	}
	if a.Value.Kind() == slog.KindGroup {
		return a
	}
	if sensitiveMetadataKey(a.Key) {
		return slog.String(a.Key, "[scrubbed]")
	}
	if a.Value.Kind() == slog.KindString {
		return slog.String(a.Key, scrubURL(a.Value.String()))
	}
	return a
}
