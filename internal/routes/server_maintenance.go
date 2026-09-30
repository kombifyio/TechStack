package routes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/denial"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// ServerMaintenanceEntitlement is the tier-gated capability for owner-requested
// reboots and OS updates. Auth0 issues it and the Gateway signs it into the v2
// or v7 envelope (and enforces MFA step-up); Techstack accepts only that
// signed grant, never the membership fallback. Together with server ownership
// and tenant scope it is the whole authorization: no inventory entitlement or
// FGA tuple is required, because maintaining one's own server is an owner
// right that the tier grant, not an inventory role, meters.
const ServerMaintenanceEntitlement = "techstack.servers.maintenance"

// Stable refusal reasons of POST /api/v1/servers/{serverId}/actions.
const (
	maintenanceReasonEntitlement       = "entitlement_required"
	maintenanceReasonControlPlaneHost  = "control_plane_host"
	maintenanceReasonSubstrate         = "substrate_node"
	maintenanceReasonProtectedDemo     = "protected_demo_server"
	maintenanceReasonServiceLocked     = "service_locked"
	maintenanceReasonActive            = "maintenance_active"
	maintenanceReasonNodeOperation     = "node_operation_active"
	maintenanceReasonPlanStale         = "plan_stale"
	maintenanceReasonCapabilityMissing = "agent_capability_missing"
	maintenanceReasonBootIDUnknown     = "boot_id_unknown"
	maintenanceReasonNotConnected      = "agent_not_connected"
	maintenanceReasonHeartbeatStale    = "heartbeat_stale"
	maintenanceReasonRevisionStale     = "inventory_revision_stale"
	maintenanceReasonIdempotencyReused = "idempotency_key_reused"
	maintenanceReasonStepUpRequired    = "server_maintenance_step_up_required"
)

// serverMaintenanceStepUpMaxAge bounds how old the multi-factor sign-in
// behind a reboot or OS update may be.
const serverMaintenanceStepUpMaxAge = 300 * time.Second

var maintenancePlanDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ServerMaintenanceRouteConfig wires POST /api/v1/servers/{serverId}/actions.
// Dispatcher runs the StackKits host commands on the node's agent
// (jobs.HostMaintenanceDispatcher); while it is nil no maintenance action is
// advertised or admitted.
type ServerMaintenanceRouteConfig struct {
	Jobs         controlplane.ServerMaintenanceStore
	Services     controlplane.ServiceRuntimeStore
	Events       controlplane.ServerEventStore
	Entitlements EntitlementGate
	Dispatcher   servermaintenance.Dispatcher
	// AgentCapabilities reports what a server's agent advertised: an mTLS
	// agent at registration, an HTTPS agent through its heartbeat facts.
	AgentCapabilities func(server controlplane.ServerRuntime) []string
	// ControlPlaneMachineIDDigest is servermaintenance.MachineIDDigest of this
	// control plane's own host; "" when unknown.
	ControlPlaneMachineIDDigest string
}

type serverMaintenanceHandlers struct {
	jobs              controlplane.ServerMaintenanceStore
	services          controlplane.ServiceRuntimeStore
	events            controlplane.ServerEventStore
	entitlements      EntitlementGate
	dispatcher        servermaintenance.Dispatcher
	agentCapabilities func(server controlplane.ServerRuntime) []string
	controlPlaneHost  string
}

func newServerMaintenanceHandlers(cfg *ServerMaintenanceRouteConfig) *serverMaintenanceHandlers {
	if cfg == nil {
		return nil
	}
	if cfg.Jobs == nil || cfg.Services == nil {
		panic("RegisterServerRuntimeRoutes: server maintenance needs its job and service stores")
	}
	return &serverMaintenanceHandlers{
		jobs: cfg.Jobs, services: cfg.Services, events: cfg.Events,
		entitlements: entitlementGateOrHosted(cfg.Entitlements), dispatcher: cfg.Dispatcher,
		agentCapabilities: cfg.AgentCapabilities, controlPlaneHost: strings.TrimSpace(cfg.ControlPlaneMachineIDDigest),
	}
}

type serverMaintenanceRequest struct {
	Action                    string `json:"action"`
	ExpectedInventoryRevision int64  `json:"expected_inventory_revision"`
	OwnerApproved             bool   `json:"owner_approved"`
	ExpectedPlanDigest        string `json:"expected_plan_digest,omitempty"`
}

