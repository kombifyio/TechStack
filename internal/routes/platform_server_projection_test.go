package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func TestProjectServerForPlatformCarriesWhatTheProductsRead(t *testing.T) {
	retired := time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC)
	server := controlplane.ServerRuntime{
		ID:               "srv-1",
		TenantID:         "usr:google-oauth2|1",
		OwnerSubjectID:   "google-oauth2|1",
		StackID:          "stack-1",
		Name:             "alpha",
		LifecycleState:   "running",
		ConnectionState:  "connected",
		HealthState:      "healthy",
		Revision:         42,
		UpdatedAt:        time.Date(2026, 9, 18, 7, 0, 0, 0, time.UTC),
		DecommissionedAt: &retired,
		Metadata: map[string]any{
			"provider_id": "hetzner",
			"deployment": map[string]any{
				"stackkit":         "media-home",
				"stackkit_version": "1.4.0",
			},
		},
	}

	got := projectServerForPlatform(server, []string{"jellyfin", "nextcloud"})

	if got.ServerID != "srv-1" || got.TenantID != "usr:google-oauth2|1" || got.OwnerID != "google-oauth2|1" {
		t.Fatalf("identity was not carried: %#v", got)
	}
	if got.Provider != "hetzner" {
		t.Fatalf("provider label: %q", got.Provider)
	}
	// StackKit name and version come from the deployment block when the top
	// level does not carry them — the same fallback the inventory API uses.
	if got.StackkitName != "media-home" || got.StackkitVersion != "1.4.0" {
		t.Fatalf("stackkit labels: %q %q", got.StackkitName, got.StackkitVersion)
	}
	if got.AggregateRevision != 42 {
		t.Fatalf("the revision fence was not carried: %d", got.AggregateRevision)
	}
	if !got.Retired {
		t.Fatal("a decommissioned server must be published as retired, not dropped")
	}
	if got.ObservedAt == "" {
		t.Fatal("observed_at is what the reader orders by")
	}
	// The applications a surface shows travel with the row; without them every
	// surface goes back to asking this service directly.
	if got.PlatformOS != "" {
		t.Fatalf("no host os was set, so none should be published: %q", got.PlatformOS)
	}
	if len(got.ServiceNames) != 2 || got.ServiceCount != 2 {
		t.Fatalf("service names/count: %#v %d", got.ServiceNames, got.ServiceCount)
	}
}

func TestProjectServerForPlatformBoundsTheServiceNamesItCarries(t *testing.T) {
	many := make([]string, 12)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	got := projectServerForPlatform(controlplane.ServerRuntime{
		ID: "srv-1", TenantID: "t", Metadata: map[string]any{"host": map[string]any{"os": "debian"}},
	}, many)
	if len(got.ServiceNames) != maxProjectedServiceNames {
		t.Fatalf("names were not bounded: %d", len(got.ServiceNames))
	}
	// The count still tells the truth about the rest.
	if got.ServiceCount != 12 {
		t.Fatalf("the count must not be truncated with the names: %d", got.ServiceCount)
	}
	if got.PlatformOS != "debian" {
		t.Fatalf("host os: %q", got.PlatformOS)
	}
}

func TestNewPlatformPublisherIsOptionalButNeverHalfConfigured(t *testing.T) {
	// No origin is the self-hosted default: no publisher, no error.
	publisher, err := NewPlatformPublisher(PlatformPublisherConfig{})
	if err != nil || publisher != nil {
		t.Fatalf("an unconfigured projection must be a silent no-op: %v %v", publisher, err)
	}
	// An origin without a secret is a misconfiguration and must be loud.
	if _, err := NewPlatformPublisher(PlatformPublisherConfig{
		CloudOrigin: "https://kombify.io",
	}); err == nil {
		t.Fatal("an origin without a signing secret must not be accepted")
	}
}

func TestPlatformPublisherSignsAndSendsTheBatch(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	var gotToken string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(servicecall.HeaderServiceAuth)
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"applied":1}`))
	}))
	defer server.Close()

	publisher, err := NewPlatformPublisher(PlatformPublisherConfig{
		CloudOrigin: server.URL,
		Secret:      secret,
		Client:      server.Client(),
	})
	if err != nil || publisher == nil {
		t.Fatalf("NewPlatformPublisher: %v %v", publisher, err)
	}
	err = publisher.PublishServers(context.Background(), "tenant-a", []serverregistry.ProjectedServer{
		{ServerID: "srv-1", TenantID: "tenant-a", AggregateRevision: 3},
	})
	if err != nil {
		t.Fatalf("PublishServers: %v", err)
	}
	if gotToken == "" {
		t.Fatal("the batch went out unsigned")
	}
	servers, _ := gotBody["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
	// The receiver records the pass against this tenant, including when the
	// row list is empty, so it must travel in the envelope.
	if gotBody["tenant_id"] != "tenant-a" {
		t.Fatalf("the tenant did not travel in the envelope: %#v", gotBody["tenant_id"])
	}
}

func TestPlatformPublisherStillSpeaksForATenantWithNoServers(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	publisher, _ := NewPlatformPublisher(PlatformPublisherConfig{
		CloudOrigin: server.URL,
		Secret:      "0123456789abcdef0123456789abcdef",
		Client:      server.Client(),
	})
	if err := publisher.PublishServers(context.Background(), "tenant-empty", nil); err != nil {
		t.Fatalf("PublishServers: %v", err)
	}
	if gotBody == nil {
		t.Fatal("an empty tenant must still be published, or it can never be told apart from an unprojected one")
	}
	if gotBody["tenant_id"] != "tenant-empty" {
		t.Fatalf("tenant: %#v", gotBody["tenant_id"])
	}
	if servers, ok := gotBody["servers"].([]any); !ok || len(servers) != 0 {
		t.Fatalf("servers: %#v", gotBody["servers"])
	}
}

func TestPlatformPublisherReportsARefusedBatch(t *testing.T) {
	// A refusal must surface: the projector counts it and retries next pass,
	// which only works if the error is not swallowed here.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"service_auth_invalid"}`))
	}))
	defer server.Close()

	publisher, _ := NewPlatformPublisher(PlatformPublisherConfig{
		CloudOrigin: server.URL,
		Secret:      "0123456789abcdef0123456789abcdef",
		Client:      server.Client(),
	})
	err := publisher.PublishServers(context.Background(), "tenant-a", []serverregistry.ProjectedServer{
		{ServerID: "srv-1", TenantID: "tenant-a"},
	})
	if err == nil {
		t.Fatal("a 401 from the platform must not look like a successful publish")
	}
}
