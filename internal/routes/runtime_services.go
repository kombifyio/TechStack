package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

type ServiceRuntimeRouteConfig struct {
	Store        controlplane.ServiceRuntimeStore
	Stacks       controlplane.StackStore
	Servers      controlplane.ServerRuntimeStore
	Jobs         controlplane.JobStore
	Now          func() time.Time
	Orchestrator serviceActionOrchestrator
}

type serviceActionOrchestrator interface {
	EnqueueStackKitLifecycle(context.Context, jobs.StackKitLifecycleRequest) (string, error)
}

type serviceRuntimeHandlers struct {
	store   controlplane.ServiceRuntimeStore
	stacks  controlplane.StackStore
	servers controlplane.ServerRuntimeStore
	jobs    controlplane.JobStore
	now     func() time.Time
	orch    serviceActionOrchestrator
	mu      sync.Mutex
}

type serviceActionRequest struct {
	Action                    string `json:"action"`
	ExpectedInventoryRevision int64  `json:"expected_inventory_revision"`
	OwnerApproved             bool   `json:"owner_approved"`
	Limit                     int    `json:"limit,omitempty"`
	Cursor                    string `json:"cursor,omitempty"`
}

type serviceActionResponse struct {
	JobID     string `json:"job_id"`
	ServiceID string `json:"service_id"`
	Action    string `json:"action"`
	Status    string `json:"status"`
}

type serviceLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

type serviceLogsResponse struct {
	ServiceID  string            `json:"service_id"`
	JobID      string            `json:"job_id,omitempty"`
	Status     string            `json:"status"`
	Entries    []serviceLogEntry `json:"entries"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type serviceRuntimeResponse struct {
	ID              string                  `json:"id"`
	TechstackID     string                  `json:"techstack_id"`
	ServerID        string                  `json:"server_id,omitempty"`
	TargetKind      string                  `json:"target_kind"`
	Placement       serviceRuntimePlacement `json:"placement"`
	ServiceKey      string                  `json:"service_key"`
	ServiceInstance string                  `json:"service_instance"`
	Name            string                  `json:"name"`
	// ManagementState is the persisted ownership dimension. `desired_state` is
	// only a declared contract when this is `managed`; for an `observed` service
	// there is no declared target and drift comparison is undefined.
	ManagementState   string                 `json:"management_state"`
	DesiredState      string                 `json:"desired_state"`
	ObservedState     string                 `json:"observed_state"`
	Health            serviceRuntimeHealth   `json:"health"`
	StackKitVersion   string                 `json:"stackkit_version,omitempty"`
	Access            map[string]any         `json:"access"`
	AllowedActions    []string               `json:"allowed_actions"`
	InventoryRevision int64                  `json:"inventory_revision"`
	EvidenceRef       string                 `json:"evidence_ref,omitempty"`
	Source            string                 `json:"source"`
	Provenance        map[string]interface{} `json:"provenance"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

type serviceRuntimeHealth struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	ReasonCode string     `json:"reason_code,omitempty"`
}

// serviceRuntimePlacement is evidence-backed placement metadata. The
// managed_workload shape is intentionally data-only until a dedicated provider
// slice proves its SLA, backup, runtime and cleanup semantics.
type serviceRuntimePlacement struct {
	ProviderID         string                       `json:"provider_id,omitempty"`
	ManagedTargetRef   string                       `json:"managed_target_ref,omitempty"`
	ProviderReceiptRef string                       `json:"provider_receipt_ref,omitempty"`
	SLAPolicyRef       string                       `json:"sla_policy_ref,omitempty"`
	BackupPolicyRef    string                       `json:"backup_policy_ref,omitempty"`
	EvidenceRef        string                       `json:"evidence_ref,omitempty"`
	ObservedAt         *time.Time                   `json:"observed_at,omitempty"`
	Freshness          serverRuntimeTargetFreshness `json:"freshness"`
}

