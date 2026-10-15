// Package mcporiginauth verifies the body-bound identity envelope used by the
// Kombify Gateway when it calls a product-native MCP origin without forwarding
// the user's bearer token.
package mcporiginauth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	contextHeader   = "X-Kombify-Mcp-Origin-Context"
	signatureHeader = "X-Kombify-Mcp-Origin-Signature"
	keyIDHeader     = "X-Kombify-Mcp-Origin-Key-Id"
	timestampHeader = "X-Kombify-Mcp-Origin-Timestamp"
	nonceHeader     = "X-Kombify-Mcp-Origin-Nonce"
	bodyHashHeader  = "X-Kombify-Mcp-Origin-Body-Sha256"
	version         = "v1"
	defaultMaxBody  = int64(4 << 20)
)

// Config fixes the product-native trust boundary. Keys are selected by the
// key-id header so rotations can overlap without accepting an unknown
// signer.
type Config struct {
	Audience     string
	ServerKey    string
	Keys         map[string]string
	MaxBodyBytes int64
	Now          func() time.Time
	// VerifyDelegatedAuthority checks the product's current authority before a
	// v2 principal reaches its handler. A signed projection alone is no grant.
	VerifyDelegatedAuthority func(context.Context, Context) error
	// ReserveNonce atomically reserves a v2 nonce across origin replicas until
	// expiry. A process-local cache is insufficient for delegated operations.
	ReserveNonce func(context.Context, string, time.Time) (bool, error)
}

// Context is the verified Gateway identity and authorization context. Product
// origins must derive their tenant exclusively from Sub, never from MCP input.
type Context struct {
	Subject        string
	OrganizationID string
	Scopes         []string
	ActorSubject   string
	AgentClass     string
	// AgentInstanceID is the exact runtime target of a tool-specific lease
	// verified by Gateway. It is distinct from the RFC 8693 broker actor and
	// does not itself grant access. Empty means no verified runtime provenance.
	AgentInstanceID string
	ClientID        string
	PrincipalType   string
	RequestID       string
	TraceID         string
	ToolName        string
	OperationIDs    []string
	IdempotencyKey  string
	CatalogRevision string
	// DelegatedAuthority and Arguments are signed, body-bound evidence for the
	// configured authority verifier. Their product schemas remain product-owned.
	DelegatedAuthority json.RawMessage
	Arguments          json.RawMessage
	ExpiresAt          time.Time
}

type contextKey struct{}

// FromContext returns a context only after Middleware verified the complete
// envelope, signature, body binding, tool binding, expiry and nonce.
func FromContext(ctx context.Context) (Context, bool) {
	verified, ok := ctx.Value(contextKey{}).(Context)
	return verified, ok
}

// Middleware accepts ordinary requests unchanged and authenticates requests
// carrying the Gateway MCP-origin marker. Invalid marked requests fail before
// the product handler can observe a principal or cause a side effect.
func Middleware(cfg Config) func(http.Handler) http.Handler {
	nonces := &nonceCache{seen: make(map[string]time.Time)}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = defaultMaxBody
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hasOriginMarker(r.Header) {
				next.ServeHTTP(w, r)
				return
			}
			verified, err := verify(r, cfg, nonces)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="api.kombify.io"`)
				http.Error(w, `{"error":"mcp_origin_auth_invalid"}`, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, verified)))
		})
	}
}

func hasOriginMarker(h http.Header) bool {
	return h.Get(contextHeader) != "" || h.Get(signatureHeader) != ""
}

type nonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (c *nonceCache) consume(key string, expiry, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for seen, until := range c.seen {
		if !until.After(now) {
			delete(c.seen, seen)
		}
	}
	if until, exists := c.seen[key]; exists && until.After(now) {
		return false
	}
	if len(c.seen) >= 100_000 {
		return false
	}
	c.seen[key] = expiry
	return true
}
