package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServiceCardsShareOnlyEligibleRequestAdmissionAndExecuteAfresh(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx, now := t.Context(), time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackKitInstanceID: "home-main", Config: map[string]any{"stackkit": "basement-kit"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", InventoryRevision: 7, ConnectionState: "connected"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []controlplane.ServiceRuntime{
		{ID: "managed", ServiceKey: "a-managed", ManagementState: "managed", Metadata: map[string]any{"container_id": strings.Repeat("a", 64)}},
		{ID: "unsupported", ServiceKey: "b-unsupported", ManagementState: "observed", Metadata: map[string]any{"container_id": "not-a-retained-container"}},
		{ID: "observed-1", ServiceKey: "c-observed", ManagementState: "observed", Metadata: map[string]any{"container_id": strings.Repeat("c", 64)}},
		{ID: "observed-2", ServiceKey: "d-observed", ManagementState: "observed", Metadata: map[string]any{"container_id": strings.Repeat("d", 64)}},
	} {
		row.TenantID, row.StackID, row.ServerID = "tenant-1", "stack-1", "server-1"
		row.Source, row.ObservedState, row.ObservedAt = "observed", "running", &now
		row.Metadata["inventory_revision"] = int64(7)
		if _, err := store.UpsertServiceRuntime(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	pin := &agentpb.StackKitReleasePin{Version: "v0.50.9", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)}
	admissionAvailable := false
	orch := &recordingServiceActionOrchestrator{store: store}
	h := serviceRuntimeHandlers{policy: NewSelfHostedInventoryPolicy(), store: store, stacks: store, servers: store, jobs: store, orch: orch, now: func() time.Time { return now }, applicationAdmission: func() (*agentpb.StackKitReleasePin, error) {
		if !admissionAvailable {
			return nil, errors.New("signed application release is unavailable")
		}
		// One current admission may be shared by this projection; another
		// evaluation observes a changed release and must not use that result.
		admissionAvailable = false
		return pin, nil
	}}
	read := func(path string, handler func(*httpx.Event) error, available bool) {
		t.Helper()
		e, w := registryRouteStoreTestEvent(http.MethodGet, path, "owner-1", "tenant-1", nil)
		if err := handler(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("service projection failed: %d", w.Code)
		}
		var envelope struct{ Data json.RawMessage }
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var cards []serviceRuntimeResponse
		if strings.HasPrefix(path, "/api/v1/service-applications") {
			var applications []serviceApplicationResponse
			if err := json.Unmarshal(envelope.Data, &applications); err != nil {
				t.Fatal(err)
			}
			for _, application := range applications {
				cards = append(cards, application.Components...)
			}
		} else if err := json.Unmarshal(envelope.Data, &cards); err != nil {
			t.Fatal(err)
		}
		byID := make(map[string]serviceRuntimeResponse)
		for _, card := range cards {
			byID[card.ID] = card
		}
		for _, id := range []string{"observed-1", "observed-2"} {
			card := byID[id]
			if card.ID != id || card.Adoption.PreviewAvailable != available || card.Adoption.ExecutionAvailable != available || containsString(card.AllowedActions, serviceActionAdopt) != available {
				t.Fatalf("eligible service did not retain this request's admission: %#v", card)
			}
		}
		for _, id := range []string{"managed", "unsupported"} {
			card := byID[id]
			if card.Adoption.PreviewAvailable || card.Adoption.ExecutionAvailable || card.Adoption.ReleaseAvailable || containsString(card.AllowedActions, serviceActionAdopt) {
				t.Fatalf("ineligible service was offered adoption: %#v", card)
			}
		}
	}
	for _, route := range []struct {
		path    string
		handler func(*httpx.Event) error
	}{{"/api/v1/services", h.list}, {"/api/v1/services?kit_deployment_id=stack-1", h.list}, {"/api/v1/service-applications", h.listApplications}} {
		admissionAvailable = true
		read(route.path, route.handler, true)
		read(route.path, route.handler, false)
	}

	// A preceding successful read never admits a later lifecycle command.
	admissionAvailable = true
	read("/api/v1/services", h.list, true)
	e, w := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/services/observed-1/adoption-plans", "owner-1", "tenant-1", map[string]any{"mode": "adopt_in_place", "expected_inventory_revision": 7})
	e.Request.SetPathValue("serviceId", "observed-1")
	e.Request.Header.Set("Idempotency-Key", "fresh-admission")
	if err := h.prepareAdoption(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatal(err)
	}
	if w.Code != http.StatusServiceUnavailable || len(orch.requests) != 0 {
		t.Fatal("stale read admission reached lifecycle execution")
	}
}

func TestApplicationAdoptionRequiresExactRetainedOwnerApprovedSource(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	ctx := t.Context()
	container := strings.Repeat("a", 64)
	_, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackKitInstanceID: "home-main", Config: map[string]any{"stackkit": "basement-kit"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", InventoryRevision: 7, ConnectionState: "connected"})
	if err != nil {
		t.Fatal(err)
	}
	service := controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "docker/ha", ManagementState: "observed", Source: "observed", ObservedState: "running", ObservedAt: &now, Metadata: map[string]any{"container_id": container, "inventory_revision": int64(7)}}
	_, err = store.UpsertServiceRuntime(ctx, service)
	if err != nil {
		t.Fatal(err)
	}
	orch := &recordingServiceActionOrchestrator{store: store}
	pin := &agentpb.StackKitReleasePin{Version: "v0.50.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)}
	h := serviceRuntimeHandlers{policy: NewSelfHostedInventoryPolicy(), store: store, stacks: store, servers: store, jobs: store, orch: orch, now: func() time.Time { return now }, applicationAdmission: func() (*agentpb.StackKitReleasePin, error) { return pin, nil }}
	signedOperate := false
	call := func(handler func(*httpx.Event) error, owner, key, plan string, body map[string]any) int {
		e, w := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/services/service-1/adoption-plans", owner, "tenant-1", body)
		if signedOperate {
			e.Request = e.Request.WithContext(middleware.WithSignedEntitlements(e.Request.Context(), InventoryEntitlementOperate))
		}
		e.Request.SetPathValue("serviceId", "service-1")
		e.Request.SetPathValue("planId", plan)
		e.Request.Header.Set("Idempotency-Key", key)
		if err := handler(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
			t.Fatal(err)
		}
		return w.Code
	}
	body := map[string]any{"mode": "adopt_in_place", "expected_inventory_revision": 7}
	if code := call(h.prepareAdoption, "foreign-owner", "inspect", "", body); code != http.StatusNotFound {
		t.Fatalf("foreign Owner admitted: %d", code)
	}
	if code := call(h.prepareAdoption, "owner-1", "inspect", "", body); code != http.StatusAccepted {
		t.Fatalf("prepare status=%d", code)
	}
	inspect := orch.requests[0]
	if inspect.OwnerApproved || inspect.Application.ContainerID != container {
		t.Fatalf("source assessment changed authority: %#v", inspect)
	}
	evidence := stackkitcommand.ApplicationEvidence{APIVersion: "stackkit.application-adoption-plan/v1", ProfileRef: stackkitcommand.HomeAssistantAdoptionProfile, PlanDigest: "sha256:" + strings.Repeat("c", 64)}
	_, err = store.UpsertJob(ctx, controlplane.UpsertJobRequest{ID: inspect.DurableJobID, TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), State: "completed", Payload: jobs.StackKitLifecyclePayload(inspect), Result: map[string]any{"service_action_receipt": jobs.StackKitServiceActionReceipt(inspect), "application_binding": inspect.Application, "application_evidence": stackkitcommand.ApplicationEvidenceMap(evidence)}})
	if err != nil {
		t.Fatal(err)
	}
	body = map[string]any{"mode": "adopt_in_place", "expected_inventory_revision": 7, "plan_digest": evidence.PlanDigest, "owner_approved": false}
	if code := call(h.executeAdoption, "owner-1", "execute", inspect.DurableJobID, body); code != http.StatusBadRequest {
		t.Fatalf("unapproved execute admitted: %d", code)
	}
	body["owner_approved"] = true
	body["plan_digest"] = "sha256:" + strings.Repeat("d", 64)
	if code := call(h.executeAdoption, "owner-1", "execute", inspect.DurableJobID, body); code != http.StatusConflict {
		t.Fatalf("changed digest admitted: %d", code)
	}
	body["plan_digest"] = evidence.PlanDigest
	checker := &fakeInventoryRelationshipChecker{allowed: true}
	h.policy = NewInventoryFGAPolicy(checker)
	beforeDenied := orch.requests[len(orch.requests)-1].DurableJobID
	if code := call(h.executeAdoption, "owner-1", "execute", inspect.DurableJobID, body); code != http.StatusForbidden {
		t.Fatalf("missing operate authority admitted: %d", code)
	}
	signedOperate = true
	checker.allowed = false
	if code := call(h.executeAdoption, "owner-1", "execute", inspect.DurableJobID, body); code != http.StatusForbidden {
		t.Fatalf("denied server relationship admitted: %d", code)
	}
	if orch.requests[len(orch.requests)-1].DurableJobID != beforeDenied {
		t.Fatal("denied adoption enqueued a host mutation")
	}
	checker.allowed = true
	if code := call(h.executeAdoption, "owner-1", "execute", inspect.DurableJobID, body); code != http.StatusAccepted {
		t.Fatalf("approved execute status=%d", code)
	}
	queued := orch.requests[1]
	if queued.Application.PlanJobID != inspect.DurableJobID || queued.Application.ReviewedPlan.PlanDigest != evidence.PlanDigest {
		t.Fatalf("execute did not retain source plan: %#v", queued)
	}
	got, _ := store.GetServiceRuntime(ctx, "tenant-1", service.ID)
	if got.ManagementState != "observed" {
		t.Fatal("queued adoption prematurely claimed managed ownership")
	}
	service.Metadata["container_id"] = strings.Repeat("e", 64)
	_, err = store.UpsertServiceRuntime(ctx, service)
	if err != nil {
		t.Fatal(err)
	}
	if code := call(h.executeAdoption, "owner-1", "another-execute", inspect.DurableJobID, body); code != http.StatusConflict {
		t.Fatalf("replacement source admitted: %d", code)
	}
}

// Public boundary: source-preview cannot dispatch, client-supplied custody is
// rejected, and a qualified request retains only canonical source metadata.
func TestApplicationConnectorServiceBoundaryRequiresPublishedOwnedSource(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	now := time.Now().UTC()
	_, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackKitInstanceID: "home-main", Config: map[string]any{"stackkit": "basement-kit"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", NodeID: "node-1", Generation: 1, InventoryRevision: 7, ConnectionState: "connected"})
	if err != nil {
		t.Fatal(err)
	}
	container := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("b", 64)
	_, err = store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "smart-home", Source: serviceregistry.SourceStackKitsInventory, ManagementState: "managed", ObservedState: "running", ObservedAt: &now, Metadata: map[string]any{"container_id": container, "image": "ghcr.io/home-assistant/home-assistant@" + image, "inventory_revision": int64(7)}})
	if err != nil {
		t.Fatal(err)
	}
	orch := &recordingServiceActionOrchestrator{store: store}
	available := false
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, orch: orch, policy: NewSelfHostedInventoryPolicy(), now: func() time.Time { return now }, connectorAdmission: func() (*agentpb.StackKitReleasePin, error) {
		if !available {
			return nil, errors.New("current pin has no connector catalog")
		}
		return &agentpb.StackKitReleasePin{Version: "v0.51.8", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)}, nil
	}}
	call := func(owner, key string, body map[string]any) int {
		e, w := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/services/service-1/connector", owner, "tenant-1", body)
		e.Request = e.Request.WithContext(middleware.WithSignedEntitlements(e.Request.Context(), InventoryEntitlementOperate))
		e.Request.SetPathValue("serviceId", "service-1")
		e.Request.Header.Set("Idempotency-Key", key)
		if err := h.setupConnector(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
			t.Fatal(err)
		}
		return w.Code
	}
	readAvailability := func(preview bool) {
		t.Helper()
		e, w := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/service-1/connector", "owner-1", "tenant-1", nil)
		e.Request.SetPathValue("serviceId", "service-1")
		if err := h.readConnector(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
			t.Fatal(err)
		}
		var response struct {
			Data struct {
				SourcePreview bool `json:"source_preview"`
				Dispatchable  bool `json:"dispatchable"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || response.Data.SourcePreview != preview || response.Data.Dispatchable {
			t.Fatal("unqualified node observation was admitted or confused with an unavailable supporting source")
		}
	}
	readAvailability(true)
	body := map[string]any{"action": "ensure", "lifespan_days": 30, "owner_approved": true, "expected_inventory_revision": 7}
	if status := call("owner-1", "preview", body); status != http.StatusServiceUnavailable || len(orch.requests) != 0 {
		t.Fatal("source-preview reached transport")
	}
	available = true
	readAvailability(false)
	body["token"] = "must-never-transport"
	if status := call("owner-1", "raw-custody", body); status != http.StatusBadRequest || len(orch.requests) != 0 {
		t.Fatal("raw custody reached transport")
	}
	delete(body, "token")
	if status := call("other-owner", "wrong-owner", body); status != http.StatusNotFound || len(orch.requests) != 0 {
		t.Fatal("foreign service reached transport")
	}
	body["expected_inventory_revision"] = 6
	if status := call("owner-1", "stale", body); status != http.StatusConflict || len(orch.requests) != 0 {
		t.Fatal("stale observation reached transport")
	}
	body["expected_inventory_revision"] = 7
	if status := call("owner-1", "qualified-synthetic", body); status != http.StatusAccepted {
		t.Fatalf("qualified synthetic source refused: %d", status)
	}
	req := orch.requests[len(orch.requests)-1]
	if req.Connector == nil || req.Application != nil || req.Connector.Request.Connector.Binding.ContainerID != container || req.Connector.Request.Connector.Binding.ImageDigest != image || req.Connector.Request.Connector.Binding.ResourceID != "service-1" || req.Connector.NodeID != "node-1" {
		t.Fatal("backend source was not retained exactly")
	}
}

// The public service consumer and durable job use real canonical stores, with
// only the pinned CLI/Guard result and backend FGA decision synthetic. The
// completed setup is immutable while a distinct read refreshes its projection.
type jobStoreWithoutServiceOperationFence struct{ controlplane.JobStore }

func TestCompletedConnectorRefreshPreservesSetupAndFencesWithdrawals(t *testing.T) {
	for _, scenario := range []string{"current", "store-capability", "older-pending", "older-unresolved", "unrelated-service", "automatic", "stale-saved", "lost-ack", "policy-before", "policy-missing", "metadata", "policy-during", "owner-intent", "source", "key", "generation", "revoke", "receipt"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, now := t.Context(), time.Now().UTC()
			store := controlplane.NewMemoryStore()
			pin := connectorRefreshFixturePin(t)
			release, err := jobs.ConfiguredTargetStackKitRelease()
			if err != nil || release == nil {
				t.Fatal(err)
			}
			_, err = store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackKitInstanceID: "home", Status: "running"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.UpdateStackRuntime(ctx, "tenant-1", "stack-1", controlplane.RuntimeUpdate{Status: "running", RuntimeSummary: map[string]any{jobs.StackKitRolloutBindingResultField: jobs.StackKitRolloutBinding{StackKit: "basement-kit", SpecPath: "bound.json"}.Map()}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{ID: "worker-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1", Hostname: "main", Approved: true, Capabilities: map[string]any{"runtime_agent_id": "worker-1", "server_id": "server-1", "node_id": "server-1", "identity_binding": controlplane.IdentityBindingHostKey}})
			if err != nil {
				t.Fatal(err)
			}
			digest := strings.Repeat("a", 64)
			_, err = store.CompareAndSwapWorkerCredential(ctx, controlplane.WorkerCredentialCAS{TenantID: "tenant-1", WorkerID: "worker-1", Next: controlplane.WorkerCredentialState{Generation: 1, TokenSHA256: digest, IdempotencySHA256: digest, RequestSHA256: digest}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.IssueWorkerIdentity(ctx, controlplane.WorkerIdentityIssue{TenantID: "tenant-1", WorkerID: "worker-1", Thumbprint: "synthetic-key", PublicKeyJWK: "synthetic-jwk", IssuedAt: now})
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.ConfirmWorkerIdentity(ctx, "tenant-1", "worker-1", "synthetic-key", now)
			if err != nil {
				t.Fatal(err)
			}
			runtime := controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1", NodeID: "server-1", Generation: 1, InventoryRevision: 7, LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now, Metadata: map[string]any{"identity_binding": controlplane.IdentityBindingHostKey}}
			if _, err = store.UpsertServerRuntime(ctx, runtime); err != nil {
				t.Fatal(err)
			}
			membership := controlplane.Membership{TenantID: "tenant-1", UserID: "owner-1", Status: "active", Metadata: map[string]any{"entitlements": []string{InventoryEntitlementRead}}}
			container, image := strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64)
			_, err = store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{ID: "ha", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "smart-home", Source: serviceregistry.SourceStackKitsInventory, ManagementState: "managed", ObservedState: "running", ObservedAt: &now, Metadata: map[string]any{"container_id": container, "image": "ghcr.io/home-assistant/home-assistant@" + image, "inventory_revision": int64(7)}})
			if err != nil {
				t.Fatal(err)
			}
			head, _ := store.ReadServiceEventHead(ctx, "tenant-1", "ha")
			request := stackkitcommand.ConnectorRequest{SchemaVersion: stackkitcommand.ApplicationConnectorRequestSchema}
			request.Connector.Binding = stackkitcommand.ConnectorSource{ResourceID: "ha", ContainerID: container, ImageDigest: image, ServiceRevision: head.Revision, Node: stackkitcommand.ConnectorNode{SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"}}
			request.Connector.Action, request.Connector.LifespanDays = "ensure", 30
			originalReq := jobs.StackKitLifecycleRequest{TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", StackKitInstanceID: "home", AgentID: "worker-1", NodeID: "server-1", NodeRef: "main", StackKit: "basement-kit", SpecPath: "bound.json", WorkingDirectory: "/opt/stackkit", Operation: jobs.StackKitLifecycleApplicationConnector, OwnerApproved: true, ServiceID: "ha", ServiceKey: "smart-home", DurableJobID: "original-setup", ServiceActionDigest: digest, Connector: &jobs.ApplicationConnectorBinding{ReleasePin: pin, Request: request, ServerID: "server-1", ServerGeneration: 1, WorkerGeneration: 1, NodeID: "server-1", ExpectedIntentRevision: jobs.ApplicationIntentRevision(head.Service)}}
			originalReq, err = jobs.NormalizeStackKitLifecycleRequest(originalReq)
			if err != nil {
				t.Fatal(err)
			}
			sender := &connectorRefreshBoundarySender{t: t, request: request, operation: originalReq.DurableJobID, planHash: "sha256:" + digest}
			baseline := sender.proof("original-read", pin, now.Add(-2*time.Minute))
			var envelope struct {
				Data stackkitcommand.ApplicationConnectorEvidence `json:"data"`
			}
			if err = json.Unmarshal(baseline.CommandResultJson, &envelope); err != nil {
				t.Fatal(err)
			}
			originalResult := map[string]any{"connector_plan_hash": sender.planHash, "connector_result_proof": baseline, "connector_proof_read_only": true, "connector_outcome_unresolved": false, "service_action_receipt": jobs.StackKitServiceActionReceipt(originalReq)}
			_, err = store.CreateJob(ctx, controlplane.UpsertJobRequest{ID: originalReq.DurableJobID, TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), Payload: jobs.StackKitLifecyclePayload(originalReq), Result: originalResult})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.StartJob(ctx, "tenant-1", originalReq.DurableJobID, now); err != nil {
				t.Fatal(err)
			}
			if _, err = store.CompleteJob(ctx, "tenant-1", originalReq.DurableJobID, originalResult, now); err != nil {
				t.Fatal(err)
			}
			metadata := head.Service.Metadata
			metadata["application_connector_job_id"], metadata["application_connector_projection_revision"] = originalReq.DurableJobID, head.Revision+1
			metadata["application_connector"] = map[string]any{"status": "active", "receipt_digest": envelope.Data.ReceiptDigest, "operation_id": originalReq.DurableJobID, "container_id": container, "image_digest": image, "verified_at": now.Add(-2 * time.Minute).Format(time.RFC3339Nano), "server_generation": int64(1), "worker_generation": int64(1), "node_id": "server-1"}
			if _, err = store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "ha", Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: controlplane.ServiceRuntime{Metadata: metadata}}); err != nil {
				t.Fatal(err)
			}
			original, _ := store.GetJob(ctx, "tenant-1", originalReq.DurableJobID)
			originalBytes, _ := json.Marshal(original)
			// More recent unrelated history must never hide an unresolved target.
			if scenario == "older-pending" || scenario == "older-unresolved" || scenario == "unrelated-service" {
				target, state := "ha", "pending"
				result := map[string]any{}
				if scenario == "older-unresolved" {
					state = "failed"
					result["connector_outcome_unresolved"] = true
					result["service_action_receipt"] = map[string]any{"service_id": target}
				}
				if scenario == "unrelated-service" {
					target = "other-service"
				}
				if _, err = store.CreateJob(ctx, controlplane.UpsertJobRequest{ID: "older-operation", TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), State: state, Payload: map[string]any{"service_id": target}, Result: result}); err != nil {
					t.Fatal(err)
				}
				for index := 0; index < 125; index++ {
					if _, err = store.CreateJob(ctx, controlplane.UpsertJobRequest{ID: fmt.Sprintf("unrelated-history-%d", index), TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), State: "completed"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var jobStore controlplane.JobStore = store
			if scenario == "store-capability" {
				jobStore = jobStoreWithoutServiceOperationFence{store}
			}
			orch := orchestrator.New(&orchestrator.Config{Workers: 0, StackStore: store, JobStore: jobStore, WorkerStore: store}, nil)
			t.Cleanup(orch.Stop)
			orch.ConfigureApplicationConnectorRefreshAuthorization(func(ctx context.Context, tenant, owner, server string) error {
				member := membership
				if member.Status != "active" || member.Metadata["entitlements"] == nil {
					return ErrInventoryAccessDenied
				}
				// The FGA fixture admits this exact owned server only.
				if tenant != "tenant-1" || owner != "owner-1" || server != "server-1" {
					return ErrInventoryAccessDenied
				}
				return nil
			})
			withdraw := func() { membership.Status = "inactive" }
			if scenario == "policy-before" {
				withdraw()
			}
			h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, orch: orch, policy: NewSelfHostedInventoryPolicy(), now: time.Now, connectorAdmission: jobs.ApplicationConnectorRelease}
			if scenario == "policy-missing" {
				h.policy = nil
			}
			if scenario == "automatic" {
				orch.ReconcileApplicationConnectors(ctx, "tenant-1", "owner-1", "stack-1", "server-1")
			}
			body := map[string]any{}
			if scenario == "metadata" {
				body["token"] = "must-not-transport"
			}
			e, w := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/services/ha/connector/verify", "owner-1", "tenant-1", body)
			e.Request.SetPathValue("serviceId", "ha")
			if err := h.verifyConnector(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
				t.Fatal(err)
			}
			if scenario == "older-pending" || scenario == "older-unresolved" || scenario == "store-capability" {
				if w.Code != http.StatusConflict {
					t.Fatalf("older unresolved service work admitted refresh: %d %s", w.Code, w.Body.String())
				}
				retained, _ := store.GetJob(ctx, "tenant-1", originalReq.DurableJobID)
				retainedBytes, _ := json.Marshal(retained)
				if !bytes.Equal(originalBytes, retainedBytes) {
					t.Fatal("denial changed original setup")
				}
				return
			}
			if scenario == "metadata" {
				if w.Code != http.StatusBadRequest {
					t.Fatal("caller supplied connector metadata reached consumer")
				}
				return
			}
			if scenario == "policy-missing" {
				if w.Code != http.StatusServiceUnavailable {
					t.Fatal("missing backend policy admitted refresh")
				}
				return
			}
			if scenario == "policy-before" {
				if w.Code != http.StatusConflict {
					t.Fatal("withdrawn backend authority queued refresh")
				}
				return
			}
			if w.Code != http.StatusAccepted {
				t.Fatalf("read consumer rejected fixture: %d %s", w.Code, w.Body.String())
			}
			var response struct {
				Data serviceActionResponse `json:"data"`
			}
			if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			job, ok := orch.Queue().Get(response.Data.JobID)
			if !ok || job.ID == originalReq.DurableJobID {
				t.Fatal("read consumer reused completed mutation")
			}
			queue := orch.Queue()
			persist := func(ctx context.Context, s jobs.JobSnapshot) error {
				_, err := store.SyncJobSnapshot(ctx, controlplane.SyncJobSnapshotRequest{ObservedState: "pending", Job: controlplane.UpsertJobRequest{ID: s.ID, TenantID: "tenant-1", StackID: "stack-1", Type: string(s.Type), State: "pending", Result: s.Result, Payload: s.Payload}})
				return err
			}
			queue.SetExecutionSnapshotSyncer(persist)
			if scenario == "stale-saved" {
				job.Result["connector_dispatch_intent"] = true
				job.Result["connector_result_proof"] = sender.proof("old-read-before-cas", pin, now.Add(-2*time.Minute))
				if err := persist(ctx, job.Snapshot()); err != nil {
					t.Fatal(err)
				}
			}
			sender.persisted = func() bool {
				retained, err := store.GetJob(ctx, "tenant-1", job.ID)
				return err == nil && retained.Result["connector_dispatch_intent"] == true
			}
			sender.beforeResult = func() {
				var err error
				switch scenario {
				case "policy-during":
					withdraw()
				case "owner-intent":
					_, err = store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "ha", Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: controlplane.ServiceRuntime{DesiredState: "stopped"}})
				case "source":
					_, err = store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "ha", Authority: controlplane.ServiceEventAuthorityGuard, Runtime: controlplane.ServiceRuntime{Metadata: map[string]any{"container_id": strings.Repeat("d", 64), "image": "ghcr.io/home-assistant/home-assistant@" + image}}})
				case "key":
					err = store.RevokeWorkerIdentity(ctx, "tenant-1", "worker-1", now)
				case "generation":
					runtime.Generation++
					_, err = store.UpsertServerRuntime(ctx, runtime)
				case "revoke":
					current, _ := store.ReadServiceEventHead(ctx, "tenant-1", "ha")
					current.Service.Metadata["application_connector_job_id"] = "later-revoke"
					_, err = store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "ha", Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: controlplane.ServiceRuntime{Metadata: current.Service.Metadata}})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			sender.loseACK = scenario == "lost-ack"
			sender.changeReceipt = scenario == "receipt"
			handler := jobs.StackKitLifecycleHandler(jobs.StackKitLifecycleConfig{Sender: sender, Services: store, ConnectorAuthorize: orch.AuthorizeApplicationConnectorRead})
			err = handler(ctx, job, queue)
			if scenario == "lost-ack" {
				if err == nil {
					t.Fatal("unknown read ACK released fence")
				}
				err = handler(ctx, job, queue)
			}
			after, _ := store.ReadServiceEventHead(ctx, "tenant-1", "ha")
			projection, _ := after.Service.Metadata["application_connector"].(map[string]any)
			fresh := scenario == "current" || scenario == "unrelated-service" || scenario == "automatic" || scenario == "stale-saved" || scenario == "lost-ack"
			if fresh {
				if err != nil || after.Service.Metadata["application_connector_job_id"] != originalReq.DurableJobID || projection["verification_job_id"] != job.ID {
					t.Fatalf("current read failed to refresh original projection: %v", err)
				}
				projectedJSON, _ := json.Marshal(projection)
				_ = json.Unmarshal(projectedJSON, &projection)
				stamp, _ := projection["verified_at"].(string)
				verified, _ := time.Parse(time.RFC3339Nano, stamp)
				if time.Since(verified) > 5*time.Second || projection["receipt_digest"] != envelope.Data.ReceiptDigest {
					t.Fatal("refresh changed receipt or failed freshness")
				}
				sender.noSend = true
				if err = handler(ctx, job, queue); err != nil {
					t.Fatalf("durable projected proof replayed: %v", err)
				}
				readAvailability := func() bool {
					e, w := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/ha/connector", "owner-1", "tenant-1", nil)
					e.Request.SetPathValue("serviceId", "ha")
					if err := h.readConnector(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
						t.Fatal(err)
					}
					var response struct {
						Data struct {
							Dispatchable bool `json:"dispatchable"`
						} `json:"data"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					return response.Data.Dispatchable
				}
				if !readAvailability() {
					t.Fatal("fresh current supporting source remained unavailable")
				}
				if _, err := store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "ha", Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: controlplane.ServiceRuntime{DesiredState: "stopped"}}); err != nil {
					t.Fatal(err)
				}
				if readAvailability() {
					t.Fatal("owner intent change retained dispatchable connector projection")
				}

			} else {
				if err == nil || projection["verification_job_id"] == job.ID {
					t.Fatal("withdrawn authority or altered proof resurrected connector")
				}
				retained, _ := store.GetJob(ctx, "tenant-1", job.ID)
				if retained.Result["connector_outcome_unresolved"] != true {
					t.Fatal("unqualified read released service fence")
				}
			}
			unchanged, _ := store.GetJob(ctx, "tenant-1", originalReq.DurableJobID)
			unchangedBytes, _ := json.Marshal(unchanged)
			if !bytes.Equal(originalBytes, unchangedBytes) {
				t.Fatal("refresh changed completed setup row or signed receipt")
			}
		})
	}
}

