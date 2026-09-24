// Package trust provides handlers for worker-enrollment pairing tokens.
package trust

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/google/uuid"
)

// pairingTokenRequest is the add-server wizard's create-pairing-token body.
type pairingTokenRequest struct {
	Name                    string   `json:"name"`
	ExpiryMinutes           *int     `json:"expiry_minutes"`
	StackID                 string   `json:"stack_id"`
	ServerProvisioningMode  string   `json:"server_provisioning_mode"`
	EnvironmentClass        string   `json:"environment_class"`
	NodeRole                string   `json:"node_role"`
	StackKit                string   `json:"stackkit"`
	Services                []string `json:"services"`
	SpecNodeID              string   `json:"spec_node_id"`
	ServerRemoteHost        string   `json:"server_remote_host"`
	ServerRemotePort        *int     `json:"server_remote_port"`
	ServerRemoteUser        string   `json:"server_remote_user"`
	ServerRemoteAuthMethod  string   `json:"server_remote_auth_method"`
	ServerRemoteSSHKeyLabel string   `json:"server_remote_ssh_key_label"`
	ServerRemoteUseSudo     bool     `json:"server_remote_use_sudo"`
}

const (
	trustMessageField                = "message"
	pairingTokenDefaultExpiryMinutes = 15
	pairingTokenMaxExpiryMinutes     = 30
)

func pairingTokenExpiresAt(now time.Time, requestedMinutes *int) time.Time {
	minutes := pairingTokenDefaultExpiryMinutes
	if requestedMinutes != nil && *requestedMinutes > 0 {
		minutes = *requestedMinutes
		if minutes > pairingTokenMaxExpiryMinutes {
			minutes = pairingTokenMaxExpiryMinutes
		}
	}
	return now.Add(time.Duration(minutes) * time.Minute)
}

func RegisterPairingRoutesWithStores(r *httpx.Router, stores RouteStores) {
	r.GET("/api/v1/trust/pairing-tokens", listPairingTokensFromStore(stores.Workers))
	r.POST("/api/v1/trust/pairing-tokens", createPairingTokenFromStore(stores))
	r.DELETE("/api/v1/trust/pairing-tokens/{id}", deletePairingTokenFromStore(stores.Workers))
}

func requireTrustUserID(e *httpx.Event) (string, error) {
	if userID, ok := trustUserID(e); ok {
		return userID, nil
	}
	return "", httpx.NewUnauthorizedError("Authentication required", nil)
}

func trustUserID(e *httpx.Event) (string, bool) {
	if e == nil {
		return "", false
	}
	if e.Auth != nil && e.Auth.Id != "" {
		return e.Auth.Id, true
	}
	if e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil && id.IsAuthenticated() {
			return id.UserID, true
		}
	}
	return "", false
}

// ============================================================================
// Pairing Tokens (Remote enrollment)
// ============================================================================

func listPairingTokensFromStore(store controlplane.WorkerStore) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, authErr := requireTrustUserID(e)
		if authErr != nil {
			return authErr
		}
		tenantID, err := requireTrustTenantID(e, userID, "techstack.trust.pairing-tokens.list")
		if err != nil {
			return err
		}
		if store == nil {
			return trustStoreUnavailable(e)
		}
		tokens, err := store.ListPairingTokensByOwner(e.Request.Context(), tenantID, userID)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch pairing tokens", nil)
		}
		out := make([]map[string]any, 0, len(tokens))
		for _, token := range tokens {
			out = append(out, map[string]any{
				"id":         token.ID,
				"name":       token.Name,
				"used":       token.Status == "used",
				"expires_at": token.ExpiresAt,
				"used_at":    token.UsedAt,
				"created":    token.CreatedAt,
			})
		}

		return httpx.Success(e, http.StatusOK, map[string]any{"tokens": out, "count": len(out)})
	}
}