func RegisterServiceRuntimeRoutes(r *httpx.Router, cfg ServiceRuntimeRouteConfig) {
	if cfg.Store == nil || cfg.Stacks == nil || cfg.Servers == nil {
		panic("RegisterServiceRuntimeRoutes: service, stack and server stores required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	h := serviceRuntimeHandlers{store: cfg.Store, stacks: cfg.Stacks, servers: cfg.Servers, jobs: cfg.Jobs, now: cfg.Now, orch: cfg.Orchestrator}
	r.GET("/api/v1/services", h.list)
	r.GET("/api/v1/services/{serviceId}", h.get)
	r.POST("/api/v1/registry/services/{serviceId}/actions", h.action)
	r.GET("/api/v1/registry/services/{serviceId}/logs", h.logs)
}

func (h *serviceRuntimeHandlers) action(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	if h.orch == nil || h.jobs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Managed service control is unavailable", nil)
	}
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	idempotencyKey := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if serviceID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return httpx.BadRequest(e, "Service ID and a bounded Idempotency-Key are required", nil)
	}
	var request serviceActionRequest
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return httpx.BadRequest(e, "Request body must contain one JSON document", nil)
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if request.Action != "start" && request.Action != "stop" && request.Action != serviceActionRestart && request.Action != "logs" {
		return httpx.BadRequest(e, "Action must be start, stop, restart, or logs", nil)
	}
	if request.Action != "logs" && !request.OwnerApproved {
		return httpx.BadRequest(e, "Service mutations require explicit Owner approval", nil)
	}
	request.Cursor = strings.TrimSpace(request.Cursor)
	if request.Action == "logs" {
		if request.Limit == 0 {
			request.Limit = 100
		}
		if request.Limit < 1 || request.Limit > 200 || len(request.Cursor) > 2048 {
			return httpx.BadRequest(e, "Service logs require limit 1..200 and a bounded cursor", nil)
		}
	} else if request.Limit != 0 || request.Cursor != "" {
		return httpx.BadRequest(e, "limit and cursor are only valid for logs", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	service, err := h.store.GetServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load service", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to authorize service", nil)
	}
	placement := serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	if placement.TargetKind != serviceregistry.TargetKindServer {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service runtime target is not server-bound", map[string]any{"target_kind": placement.TargetKind})
	}
	server, err := h.servers.GetServerRuntime(e.Request.Context(), tenantID, service.ServerID)
	if err != nil || server == nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service server is unavailable", nil)
	}
	if request.ExpectedInventoryRevision <= 0 || request.ExpectedInventoryRevision != server.InventoryRevision || metadataInt64(service.Metadata, "inventory_revision") != server.InventoryRevision {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Inventory revision is stale", map[string]any{"current_inventory_revision": server.InventoryRevision})
	}
	if service.ObservedAt == nil || h.now().UTC().Sub(service.ObservedAt.UTC()) > runtimehealth.FreshHeartbeatWindow {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service observation is stale", nil)
	}
	if server.ConnectionState != string(serverregistry.ConnectionConnected) && server.ConnectionState != string(serverregistry.ConnectionDegraded) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service agent is not connected", nil)
	}
	if !containsString(canonicalServiceActions(service.Capabilities), request.Action) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Service action is not allowed", nil)
	}
	if request.Action == "stop" && (service.ServiceKey == "base" || service.ServiceKey == "auth" || service.ServiceKey == "id") {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Critical control-plane services cannot be stopped", map[string]any{"reason_code": "critical_control_plane"})
	}
	digestBytes := sha256.Sum256([]byte(strings.Join([]string{
		tenantID, serviceID, request.Action, strconv.FormatInt(request.ExpectedInventoryRevision, 10),
		strconv.FormatBool(request.OwnerApproved), strconv.Itoa(request.Limit), request.Cursor,
	}, "\x00")))
	digest := hex.EncodeToString(digestBytes[:])
	stackKit := strings.TrimSpace(strings.Split(service.StackKitVersion, "@")[0])
	if stackKit == "" {
		stackKit = firstNonEmptyString(stringFromAnyMap(stack.Config, "stackkit"), stringFromAnyMap(stack.Config, "kit"))
	}
	operation := map[string]string{"start": jobs.StackKitLifecycleServiceStart, "stop": jobs.StackKitLifecycleServiceStop, serviceActionRestart: jobs.StackKitLifecycleServiceRestart, "logs": jobs.StackKitLifecycleServiceLogs}[request.Action]
	lifecycleRequest := jobs.StackKitLifecycleRequest{
		StackID: stack.ID, TenantID: tenantID, OwnerID: ownerID, AgentID: server.WorkerID,
		Operation: operation, OwnerApproved: request.Action != "logs", StackKit: stackKit, ServiceKey: service.ServiceKey,
		LogTail: int32(request.Limit), LogCursor: request.Cursor, ServiceID: service.ID,
		DurableJobID: jobs.StackKitServiceActionJobID(tenantID, ownerID, idempotencyKey), ServiceActionDigest: digest,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, getErr := h.jobs.GetJob(e.Request.Context(), tenantID, lifecycleRequest.DurableJobID); getErr == nil {
		if !jobs.MatchesStackKitServiceActionReceipt(existing.Result, lifecycleRequest) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key was already used for a different request", nil)
		}
		if !strings.EqualFold(existing.State, "pending") {
			return httpx.Success(e, http.StatusAccepted, serviceActionResponse{
				JobID: existing.ID, ServiceID: service.ID, Action: request.Action, Status: serviceActionJobStatus(existing.State),
			})
		}
	} else if !errors.Is(getErr, controlplane.ErrNotFound) {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to inspect service action replay", nil)
	}
	if conflictID, conflictErr := h.activeServiceActionJob(e.Request.Context(), tenantID, stack.ID, service.ID, lifecycleRequest.DurableJobID); conflictErr != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to inspect active service actions", nil)
	} else if conflictID != "" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "A service action is already active", map[string]any{"job_id": conflictID})
	}
	jobID, err := h.orch.EnqueueStackKitLifecycle(e.Request.Context(), lifecycleRequest)
	if err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key was already used for a different request", nil)
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to enqueue managed service action", map[string]any{"reason": err.Error()})
	}
	response := serviceActionResponse{JobID: jobID, ServiceID: service.ID, Action: request.Action, Status: "queued"}
	return httpx.Success(e, http.StatusAccepted, response)
}