type connectorRefreshBoundarySender struct {
	t                              *testing.T
	request                        stackkitcommand.ConnectorRequest
	operation, planHash            string
	envelope                       []byte
	persisted                      func() bool
	beforeResult                   func()
	loseACK, changeReceipt, noSend bool
	lastCommand                    string
}

func (s *connectorRefreshBoundarySender) SendStackKitCommand(_ context.Context, _ string, c *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	s.t.Helper()
	if s.noSend || c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY || c.OwnerApproved || c.ApplicationOperationId != s.operation || c.CommandId == s.operation || !s.persisted() {
		s.t.Fatal("refresh replayed setup or sent before durable read intent")
	}
	var request stackkitcommand.ConnectorRequest
	if json.Unmarshal(c.ApplicationConnectorRequestJson, &request) != nil || request != s.request {
		s.t.Fatal("refresh changed original signed request")
	}
	if s.loseACK {
		s.loseACK = false
		s.lastCommand = c.CommandId
		return nil, errors.New("synthetic read ACK lost")
	}
	if s.lastCommand == c.CommandId {
		s.t.Fatal("unknown read ACK reused command identity")
	}
	if s.beforeResult != nil {
		s.beforeResult()
		s.beforeResult = nil
	}
	result := s.proof(c.CommandId, c.Release, time.Now())
	if s.changeReceipt {
		var envelope map[string]any
		_ = json.Unmarshal(result.CommandResultJson, &envelope)
		data := envelope["data"].(map[string]any)
		data["receiptBase64"] = "altered"
		result.CommandResultJson, _ = json.Marshal(envelope)
		hash := sha256.Sum256(result.CommandResultJson)
		result.PinnedCliEvidenceSha256 = "sha256:" + hex.EncodeToString(hash[:])
	}
	return result, nil
}
func (s *connectorRefreshBoundarySender) proof(command string, pin *agentpb.StackKitReleasePin, finished time.Time) *agentpb.StackKitResult {
	if s.envelope == nil {
		now := time.Now().UTC()
		receipt := stackkitcommand.ConnectorSignedReceipt{Signature: stackkitcommand.ConnectorSignature{OwnerRef: "synthetic-owner", KeyID: "synthetic-key", Value: "synthetic-signature"}}
		receipt.Result = stackkitcommand.ConnectorSetupResult{APIVersion: "stackkit.application-setup-result/v1", Authority: stackkitcommand.ApplicationAuthority{PlanHash: s.planHash, LifecycleContractHash: s.planHash, LifecycleVersion: "v1", PackageRef: "smart-home@v1"}, WorkloadRef: "smart-home", OperationID: s.operation, ActionRef: "home-assistant-owner-bootstrap", ApplyResultHash: s.planHash, ArtifactDigest: s.planHash, InstanceRef: "local-ha", ApplicationVersion: "2026.7.2", AccountRef: "local-ha-user", Initialized: true, AdminLoginVerified: true, OnboardingComplete: true, VerifiedAt: now}
		deployment := stackkitcommand.ConnectorDeployment{Node: s.request.Connector.Binding.Node, InstanceRef: "local-ha", ContainerID: s.request.Connector.Binding.ContainerID, ImageDigest: s.request.Connector.Binding.ImageDigest}
		receipt.Result.HomeAssistantConnector.Issuer = stackkitcommand.ConnectorIssuer{OwnerRef: receipt.Signature.OwnerRef, HAUserID: "local-ha-user", HAVersion: "2026.7.2", RequestedBinding: s.request.Connector.Binding, SecretRef: "secret://home-assistant/connector/" + strings.Repeat("e", 64), Status: "active", ObservedAt: now, ExpiresAt: now.Add(24 * time.Hour), Signature: receipt.Signature}
		receipt.Result.HomeAssistantConnector.VerifiedLocalDeployment = deployment
		raw, _ := json.Marshal(receipt)
		hash := sha256.Sum256(raw)
		evidence := stackkitcommand.ApplicationConnectorEvidence{SchemaVersion: stackkitcommand.ApplicationConnectorResultSchema, SignatureVerified: true, ReceiptBase64: base64.StdEncoding.EncodeToString(raw), ReceiptDigest: "sha256:" + hex.EncodeToString(hash[:]), CurrentLocalDeployment: deployment}
		s.envelope, _ = json.Marshal(map[string]any{"schemaVersion": stackkitcommand.CommandResultVersion, "command": "stackkit setup connector", "status": "success", "data": evidence})
	}
	hash := sha256.Sum256(s.envelope)
	return &agentpb.StackKitResult{CommandId: command, Release: pin, Success: true, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion, EventsSchemaVersion: stackkitcommand.RolloutEventVersion, CommandResultJson: append([]byte(nil), s.envelope...), FinishedAtUnix: finished.Unix(), PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(hash[:])}
}
func connectorRefreshFixturePin(t *testing.T) *agentpb.StackKitReleasePin {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "stackkit")
	content := []byte("synthetic CLI fixture; execution injected through typed Guard sender")
	if err := os.WriteFile(binary, content, 0700); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	raw, _ := json.Marshal(map[string]any{"schemaVersion": stackkitrelease.PinSchemaVersion, "kit": "basement-kit", "version": "v0.51.9", "platform": map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}, "archiveSha256": strings.Repeat("a", 64), "indexSha256": strings.Repeat("b", 64), "binarySha256": hex.EncodeToString(hash[:]), "binaryPath": binary})
	pinPath := filepath.Join(root, "fixture-pin.json")
	if err := os.WriteFile(pinPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "stackkitrelease", "testdata", "advanced-operations-v0.50.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	_ = json.Unmarshal(raw, &catalog)
	operations := catalog["operations"].([]any)
	for _, operation := range []string{"application.connector", "application.connector.verify"} {
		operations = append(operations, map[string]any{"operation": operation, "status": "available", "sinceRelease": "pending", "command": "stackkit setup connector", "argv": []string{"setup", "connector"}})
	}
	catalog["operations"] = operations
	raw, _ = json.Marshal(catalog)
	if err = os.WriteFile(filepath.Join(root, stackkitrelease.AdvancedOperationsFileName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TECHSTACK_STACKKIT_RELEASE_BUNDLE", "")
	t.Setenv("TECHSTACK_STACKKIT_RELEASE_PIN", pinPath)
	t.Setenv("TECHSTACK_STACKKIT_RELEASE_CACHE", root)
	release, err := jobs.ConfiguredTargetStackKitRelease()
	if err != nil || release == nil {
		t.Fatal(err)
	}
	return grpcserver.StackKitReleasePinFor(*release)
}
