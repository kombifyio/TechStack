package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/platformprojection"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

// The registry side of the platform server list: it reads what this service
// already owns and hands it to the projector, which publishes it into
// kombify-db so every kombify product reads one server list instead of calling
// this service (ADR-038; DATA-ARCHITECTURE row 1 `infra_user_servers`).

// PlatformProjectionStore is the slice of the control plane the source needs.
type PlatformProjectionStore interface {
	ListServerProjectionTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error)
	ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]controlplane.ServerRuntime, error)
	ListServiceRuntimes(ctx context.Context, tenantID, stackID, serverID string) ([]controlplane.ServiceRuntime, error)
}

// PlatformProjectionSource maps runtime aggregates onto the platform's wire
// shape. The field mapping deliberately matches the one the inventory API
// already publishes (`projectServer`), so a product reading the platform list
// sees the same provider and StackKit labels it saw over HTTP.
type PlatformProjectionSource struct {
	Store PlatformProjectionStore
}

func (s PlatformProjectionSource) ListServerProjectionTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("routes: platform projection store required")
	}
	return s.Store.ListServerProjectionTenants(ctx, afterTenantID, limit)
}

func (s PlatformProjectionSource) ListProjectedServers(ctx context.Context, tenantID string) ([]serverregistry.ProjectedServer, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("routes: platform projection store required")
	}
	servers, err := s.Store.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	// Services are read once per tenant and grouped, not once per server: a
	// projection pass must not turn one tenant into N+1 queries.
	services, err := s.Store.ListServiceRuntimes(ctx, tenantID, "", "")
	if err != nil {
		return nil, err
	}
	byServer := make(map[string][]string, len(servers))
	for _, service := range services {
		serverID := strings.TrimSpace(service.ServerID)
		if serverID == "" {
			continue
		}
		name := strings.TrimSpace(firstNonEmptyString(service.Name, service.ServiceKey))
		if name == "" {
			continue
		}
		byServer[serverID] = append(byServer[serverID], safeLabel(name, 96))
	}
	projected := make([]serverregistry.ProjectedServer, 0, len(servers))
	for _, server := range servers {
		projected = append(projected, projectServerForPlatform(server, byServer[strings.TrimSpace(server.ID)]))
	}
	return projected, nil
}

func projectServerForPlatform(server controlplane.ServerRuntime, serviceNames []string) serverregistry.ProjectedServer {
	return platformprojection.Server(server, serviceNames)
}

// PlatformPublisherConfig points the publisher at kombify Cloud, which owns the
// platform database and therefore performs the write.
type PlatformPublisherConfig struct {
	CloudOrigin string
	ServiceName string
	TargetName  string
	Secret      string
	Timeout     time.Duration
	Client      *http.Client
}

type platformHTTPPublisher struct {
	origin string
	auth   servicecall.Config
	target string
	client *http.Client
}

// NewPlatformPublisher returns nil when the projection is not configured, which
// is the self-hosted and local default: no origin, no publishing, no error.
func NewPlatformPublisher(cfg PlatformPublisherConfig) (serverregistry.PlatformPublisher, error) {
	origin := strings.TrimRight(strings.TrimSpace(cfg.CloudOrigin), "/")
	if origin == "" {
		return nil, nil
	}
	if !serviceauth.CanSign(cfg.Secret) {
		return nil, fmt.Errorf("routes: platform projection requires a service auth secret or Ed25519 key")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	target := firstNonEmptyString(strings.TrimSpace(cfg.TargetName), "cloud")
	auth, err := serviceauth.Apply(servicecall.Config{
		ServiceName: firstNonEmptyString(strings.TrimSpace(cfg.ServiceName), "techstack"),
		Target:      target,
		Secret:      strings.TrimSpace(cfg.Secret),
	})
	if err != nil {
		return nil, fmt.Errorf("routes: platform projection service auth: %w", err)
	}
	return &platformHTTPPublisher{
		origin: origin,
		auth:   auth,
		target: target,
		client: client,
	}, nil
}

func (p *platformHTTPPublisher) PublishServers(ctx context.Context, tenantID string, servers []serverregistry.ProjectedServer) error {
	if p == nil {
		return nil
	}
	// The tenant travels in the envelope, not only in the rows. A pass that
	// found no servers still has something to say — "this tenant has none" —
	// and the platform cannot record that from an empty row list alone.
	if servers == nil {
		servers = []serverregistry.ProjectedServer{}
	}
	body, err := json.Marshal(map[string]any{"tenant_id": tenantID, "servers": servers})
	if err != nil {
		return fmt.Errorf("encode servers: %w", err)
	}
	token, err := servicecall.IssueToken(p.auth, p.target, nil, "")
	if err != nil {
		return fmt.Errorf("issue service token: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.origin+"/api/v1/internal/infra/servers", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(servicecall.HeaderServiceAuth, token)

	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("publish tenant %q: %w", tenantID, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("publish tenant %q: http_%d %s", tenantID, response.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return nil
}
