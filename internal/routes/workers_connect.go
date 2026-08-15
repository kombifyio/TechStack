package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

type workerConnectRequest struct {
	ServerID       string `json:"server_id"`
	RuntimeAgentID string `json:"runtime_agent_id"`
	StackID        string `json:"stack_id"`
	LeaseID        string `json:"lease_id"`
	Hostname       string `json:"hostname"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Mode           string `json:"mode"`
	ConnectionMode string `json:"connection_mode"`
	Provider       string `json:"provider"`
}

type workerConnectCommand struct {
	TenantID           string
	OwnerID            string
	Request            workerConnectRequest
	EnrollmentSource   string
	EnrollmentMetadata map[string]any
	IdempotencyKey     string
	CredentialPolicy   workerConnectCredentialPolicy
}

type workerConnectCredentialPolicy string

const (
	workerConnectCredentialStrict workerConnectCredentialPolicy = "strict"
	workerConnectCredentialEnsure workerConnectCredentialPolicy = "ensure"
	workerConnectCredentialDefer  workerConnectCredentialPolicy = "defer"

	workerCredentialIdempotencyDigestDomain = "kombify-techstack/worker-credential-idempotency/v1"
	workerCredentialCASAttempts             = 8
	workerIdempotencyKeyMinLength           = 8
	workerIdempotencyKeyMaxLength           = 256
)

type workerConnectResult struct {
	Enrollment       workerEnrollmentContext
	WorkerCreated    bool
	ServerCreated    bool
	CredentialIssued bool
}

func (h workerRouteHandlers) connectServer(e *httpx.Event) error {
	if h.wst == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Worker store is required for server connect", nil)
	}
	ownerID, authErr := requireWorkerOwner(e)
	if authErr != nil || ownerID == "" {
		return authErr
	}
	req, decodeErr := readWorkerConnectRequest(e)
	if decodeErr != nil {
		return httpx.BadRequest(e, decodeErr.Error(), nil)
	}
	idempotencyKey, idempotencyErr := requiredWorkerIdempotencyKey(e.Request)
	if idempotencyErr != nil {
		return httpx.BadRequest(e, idempotencyErr.Error(), nil)
	}
	tenantID := requestTenantID(e, ownerID)
	stackID := strings.TrimSpace(req.StackID)
	leaseID := strings.TrimSpace(req.LeaseID)
	if leaseID != "" {
		if !h.validateManagedRuntimeLeaseForConnect(e, tenantID, ownerID, stackID, leaseID) {
			return nil
		}
	}
	result, err := h.connectServerApplication(e.Request.Context(), workerConnectCommand{
		TenantID: tenantID, OwnerID: ownerID, Request: req,
		EnrollmentSource: "connect-request", IdempotencyKey: idempotencyKey,
		CredentialPolicy: workerConnectCredentialStrict,
	})
	if err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Server enrollment conflicts with an existing runtime identity", nil)
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create server enrollment", nil)
	}
	return httpx.Success(e, http.StatusOK, h.workerEnrollmentResponse(e, result.Enrollment))
}

// connectServerApplication is the single application-level composition for
// user-facing Connect and trusted demo reconciliation. It owns the stable
// worker/server binding and one-way credential persistence; authenticated
// heartbeat/inventory routes remain the sole observation and service authority.
func (h workerRouteHandlers) connectServerApplication(ctx context.Context, command workerConnectCommand) (*workerConnectResult, error) {
	if h.wst == nil {
		return nil, fmt.Errorf("worker store is required")
	}
	tenantID := strings.TrimSpace(command.TenantID)
	ownerID := strings.TrimSpace(command.OwnerID)
	req := normalizeWorkerConnectRequest(command.Request)
	stackID := strings.TrimSpace(req.StackID)
	leaseID := strings.TrimSpace(req.LeaseID)
	if tenantID == "" || ownerID == "" {
		return nil, fmt.Errorf("%w: tenant and owner are required", controlplane.ErrConflict)
	}
	serverID := strings.TrimSpace(req.ServerID)
	if leaseID != "" {
		serverID = runtimeidentity.LeaseServerID(leaseID)
	}
	if serverID == "" {
		serverID = h.plannedServerIDForStack(ctx, tenantID, ownerID, stackID)
	}
	if serverID == "" {
		serverID = "server_" + stableRouteID(tenantID, ownerID, stackID, req.Hostname)
	}
	runtimeAgentID := strings.TrimSpace(req.RuntimeAgentID)
	if runtimeAgentID == "" {
		runtimeAgentID = "runtime_" + stableRouteID(tenantID, serverID)
	}

	serverMissing := false
	serverNeedsEnrollment := false
	if h.serverStore != nil {
		existingServer, serverErr := h.serverStore.GetServerRuntime(ctx, tenantID, serverID)
		if serverErr != nil && !errors.Is(serverErr, controlplane.ErrNotFound) {
			return nil, serverErr
		}
		serverMissing = errors.Is(serverErr, controlplane.ErrNotFound)
		if !serverMissing {
			if canClaimPlannedServer(*existingServer, tenantID, ownerID, stackID, serverID) {
				serverNeedsEnrollment = true
			} else if err := validateServerConnectBinding(*existingServer, tenantID, ownerID, stackID, serverID, runtimeAgentID); err != nil {
				return nil, err
			}
		}
	}

	result := &workerConnectResult{
		ServerCreated: serverMissing,
		Enrollment: workerEnrollmentContext{
			WorkerID: runtimeAgentID, ServerID: serverID, RuntimeAgentID: runtimeAgentID,
			TenantID: tenantID, OwnerID: ownerID, StackID: stackID, LeaseID: leaseID, Accepted: true,
		},
	}
	now := time.Now().UTC()
	connectCapabilities := map[string]any{
		"server_id":         serverID,
		"runtime_agent_id":  runtimeAgentID,
		"mode":              firstNonEmpty(req.Mode, "advanced"),
		"connection_mode":   req.ConnectionMode,
		"liveness_required": "guard_inventory",
	}
	if leaseID != "" {
		connectCapabilities[runtimeLeaseIDKey] = leaseID
	}
	// Claim the immutable tenant+worker binding before any server projection or
	// credential write. This is a declarative enrollment, not a heartbeat:
	// LastSeenAt remains nil until authenticated Guard traffic arrives.
	workerProjection := controlplane.Worker{
		ID:             runtimeAgentID,
		TenantID:       tenantID,
		StackID:        stackID,
		Hostname:       firstNonEmpty(req.Hostname, serverID),
		OS:             req.OS,
		Arch:           req.Arch,
		Status:         "pending",
		Approved:       true,
		ApprovedAt:     &now,
		LastSeenAt:     nil,
		Type:           "runtime",
		Provider:       req.Provider,
		OwnerSubjectID: ownerID,
		Capabilities:   connectCapabilities,
	}
	enrollmentStore, ok := h.workerEnrollmentStore()
	if !ok {
		return nil, errors.New("worker enrollment store is required")
	}
	workerClaim, workerErr := enrollmentStore.ClaimWorkerEnrollment(ctx, controlplane.WorkerEnrollmentClaim{
		Binding: controlplane.WorkerEnrollmentBinding{
			TenantID: tenantID, WorkerID: runtimeAgentID, OwnerSubjectID: ownerID,
			StackID: stackID, ServerID: serverID, RuntimeAgentID: runtimeAgentID, LeaseID: leaseID,
		},
		Worker: workerProjection,
	})
	if workerErr != nil {
		return nil, workerErr
	}
	if workerClaim == nil || workerClaim.Worker == nil {
		return nil, errors.New("worker enrollment claim returned no worker")
	}
	worker := workerClaim.Worker
	result.WorkerCreated = workerClaim.Created
	if h.serverStore != nil && (serverMissing || serverNeedsEnrollment) {
		source := firstNonEmpty(command.EnrollmentSource, "connect-request")
		if err := h.projectServerEnrollmentWithMetadata(
			ctx, *worker, serverID, leaseID, time.Now().UTC(), source, command.EnrollmentMetadata,
		); err != nil {
			return nil, err
		}
	}

	policy := command.CredentialPolicy
	if policy == "" {
		policy = workerConnectCredentialStrict
	}
	if policy != workerConnectCredentialDefer {
		requestDigest, digestErr := workerConnectRequestDigest(
			tenantID, ownerID, serverID, runtimeAgentID, req,
		)
		if digestErr != nil {
			return nil, digestErr
		}
		credential, credentialErr := h.ensureWorkerCredential(ctx, workerCredentialRequest{
			TenantID: tenantID, OwnerID: ownerID, StackID: stackID,
			ServerID: serverID, RuntimeAgentID: runtimeAgentID,
			IdempotencyKey: command.IdempotencyKey, RequestDigest: requestDigest,
			AllowExisting: policy == workerConnectCredentialEnsure,
		})
		if credentialErr != nil {
			return nil, credentialErr
		}
		result.CredentialIssued = credential.Changed
		result.Enrollment.AgentToken = credential.Token
		result.Enrollment.CredentialGeneration = credential.Generation
	} else if worker != nil {
		state, stateErr := controlplane.WorkerCredentialStateFromWorker(*worker)
		if stateErr != nil {
			return nil, stateErr
		}
		result.Enrollment.CredentialGeneration = state.Generation
	}
	return result, nil
}

type workerCredentialRequest struct {
	TenantID           string
	OwnerID            string
	StackID            string
	ServerID           string
	RuntimeAgentID     string
	IdempotencyKey     string
	RequestDigest      string
	ExpectedGeneration int64
	Rotate             bool
	AllowExisting      bool
}

type workerCredentialResult struct {
	Token      string
	Generation int64
	Changed    bool
}

func (h workerRouteHandlers) ensureWorkerCredential(ctx context.Context, request workerCredentialRequest) (workerCredentialResult, error) {
	var result workerCredentialResult
	idempotencyKey, err := validateWorkerIdempotencyKey(request.IdempotencyKey)
	if err != nil {
		return result, err
	}
	request.IdempotencyKey = idempotencyKey
	request.RequestDigest = strings.TrimSpace(request.RequestDigest)
	if request.RequestDigest == "" {
		return result, fmt.Errorf("%w: credential request digest is required", controlplane.ErrConflict)
	}
	secret := h.configuredWorkerCredentialSecret()
	idempotencyDigest, err := workerauth.KeyedDigest(
		secret, workerCredentialIdempotencyDigestDomain, idempotencyKey,
	)
	if err != nil {
		return result, err
	}
	store, ok := h.workerCredentialStore()
	if !ok {
		return result, errors.New("worker credential store is required")
	}

	for attempt := 0; attempt < workerCredentialCASAttempts; attempt++ {
		worker, getErr := h.wst.GetWorker(ctx, request.TenantID, request.RuntimeAgentID)
		if getErr != nil {
			return result, getErr
		}
		state, stateErr := controlplane.WorkerCredentialStateFromWorker(*worker)
		if stateErr != nil {
			return result, stateErr
		}
		if state.TokenSHA256 != "" &&
			state.IdempotencySHA256 == idempotencyDigest &&
			state.RequestSHA256 != request.RequestDigest {
			return result, fmt.Errorf("%w: idempotency key was already used for a different worker credential request", controlplane.ErrConflict)
		}
		if !request.Rotate && request.AllowExisting && state.TokenSHA256 != "" {
			return workerCredentialResult{Generation: state.Generation}, nil
		}
		if state.TokenSHA256 != "" &&
			state.IdempotencySHA256 == idempotencyDigest &&
			state.RequestSHA256 == request.RequestDigest {
			token, deriveErr := deriveWorkerCredential(secret, request, state.Generation)
			if deriveErr != nil {
				return result, deriveErr
			}
			if workerauth.SHA256Hex(token) != state.TokenSHA256 {
				return result, fmt.Errorf("%w: configured worker token secret cannot reproduce the active credential", controlplane.ErrConflict)
			}
			return workerCredentialResult{Token: token, Generation: state.Generation}, nil
		}
		if request.Rotate {
			if state.Generation != request.ExpectedGeneration {
				return result, fmt.Errorf("%w: stale worker credential generation", controlplane.ErrConflict)
			}
		} else if state.TokenSHA256 != "" {
			return result, fmt.Errorf("%w: idempotency key or request does not match the active worker credential", controlplane.ErrConflict)
		} else if state.Generation != 0 {
			return result, fmt.Errorf("%w: tokenless worker has non-zero credential generation", controlplane.ErrConflict)
		}

		nextGeneration := state.Generation + 1
		token, deriveErr := deriveWorkerCredential(secret, request, nextGeneration)
		if deriveErr != nil {
			return result, deriveErr
		}
		next := controlplane.WorkerCredentialState{
			TokenSHA256: workerauth.SHA256Hex(token), IdempotencySHA256: idempotencyDigest,
			RequestSHA256: request.RequestDigest, Generation: nextGeneration,
		}
		if _, casErr := store.CompareAndSwapWorkerCredential(ctx, controlplane.WorkerCredentialCAS{
			TenantID: request.TenantID, WorkerID: request.RuntimeAgentID,
			Expected: state, Next: next,
		}); casErr == nil {
			return workerCredentialResult{Token: token, Generation: nextGeneration, Changed: true}, nil
		} else if !errors.Is(casErr, controlplane.ErrConflict) {
			return result, casErr
		}
	}
	return result, fmt.Errorf("%w: concurrent worker credential update did not converge", controlplane.ErrConflict)
}

func deriveWorkerCredential(secret []byte, request workerCredentialRequest, generation int64) (string, error) {
	return workerauth.DeriveOpaqueToken(secret, workerauth.OpaqueTokenContext{
		TenantID: request.TenantID, OwnerID: request.OwnerID, StackID: request.StackID,
		ServerID: request.ServerID, RuntimeAgentID: request.RuntimeAgentID,
		RequestDigest: request.RequestDigest, IdempotencyKey: request.IdempotencyKey,
		Generation: generation,
	})
}

func (h workerRouteHandlers) workerCredentialStore() (controlplane.WorkerCredentialStore, bool) {
	if h.credentialStore != nil {
		return h.credentialStore, true
	}
	store, ok := h.wst.(controlplane.WorkerCredentialStore)
	return store, ok
}

func (h workerRouteHandlers) workerEnrollmentStore() (controlplane.WorkerEnrollmentStore, bool) {
	if h.enrollmentStore != nil {
		return h.enrollmentStore, true
	}
	store, ok := h.wst.(controlplane.WorkerEnrollmentStore)
	return store, ok
}

func (h workerRouteHandlers) configuredWorkerCredentialSecret() []byte {
	if len(h.credentialSecret) > 0 {
		return append([]byte(nil), h.credentialSecret...)
	}
	return workerauth.SecretFromEnv()
}

func workerConnectRequestDigest(tenantID, ownerID, serverID, runtimeAgentID string, req workerConnectRequest) (string, error) {
	return workerCredentialRequestDigest(struct {
		Operation      string `json:"operation"`
		TenantID       string `json:"tenant_id"`
		OwnerID        string `json:"owner_id"`
		ServerID       string `json:"server_id"`
		RuntimeAgentID string `json:"runtime_agent_id"`
		StackID        string `json:"stack_id"`
		LeaseID        string `json:"lease_id"`
		Hostname       string `json:"hostname"`
		OS             string `json:"os"`
		Arch           string `json:"arch"`
		Mode           string `json:"mode"`
		ConnectionMode string `json:"connection_mode"`
		Provider       string `json:"provider"`
	}{
		Operation: "connect", TenantID: strings.TrimSpace(tenantID),
		OwnerID: strings.TrimSpace(ownerID), ServerID: strings.TrimSpace(serverID),
		RuntimeAgentID: strings.TrimSpace(runtimeAgentID), StackID: strings.TrimSpace(req.StackID),
		LeaseID:  strings.TrimSpace(req.LeaseID),
		Hostname: strings.ToLower(firstNonEmpty(req.Hostname, serverID)),
		OS:       strings.ToLower(strings.TrimSpace(req.OS)), Arch: strings.ToLower(strings.TrimSpace(req.Arch)),
		Mode:           strings.ToLower(firstNonEmpty(req.Mode, "advanced")),
		ConnectionMode: strings.ToLower(strings.TrimSpace(req.ConnectionMode)),
		Provider:       strings.ToLower(strings.TrimSpace(req.Provider)),
	})
}

func workerCredentialRequestDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func requiredWorkerIdempotencyKey(request *http.Request) (string, error) {
	if request == nil {
		return "", errors.New("exactly one Idempotency-Key header is required")
	}
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", errors.New("exactly one Idempotency-Key header is required")
	}
	return validateWorkerIdempotencyKey(values[0])
}

func validateWorkerIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < workerIdempotencyKeyMinLength || len(value) > workerIdempotencyKeyMaxLength {
		return "", fmt.Errorf("Idempotency-Key must contain %d to %d characters", workerIdempotencyKeyMinLength, workerIdempotencyKeyMaxLength)
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return "", errors.New("Idempotency-Key must contain visible ASCII characters only")
		}
	}
	return value, nil
}

type workerCredentialRotateRequest struct {
	ExpectedCredentialGeneration *int64 `json:"expected_credential_generation"`
}

func (h workerRouteHandlers) rotateServerCredential(e *httpx.Event) error {
	ownerID, authErr := requireWorkerOwner(e)
	if authErr != nil || ownerID == "" {
		return authErr
	}
	idempotencyKey, idempotencyErr := requiredWorkerIdempotencyKey(e.Request)
	if idempotencyErr != nil {
		return httpx.BadRequest(e, idempotencyErr.Error(), nil)
	}
	var req workerCredentialRotateRequest
	if decodeErr := json.NewDecoder(e.Request.Body).Decode(&req); decodeErr != nil {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	if req.ExpectedCredentialGeneration == nil || *req.ExpectedCredentialGeneration < 0 {
		return httpx.BadRequest(e, "expected_credential_generation must be a non-negative integer", nil)
	}
	tenantID := requestTenantID(e, ownerID)
	serverID := strings.TrimSpace(e.Request.PathValue("id"))
	server, serverErr := h.serverStore.GetServerRuntime(e.Request.Context(), tenantID, serverID)
	if errors.Is(serverErr, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Server not found")
	}
	if serverErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load server credential binding", nil)
	}
	if strings.TrimSpace(server.OwnerSubjectID) != ownerID || strings.TrimSpace(server.WorkerID) == "" {
		return httpx.Forbidden(e, "Not allowed")
	}
	if bindingErr := validateServerConnectBinding(
		*server, tenantID, ownerID, server.StackID, server.ID, server.WorkerID,
	); bindingErr != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Server credential binding conflicts with runtime inventory", nil)
	}
	worker, workerErr := h.wst.GetWorker(e.Request.Context(), tenantID, server.WorkerID)
	if errors.Is(workerErr, controlplane.ErrNotFound) {
		return httpx.NotFound(e, "Runtime agent not found")
	}
	if workerErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load runtime agent credential binding", nil)
	}
	if bindingErr := validateWorkerConnectBinding(
		*worker, tenantID, ownerID, server.StackID, server.ID, server.WorkerID,
	); bindingErr != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Server credential binding conflicts with runtime inventory", nil)
	}
	requestDigest, digestErr := workerCredentialRotationRequestDigest(
		tenantID, ownerID, server.StackID, server.ID, server.WorkerID,
		*req.ExpectedCredentialGeneration,
	)
	if digestErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to bind credential recovery request", nil)
	}
	credential, credentialErr := h.ensureWorkerCredential(e.Request.Context(), workerCredentialRequest{
		TenantID: tenantID, OwnerID: ownerID, StackID: server.StackID,
		ServerID: server.ID, RuntimeAgentID: server.WorkerID,
		IdempotencyKey: idempotencyKey, RequestDigest: requestDigest,
		ExpectedGeneration: *req.ExpectedCredentialGeneration, Rotate: true,
	})
	if errors.Is(credentialErr, controlplane.ErrConflict) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Worker credential generation or idempotency request conflicts with the active credential", nil)
	}
	if credentialErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to rotate runtime agent credential", nil)
	}
	return httpx.Success(e, http.StatusOK, h.workerEnrollmentResponse(e, workerEnrollmentContext{
		WorkerID: server.WorkerID, ServerID: server.ID, RuntimeAgentID: server.WorkerID,
		TenantID: tenantID, OwnerID: ownerID, StackID: server.StackID,
		LeaseID: runtimeLeaseIDFromMetadata(worker.Capabilities), Accepted: true,
		AgentToken: credential.Token, CredentialGeneration: credential.Generation,
	}))
}

func workerCredentialRotationRequestDigest(
	tenantID, ownerID, stackID, serverID, runtimeAgentID string,
	expectedGeneration int64,
) (string, error) {
	return workerCredentialRequestDigest(struct {
		Operation                    string `json:"operation"`
		TenantID                     string `json:"tenant_id"`
		OwnerID                      string `json:"owner_id"`
		StackID                      string `json:"stack_id"`
		ServerID                     string `json:"server_id"`
		RuntimeAgentID               string `json:"runtime_agent_id"`
		ExpectedCredentialGeneration int64  `json:"expected_credential_generation"`
	}{
		Operation: "rotate", TenantID: strings.TrimSpace(tenantID), OwnerID: strings.TrimSpace(ownerID),
		StackID: strings.TrimSpace(stackID), ServerID: strings.TrimSpace(serverID),
		RuntimeAgentID:               strings.TrimSpace(runtimeAgentID),
		ExpectedCredentialGeneration: expectedGeneration,
	})
}

func canClaimPlannedServer(server controlplane.ServerRuntime, tenantID, ownerID, stackID, serverID string) bool {
	return strings.TrimSpace(server.ID) == serverID &&
		strings.TrimSpace(server.TenantID) == tenantID &&
		strings.TrimSpace(server.OwnerSubjectID) == ownerID &&
		strings.TrimSpace(server.StackID) == stackID &&
		strings.TrimSpace(server.WorkerID) == "" &&
		strings.TrimSpace(server.NodeID) == "" &&
		strings.TrimSpace(server.LifecycleState) == string(serverregistry.LifecyclePlanned)
}

func validateWorkerConnectBinding(worker controlplane.Worker, tenantID, ownerID, stackID, serverID, runtimeAgentID string) error {
	if strings.TrimSpace(worker.ID) != runtimeAgentID ||
		strings.TrimSpace(worker.TenantID) != tenantID ||
		strings.TrimSpace(worker.OwnerSubjectID) != ownerID ||
		strings.TrimSpace(worker.StackID) != stackID ||
		strings.TrimSpace(stringFromAny(worker.Capabilities["server_id"])) != serverID ||
		strings.TrimSpace(stringFromAny(worker.Capabilities["runtime_agent_id"])) != runtimeAgentID {
		return fmt.Errorf("%w: runtime agent is already bound to another tenant, owner, stack, or server", controlplane.ErrConflict)
	}
	return nil
}

func validateServerConnectBinding(server controlplane.ServerRuntime, tenantID, ownerID, stackID, serverID, runtimeAgentID string) error {
	if strings.TrimSpace(server.ID) != serverID ||
		strings.TrimSpace(server.TenantID) != tenantID ||
		strings.TrimSpace(server.OwnerSubjectID) != ownerID ||
		strings.TrimSpace(server.StackID) != stackID ||
		strings.TrimSpace(server.WorkerID) != runtimeAgentID ||
		strings.TrimSpace(server.NodeID) != serverID {
		return fmt.Errorf("%w: server is already bound to another tenant, owner, stack, or runtime agent", controlplane.ErrConflict)
	}
	return nil
}

func (h workerRouteHandlers) plannedServerIDForStack(ctx context.Context, tenantID, ownerID, stackID string) string {
	if h.serverStore == nil || strings.TrimSpace(stackID) == "" {
		return ""
	}
	servers, err := h.serverStore.ListServerRuntimesByTenant(ctx, tenantID, stackID)
	if err != nil {
		return ""
	}
	for _, server := range servers {
		if server.OwnerSubjectID == ownerID && server.LifecycleState != "decommissioned" {
			return server.ID
		}
	}
	return ""
}

// validateManagedRuntimeLeaseForConnect keeps the user-facing connect route
// from minting a lease-bound agent token for an arbitrary, inactive, or
// cross-tenant lease. The configured lease lister is the authority; if it is
// absent or unavailable, the managed enrollment fails closed.
func (h workerRouteHandlers) validateManagedRuntimeLeaseForConnect(e *httpx.Event, tenantID, ownerID, stackID, leaseID string) bool {
	if h.managedRuntimeLeases == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Managed runtime lease authority is unavailable; enrollment was not issued", nil)
		return false
	}
	inventory, ok := h.managedRuntimeLeases.(managedRuntimeLeaseInventoryLister)
	if !ok {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Managed runtime execution authority is unavailable; enrollment was not issued", nil)
		return false
	}
	records, err := inventory.ListInventoryByTenant(e.Request.Context(), strings.TrimSpace(tenantID))
	if err != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Managed runtime lease authority could not be verified; enrollment was not issued", nil)
		return false
	}
	var matched *vmleases.LeaseInventoryRecord
	for index := range records {
		if strings.TrimSpace(string(records[index].Lease.ID)) != strings.TrimSpace(leaseID) {
			continue
		}
		if matched != nil {
			_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "Managed runtime lease authority returned duplicate inventory; enrollment was not issued", nil)
			return false
		}
		matched = &records[index]
	}
	if matched == nil {
		_ = httpx.Forbidden(e, "Managed runtime lease was not found for this tenant")
		return false
	}
	lease := matched.Lease
	if strings.TrimSpace(lease.Subject.OrgID) != strings.TrimSpace(tenantID) ||
		!managedRuntimeLeaseVisibleToOwner(lease, tenantID, ownerID) ||
		!managedRuntimeLeaseActive(lease) ||
		!monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) ||
		strings.TrimSpace(lease.Metadata["stack_id"]) != strings.TrimSpace(stackID) {
		_ = httpx.Forbidden(e, "Managed runtime lease does not match this tenant, owner, stack, or lifecycle")
		return false
	}
	if !matched.NativeActive() {
		_ = httpx.Forbidden(e, "Managed runtime lease is not active under TechStack provider control")
		return false
	}
	return true
}

func readWorkerConnectRequest(e *httpx.Event) (workerConnectRequest, error) {
	var req workerConnectRequest
	if e == nil || e.Request == nil || e.Request.Body == nil {
		return req, nil
	}
	err := json.NewDecoder(e.Request.Body).Decode(&req)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return req, err
	}
	return normalizeWorkerConnectRequest(req), nil
}

func normalizeWorkerConnectRequest(req workerConnectRequest) workerConnectRequest {
	req.ServerID = strings.TrimSpace(req.ServerID)
	req.RuntimeAgentID = strings.TrimSpace(req.RuntimeAgentID)
	req.StackID = strings.TrimSpace(req.StackID)
	req.LeaseID = strings.TrimSpace(req.LeaseID)
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.OS = strings.TrimSpace(req.OS)
	req.Arch = strings.TrimSpace(req.Arch)
	req.Mode = strings.TrimSpace(req.Mode)
	req.ConnectionMode = strings.TrimSpace(req.ConnectionMode)
	req.Provider = strings.TrimSpace(req.Provider)
	return req
}