func serviceActionJobStatus(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "pending", "waiting":
		return "queued"
	case "running", "completed", "failed", "cancelled", "canceled":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return "queued"
	}
}

func (h *serviceRuntimeHandlers) activeServiceActionJob(ctx context.Context, tenantID, stackID, serviceID, excludeJobID string) (string, error) {
	rows, err := h.jobs.ListJobsByStack(ctx, tenantID, stackID, 100)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if row.ID == excludeJobID {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(row.State))
		if state != "pending" && state != "running" && state != "waiting" {
			continue
		}
		receipt, _ := row.Result["service_action_receipt"].(map[string]any)
		if stringFromAnyMap(receipt, "service_id") == serviceID {
			return row.ID, nil
		}
	}
	return "", nil
}

func (h *serviceRuntimeHandlers) logs(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	if h.jobs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service logs are unavailable", nil)
	}
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	if serviceID == "" {
		return httpx.BadRequest(e, "Service ID is required", nil)
	}
	limit := 100
	if raw := strings.TrimSpace(e.Request.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			return httpx.BadRequest(e, "limit must be between 1 and 200", nil)
		}
		limit = parsed
	}
	cursor := strings.TrimSpace(e.Request.URL.Query().Get("cursor"))
	if len(cursor) > 2048 {
		return httpx.BadRequest(e, "cursor is too long", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	service, err := h.store.GetServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load service", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to authorize service", nil)
	}
	rows, err := h.jobs.ListJobsByStack(e.Request.Context(), tenantID, service.StackID, 100)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to load service logs", nil)
	}
	for _, row := range rows {
		receipt, _ := row.Result["service_action_receipt"].(map[string]any)
		if stringFromAnyMap(receipt, "service_id") != serviceID || stringFromAnyMap(receipt, "action") != "logs" ||
			stringFromAnyMap(receipt, "log_cursor") != cursor {
			continue
		}
		if strings.EqualFold(row.State, "pending") || strings.EqualFold(row.State, "running") || strings.EqualFold(row.State, "waiting") {
			return httpx.Success(e, http.StatusAccepted, serviceLogsResponse{
				ServiceID: serviceID, JobID: row.ID, Status: serviceActionJobStatus(row.State), Entries: []serviceLogEntry{},
			})
		}
		if !strings.EqualFold(row.State, "completed") {
			continue
		}
		page, _ := row.Result["service_logs"].(map[string]any)
		entries := sanitizedServiceLogEntries(page["entries"], limit)
		return httpx.Success(e, http.StatusOK, serviceLogsResponse{
			ServiceID: serviceID, JobID: row.ID, Status: "completed", Entries: entries,
			NextCursor: firstNonEmptyString(stringFromAnyMap(page, "nextCursor"), stringFromAnyMap(page, "next_cursor")),
		})
	}
	return httpx.Success(e, http.StatusOK, serviceLogsResponse{ServiceID: serviceID, Status: "empty", Entries: []serviceLogEntry{}})
}

func sanitizedServiceLogEntries(value any, limit int) []serviceLogEntry {
	rows, _ := value.([]any)
	entries := make([]serviceLogEntry, 0, min(len(rows), limit))
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		timestamp, err := time.Parse(time.RFC3339Nano, stringFromAnyMap(row, "timestamp"))
		if err != nil {
			continue
		}
		message := strings.TrimSpace(secrets.Redact(stringFromAnyMap(row, "message")))
		if len(message) > 16*1024 {
			message = message[:16*1024]
		}
		entries = append(entries, serviceLogEntry{Timestamp: timestamp.UTC(), Message: message})
		if len(entries) == limit {
			break
		}
	}
	return entries
}