func createPairingTokenFromStore(stores RouteStores) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, authErr := requireTrustUserID(e)
		if authErr != nil {
			return authErr
		}
		tenantID, err := requireTrustTenantID(e, userID, "techstack.trust.pairing-tokens.create")
		if err != nil {
			return err
		}

		if stores.Stacks == nil || stores.Workers == nil || stores.Jobs == nil {
			return trustStoreUnavailable(e)
		}

		var req pairingTokenRequest
		if err := e.BindBody(&req); err != nil {
			return httpx.BadRequest(e, "Invalid request body")
		}

		ctx := e.Request.Context()
		stackID := strings.TrimSpace(req.StackID)
		var stack *controlplane.Stack
		if stackID != "" {
			var err error
			stack, err = stores.Stacks.GetStack(ctx, tenantID, stackID)
			if err != nil {
				if errors.Is(err, controlplane.ErrNotFound) {
					return httpx.NotFound(e, "Stack not found")
				}
				return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch stack", nil)
			}
			if stack.OwnerSubjectID != userID {
				return httpx.Forbidden(e, "Not your stack")
			}
		}

		minted, err := MintStackPairingToken(ctx, stores, tenantID, userID, stack, req)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, minted.FailureMessage(), nil)
		}

		response := map[string]any{
			"id":         minted.TokenID,
			"token":      minted.Token,
			"expires_at": minted.ExpiresAt,
		}
		if stack != nil {
			response["stack_id"] = stack.ID
			if minted.JobID != "" {
				response["job_id"] = minted.JobID
			}
		}
		return httpx.Success(e, http.StatusCreated, response)
	}
}

// PairingTokenParams is the exported shape of the pairing-token request for
// callers outside the trust HTTP surface (the wizard-run facade). It mirrors
// pairingTokenRequest field-for-field.
type PairingTokenParams = pairingTokenRequest

// MintedStackPairing is the outcome of MintStackPairingToken. The raw Token is
// a live enrollment credential; callers must not persist it anywhere except
// the registration job result (where the add-server wizard reads it).
type MintedStackPairing struct {
	TokenID   string
	Token     string
	ExpiresAt time.Time
	// JobID is the completed registration job the wizard polls. Empty when the
	// mint was stack-less or the job store is not wired.
	JobID string
	// failureStage records which phase failed for the HTTP error message.
	failureStage string
}

// FailureMessage maps the failed mint phase onto the public HTTP error.
func (m MintedStackPairing) FailureMessage() string {
	switch m.failureStage {
	case "job":
		return "Failed to create registration job"
	case "persist":
		return "Failed to save token"
	default:
		return "Failed to generate token"
	}
}

// MintStackPairingToken is the pairing mint core shared by the trust endpoint
// and the wizard-run facade: generate a tenant-routable kpt1 token, persist
// its hash with the node-handoff metadata, and mint the completed add-server
// registration job when the token is stack-scoped. Stack ownership must be
// verified by the caller before minting.
func MintStackPairingToken(ctx context.Context, stores RouteStores, tenantID, userID string, stack *controlplane.Stack, req PairingTokenParams) (MintedStackPairing, error) {
	minted, err := MintStackPairingTokenOnly(ctx, stores, tenantID, userID, stack, req)
	if err != nil {
		return minted, err
	}
	if stack != nil {
		// The add-server wizard drives BYOS registration off a creation job
		// (it polls the job and shows the install command from its result).
		// Post-PocketBase the store path must mint that job too, or the BYOS
		// lane 500s the wizard ("did not return a creation job").
		result := storePairingJobResult(req, minted.Token, minted.ExpiresAt)
		var jobID string
		var jobErr error
		if normalizePairingServerMode(req.ServerProvisioningMode) == "connect-remote" {
			jobID, jobErr = createStoreRemoteEnrollmentJob(ctx, stores.Jobs, stack, result)
		} else {
			jobID, jobErr = createStorePairingJob(ctx, stores.Jobs, stack, result)
		}
		if jobErr != nil {
			minted.failureStage = "job"
			return minted, jobErr
		}
		minted.JobID = jobID
	}
	return minted, nil
}