type serverMaintenanceResponse struct {
	JobID    string `json:"job_id"`
	ServerID string `json:"server_id"`
	Action   string `json:"action"`
	Status   string `json:"status"`
}

// nodeIneligibility is the node's own answer to "may maintenance run here":
// "" when eligible, otherwise the stable refusal reason. The read model
// advertises maintenance exactly when this is "", and the route enforces the
// same function, so advertised and admitted cannot drift apart.
func (m *serverMaintenanceHandlers) nodeIneligibility(server controlplane.ServerRuntime) string {
	switch {
	case stringFromAnyMap(server.Metadata, "server_node_role") == "substrate":
		return maintenanceReasonSubstrate
	case demoguard.IsProtectedLease(server.LeaseID):
		return maintenanceReasonProtectedDemo
	case m.isControlPlaneHost(server):
		return maintenanceReasonControlPlaneHost
	case serverregistry.LifecycleState(strings.TrimSpace(server.LifecycleState)) != serverregistry.LifecycleActive ||
		strings.TrimSpace(server.WorkerID) == "" || !serverregistry.MutationsAllowed(server.ConnectionState):
		return maintenanceReasonNotConnected
	case !m.agentAdvertisesMaintenance(server):
		return maintenanceReasonCapabilityMissing
	default:
		return ""
	}
}

// isControlPlaneHost compares the agent-reported machine-id digest with this
// control plane's own. Either side unknown is not a match; the StackKits
// reboot guard refuses a node that runs the control plane as the backstop.
func (m *serverMaintenanceHandlers) isControlPlaneHost(server controlplane.ServerRuntime) bool {
	reported := strings.TrimSpace(stringFromAnyMap(server.Metadata, servermaintenance.MetadataMachineIDDigest))
	return m.controlPlaneHost != "" && reported != "" && reported == m.controlPlaneHost
}

func (m *serverMaintenanceHandlers) agentAdvertisesMaintenance(server controlplane.ServerRuntime) bool {
	if m.dispatcher == nil || m.agentCapabilities == nil || strings.TrimSpace(server.WorkerID) == "" {
		return false
	}
	return containsString(m.agentCapabilities(server), servermaintenance.AgentCapability)
}

// rebootIneligibility adds the reboot-only precondition: the node must
// report its boot id, because a new boot id is the only proof that it
// rebooted.
func (m *serverMaintenanceHandlers) rebootIneligibility(server controlplane.ServerRuntime) string {
	if strings.TrimSpace(stringFromAnyMap(server.Metadata, servermaintenance.MetadataBootID)) == "" {
		return maintenanceReasonBootIDUnknown
	}
	return ""
}

func (m *serverMaintenanceHandlers) advertisedActions(server controlplane.ServerRuntime) []string {
	if m == nil || m.nodeIneligibility(server) != "" {
		return nil
	}
	actions := []string{controlplane.ServerMaintenanceActionPlan, controlplane.ServerMaintenanceActionUpdate}
	if m.rebootIneligibility(server) == "" {
		actions = append(actions, controlplane.ServerMaintenanceActionReboot)
	}
	return actions
}

