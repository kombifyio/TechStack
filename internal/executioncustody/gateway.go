package executioncustody

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
)

type gatewayEntry struct {
	verified context.Context
	until    time.Time
	purpose  [32]byte
	created  bool
}

// GatewayAdmission reuses the existing Gateway verifier and its sealed context.
// One original envelope may serve only the identical native lease preflight
// and one create. It never authorizes another provider, DTO or action.
type GatewayAdmission struct {
	sync.Mutex
	config  *config.Config
	audit   *sql.DB
	entries map[[32]byte]*gatewayEntry
}

func NewGatewayAdmission(cfg *config.Config, audit *sql.DB) *GatewayAdmission {
	return &GatewayAdmission{config: cfg, audit: audit, entries: map[[32]byte]*gatewayEntry{}}
}

type gatewayContext struct {
	context.Context
	verified context.Context
}

func (c gatewayContext) Value(key any) any {
	if value := c.verified.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}

func (g *GatewayAdmission) admit(ctx context.Context, grant CallerGrant, action string, request providerRequest) (context.Context, error) {
	denied := errors.New("Gateway admission purpose denied")
	envelope := request.Envelope
	if g == nil || envelope == nil || request.Lease == nil || (action != remotePath+"provider/preflight" && action != remotePath+"provider/create") {
		return nil, denied
	}
	u, err := url.ParseRequestURI(envelope.RequestURI)
	if err != nil || u.IsAbs() || u.Fragment != "" {
		return nil, denied
	}
	path := u.Path
	allowed := false
	if envelope.Method == http.MethodGet && action == remotePath+"provider/preflight" && path == "/api/v1/managed-runtimes/authority" {
		allowed = true
	}
	if envelope.Method == http.MethodPost {
		allowed = path == "/api/v1/stacks" || path == "/api/v1/wizard/runs"
		if strings.HasPrefix(path, "/api/v1/stacks/") && strings.HasSuffix(path, "/managed-runtimes") {
			stack := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/stacks/"), "/managed-runtimes")
			allowed = stack != "" && !strings.Contains(stack, "/") && stack == request.Lease.StackID
		}
		if strings.HasPrefix(path, "/api/v1/monthly-runtimes/") && strings.HasSuffix(path, "/recreate") {
			lease := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/monthly-runtimes/"), "/recreate")
			allowed = lease != "" && !strings.Contains(lease, "/")
		}
	}
	if !allowed || request.Lease.TenantID != grant.TenantID || request.Lease.OwnerID != grant.OwnerID {
		return nil, denied
	}
	// Use the signed nonce, not the serialized header map. Adding an unsigned
	// header must not create another identity for the same admission envelope.
	nonce := envelope.Header.Get(edgeauth.HeaderEdgeNonce)
	if nonce == "" {
		return nil, denied
	}
	raw, err := json.Marshal([]string{grant.TenantID, grant.OwnerID, grant.Caller, nonce})
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(raw)
	raw, err = json.Marshal(struct {
		Method string
		URI    string
		Lease  any
	}{envelope.Method, envelope.RequestURI, request.Lease})
	if err != nil {
		return nil, err
	}
	purpose := sha256.Sum256(raw)
	g.Lock()
	defer g.Unlock()
	now := time.Now()
	for hash, entry := range g.entries {
		if !now.Before(entry.until) {
			delete(g.entries, hash)
		}
	}
	entry := g.entries[key]
	if entry == nil {
		if len(g.entries) >= 1000 {
			return nil, errors.New("Gateway admission cache is full")
		}
		verified, err := middleware.ReverifyEdgeEnvelope(envelope, g.config)
		if err != nil {
			return nil, err
		}
		id := identity.FromContext(verified)
		flags, ok := edgeauth.FlagsFromContext(verified)
		if !ok {
			return nil, denied
		}
		binding, ok := flags.VerifiedDecisionBinding()
		if !ok || id == nil || !id.EmailVerified || id.UserID != grant.OwnerID || id.OrgID != grant.TenantID || binding.SubjectID != grant.OwnerID || binding.TenantID != grant.TenantID || binding.Method != envelope.Method || binding.Audience != "techstack" || binding.PublicPrefix != "/v1/techstack" {
			return nil, denied
		}
		seconds, err := strconv.ParseInt(envelope.Header.Get(edgeauth.HeaderEdgeTimestamp), 10, 64)
		if err != nil {
			return nil, denied
		}
		until := time.Unix(seconds, 0).Add(5 * time.Minute)
		decisionUntil := binding.IssuedAt.Add(5 * time.Minute)
		if decisionUntil.Before(until) {
			until = decisionUntil
		}
		if !now.Before(until) {
			return nil, denied
		}
		entry = &gatewayEntry{verified: verified, until: until, purpose: purpose}
		g.entries[key] = entry
	}
	if entry.purpose != purpose || !now.Before(entry.until) {
		return nil, denied
	}
	if err := g.claimEnvelope(ctx, grant, key, purpose, action == remotePath+"provider/create"); err != nil {
		return nil, err
	}
	if action == remotePath+"provider/create" {
		if entry.created {
			return nil, denied
		}
		entry.created = true
	}
	return gatewayContext{Context: ctx, verified: entry.verified}, nil
}
