package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

type serviceConnectorRequest struct {
	ExpectedInventoryRevision int64  `json:"expected_inventory_revision"`
	Action                    string `json:"action"`
	LifespanDays              int    `json:"lifespan_days"`
	OwnerApproved             bool   `json:"owner_approved"`
}

func (h *serviceRuntimeHandlers) connectorRelease() (*agentpb.StackKitReleasePin, error) {
	if h.connectorAdmission != nil {
		return h.connectorAdmission()
	}
	return jobs.ApplicationConnectorRelease()
}
func connectorUnavailable(e *httpx.Event) error {
	return httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Connector setup awaits a published supporting StackKits pin and current node qualification", map[string]any{"reason_code": "application_connector_source_preview", "source_preview": true, "dispatchable": false, "guidance": "The node must have a current signed apply and verified local Owner setup before connector setup; a managed or adopted service flag does not establish that authority."})
}

func (h *serviceRuntimeHandlers) readConnector(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, false)
	if err != nil {
		return err
	}

	_, admissionErr := h.connectorRelease()
	var projection map[string]any
	data, _ := json.Marshal(t.service.Metadata["application_connector"])
	var retained map[string]any
	_ = json.Unmarshal(data, &retained)
	if retained != nil {
		projection = map[string]any{}
		for _, key := range []string{"status", "receipt_digest", "operation_id", "application_version", "verified_at", "expires_at"} {
			if value := retained[key]; value != nil {
				projection[key] = value
			}
		}
	}
	verifiedAt, _ := retained["verified_at"].(string)
	verifiedTime, verifyErr := time.Parse(time.RFC3339Nano, verifiedAt)

	nodeCurrent := false
	if authority, ok := h.orch.(interface {
		AuthorizeApplicationConnectorRead(context.Context, jobs.StackKitLifecycleRequest) error
	}); ok {
		binding := &jobs.ApplicationConnectorBinding{ServerID: t.server.ID, ServerGeneration: metadataInt64(retained, "server_generation"), WorkerGeneration: metadataInt64(retained, "worker_generation"), NodeID: stringFromAny(retained["node_id"]), LeaseID: stringFromAny(retained["lease_id"]), ProviderRef: stringFromAny(retained["provider_ref"])}
		nodeCurrent = authority.AuthorizeApplicationConnectorRead(e.Request.Context(), jobs.StackKitLifecycleRequest{TenantID: t.tenant, OwnerID: t.owner, StackID: t.stack.ID, StackKitInstanceID: t.stack.StackKitInstanceID, AgentID: t.server.WorkerID, NodeID: binding.NodeID, Connector: binding}) == nil
	}
	intentCurrent := jobs.ApplicationIntentRevision(t.service) > 0 && jobs.ApplicationIntentRevision(t.service) == metadataInt64(t.service.Metadata, "application_connector_projection_revision")
	qualified := intentCurrent && nodeCurrent && admissionErr == nil && verifyErr == nil && !verifiedTime.After(h.now().Add(time.Minute)) && h.now().Sub(verifiedTime) <= runtimehealth.FreshHeartbeatWindow && t.service.Source == serviceregistry.SourceStackKitsInventory && t.service.ManagementState == "managed" && !t.service.MutationLock.Locked() && retained["container_id"] == jobs.ApplicationContainerIdentity(t.service) && retained["image_digest"] == jobs.ApplicationConnectorImageDigest(t.service) && t.service.ObservedAt != nil && h.now().Sub(*t.service.ObservedAt) >= -30*time.Second && h.now().Sub(*t.service.ObservedAt) <= runtimehealth.FreshHeartbeatWindow
	reason := ""
	if admissionErr != nil {
		reason = "application_connector_source_preview"
	} else if !qualified {
		reason = "application_connector_verification_required"
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"service_id": t.service.ID, "source_preview": admissionErr != nil, "dispatchable": qualified, "connector": projection, "reason_code": reason})

}