//nolint:gocyclo // The request order is the contract: authz, decode, replay, ownership, guardrails, target, fence.
func (h serverRuntimeHandlers) maintenanceAction(e *httpx.Event) error {
	m := h.maintenance
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, ServerMaintenanceEntitlement)
	if tenantErr != nil {
		return tenantErr
	}
	serverID := strings.TrimSpace(e.Request.PathValue("serverId"))
	if serverID == "" {
		return httpx.BadRequest(e, "Server ID is required", nil)
	}
	ctx := e.Request.Context()
	if !m.entitlements.SignedGrant(ctx, ServerMaintenanceEntitlement) {
		return writeServerMaintenanceDenial(e, http.StatusForbidden, maintenanceReasonEntitlement, nil)
	}
	request, err := decodeServerMaintenanceRequest(e)
	if err != nil {
		return err
	}
	// Defense in depth behind the Gateway step-up: a reboot or OS update needs
	// a fresh multi-factor sign-in bound by the verified edge envelope.
	if request.Action != controlplane.ServerMaintenanceActionPlan &&
		!m.entitlements.FreshMultiFactor(ctx, h.now().UTC(), serverMaintenanceStepUpMaxAge) {
		return writeServerMaintenanceDenial(e, http.StatusForbidden, maintenanceReasonStepUpRequired, nil)
	}
	idempotencyKey := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return httpx.BadRequest(e, "A bounded Idempotency-Key is required", nil)
	}
	jobID := serverMaintenanceJobID(tenantID, ownerID, idempotencyKey)
	digest := serverMaintenanceRequestDigest(tenantID, serverID, request)
	// Replay before validating the target: a lost-response retry must return
	// the stored receipt even after the first attempt moved the inventory
	// revision or rebooted the node.
	if replayed, replayErr := m.replay(e, tenantID, jobID, serverID, digest); replayed || replayErr != nil {
		return replayErr
	}

	server, err := h.store.GetServerRuntime(ctx, tenantID, serverID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !serverRuntimeOwnedBy(*server, ownerID)) {
		return httpx.NotFound(e, "Server not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	now := h.now().UTC()
	// Guardrails.
	switch reason := m.nodeIneligibility(*server); reason {
	case maintenanceReasonSubstrate, maintenanceReasonProtectedDemo, maintenanceReasonControlPlaneHost:
		return writeServerMaintenanceDenial(e, http.StatusConflict, reason, nil)
	}
	if request.Action != controlplane.ServerMaintenanceActionPlan {
		locked, lockErr := m.lockedServices(ctx, tenantID, server.ID)
		if lockErr != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service lock state is unavailable", nil)
		}
		if len(locked) > 0 {
			return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonServiceLocked, map[string]any{"locked_service_ids": locked})
		}
	}
	// Target validation.
	if request.ExpectedInventoryRevision != server.InventoryRevision {
		return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonRevisionStale, map[string]any{"current_inventory_revision": server.InventoryRevision})
	}
	if reason := m.nodeIneligibility(*server); reason != "" {
		return writeServerMaintenanceDenial(e, http.StatusConflict, reason, nil)
	}
	if request.Action == controlplane.ServerMaintenanceActionReboot {
		if reason := m.rebootIneligibility(*server); reason != "" {
			return writeServerMaintenanceDenial(e, http.StatusConflict, reason, nil)
		}
	}
	if server.LastHeartbeatAt == nil || now.Sub(server.LastHeartbeatAt.UTC()) > runtimehealth.FreshHeartbeatWindow {
		return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonHeartbeatStale, nil)
	}
	if request.Action == controlplane.ServerMaintenanceActionUpdate {
		plan, planErr := m.jobs.LatestServerMaintenancePlan(ctx, tenantID, server.ID, request.ExpectedPlanDigest)
		if planErr != nil && !errors.Is(planErr, controlplane.ErrNotFound) {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Update plans are unavailable", nil)
		}
		if plan == nil || plan.CompletedAt == nil || now.Sub(plan.CompletedAt.UTC()) > servermaintenance.PlanMaxAge {
			return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonPlanStale, nil)
		}
	}
	// Node check, durable per-server fence and enqueue in one transaction
	// under the node's advisory lock. A job the runner never starts expires at
	// its deadline and frees the fence.
	queueDeadline := now.Add(servermaintenance.QueueTimeout)
	job, err := m.jobs.AdmitServerMaintenanceJob(ctx, controlplane.ServerMaintenanceJob{
		ID: jobID, TenantID: tenantID, ServerID: server.ID, AgentID: server.WorkerID, StackID: server.StackID,
		OwnerSubjectID: ownerID, Action: request.Action, RequestDigest: digest,
		State: controlplane.ServerMaintenanceStateQueued, InventoryRevision: server.InventoryRevision,
		PlanDigest: request.ExpectedPlanDigest, DeadlineAt: &queueDeadline,
	})
	var busy *controlplane.NodeBusyError
	switch {
	case errors.As(err, &busy):
		return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonNodeOperation, map[string]any{"job_id": busy.JobID})
	case errors.Is(err, controlplane.ErrServerMaintenanceActive):
		return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonActive, nil)
	case errors.Is(err, controlplane.ErrConflict):
		// A concurrent request with the same key won the insert.
		if replayed, replayErr := m.replay(e, tenantID, jobID, serverID, digest); replayed || replayErr != nil {
			return replayErr
		}
		return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonIdempotencyReused, nil)
	case err != nil:
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance is unavailable", nil)
	}
	m.recordRequested(ctx, h.store, *server, *job, now)
	return httpx.Success(e, http.StatusAccepted, serverMaintenanceResponse{
		JobID: job.ID, ServerID: job.ServerID, Action: job.Action, Status: serverMaintenanceStatus(job.State),
	})
}

