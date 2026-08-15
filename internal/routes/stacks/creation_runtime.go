package stacks

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercatalog"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	creationAuthorityTechstack       = "techstack"
	creationSourceWizard             = "wizard"
	creationServerProvisionModeField = "server_provisioning_mode"
	creationServerConnectionField    = "server_connection_mode"
	creationOperationsURLField       = "operations_url"
	creationStackIDField             = "stack_id"
	creationJobIDField               = "job_id"
	creationNameField                = "name"
	creationMessageField             = "message"
)

func (h crudRouteHandlers) persistCreateServerIntent(e *httpx.Event, ownerID string, stack *persistedStack, req normalizedCreateStackRequest) (string, error) {
	if h.serverStore == nil || stack == nil {
		return "", nil
	}
	tenantID := tenantIDFromRequest(e)
	if tenantID == "" {
		return "", nil
	}
	fields := runtimeFieldsFromConfig(runtimePolicyConfigFromRequest(req))
	managed := hasManagedRuntimeFields(runtimePolicyConfigFromRequest(req), fields)
	leaseID := fieldString(fields, "lease_id")
	if managed && leaseID == "" {
		leaseID = "lease-" + sanitizeRuntimeIdentity(stack.Id)
	}
	serverID := runtimeidentity.StackServerID(stack.Id, "primary")
	reasonCode := "awaiting_pairing"
	if managed {
		serverID = runtimeidentity.LeaseServerID(leaseID)
		reasonCode = "awaiting_server_allocation"
	}
	if existing, err := h.serverStore.GetServerRuntime(e.Request.Context(), tenantID, serverID); err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return "", fmt.Errorf("load server intent: %w", err)
	}
	if managed {
		// Native provider admission creates the lease and runtime server as one
		// closed, deferred-FK aggregate. Before that transaction exists, the
		// Wizard may reserve and return the deterministic server identity, but
		// it must not publish a server row bound to an invented future lease.
		return serverID, nil
	}
	now := time.Now().UTC()
	server, err := h.serverStore.UpsertServerRuntime(e.Request.Context(), controlplane.ServerRuntime{
		ID: serverID, TenantID: tenantID, StackID: stack.Id, OwnerSubjectID: ownerID,
		LeaseID: leaseID, ProviderRef: fieldString(fields, providercatalog.ProviderIDField),
		Name: stack.Name + " primary", LifecycleState: string(serverregistry.LifecyclePlanned),
		DesiredState: string(serverregistry.DesiredRunning), ConnectionState: string(serverregistry.ConnectionPending),
		HealthState: string(serverregistry.HealthUnknown), ReasonCode: reasonCode,
		ConnectionChangedAt: now,
		Metadata: map[string]any{
			"authority": creationAuthorityTechstack, "creation_source": creationSourceWizard,
			creationServerProvisionModeField: fieldString(fields, creationServerProvisionModeField),
			creationServerConnectionField:    fieldString(fields, creationServerConnectionField),
		},
	})
	if err != nil {
		return "", fmt.Errorf("persist server intent: %w", err)
	}
	for _, dimension := range []struct{ name, state string }{
		{"lifecycle", server.LifecycleState}, {"connection", server.ConnectionState}, {"health", server.HealthState},
	} {
		if _, err := h.serverStore.AppendServerTransition(e.Request.Context(), controlplane.ServerStateTransition{
			TenantID: tenantID, ServerID: server.ID, Dimension: dimension.name, ToState: dimension.state,
			ReasonCode: reasonCode, Source: "creation-wizard", ObservedAt: now,
			Evidence: map[string]any{creationStackIDField: stack.Id},
		}); err != nil {
			return "", fmt.Errorf("persist server %s transition: %w", dimension.name, err)
		}
	}
	return server.ID, nil
}

func (h crudRouteHandlers) replayCreateJob(e *httpx.Event, stack *persistedStack, serverIDs ...string) (bool, error) {
	if h.jobStore == nil || stack == nil || tenantIDFromRequest(e) == "" {
		return false, nil
	}
	jobs, err := h.jobStore.ListJobsByStack(e.Request.Context(), tenantIDFromRequest(e), stack.Id, 50)
	if err != nil {
		return true, httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resume stack creation", map[string]any{
			creationStackIDField: stack.Id, creationOperationsURLField: operationsURL(stack.Id),
		})
	}
	if len(jobs) == 0 {
		return false, nil
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	job := jobs[0]
	response := map[string]any{
		creationStackIDField: stack.Id, creationJobIDField: job.ID, creationNameField: stack.Name,
		"state": job.State, creationMessageField: "Stack creation request resumed",
		"idempotent_replay": true, creationOperationsURLField: operationsURL(stack.Id),
	}
	if len(serverIDs) > 0 && strings.TrimSpace(serverIDs[0]) != "" {
		response["server_id"] = strings.TrimSpace(serverIDs[0])
	}
	return true, httpx.Success(e, http.StatusAccepted, response)
}

func operationsURL(stackID string) string {
	return "/stacks?stack_id=" + url.QueryEscape(strings.TrimSpace(stackID))
}

func sanitizeRuntimeIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			result.WriteRune(char)
		} else {
			result.WriteByte('-')
		}
	}
	if sanitized := strings.Trim(result.String(), "-"); sanitized != "" {
		return sanitized
	}
	return "stack"
}