// MintStackPairingTokenOnly mints and persists the pairing capability without
// creating a registration job. Recovery paths that already own a durable
// enrollment row use it to re-issue only the one-time token.
func MintStackPairingTokenOnly(ctx context.Context, stores RouteStores, tenantID, userID string, stack *controlplane.Stack, req PairingTokenParams) (MintedStackPairing, error) {
	rawToken, tokenHashHex, err := pairingtoken.Generate(tenantID)
	if err != nil {
		return MintedStackPairing{failureStage: "generate"}, err
	}
	expiresAt := pairingTokenExpiresAt(time.Now().UTC(), req.ExpiryMinutes)

	stackID := ""
	if stack != nil {
		stackID = stack.ID
	}
	token, err := stores.Workers.UpsertPairingToken(ctx, controlplane.PairingToken{
		ID:             storePairingTokenID(tokenHashHex),
		TenantID:       tenantID,
		StackID:        stackID,
		OwnerSubjectID: userID,
		Name:           strings.TrimSpace(req.Name),
		TokenHash:      tokenHashHex,
		Status:         "active",
		ExpiresAt:      &expiresAt,
		Metadata: pairingTokenMetadata(
			stack, req.ServerProvisioningMode, req.EnvironmentClass, req.NodeRole, req.StackKit, req.Services,
			req.SpecNodeID, req.ServerRemoteHost, req.ServerRemotePort, req.ServerRemoteUser,
			req.ServerRemoteAuthMethod, req.ServerRemoteSSHKeyLabel, req.ServerRemoteUseSudo,
		),
	})
	if err != nil {
		return MintedStackPairing{failureStage: "persist"}, err
	}
	return MintedStackPairing{TokenID: token.ID, Token: rawToken, ExpiresAt: expiresAt}, nil
}

// storePairingJobResult builds the add-server wizard's registration payload.
func storePairingJobResult(req pairingTokenRequest, rawToken string, expiresAt time.Time) map[string]any {
	result := map[string]any{
		"creation_operation":       "add-server",
		"registration_token":       rawToken,
		"token_expires_at":         expiresAt,
		"server_provisioning_mode": normalizePairingServerMode(req.ServerProvisioningMode),
		"server_node_role":         nodehandoff.NormalizeNodeRole(req.NodeRole),
		"stackkit_foundation":      normalizePairingStackKit(firstNonEmptyPairing(req.StackKit, "basement-kit")),
		"requested_services":       nodehandoff.NormalizeServiceKeys(req.Services),
	}
	if environmentClass := strings.ToLower(strings.TrimSpace(req.EnvironmentClass)); environmentClass != "" {
		result["environment_class"] = environmentClass
	}
	if host := strings.TrimSpace(req.ServerRemoteHost); host != "" {
		result["server_remote_host"] = host
		result["server_remote_host_present"] = true
	}
	if req.ServerRemotePort != nil && *req.ServerRemotePort > 0 {
		result["server_remote_port"] = *req.ServerRemotePort
	}
	if user := strings.TrimSpace(req.ServerRemoteUser); user != "" {
		result["server_remote_user"] = user
		result["server_remote_user_present"] = true
	}
	if method := strings.TrimSpace(req.ServerRemoteAuthMethod); method != "" {
		result["server_remote_auth_method"] = method
	}
	if ref := strings.TrimSpace(req.ServerRemoteSSHKeyLabel); ref != "" {
		result["server_remote_credential_ref"] = ref
	}
	if req.ServerRemoteUseSudo {
		result["server_remote_use_sudo"] = true
	}
	return result
}

// createStorePairingJob persists a completed add-server registration job in the
// control-plane store and returns its id. A nil job store (e.g. partial wiring)
// yields an empty id without error so the token is still issued.
func createStorePairingJob(ctx context.Context, jobs controlplane.JobStore, stack *controlplane.Stack, result map[string]any) (string, error) {
	if jobs == nil || stack == nil {
		return "", nil
	}
	job, err := jobs.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:       uuid.NewString(),
		TenantID: strings.TrimSpace(stack.TenantID),
		StackID:  stack.ID,
		Type:     "update",
		State:    "completed",
		Progress: 100,
		Step:     "create_spec",
		Message:  "Server registration prepared",
		Result:   result,
	})
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

func createStoreRemoteEnrollmentJob(ctx context.Context, jobs controlplane.JobStore, stack *controlplane.Stack, result map[string]any) (string, error) {
	if jobs == nil || stack == nil {
		return "", nil
	}
	job, err := jobs.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID:       uuid.NewString(),
		TenantID: strings.TrimSpace(stack.TenantID),
		StackID:  stack.ID,
		Type:     "remote_enrollment",
		State:    "pending",
		Progress: 5,
		Step:     "remote_ssh_connect",
		Message:  "Connecting to your Node over SSH…",
		Result:   result,
	})
	if err != nil {
		return "", err
	}
	return job.ID, nil
}