// serverMaintenanceRequestFields are the only accepted body keys, matched
// exactly. The Gateway exempts os_update_plan from step-up by reading
// `action`, so the body must mean the same thing to both: encoding/json alone
// matches keys case-insensitively and lets a later duplicate win, which would
// let {"action":"os_update_plan","ACTION":"reboot"} reboot past the edge check.
var serverMaintenanceRequestFields = map[string]bool{
	"action": true, "expected_inventory_revision": true, "owner_approved": true, "expected_plan_digest": true,
}

func decodeServerMaintenanceRequest(e *httpx.Event) (serverMaintenanceRequest, error) {
	var request serverMaintenanceRequest
	raw, err := io.ReadAll(io.LimitReader(e.Request.Body, 4097))
	if err != nil || len(raw) > 4096 || !exactJSONObjectKeys(raw, serverMaintenanceRequestFields) {
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid request body", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid request body", nil)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Request body must contain one JSON document", nil)
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.ExpectedPlanDigest = strings.TrimSpace(request.ExpectedPlanDigest)
	switch request.Action {
	case controlplane.ServerMaintenanceActionPlan, controlplane.ServerMaintenanceActionUpdate, controlplane.ServerMaintenanceActionReboot:
	default:
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Action must be os_update_plan, os_update or reboot", nil)
	}
	if request.ExpectedInventoryRevision <= 0 {
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "expected_inventory_revision is required", nil)
	}
	if request.Action != controlplane.ServerMaintenanceActionPlan && !request.OwnerApproved {
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Reboots and OS updates require explicit Owner approval", nil)
	}
	if (request.Action == controlplane.ServerMaintenanceActionUpdate) != (request.ExpectedPlanDigest != "") ||
		(request.ExpectedPlanDigest != "" && !maintenancePlanDigestPattern.MatchString(request.ExpectedPlanDigest)) {
		return request, httpx.Reject(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "expected_plan_digest (sha256:<hex>) is required for os_update and only for it", nil)
	}
	return request, nil
}

// exactJSONObjectKeys reports whether raw is one JSON object whose keys are
// all in allowed, spelled exactly, each at most once, with nothing after it.
func exactJSONObjectKeys(raw []byte, allowed map[string]bool) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		key, isKey := token.(string)
		if err != nil || !isKey || !allowed[key] || seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func serverMaintenanceJobID(tenantID, ownerID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"server-maintenance/v1", tenantID, ownerID, idempotencyKey}, "\x00")))
	return "srvmaint-" + hex.EncodeToString(sum[:20])
}

func serverMaintenanceRequestDigest(tenantID, serverID string, request serverMaintenanceRequest) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		tenantID, serverID, request.Action, strconv.FormatInt(request.ExpectedInventoryRevision, 10),
		strconv.FormatBool(request.OwnerApproved), request.ExpectedPlanDigest,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// replay answers a retry from the stored job. The id is derived from tenant,
// principal and key, and the stored digest must match, so a replay never
// crosses scopes and a different body under the same key is a conflict.
func (m *serverMaintenanceHandlers) replay(e *httpx.Event, tenantID, jobID, serverID, digest string) (bool, error) {
	existing, err := m.jobs.GetServerMaintenanceJob(e.Request.Context(), tenantID, jobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to inspect server maintenance replay", nil)
	}
	if existing.RequestDigest != digest || existing.ServerID != serverID {
		return true, writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonIdempotencyReused, nil)
	}
	return true, httpx.Success(e, http.StatusAccepted, serverMaintenanceResponse{
		JobID: existing.ID, ServerID: existing.ServerID, Action: existing.Action, Status: serverMaintenanceStatus(existing.State),
	})
}

func serverMaintenanceStatus(state string) string {
	switch state {
	case controlplane.ServerMaintenanceStateQueued:
		return "queued"
	case controlplane.ServerMaintenanceStateCompleted, controlplane.ServerMaintenanceStateFailed, controlplane.ServerMaintenanceStateCancelled:
		return state
	default:
		return "running"
	}
}