func metadataInt64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	}
	return 0
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (h serviceRuntimeHandlers) list(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID := requestTenantID(e, ownerID)
	stackID := strings.TrimSpace(e.Request.URL.Query().Get("techstack_id"))
	serverID := strings.TrimSpace(e.Request.URL.Query().Get("server_id"))
	ownedStacks, err := h.ownedStackIDs(e, tenantID, ownerID, isAdmin)
	if err != nil {
		return err
	}
	if stackID != "" && !ownedStacks[stackID] {
		return httpx.NotFound(e, "Stack not found")
	}
	rows, err := h.store.ListServiceRuntimes(e.Request.Context(), tenantID, stackID, serverID)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service inventory is unavailable", nil)
	}
	servers, serverMapErr := h.serverMap(e, tenantID, stackID)
	if serverMapErr != nil {
		// The server aggregate is the placement authority. Returning an empty
		// map here would turn a tenant/FGA/store outage into a false offline
		// service fleet, so preserve it as a typed unavailable state instead.
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}
	items := make([]serviceRuntimeResponse, 0, len(rows))
	for _, row := range rows {
		if !ownedStacks[row.StackID] {
			continue
		}
		items = append(items, h.response(row, servers[row.ServerID]))
	}
	return httpx.Success(e, http.StatusOK, items)
}

func (h serviceRuntimeHandlers) get(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	serviceID := strings.TrimSpace(e.Request.PathValue("serviceId"))
	if serviceID == "" {
		return httpx.BadRequest(e, "Service ID is required", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	service, err := h.store.GetServiceRuntime(e.Request.Context(), tenantID, serviceID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service inventory is unavailable", nil)
	}
	stack, err := h.stacks.GetStack(e.Request.Context(), tenantID, service.StackID)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && !isAdmin && stack.OwnerSubjectID != ownerID) {
		return httpx.NotFound(e, "Service not found")
	}
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service authorization is unavailable", nil)
	}
	var server *controlplane.ServerRuntime
	if serviceregistry.NormalizePlacement(service.ServerID, service.Placement).TargetKind == serviceregistry.TargetKindServer {
		server, err = h.servers.GetServerRuntime(e.Request.Context(), tenantID, service.ServerID)
		if err != nil || server == nil {
			// A server-bound service without readable server authority is not an
			// offline service. Keep the API honest about the unavailable inventory.
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service server inventory is unavailable", nil)
		}
	}
	return httpx.Success(e, http.StatusOK, h.response(*service, server))
}

func (h serviceRuntimeHandlers) ownedStackIDs(e *httpx.Event, tenantID, ownerID string, isAdmin bool) (map[string]bool, error) {
	stacks, err := h.stacks.ListStacksByTenant(e.Request.Context(), tenantID)
	if err != nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service authorization is unavailable", nil)
	}
	result := make(map[string]bool, len(stacks))
	for _, stack := range stacks {
		if isAdmin || stack.OwnerSubjectID == ownerID {
			result[stack.ID] = true
		}
	}
	return result, nil
}

func (h serviceRuntimeHandlers) serverMap(e *httpx.Event, tenantID, stackID string) (map[string]*controlplane.ServerRuntime, error) {
	rows, err := h.servers.ListServerRuntimesByTenant(e.Request.Context(), tenantID, stackID)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*controlplane.ServerRuntime, len(rows))
	for i := range rows {
		result[rows[i].ID] = &rows[i]
	}
	return result, nil
}