func (h *serviceRuntimeHandlers) setupConnector(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, true)
	if err != nil {
		return err
	}
	if err = authorizeInventoryServerOperate(e.Request.Context(), h.policy, inventoryScope{tenantID: t.tenant, ownerID: t.owner}, t.server.ID); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	key := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		return httpx.BadRequest(e, "A bounded Idempotency-Key is required", nil)
	}
	var r serviceConnectorRequest
	d := json.NewDecoder(io.LimitReader(e.Request.Body, 4097))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF || !r.OwnerApproved || r.ExpectedInventoryRevision <= 0 || (r.Action != "ensure" && r.Action != "revoke") || r.LifespanDays < 1 || r.LifespanDays > 365 {
		return httpx.BadRequest(e, "Connector requires one closed ensure or revoke request, lifespan, current inventory revision and Owner approval", nil)
	}
	// The route never accepts a container, node, image, token, Owner key or URL
	// from the public caller. All dispatch metadata comes from canonical stores.
	if t.service.Source != serviceregistry.SourceStackKitsInventory || t.service.ManagementState != "managed" || (t.service.ServiceKey != "smart-home" && t.service.Metadata["application_key"] != "smart-home") {
		return httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Connector requires a declared owned smart-home service", map[string]any{"reason_code": "application_connector_owned_source_required"})
	}
	if err = h.validateAdoptionSource(e, t, adoptionRequest{ExpectedInventoryRevision: r.ExpectedInventoryRevision}); err != nil {
		return err
	}
	image := jobs.ApplicationConnectorImageDigest(t.service)
	if len(image) != 71 || !strings.HasPrefix(image, "sha256:") {
		return httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Current immutable application image evidence is required", nil)
	}
	pin, err := h.connectorRelease()
	if err != nil {
		return connectorUnavailable(e)
	}
	request := stackkitcommand.ConnectorRequest{SchemaVersion: stackkitcommand.ApplicationConnectorRequestSchema}
	request.Connector.Binding = stackkitcommand.ConnectorSource{ResourceID: t.service.ID, ContainerID: jobs.ApplicationContainerIdentity(t.service), ImageDigest: image, ServiceRevision: t.head.Revision}
	request.Connector.Action = r.Action
	request.Connector.LifespanDays = r.LifespanDays
	previous, _ := t.service.Metadata["application_connector_job_id"].(string)
	binding := &jobs.ApplicationConnectorBinding{ReleasePin: pin, Request: request, ServerID: t.server.ID, ServerGeneration: t.server.Generation, LeaseID: t.server.LeaseID, ProviderRef: t.server.ProviderRef, NodeID: t.server.NodeID, ExpectedIntentRevision: jobs.ApplicationIntentRevision(t.head.Service), ExpectedConnectorJobID: previous}
	// The fingerprint excludes volatile inventory observations, but includes
	// exact source and semantic action so a key cannot be reused for another one.
	fingerprint, _ := json.Marshal(struct {
		Service, Container, Image, Action string
		Lifespan                          int
	}{t.service.ID, request.Connector.Binding.ContainerID, image, r.Action, r.LifespanDays})
	digest := sha256.Sum256(fingerprint)
	req := jobs.StackKitLifecycleRequest{StackID: t.stack.ID, StackKitInstanceID: t.stack.StackKitInstanceID, TenantID: t.tenant, OwnerID: t.owner, AgentID: t.server.WorkerID, Operation: jobs.StackKitLifecycleApplicationConnector, OwnerApproved: true, ServiceID: t.service.ID, ServiceKey: "smart-home", DurableJobID: jobs.StackKitServiceActionJobID(t.tenant, t.owner, key), ServiceActionDigest: hex.EncodeToString(digest[:]), Connector: binding}
	if h.orch == nil || h.jobs == nil {
		return connectorUnavailable(e)
	}
	if replayed, replayErr := h.replayServiceAction(e, t.tenant, t.service, req.Operation, req); replayed || replayErr != nil {
		return replayErr
	}
	return h.enqueueServiceAction(e, t.tenant, t.stack.ID, t.service, req.Operation, req)
}

// verifyConnector accepts no source, node, receipt or mutation authority from
// the caller; the consumer loads the original signed setup from durable jobs.
func (h *serviceRuntimeHandlers) verifyConnector(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, false)
	if err != nil {
		return err
	}
	if h.policy == nil {
		return writeInventoryHTTPError(e, &inventoryError{status: http.StatusServiceUnavailable, reasonCode: "inventory_policy_unavailable", message: "Inventory policy unavailable"})
	}
	if _, err = (&inventoryApplication{policy: h.policy}).authorize(e.Request.Context(), inventoryScope{tenantID: t.tenant, ownerID: t.owner}, InventoryActionRead, controlplane.InventoryReadTargetServer, t.server.ID); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	var body *struct{}
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return httpx.BadRequest(e, "Connector verification accepts only an empty metadata object", nil)
	}
	if _, err = h.connectorRelease(); err != nil {
		return connectorUnavailable(e)
	}
	consumer, ok := h.orch.(interface {
		RefreshApplicationConnector(context.Context, string, string, string) (string, error)
	})
	if !ok {
		return connectorUnavailable(e)
	}
	id, err := consumer.RefreshApplicationConnector(e.Request.Context(), t.tenant, t.owner, t.service.ID)
	if err != nil {
		return httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Current connector setup, source and node authority are required for read-only verification", map[string]any{"reason_code": "application_connector_verification_required"})
	}
	return httpx.Success(e, http.StatusAccepted, serviceActionResponse{JobID: id, ServiceID: t.service.ID, Action: jobs.StackKitLifecycleApplicationConnectorVerify, Status: "queued"})
}
