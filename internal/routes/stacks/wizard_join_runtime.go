package stacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const (
	joinServerIntentSource = "wizard-join"
)

// persistJoinServerIntent reserves the canonical server identity for a newly
// projected join node before pairing or connect-remote enrollment. Without
// this row, multi-node stacks only expose bound servers and worker registration
// falls back to a worker-derived identity that never appears on the dashboard.
func (h crudRouteHandlers) persistJoinServerIntent(
	ctx context.Context,
	ownerID, tenantID string,
	stack *controlplane.Stack,
	projection *specv2.Projection,
	provisioningMode, remoteHost string,
) (string, error) {
	if h.serverStore == nil || stack == nil || projection == nil {
		return "", nil
	}
	ownerID = strings.TrimSpace(ownerID)
	tenantID = strings.TrimSpace(tenantID)
	nodeID := strings.TrimSpace(projection.NodeID)
	if ownerID == "" || tenantID == "" || nodeID == "" {
		return "", nil
	}

	serverID := runtimeidentity.StackServerID(stack.ID, nodeID)
	if existing, err := h.serverStore.GetServerRuntime(ctx, tenantID, serverID); err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, controlplane.ErrNotFound) {
		return "", fmt.Errorf("load join server intent: %w", err)
	}

	remoteHost = strings.TrimSpace(remoteHost)
	displayName := strings.TrimSpace(stack.Name + " " + nodeID)
	if remoteHost != "" {
		displayName = remoteHost
	}

	now := time.Now().UTC()
	metadata := map[string]any{
		"authority":                      creationAuthorityTechstack,
		"creation_source":                joinServerIntentSource,
		creationServerProvisionModeField: strings.TrimSpace(provisioningMode),
		"spec_node_id":                   nodeID,
	}
	if remoteHost != "" {
		metadata[nodehandoff.KeyServerRemoteHost] = remoteHost
	}

	server, err := h.serverStore.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: serverID, TenantID: tenantID, StackID: stack.ID, OwnerSubjectID: ownerID,
		NodeID: nodeID, Name: displayName,
		LifecycleState: string(serverregistry.LifecyclePlanned),
		DesiredState:   string(serverregistry.DesiredRunning),
		ConnectionState: string(serverregistry.ConnectionPending),
		HealthState:     string(serverregistry.HealthUnknown),
		ReasonCode:      "awaiting_pairing",
		ConnectionChangedAt: now,
		Metadata:            metadata,
	})
	if err != nil {
		return "", fmt.Errorf("persist join server intent: %w", err)
	}
	for _, dimension := range []struct{ name, state string }{
		{"lifecycle", server.LifecycleState},
		{"connection", server.ConnectionState},
		{"health", server.HealthState},
	} {
		if _, err := h.serverStore.AppendServerTransition(ctx, controlplane.ServerStateTransition{
			TenantID: tenantID, ServerID: server.ID, Dimension: dimension.name, ToState: dimension.state,
			ReasonCode: "awaiting_pairing", Source: joinServerIntentSource, ObservedAt: now,
			Evidence: map[string]any{creationStackIDField: stack.ID, "node_id": nodeID},
		}); err != nil {
			return "", fmt.Errorf("persist join server %s transition: %w", dimension.name, err)
		}
	}
	return server.ID, nil
}

func joinSpecNodeIDFromStack(stack *controlplane.Stack) string {
	if stack == nil {
		return ""
	}
	spec, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok {
		return ""
	}
	nodes, ok := spec["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return ""
	}
	last, ok := nodes[len(nodes)-1].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringFromAny(last["id"]))
}

func joinPlannedServerID(stack *controlplane.Stack) string {
	nodeID := joinSpecNodeIDFromStack(stack)
	if stack == nil || nodeID == "" {
		return ""
	}
	return runtimeidentity.StackServerID(stack.ID, nodeID)
}

func wizardJoinRemoteHost(run *wizardRunState, stack *controlplane.Stack) string {
	if run != nil && run.request.Remote != nil {
		if host := strings.TrimSpace(run.request.Remote.Host); host != "" {
			return host
		}
	}
	if stack != nil {
		return runtimeStringFromConfig(stack.Config, "server_remote_host")
	}
	return ""
}