// response gates service state on the PERSISTED server connection dimension.
// The registry sweeper demotes stale/offline servers as durable writes, so the
// read path no longer recomputes heartbeat freshness inline; persisted state
// is returned and demoted servers mask their services as unknown.
func (h serviceRuntimeHandlers) response(service controlplane.ServiceRuntime, server *controlplane.ServerRuntime) serviceRuntimeResponse {
	placement := serviceregistry.NormalizePlacement(service.ServerID, service.Placement)
	observedState, healthState := service.ObservedState, service.HealthState
	reasonCode := ""
	access := sanitizedServiceAccess(service.Access)
	allowedActions := canonicalServiceActions(service.Capabilities)
	switch placement.TargetKind {
	case serviceregistry.TargetKindServer:
		connection := string(serverregistry.ConnectionOffline)
		if server != nil {
			connection = server.ConnectionState
		}
		if connection != string(serverregistry.ConnectionConnected) && connection != string(serverregistry.ConnectionDegraded) {
			observedState, healthState, reasonCode = monitoringStatusUnknown, monitoringStatusUnknown, "server_connection_"+connection
		}
	case serviceregistry.TargetKindUnknown:
		observedState, healthState, reasonCode = monitoringStatusUnknown, monitoringStatusUnknown, "service_placement_unknown"
		allowedActions = []string{}
	case serviceregistry.TargetKindManagedWorkload:
		// Provider-native controls are not implemented by this release. Preserve
		// measured state but do not advertise server/StackKit actions.
		allowedActions = []string{}
	}
	if reasonCode != "" {
		access = serviceAccess(serviceAccessUnavailable, "", "", reasonCode, "")
	}
	placementFreshness := serverRuntimeTargetFreshness{State: "unknown"}
	if placement.EvidenceRef != "" && placement.ObservedAt != nil {
		seconds := int64(h.now().UTC().Sub(placement.ObservedAt.UTC()).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		placementFreshness = serverRuntimeTargetFreshness{State: "recorded", AgeSeconds: &seconds}
	}
	provenance := map[string]interface{}{
		"definition_authority": serviceStackKits, "runtime_authority": serviceRuntimeAuthority, "observation_source": service.Source,
	}
	if backfill, _ := service.Metadata["backfill"].(bool); backfill {
		provenance["migration_mode"] = "read_through_backfill"
	}
	return serviceRuntimeResponse{
		ID: service.ID, TechstackID: service.StackID, ServerID: service.ServerID,
		TargetKind: string(placement.TargetKind),
		Placement: serviceRuntimePlacement{
			ProviderID: placement.ProviderID, ManagedTargetRef: placement.ManagedTargetRef,
			ProviderReceiptRef: placement.ProviderReceiptRef, SLAPolicyRef: placement.SLAPolicyRef,
			BackupPolicyRef: placement.BackupPolicyRef, EvidenceRef: placement.EvidenceRef,
			ObservedAt: placement.ObservedAt, Freshness: placementFreshness,
		},
		ServiceKey: service.ServiceKey, ServiceInstance: service.ServiceInstance,
		Name:            service.Name,
		ManagementState: string(serviceregistry.CanonicalManagementState(service.ManagementState)),
		DesiredState:    service.DesiredState, ObservedState: observedState,
		Health:          serviceRuntimeHealth{State: healthState, ObservedAt: service.ObservedAt, ReasonCode: reasonCode},
		StackKitVersion: service.StackKitVersion, Access: access,
		AllowedActions: allowedActions, Source: service.Source,
		InventoryRevision: metadataInt64(service.Metadata, "inventory_revision"),
		EvidenceRef:       stringFromAnyMap(service.Metadata, "evidence_ref"),
		Provenance:        provenance,
		CreatedAt:         service.CreatedAt, UpdatedAt: service.UpdatedAt,
	}
}

func sanitizedServiceAccess(access map[string]any) map[string]any {
	mode := stringFromAnyMap(access, serviceAccessModeKey)
	switch mode {
	case serviceAccessDirect:
		if address := safePublicServiceURL(stringFromAnyMap(access, serviceAccessURLKey)); address != "" {
			return serviceAccess(mode, address, boundedAccessValue(access, serviceAccessProfileRefKey), "", "")
		}
		observed, _ := boolValueFromAny(access[serviceAccessObservedKey])
		if observed && stringFromAnyMap(access, serviceAccessSourceKey) == serviceStackKitManifest {
			if address := safeObservedDirectServiceURL(stringFromAnyMap(access, serviceAccessURLKey)); address != "" {
				return serviceAccess(mode, address, boundedAccessValue(access, serviceAccessProfileRefKey), "", "")
			}
		}
	case serviceAccessRelay:
		address := safeKombifyRelayURL(stringFromAnyMap(access, serviceAccessURLKey))
		routeID := boundedAccessValue(access, "route_id")
		if address != "" && routeID != "" {
			return serviceAccess(mode, address, boundedAccessValue(access, serviceAccessProfileRefKey), "", routeID)
		}
	}
	return serviceAccess(serviceAccessUnavailable, "", "", firstNonEmptyString(boundedAccessValue(access, "reason_code"), "invalid_access_contract"), "")
}

func boundedAccessValue(access map[string]any, key string) string {
	value := strings.TrimSpace(stringFromAnyMap(access, key))
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
