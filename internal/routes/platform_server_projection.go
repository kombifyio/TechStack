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
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
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

// maxProjectedServiceNames bounds what travels: a surface shows a handful of
// application chips, and the count carries the rest.
const maxProjectedServiceNames = 8

func projectServerForPlatform(server controlplane.ServerRuntime, serviceNames []string) serverregistry.ProjectedServer {
	metadata := safeAnyMap(server.Metadata)
	deployment := safeAnyMap(metadata["deployment"])
	host := safeAnyMap(metadata["host"])
	names := serviceNames
	if len(names) > maxProjectedServiceNames {
		names = names[:maxProjectedServiceNames]
	}
	observed := server.UpdatedAt
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	return serverregistry.ProjectedServer{
		ServerID: strings.TrimSpace(server.ID),
		TenantID: strings.TrimSpace(server.TenantID),
		// The platform list is owner-scoped; a server with no owner subject is
		// still projected so operators see it, but no product read will match it.
		OwnerID:  strings.TrimSpace(server.OwnerSubjectID),
		Name:     safeLabel(server.Name, 96),
		Provider: safeProviderLabel(firstNonEmptyString(stringFromAnyMap(metadata, "provider_id"), stringFromAnyMap(metadata, "provider"))),
		StackID:  strings.TrimSpace(server.StackID),
		StackkitName: safeLabel(firstNonEmptyString(
			stringFromAnyMap(metadata, "stackkit"),
			stringFromAnyMap(deployment, "stackkit"),
		), 96),
		StackkitVersion: safeLabel(firstNonEmptyString(
			stringFromAnyMap(metadata, "stackkit_version"),
			stringFromAnyMap(deployment, "stackkit_version"),
		), 96),
		LifecycleState:  strings.TrimSpace(server.LifecycleState),
		ConnectionState: strings.TrimSpace(server.ConnectionState),
		HealthState:     strings.TrimSpace(server.HealthState),
		PlatformOS: safeLabel(firstNonEmptyString(
			stringFromAnyMap(host, "os"),
			stringFromAnyMap(metadata, "os"),
		), 96),
		ServiceNames: names,
		ServiceCount: len(serviceNames),
		// Revision is the aggregate's own fence; the receiver refuses anything
		// older, so a slow pass can never overwrite a newer state.
		AggregateRevision: server.Revision,
		Retired:           server.DecommissionedAt != nil,
		ObservedAt:        observed.UTC().Format(time.RFC3339Nano),
	}
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
	origin  string
	service string
	target  string
	secret  string
	client  *http.Client
}

// NewPlatformPublisher returns nil when the projection is not configured, which
// is the self-hosted and local default: no origin, no publishing, no error.
func NewPlatformPublisher(cfg PlatformPublisherConfig) (serverregistry.PlatformPublisher, error) {
	origin := strings.TrimRight(strings.TrimSpace(cfg.CloudOrigin), "/")
	if origin == "" {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, fmt.Errorf("routes: platform projection requires a service auth secret")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &platformHTTPPublisher{
		origin:  origin,
		service: firstNonEmptyString(strings.TrimSpace(cfg.ServiceName), "techstack"),
		target:  firstNonEmptyString(strings.TrimSpace(cfg.TargetName), "cloud"),
		secret:  strings.TrimSpace(cfg.Secret),
		client:  client,
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
	token, err := servicecall.IssueToken(servicecall.Config{
		ServiceName: p.service,
		Target:      p.target,
		Secret:      p.secret,
	}, p.target, nil, "")
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
