package routes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

type recordingPortObservationWriter struct {
	observation portinventory.GuardObservation
}

func (writer *recordingPortObservationWriter) RecordGuardPorts(_ context.Context, observation portinventory.GuardObservation) error {
	writer.observation = observation
	return nil
}

func TestWorkerInventoryKeepsServicesVisibleWhenGuardClockLags(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)
	observedAt := time.Now().UTC().Add(-2 * time.Minute)
	body := strings.Replace(`{
		"source_epoch":"epoch-lag",
		"source_sequence":1,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"discovery_observed":true,
		"discovered_service_count":1,
		"host":{"hostname":"node-1","os":"ubuntu","arch":"amd64"},
		"services":[
			{"service_id":"docker/vaultwarden","key":"docker/vaultwarden","name":"vaultwarden","status":"running","source":"observed","platform_type":"docker","instance":"default"}
		]
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	postGuardInventory(t, store, token, body)

	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	if server.LastHeartbeatAt == nil || !server.LastHeartbeatAt.After(observedAt.Add(time.Minute)) {
		t.Fatalf("inventory persisted Guard clock as LastHeartbeatAt: heartbeat=%v observed_at=%v", server.LastHeartbeatAt, observedAt)
	}
	if server.ConnectionState != string(serverregistry.ConnectionConnected) {
		t.Fatalf("lagging Guard inventory connection = %s", server.ConnectionState)
	}

	sweepAt := server.LastHeartbeatAt.Add(30 * time.Second)
	store.SetNow(func() time.Time { return sweepAt })
	sweeper, sweeperErr := serverregistry.NewSweeper(serverregistry.SweeperConfig{
		Registry: store,
		Outbox:   store,
		Now:      func() time.Time { return sweepAt },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if sweeperErr != nil {
		t.Fatalf("NewSweeper: %v", sweeperErr)
	}
	result, sweepErr := sweeper.SweepOnce(t.Context())
	if sweepErr != nil {
		t.Fatalf("SweepOnce: %v", sweepErr)
	}
	if result.Demotions != 0 {
		t.Fatalf("receipt-fresh inventory was demoted: %#v", result)
	}

	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, now: func() time.Time { return sweepAt }}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services", "owner-1", "tenant-1", nil)
	if listErr := h.list(event); listErr != nil {
		t.Fatalf("list services: %v", listErr)
	}
	var envelope struct {
		Data []serviceRuntimeResponse `json:"data"`
	}
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("decode services: %v", decodeErr)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("services = %#v, want the discovered row", envelope.Data)
	}
	got := envelope.Data[0]
	if got.ObservedState != "running" || got.Health.State == monitoringStatusUnknown ||
		strings.HasPrefix(got.Health.ReasonCode, "server_connection_") {
		t.Fatalf("lagging Guard inventory was masked: %#v", got)
	}
}

func TestWorkerInventoryRejectsObservedAtTooFarInThePast(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)
	observedAt := time.Now().UTC().Add(-guardObservationSkewWindow - time.Second)
	body := strings.Replace(`{
		"source_epoch":"epoch-lag",
		"source_sequence":1,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"services":[{"service_id":"docker/app","key":"docker/app","status":"running","source":"observed","instance":"default"}]
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	handler := workerRouteHandlers{wst: store, registryStore: store, serverStore: store, rilStore: store, metricWriter: &fakeWorkerMetricWriter{}}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", body)
	event.Request.SetPathValue("id", "runtime-1")
	event.Request.Header.Set("Authorization", "Bearer "+token)
	event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(event); err != nil {
		t.Fatalf("inventory returned router error: %v", err)
	}
	if recorder.Code == http.StatusOK {
		t.Fatalf("past-skew inventory was accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkerInventoryBindsPortEvidenceToAuthenticatedGuardPosition(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)
	writer := &recordingPortObservationWriter{}
	observedAt := time.Now().UTC().Add(-2 * time.Second)
	body := strings.Replace(`{
		"source_epoch":"epoch-ports",
		"source_sequence":7,
		"observed_at":"{{observed_at}}",
		"server_id":"attacker-supplied-server",
		"runtime_agent_id":"runtime-1",
		"ports_observed":true,
		"open_ports":["tcp://0.0.0.0:443"],
		"host":{"hostname":"node-1","os":"linux","arch":"amd64"}
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	handler := workerRouteHandlers{
		wst: store, registryStore: store, serverStore: store, rilStore: store,
		metricWriter: &fakeWorkerMetricWriter{}, portObservations: writer,
	}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", body)
	event.Request.SetPathValue("id", "runtime-1")
	event.Request.Header.Set("Authorization", "Bearer "+token)
	event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(event); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("inventory = status %d body=%s error=%v", recorder.Code, recorder.Body.String(), err)
	}
	got := writer.observation
	if got.TenantID != "tenant-1" || got.RuntimeAgentID != "runtime-1" || got.SourceEpoch != "epoch-ports" ||
		got.SourceSequence != 7 || got.InventoryRevision < 1 || !got.ListenersComplete ||
		len(got.OpenPorts) != 1 || got.OpenPorts[0] != "tcp://0.0.0.0:443" {
		t.Fatalf("authenticated port observation = %#v", got)
	}
}

// enrolledInventoryAgent connects one server and returns the agent token its
// Guard uses to publish inventory.
func enrolledInventoryAgent(t *testing.T, store *controlplane.MemoryStore) string {
	t.Helper()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack one",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	connectEvent, connectRecorder := workerRouteTestEvent(
		http.MethodPost,
		"/v1/ril/servers/connect",
		`{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1","hostname":"node-1","mode":"advanced","connection_mode":"managed"}`,
	)
	connectEvent.Request = connectEvent.Request.WithContext(
		identity.NewContext(connectEvent.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}),
	)
	handler := workerRouteHandlers{wst: store, registryStore: store, serverStore: store, rilStore: store, metricWriter: &fakeWorkerMetricWriter{}}
	if err := handler.connectServer(connectEvent); err != nil {
		t.Fatalf("connectServer returned router error: %v", err)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(connectRecorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode connect response: %v", err)
	}
	token, _ := envelope.Data["agent_token"].(string)
	if token == "" {
		t.Fatalf("connect response missing agent_token: %#v", envelope.Data)
	}
	return token
}

func postGuardInventory(t *testing.T, store *controlplane.MemoryStore, token, body string) int {
	t.Helper()
	handler := workerRouteHandlers{wst: store, registryStore: store, serverStore: store, rilStore: store, metricWriter: &fakeWorkerMetricWriter{}}
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", body)
	event.Request.SetPathValue("id", "runtime-1")
	event.Request.Header.Set("Authorization", "Bearer "+token)
	event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(event); err != nil {
		t.Fatalf("inventory returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("inventory status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	return recorder.Code
}

// The end-to-end shape of the empty-services bug: a host with no StackKit
// publishes the containers and units it is actually running, and they must
// land as `observed` service rows that no reader mistakes for services we
// declared.
func TestWorkerInventoryProjectsDiscoveredServicesAsObserved(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)

	observedAt := time.Now().UTC().Add(-2 * time.Second)
	// manifest_observed is false: this host runs no StackKit at all, which is
	// exactly the case that used to yield zero rows platform-wide.
	body := strings.Replace(`{
		"source_epoch":"epoch-a",
		"source_sequence":1,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"manifest_observed":false,
		"discovery_observed":true,
		"discovered_service_count":2,
		"host":{"hostname":"node-1","os":"ubuntu","os_version":"24.04","arch":"amd64","public_ip":"203.0.113.10","cpu_cores":4,"ram_mb":8192,"disk_gb":120},
		"services":[
			{"service_id":"docker/vaultwarden","key":"docker/vaultwarden","name":"vaultwarden","status":"running","source":"observed","platform_type":"docker","platform_id":"c0ffee","container_id":"c0ffee","image":"vaultwarden/server:1.30","instance":"default","health":{"source":"docker-ps","docker_health":"healthy"}},
			{"service_id":"systemd/techstack-agent","key":"systemd/techstack-agent","name":"techstack-agent.service","status":"running","source":"observed","platform_type":"systemd","platform_id":"techstack-agent.service","instance":"default","health":{"source":"systemctl-list-units"}}
		]
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	postGuardInventory(t, store, token, body)

	services, err := store.ListServicesByStack(t.Context(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("services = %d, want the two discovered services: %#v", len(services), services)
	}
	byKey := map[string]controlplane.Service{}
	for _, service := range services {
		byKey[service.ServiceKey] = service
	}
	container, present := byKey["docker/vaultwarden"]
	if !present {
		t.Fatalf("discovered container is not a service row: %#v", services)
	}
	if container.Source != "observed" || container.ManagementState != "observed" {
		t.Fatalf("discovered service is not owned as observed: source=%q management_state=%q", container.Source, container.ManagementState)
	}
	if container.Status != "running" {
		t.Fatalf("legacy status projection = %q, want running", container.Status)
	}
	if container.Metadata["platform_type"] != "docker" || container.Metadata["container_id"] != "c0ffee" ||
		container.Metadata["image"] != "vaultwarden/server:1.30" {
		t.Fatalf("container identity not projected: %#v", container.Metadata)
	}

	runtimeRow, err := store.GetServiceRuntime(t.Context(), "tenant-1", container.ID)
	if err != nil {
		t.Fatalf("GetServiceRuntime: %v", err)
	}
	if runtimeRow.ObservedState != "running" || runtimeRow.HealthState != "healthy" {
		t.Fatalf("measured dimensions = observed:%q health:%q", runtimeRow.ObservedState, runtimeRow.HealthState)
	}
	if runtimeRow.ManagementState != "observed" || runtimeRow.Source != "observed" {
		t.Fatalf("runtime ownership = %#v", runtimeRow)
	}

	// normalizeServiceKey folds dashes to underscores for every service key, so
	// the unit `techstack-agent.service` is stored as systemd/techstack_agent.
	unit, present := byKey["systemd/techstack_agent"]
	if !present {
		t.Fatalf("discovered unit is not a service row: %#v", services)
	}
	if unit.Name != "techstack-agent.service" || unit.Metadata["platform_type"] != "systemd" {
		t.Fatalf("unit identity not projected: name=%q metadata=%#v", unit.Name, unit.Metadata)
	}
	unitRuntime, err := store.GetServiceRuntime(t.Context(), "tenant-1", unit.ID)
	if err != nil {
		t.Fatalf("GetServiceRuntime(unit): %v", err)
	}
	// A running unit with no health evidence must not be reported as healthy.
	if unitRuntime.ObservedState != "running" || unitRuntime.HealthState == "healthy" {
		t.Fatalf("a unit without health evidence was projected healthy: %#v", unitRuntime)
	}

	// The row the Guard produced is the one the aggregate refuses a declared
	// target state for, with reason code
	// `desired_state_not_applicable_for_observed_service`. That refusal is
	// proven in TestPrepareServiceEventRejectsDesiredStateForObservedServices;
	// what matters here is that ingest lands the row on the side of the rule
	// that triggers it.
	if serviceregistry.DesiredStateApplicable(serviceregistry.ManagementState(runtimeRow.ManagementState)) {
		t.Fatalf("a discovered service was given a declared contract: %#v", runtimeRow)
	}

	node, err := store.GetNode(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node.Metadata["discovery_observed"] != true {
		t.Fatalf("discovery evidence not projected onto the node: %#v", node.Metadata)
	}
}

// One Guard observation legitimately carries both halves of the model. The
// batch must not flatten a StackKit service into an observed one, nor the
// reverse.
func TestWorkerInventoryKeepsManagedAndObservedProvenanceApartInOneBatch(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)

	observedAt := time.Now().UTC().Add(-2 * time.Second)
	body := strings.Replace(`{
		"source_epoch":"epoch-a",
		"source_sequence":1,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"manifest_observed":true,
		"manifest_service_count":1,
		"discovery_observed":true,
		"discovered_service_count":1,
		"host":{"hostname":"node-1","os":"ubuntu","arch":"amd64"},
		"services":[
			{"service_id":"vaultwarden","key":"vaultwarden","name":"Vaultwarden","status":"healthy","source":"stackkits-inventory","desired_state":"running","instance":"default","endpoints":[{"url":"https://vault.example.com","visibility":"public","provenance":"stackkit-access-manifest","health":"healthy","target_type":"direct"}]},
			{"service_id":"docker/watchtower","key":"docker/watchtower","name":"watchtower","status":"running","source":"observed","platform_type":"docker","container_id":"beef","instance":"default"}
		]
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	postGuardInventory(t, store, token, body)

	services, err := store.ListServicesByStack(t.Context(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("services = %d, want both halves: %#v", len(services), services)
	}
	ownership := map[string]string{}
	for _, service := range services {
		runtimeRow, runtimeErr := store.GetServiceRuntime(t.Context(), "tenant-1", service.ID)
		if runtimeErr != nil {
			t.Fatalf("GetServiceRuntime(%s): %v", service.ServiceKey, runtimeErr)
		}
		if runtimeRow.Source != service.Source {
			t.Fatalf("%s provenance disagrees across projections: legacy=%q runtime=%q", service.ServiceKey, service.Source, runtimeRow.Source)
		}
		ownership[service.ServiceKey] = runtimeRow.ManagementState
	}
	if ownership["vaultwarden"] != "managed" {
		t.Fatalf("StackKit service ownership = %q, want managed: %#v", ownership["vaultwarden"], ownership)
	}
	if ownership["docker/watchtower"] != "observed" {
		t.Fatalf("discovered service ownership = %q, want observed: %#v", ownership["docker/watchtower"], ownership)
	}
}

// A provenance the vocabulary cannot name proves nothing about a declared
// contract, so it must fall closed to `observed` rather than be read as one of
// our rollout pipelines. The StackKits evidence-provenance vocabulary is the
// realistic confusion: it is a different axis and must never buy ownership.
func TestWorkerInventoryFoldsAnUnknownServiceProvenanceToObserved(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	token := enrolledInventoryAgent(t, store)

	observedAt := time.Now().UTC().Add(-2 * time.Second)
	body := strings.Replace(`{
		"source_epoch":"epoch-a",
		"source_sequence":1,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"host":{"hostname":"node-1","os":"ubuntu","arch":"amd64"},
		"services":[{"service_id":"docker/app","key":"docker/app","status":"running","source":"verified-apply-evidence","instance":"default"}]
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	postGuardInventory(t, store, token, body)

	services, err := store.ListServicesByStack(t.Context(), "tenant-1", "stack-1")
	if err != nil || len(services) != 1 {
		t.Fatalf("services = %#v err=%v, want one row", services, err)
	}
	if services[0].Source != "observed" || services[0].ManagementState != "observed" {
		t.Fatalf("unknown provenance bought ownership: source=%q management_state=%q",
			services[0].Source, services[0].ManagementState)
	}
}