func (m *serverMaintenanceHandlers) lockedServices(ctx context.Context, tenantID, serverID string) ([]string, error) {
	services, err := m.services.ListServiceRuntimes(ctx, tenantID, "", serverID)
	if err != nil {
		return nil, err
	}
	locked := make([]string, 0)
	for i := range services {
		if services[i].ServerID == serverID && services[i].MutationLock.Locked() {
			locked = append(locked, services[i].ID)
		}
	}
	return locked, nil
}

// recordRequested writes the owner request into the server timeline. The job
// row is the durable request record; a failed timeline write is logged and
// does not undo an accepted job.
func (m *serverMaintenanceHandlers) recordRequested(ctx context.Context, servers controlplane.ServerRuntimeStore, server controlplane.ServerRuntime, job controlplane.ServerMaintenanceJob, now time.Time) {
	if m.events == nil || job.Action == controlplane.ServerMaintenanceActionPlan {
		return
	}
	reason, title, body := "os_update_requested", "OS update requested", "The owner approved installing the planned OS updates on this server. It does not restart on its own afterwards."
	if job.Action == controlplane.ServerMaintenanceActionReboot {
		reason, title, body = "owner_reboot_requested", "Reboot requested", "The owner approved a reboot of this server. It goes offline briefly and must come back within 10 minutes."
	}
	decision := &outcome.Decision{
		Status: outcome.StatusPending, ReasonCode: reason, Capability: ServerMaintenanceEntitlement,
		UserGuidance: &outcome.Guidance{Title: title, Body: body, NextSteps: []outcome.Step{
			{ID: "follow-maintenance-job", Label: "Follow the maintenance job", Kind: "note"},
		}},
		SupportContext: map[string]any{"job_id": job.ID, "action": job.Action},
	}
	current := &server
	for attempt := 0; attempt < 2; attempt++ {
		_, err := m.events.ApplyServerEvent(ctx, controlplane.ServerEvent{
			TenantID: current.TenantID, ServerID: current.ID, ExpectedRevision: current.Revision,
			Generation: current.Generation, Authority: controlplane.ServerEventAuthorityControlPlane,
			Source: "server-maintenance", SourceID: job.ID, ObservedAt: now,
			Outcome: decision, Evidence: map[string]any{"job_id": job.ID, "action": job.Action, "owner_subject_id": job.OwnerSubjectID},
		})
		if err == nil {
			return
		}
		if !errors.Is(err, controlplane.ErrConflict) {
			break
		}
		if current, err = servers.GetServerRuntime(ctx, server.TenantID, server.ID); err != nil {
			break
		}
	}
	slog.WarnContext(ctx, "server_maintenance_timeline_write_failed", "server_id", server.ID, "job_id", job.ID)
}

type maintenanceGuidance struct {
	title, body string
	steps       []string
	retryable   bool
}

