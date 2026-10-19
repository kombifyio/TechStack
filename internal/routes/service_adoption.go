package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type adoptionRequest struct {
	ExpectedInventoryRevision int64  `json:"expected_inventory_revision"`
	Mode                      string `json:"mode,omitempty"`
	PlanDigest                string `json:"plan_digest,omitempty"`
	OwnerApproved             bool   `json:"owner_approved,omitempty"`
}
type adoptionTarget struct {
	tenant, owner string
	service       *controlplane.ServiceRuntime
	stack         *controlplane.Stack
	server        *controlplane.ServerRuntime
	head          *controlplane.ServiceEventResult
}

var applicationContainerPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func adoptionUnavailable(e *httpx.Event, err error) error {
	return httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Application adoption requires a published capable StackKits release and connected Guard", map[string]any{"reason_code": "application_adoption_unavailable", "guidance": "Activate the published application lifecycle release on this Guard before preparing adoption."})
}
func (h *serviceRuntimeHandlers) applicationHead(ctx context.Context, tenant, id string) (*controlplane.ServiceEventResult, error) {
	reader, ok := h.store.(controlplane.ServiceEventReader)
	if !ok {
		return nil, controlplane.ErrNotFound
	}
	return reader.ReadServiceEventHead(ctx, tenant, id)
}
func (h *serviceRuntimeHandlers) adoptionTarget(e *httpx.Event, mutate bool) (adoptionTarget, error) {
	var t adoptionTarget
	owner, _, ok := authenticatedUser(e)
	if !ok {
		return t, httpx.RejectUnauthorized(e, "Authentication required")
	}
	tenant, err := tenantguard.TenantScope(requestExplicitTenantID(e), owner, "techstack.services.adoption")
	if err != nil {
		return t, err
	}
	service, err := h.store.GetServiceRuntime(e.Request.Context(), tenant, e.Request.PathValue("serviceId"))
	if err != nil {
		return t, httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Service not found", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenant, service.StackID)
	if err != nil || stack.OwnerSubjectID != owner {
		return t, httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Service not found", nil)
	}
	head, err := h.applicationHead(e.Request.Context(), tenant, service.ID)
	if err != nil {
		return t, adoptionUnavailable(e, err)
	}
	server, err := h.servers.GetServerRuntime(e.Request.Context(), tenant, service.ServerID)
	if err != nil {
		return t, adoptionUnavailable(e, err)
	}
	if mutate && service.MutationLock.Locked() {
		return t, httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service is locked", map[string]any{"reason_code": "service_locked"})
	}
	return adoptionTarget{tenant: tenant, owner: owner, service: service, stack: stack, server: server, head: head}, nil
}
func decodeAdoptionRequest(e *httpx.Event) (adoptionRequest, string, error) {
	var r adoptionRequest
	key := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		return r, key, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "A bounded Idempotency-Key is required", nil)
	}
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil {
		return r, key, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid adoption request", nil)
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return r, key, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "One adoption request document is required", nil)
	}
	if r.ExpectedInventoryRevision <= 0 {
		return r, key, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Expected inventory revision is required", nil)
	}
	return r, key, nil
}
func (h *serviceRuntimeHandlers) validateAdoptionSource(e *httpx.Event, t adoptionTarget, r adoptionRequest) error {
	if r.ExpectedInventoryRevision != t.server.InventoryRevision || metadataInt64(t.service.Metadata, "inventory_revision") != r.ExpectedInventoryRevision {
		return httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Inventory revision is stale", nil)
	}
	if !applicationContainerPattern.MatchString(jobs.ApplicationContainerIdentity(t.service)) {
		return httpx.Reject(e, http.StatusConflict, ksapi.ErrCodeConflict, "Adoption requires the exact full container identity", map[string]any{"reason_code": "application_source_identity_unavailable"})
	}
	if t.service.ObservedAt == nil || h.now().Sub(*t.service.ObservedAt) > runtimehealth.FreshHeartbeatWindow || (t.server.ConnectionState != "connected" && t.server.ConnectionState != "degraded") {
		return adoptionUnavailable(e, nil)
	}
	if t.stack.StackKitInstanceID == "" {
		return adoptionUnavailable(e, nil)
	}
	return refuseDuringServerMaintenance(e, h.servers, t.tenant, controlplane.ServerMaintenanceScope{ServerID: t.server.ID, AgentID: t.server.WorkerID})
}
func (h *serviceRuntimeHandlers) prepareAdoption(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, false)
	if err != nil {
		return err
	}
	r, key, err := decodeAdoptionRequest(e)
	if err != nil {
		return err
	}
	if r.Mode != "adopt_in_place" || r.OwnerApproved || r.PlanDigest != "" {
		return httpx.BadRequest(e, "Only a read-only adopt_in_place assessment is supported", nil)
	}
	if t.service.ManagementState != "observed" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service already has a lifecycle binding", nil)
	}
	if err = h.validateAdoptionSource(e, t, r); err != nil {
		return err
	}
	pin, err := h.applicationAdmission()
	if err != nil {
		return adoptionUnavailable(e, err)
	}
	binding := retainedApplicationBinding(t)
	binding.ReleasePin = pin
	return h.enqueueAdoption(e, t, r, key, jobs.StackKitLifecycleApplicationInspect, binding)
}
func (h *serviceRuntimeHandlers) readAdoption(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, false)
	if err != nil {
		return err
	}
	plan, err := h.loadAdoptionPlan(e, t)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"plan_id": plan.ID, "service_id": t.service.ID, "status": serviceActionJobStatus(plan.State), "assessment": plan.Result["application_evidence"], "source_binding": plan.Result["application_binding"], "error": plan.Error})
}
func (h *serviceRuntimeHandlers) loadAdoptionPlan(e *httpx.Event, t adoptionTarget) (*controlplane.Job, error) {
	if h.jobs == nil {
		return nil, adoptionUnavailable(e, nil)
	}
	plan, err := h.jobs.GetJob(e.Request.Context(), t.tenant, e.Request.PathValue("planId"))
	if err != nil || plan.StackID != t.stack.ID {
		return nil, httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Adoption plan not found", nil)
	}
	receipt, _ := plan.Result["service_action_receipt"].(map[string]any)
	if receipt["service_id"] != t.service.ID || receipt["action"] != jobs.StackKitLifecycleApplicationInspect || plan.Payload["owner_id"] != t.owner {
		return nil, httpx.Reject(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Adoption plan not found", nil)
	}
	return plan, nil
}
func (h *serviceRuntimeHandlers) executeAdoption(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, true)
	if err != nil {
		return err
	}
	if err := authorizeOwnerServerOperate(e.Request.Context(), h.policy, h.servers, inventoryScope{tenantID: t.tenant, ownerID: t.owner}, t.server.ID); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	r, key, err := decodeAdoptionRequest(e)
	if err != nil {
		return err
	}
	if !r.OwnerApproved || r.Mode != "adopt_in_place" || r.PlanDigest == "" {
		return httpx.BadRequest(e, "Execute requires the reviewed in-place plan digest and Owner approval", nil)
	}
	plan, err := h.loadAdoptionPlan(e, t)
	if err != nil {
		return err
	}
	if plan.State != "completed" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Adoption assessment is not complete", nil)
	}
	var binding jobs.ApplicationLifecycleBinding
	encoded, _ := json.Marshal(plan.Result["application_binding"])
	if json.Unmarshal(encoded, &binding) != nil {
		return adoptionUnavailable(e, nil)
	}
	var evidence stackkitcommand.ApplicationEvidence
	encoded, _ = json.Marshal(plan.Result["application_evidence"])
	if json.Unmarshal(encoded, &evidence) != nil || evidence.PlanDigest != r.PlanDigest {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Reviewed adoption digest differs from retained plan", nil)
	}
	binding.PlanJobID = plan.ID
	binding.ReviewedPlan = &evidence
	if replayed, replayErr := h.replayServiceAction(e, t.tenant, t.service, jobs.StackKitLifecycleApplicationAdopt, adoptionLifecycleRequest(t, r, key, jobs.StackKitLifecycleApplicationAdopt, &binding)); replayed || replayErr != nil {
		return replayErr
	}
	if binding.ExpectedServiceRevision != t.head.Revision || binding.ContainerID != jobs.ApplicationContainerIdentity(t.service) || binding.ServerID != t.service.ServerID || t.service.ManagementState != "observed" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Retained application source changed; prepare a new plan", nil)
	}
	if err = h.validateAdoptionSource(e, t, r); err != nil {
		return err
	}
	binding.PlanJobID = plan.ID
	binding.ReviewedPlan = &evidence
	return h.enqueueAdoption(e, t, r, key, jobs.StackKitLifecycleApplicationAdopt, &binding)
}
func (h *serviceRuntimeHandlers) releaseAdoption(e *httpx.Event) error {
	t, err := h.adoptionTarget(e, true)
	if err != nil {
		return err
	}
	if err := authorizeOwnerServerOperate(e.Request.Context(), h.policy, h.servers, inventoryScope{tenantID: t.tenant, ownerID: t.owner}, t.server.ID); err != nil {
		return writeInventoryHTTPError(e, err)
	}
	r, key, err := decodeAdoptionRequest(e)
	if err != nil {
		return err
	}
	if !r.OwnerApproved || r.PlanDigest != "" || r.Mode != "" {
		return httpx.BadRequest(e, "Release requires Owner approval", nil)
	}
	if replayed, replayErr := h.replayServiceAction(e, t.tenant, t.service, jobs.StackKitLifecycleApplicationRelease, adoptionLifecycleRequest(t, r, key, jobs.StackKitLifecycleApplicationRelease, &jobs.ApplicationLifecycleBinding{ContainerID: jobs.ApplicationContainerIdentity(t.service)})); replayed || replayErr != nil {
		return replayErr
	}
	if !adoptedApplication(t.service) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service has no supported adoption binding", nil)
	}
	if err = h.validateAdoptionSource(e, t, r); err != nil {
		return err
	}
	pin, err := h.applicationAdmission()
	if err != nil {
		return adoptionUnavailable(e, err)
	}
	binding := retainedApplicationBinding(t)
	binding.ReleasePin = pin
	return h.enqueueAdoption(e, t, r, key, jobs.StackKitLifecycleApplicationRelease, binding)
}