func deletePairingTokenFromStore(store controlplane.WorkerStore) func(e *httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, authErr := requireTrustUserID(e)
		if authErr != nil {
			return authErr
		}
		tenantID, err := requireTrustTenantID(e, userID, "techstack.trust.pairing-tokens.delete")
		if err != nil {
			return err
		}
		if store == nil {
			return trustStoreUnavailable(e)
		}
		id := strings.TrimSpace(e.Request.PathValue("id"))
		if id == "" {
			return httpx.BadRequest(e, "Token ID is required")
		}
		if err := store.RevokePairingToken(e.Request.Context(), tenantID, userID, id); err != nil {
			if errors.Is(err, controlplane.ErrNotFound) {
				return httpx.NotFound(e, "Token not found")
			}
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to delete token", nil)
		}
		return httpx.Success(e, http.StatusOK, map[string]string{trustMessageField: "Token deleted"})
	}
}

func requireTrustTenantID(e *httpx.Event, userID, capability string) (string, error) {
	explicitTenantID := ""
	if e != nil && e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil {
			explicitTenantID = id.OrgID
		}
	}
	return tenantguard.TenantScope(explicitTenantID, userID, capability)
}

func trustStoreUnavailable(e *httpx.Event) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Pairing token authority is temporarily unavailable", map[string]any{
		"reason_code": "pairing_token_authority_unavailable",
		"retryable":   true,
	})
}

func storePairingTokenID(tokenHashHex string) string {
	tokenHashHex = strings.TrimSpace(tokenHashHex)
	if len(tokenHashHex) > 24 {
		tokenHashHex = tokenHashHex[:24]
	}
	return "pair_" + tokenHashHex
}

func normalizePairingServerMode(value string) string {
	switch strings.TrimSpace(value) {
	case "connect-remote":
		return "connect-remote"
	case "hypervisor":
		return "hypervisor"
	default:
		return "install-command"
	}
}

func normalizePairingStackKit(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "basement", "basementkit":
		return "basement-kit"
	case "cloud", "cloudkit":
		return "cloud-kit"
	default:
		return strings.TrimSpace(value)
	}
}

func pairingTokenMetadata(
	stack *controlplane.Stack,
	serverMode, environmentClass, nodeRole, stackKit string,
	services []string,
	specNodeID, remoteHost string,
	remotePort *int,
	remoteUser, remoteAuthMethod, remoteCredentialRef string,
	remoteUseSudo bool,
) map[string]any {
	metadata := map[string]any{
		"server_provisioning_mode":       normalizePairingServerMode(serverMode),
		nodehandoff.KeyServerNodeRole:    nodehandoff.NormalizeNodeRole(nodeRole),
		"stackkit_foundation":            normalizePairingStackKit(firstNonEmptyPairing(stackKit, "basement-kit")),
		nodehandoff.KeyRequestedServices: nodehandoff.NormalizeServiceKeys(services),
	}
	if nodeID := strings.TrimSpace(specNodeID); nodeID != "" {
		metadata["spec_node_id"] = nodeID
		if stack != nil {
			metadata["planned_server_id"] = runtimeidentity.StackServerID(stack.ID, nodeID)
		}
	}
	if nodeRole == "substrate" {
		delete(metadata, "stackkit_foundation")
		metadata[nodehandoff.KeyRequestedServices] = []string{}
	}
	if value := strings.ToLower(strings.TrimSpace(environmentClass)); value != "" {
		metadata[nodehandoff.KeyRuntimeEnvironmentClass] = value
	}
	if value := strings.TrimSpace(remoteHost); value != "" {
		metadata[nodehandoff.KeyServerRemoteHost] = value
	}
	if remotePort != nil && *remotePort > 0 {
		metadata[nodehandoff.KeyServerRemotePort] = *remotePort
	}
	if value := strings.TrimSpace(remoteUser); value != "" {
		metadata[nodehandoff.KeyServerRemoteUser] = value
	}
	if value := strings.TrimSpace(remoteAuthMethod); value != "" {
		metadata[nodehandoff.KeyServerRemoteAuthMethod] = value
	}
	if value := strings.TrimSpace(remoteCredentialRef); value != "" {
		metadata[nodehandoff.KeyServerRemoteCredential] = value
		metadata[nodehandoff.KeyServerRemoteSSHKey] = value
	}
	if remoteUseSudo {
		metadata[nodehandoff.KeyServerRemoteUseSudo] = true
	}
	return metadata
}

func firstNonEmptyPairing(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