var serverMaintenanceGuidance = map[string]maintenanceGuidance{
	maintenanceReasonEntitlement: {
		title: "Server maintenance is not included in your plan",
		body:  "Reboots and OS updates from kombify need the server maintenance entitlement and a recent multi-factor sign-in.",
		steps: []string{"Upgrade to a plan that includes server maintenance.", "Sign in again with your second factor and retry."},
	},
	maintenanceReasonStepUpRequired: {
		title:     "Confirm it is you",
		body:      "Reboots and OS updates need a multi-factor sign-in from the last five minutes.",
		steps:     []string{"Sign in again with your second factor, then retry."},
		retryable: true,
	},
	maintenanceReasonControlPlaneHost: {
		title: "This server runs the control plane",
		body:  "Techstack does not reboot or update the host it runs on: it would stop itself before it could confirm the result.",
		steps: []string{"Update this host from its own console."},
	},
	maintenanceReasonSubstrate: {
		title: "This node is a substrate host",
		body:  "Substrate hosts carry other servers, so they are maintained outside this action.",
		steps: []string{"Maintain the substrate host directly."},
	},
	maintenanceReasonProtectedDemo: {
		title: "Demo servers are protected",
		body:  "The shared demo servers cannot be rebooted or updated.",
		steps: []string{"Add your own server to try maintenance actions."},
	},
	maintenanceReasonServiceLocked: {
		title: "A locked service blocks maintenance",
		body:  "At least one service on this server is locked. A reboot or update would interrupt it.",
		steps: []string{"Unlock the listed services, then retry."},
	},
	maintenanceReasonActive: {
		title:     "Maintenance is already running",
		body:      "This server already has a reboot, update or update plan in progress.",
		steps:     []string{"Wait for the running maintenance job to finish, then retry."},
		retryable: true,
	},
	maintenanceReasonNodeOperation: {
		title:     "Another operation is running on this server",
		body:      "A stack or service operation is using this server's agent right now.",
		steps:     []string{"Wait for the running operation to finish, then retry."},
		retryable: true,
	},
	maintenanceReasonPlanStale: {
		title:     "The update plan is out of date",
		body:      "An OS update installs exactly one plan, and that plan must be at most 15 minutes old.",
		steps:     []string{"Create a new update plan and approve it."},
		retryable: true,
	},
	maintenanceReasonBootIDUnknown: {
		title: "The server does not report its boot ID",
		body:  "Techstack confirms a reboot only when the server comes back with a new boot ID, and this server's agent does not report one.",
		steps: []string{"Update the server agent, then retry."},
	},
	maintenanceReasonCapabilityMissing: {
		title: "The server agent cannot run maintenance yet",
		body:  "This server's agent does not offer reboots or OS updates.",
		steps: []string{"Update the server agent, then retry."},
	},
	maintenanceReasonNotConnected: {
		title:     "The server is not connected",
		body:      "Maintenance needs an active server with a connected agent.",
		steps:     []string{"Reconnect the server, then retry."},
		retryable: true,
	},
	maintenanceReasonHeartbeatStale: {
		title:     "The server has not reported recently",
		body:      "Maintenance needs a fresh heartbeat from the server agent.",
		steps:     []string{"Wait for the agent to report again, then retry."},
		retryable: true,
	},
	maintenanceReasonRevisionStale: {
		title:     "The server changed since you looked",
		body:      "The request was based on an older view of this server.",
		steps:     []string{"Reload the server and confirm again."},
		retryable: true,
	},
	maintenanceReasonIdempotencyReused: {
		title: "This request key was already used",
		body:  "The Idempotency-Key belongs to a different maintenance request.",
		steps: []string{"Send a new request with a new Idempotency-Key."},
	},
}

// writeServerMaintenanceDenial writes one client-error-envelope/v1 denial and
// returns httpx.ErrResponseWritten.
func writeServerMaintenanceDenial(e *httpx.Event, status int, reason string, support map[string]any) error {
	guidance := serverMaintenanceGuidance[reason]
	env := denial.Envelope{
		ErrorCode: reason, ReasonCode: reason, Capability: ServerMaintenanceEntitlement,
		Retryable:      guidance.retryable,
		UserGuidance:   denial.UserGuidance{Title: guidance.title, Body: guidance.body, NextSteps: guidance.steps},
		SupportContext: support,
	}
	if reason == maintenanceReasonEntitlement {
		env.RequiredFeatures = []string{ServerMaintenanceEntitlement}
		env.MissingFeatures = []string{ServerMaintenanceEntitlement}
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return httpx.Reject(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Server maintenance unavailable", nil)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return httpx.Reject(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Server maintenance unavailable", nil)
	}
	code := ksapi.ErrCodeConflict
	if status == http.StatusForbidden {
		code = ksapi.ErrCodeForbidden
	}
	payload["error"] = map[string]any{"code": code, "message": guidance.title, "details": map[string]any{"reason": reason}}
	if err := e.JSON(status, payload); err != nil {
		return err
	}
	return httpx.ErrResponseWritten
}

// refuseDuringServerMaintenance is the read-only reverse fence of a service
// action: it refuses while a reboot or OS update holds the node. It takes no
// lock, so service actions never wait on each other; the maintenance runner's
// claim re-checks the node for stack and service work. The canonical stores
// implement ServerMaintenanceStore; any other store fails closed rather than
// run past an unknown maintenance state.
func refuseDuringServerMaintenance(e *httpx.Event, servers any, tenantID string, scope controlplane.ServerMaintenanceScope) error {
	store, ok := servers.(controlplane.ServerMaintenanceStore)
	if !ok {
		return httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance state is unavailable", nil)
	}
	job, err := store.ActiveServerMaintenanceJob(e.Request.Context(), tenantID, scope)
	if errors.Is(err, controlplane.ErrNotFound) {
		return nil
	}
	if err != nil {
		return httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server maintenance state is unavailable", nil)
	}
	return writeServerMaintenanceDenial(e, http.StatusConflict, maintenanceReasonActive, map[string]any{"job_id": job.ID})
}