func retainedApplicationBinding(t adoptionTarget) *jobs.ApplicationLifecycleBinding {
	jobID, _ := t.head.Service.Metadata["application_adoption_job_id"].(string)
	return &jobs.ApplicationLifecycleBinding{ContainerID: jobs.ApplicationContainerIdentity(t.head.Service), ServerID: t.head.Service.ServerID,
		ExpectedServiceRevision: t.head.Revision, ExpectedIntentRevision: jobs.ApplicationIntentRevision(t.head.Service),
		ExpectedManagementState: t.head.Service.ManagementState, ExpectedDesiredState: t.head.Service.DesiredState, ExpectedAdoptionJobID: jobID}
}
func adoptedApplication(service *controlplane.ServiceRuntime) bool {
	data, _ := json.Marshal(service.Metadata["application_adoption"])
	var e stackkitcommand.ApplicationEvidence
	return json.Unmarshal(data, &e) == nil && e.ProfileRef == stackkitcommand.HomeAssistantAdoptionProfile && e.Status == "verified" && service.ManagementState == "managed"
}
func (h *serviceRuntimeHandlers) enqueueAdoption(e *httpx.Event, t adoptionTarget, r adoptionRequest, key, op string, binding *jobs.ApplicationLifecycleBinding) error {
	if h.orch == nil || h.jobs == nil {
		return adoptionUnavailable(e, nil)
	}
	req := adoptionLifecycleRequest(t, r, key, op, binding)
	if replayed, replayErr := h.replayServiceAction(e, t.tenant, t.service, op, req); replayed || replayErr != nil {
		return replayErr
	}
	return h.enqueueServiceAction(e, t.tenant, t.stack.ID, t.service, op, req)
}
func adoptionLifecycleRequest(t adoptionTarget, r adoptionRequest, key, op string, binding *jobs.ApplicationLifecycleBinding) jobs.StackKitLifecycleRequest {
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{t.tenant, t.service.ID, op, strconv.FormatInt(r.ExpectedInventoryRevision, 10), r.PlanDigest, binding.PlanJobID, binding.ContainerID}, "\x00")))
	return jobs.StackKitLifecycleRequest{StackID: t.stack.ID, StackKitInstanceID: t.stack.StackKitInstanceID, TenantID: t.tenant, OwnerID: t.owner, AgentID: t.server.WorkerID, Operation: op, OwnerApproved: r.OwnerApproved, ServiceID: t.service.ID, ServiceKey: "smart-home", DurableJobID: jobs.StackKitServiceActionJobID(t.tenant, t.owner, key), ServiceActionDigest: hex.EncodeToString(fingerprint[:]), Application: binding}
}

