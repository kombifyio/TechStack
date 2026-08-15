// Package secrets provides centralized secret redaction for logs, command
// output, and any other text that may flow into persistent storage or external
// systems.
//
// The redactor matches a curated catalogue of high-confidence secret patterns
// (secret-provider tokens, AWS access keys, GitHub PATs, OpenAI keys, Render tokens,
// Slack tokens, Bearer tokens, SSH private-key blocks, generic password/token
// assignments) and replaces the secret value with [REDACTED] while preserving
// surrounding context where useful. Known-safe example tokens (allow-list)
// pass through untouched so that documentation snippets and test fixtures do
// not produce noisy false-positive replacements.
//
// Usage:
//
//	clean := secrets.Redact(commandOutput)
//
// For tests or callers that need a custom catalogue, build a Redactor:
//
//	r := secrets.NewRedactor(secrets.Options{Patterns: ..., AllowList: ...})
//	clean := r.Redact(input)
package secrets

import (
	"regexp"
	"strings"
	"sync"
)

// Pattern describes a single secret-detection rule. Replacement may use
// regexp back-references ($1, ${name}, …) — they are expanded against the
// matched substring via Regexp.ExpandString.
type Pattern struct {
	Name        string
	Regex       *regexp.Regexp
	Replacement string
}

// Options configures a Redactor. Both fields are optional; nil/empty values
// fall back to the package-default catalogue and allow-list.
type Options struct {
	Patterns  []Pattern
	AllowList []string
}

// Redactor applies a fixed catalogue of secret patterns to input strings.
// Redactor is safe for concurrent use; the underlying compiled regexes are
// immutable after construction.
type Redactor struct {
	patterns  []Pattern
	allowList map[string]struct{}
}

// NewRedactor returns a Redactor backed by the given options. Empty Patterns
// or AllowList fields are populated from the package defaults.
func NewRedactor(opts Options) *Redactor {
	patterns := opts.Patterns
	if len(patterns) == 0 {
		patterns = defaultPatterns()
	}
	rawAllow := opts.AllowList
	if len(rawAllow) == 0 {
		rawAllow = defaultAllowList()
	}
	allow := make(map[string]struct{}, len(rawAllow))
	for _, a := range rawAllow {
		allow[a] = struct{}{}
	}
	return &Redactor{patterns: patterns, allowList: allow}
}

// Redact returns s with every recognised secret value replaced. Empty input
// is returned unchanged. Allow-listed substrings (defined by Options.AllowList
// or the package defaults) pass through even when they match a pattern.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	out := s
	for _, p := range r.patterns {
		out = p.Regex.ReplaceAllStringFunc(out, func(match string) string {
			if _, ok := r.allowList[match]; ok {
				return match
			}
			idx := p.Regex.FindStringSubmatchIndex(match)
			if idx == nil {
				return match
			}
			return string(p.Regex.ExpandString(nil, p.Replacement, match, idx))
		})
	}
	return out
}

// Redact applies the package-default Redactor to s. The default Redactor is
// constructed lazily on first use and reused across calls.
func Redact(s string) string {
	defaultOnce.Do(func() {
		defaultRedactor = NewRedactor(Options{})
	})
	return defaultRedactor.Redact(s)
}

var (
	defaultRedactor *Redactor
	defaultOnce     sync.Once
)

// defaultPatterns returns the curated secret catalogue. Order matters: the
// largest / most specific patterns run first so that later, more permissive
// patterns (generic password=, token=) do not match inside an already-redacted
// region. SSH private-key blocks run first because they are multi-line and
// could otherwise contain substrings that match other patterns.
func defaultPatterns() []Pattern {
	return []Pattern{
		{
			Name:        "ssh-private-key-block",
			Regex:       regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
			Replacement: `-----BEGIN PRIVATE KEY-----[REDACTED]-----END PRIVATE KEY-----`,
		},
		{
			Name:        "provider-personal-token",
			Regex:       regexp.MustCompile(`\bdp\.pt\.[A-Za-z0-9]{40,}\b`),
			Replacement: `dp.pt.[REDACTED]`,
		},
		{
			Name:        "provider-service-token",
			Regex:       regexp.MustCompile(`\bdp\.st\.[a-z0-9_-]+\.[A-Za-z0-9]{40,}\b`),
			Replacement: `dp.st.[REDACTED]`,
		},
		{
			Name:        "aws-access-key",
			Regex:       regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
			Replacement: `AKIA[REDACTED]`,
		},
		{
			Name:        "github-pat",
			Regex:       regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`),
			Replacement: `${1}_[REDACTED]`,
		},
		{
			Name:        "openai-key",
			Regex:       regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
			Replacement: `sk-[REDACTED]`,
		},
		{
			Name:        "render-api-token",
			Regex:       regexp.MustCompile(`\brnd_[A-Za-z0-9]{32,}\b`),
			Replacement: `rnd_[REDACTED]`,
		},
		{
			Name:        "slack-token",
			Regex:       regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
			Replacement: `xox-[REDACTED]`,
		},
		{
			Name:        "bearer-token",
			Regex:       regexp.MustCompile(`\bBearer\s+[A-Za-z0-9._\-]{16,}\b`),
			Replacement: `Bearer [REDACTED]`,
		},
		// Identifier-suffix patterns: match keywords like PASSWORD / TOKEN /
		// API_KEY / CLIENT_SECRET when they appear at the END of an
		// identifier. The optional [A-Za-z_]* prefix captures common
		// vendor-prefixed names such as PGPASSWORD, MYSQL_PASSWORD, or
		// OAUTH_CLIENT_SECRET.
		{
			Name:        "client-secret",
			Regex:       regexp.MustCompile(`(?i)\b([A-Za-z_]*client[_-]?secret)\s*[=:]\s*[A-Za-z0-9._~\-]{16,}`),
			Replacement: `${1}=[REDACTED]`,
		},
		{
			Name:        "generic-password",
			Regex:       regexp.MustCompile(`(?i)\b([A-Za-z_]*(?:password|passwd|pwd))\s*[=:]\s*\S+`),
			Replacement: `${1}=[REDACTED]`,
		},
		{
			Name:        "generic-api-key",
			Regex:       regexp.MustCompile(`(?i)\b([A-Za-z_]*api[_-]?key)\s*[=:]\s*[A-Za-z0-9._~\-]{16,}`),
			Replacement: `${1}=[REDACTED]`,
		},
		{
			Name:        "generic-token-assignment",
			Regex:       regexp.MustCompile(`(?i)\b([A-Za-z_]*(?:api[_-]?|access[_-]?|refresh[_-]?|auth[_-]?)?token)\s*[=:]\s*[A-Za-z0-9._~\-]{16,}`),
			Replacement: `${1}=[REDACTED]`,
		},
	}
}

// defaultAllowList enumerates substrings that look like secrets but are
// publicly known placeholders / examples. Treated case-sensitively.
func defaultAllowList() []string {
	return []string{
		"dp.pt.example",
		"dp.pt.test_token",
		"dp." + "pt." + strings.Repeat("0", 40),
		"AKIAIOSFODNN7EXAMPLE",
		"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
	}
}