type serviceAdoptionAvailability struct {
	PreviewAvailable   bool   `json:"preview_available"`
	ExecutionAvailable bool   `json:"execution_available"`
	ReleaseAvailable   bool   `json:"release_available"`
	ProfileRef         string `json:"profile_ref,omitempty"`
	ReasonCode         string `json:"reason_code,omitempty"`
}

func (h *serviceRuntimeHandlers) adoptionAvailability(s controlplane.ServiceRuntime, admission func() (*agentpb.StackKitReleasePin, error)) serviceAdoptionAvailability {
	a := serviceAdoptionAvailability{ReasonCode: "application_adoption_unavailable"}
	if s.ManagementState != "observed" && !adoptedApplication(&s) {
		return a
	}
	if !applicationContainerPattern.MatchString(jobs.ApplicationContainerIdentity(&s)) {
		a.ReasonCode = "application_source_identity_unavailable"
		return a
	}
	if admission == nil {
		return a
	}
	if _, err := admission(); err != nil {
		return a
	}
	a.ProfileRef = stackkitcommand.HomeAssistantAdoptionProfile
	if s.ManagementState == "observed" {
		a.PreviewAvailable = true
		a.ExecutionAvailable = true
		a.ReasonCode = ""
	}
	if adoptedApplication(&s) && !s.MutationLock.Locked() {
		a.ReleaseAvailable = true
		a.ReasonCode = ""
	}
	return a
}
