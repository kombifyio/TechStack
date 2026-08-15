//nolint:goconst
package routes

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/pocketbase/pocketbase/core" // pocketbase-migration-compat: moved legacy projection only
)

type stackOperationService struct {
	ID             string         `json:"id,omitempty"`
	Name           string         `json:"name"`
	DisplayName    string         `json:"display_name"`
	Type           string         `json:"type"`
	Status         string         `json:"status"`
	URL            string         `json:"url,omitempty"`
	Port           int            `json:"port,omitempty"`
	TargetServer   string         `json:"target_server,omitempty"`
	TargetServerID string         `json:"target_server_id,omitempty"`
	DesiredState   string         `json:"desired_state,omitempty"`
	ObservedState  string         `json:"observed_state,omitempty"`
	Access         map[string]any `json:"access,omitempty"`
	AllowedActions []string       `json:"allowed_actions,omitempty"`
}

func (h stackOperationsRouteHandlers) operationServicesFromStore(ctx context.Context, tenantID string, stack *controlplane.Stack, servers []stackOperationServer) []stackOperationService {
	if stack == nil {
		return []stackOperationService{}
	}
	services := h.operationServicesFromRuntimeRegistry(ctx, tenantID, stack.ID, servers)
	if len(services) == 0 {
		services, _ = h.operationServicesFromRegistry(ctx, tenantID, stack.ID, servers)
	}
	if len(services) == 0 {
		services = operationServicesFromRuntimeSummary(stack, servers)
	}
	if len(services) == 0 {
		outputs := stackKitOutputsFromLatestDeployJob(ctx, h.jobStore, tenantID, stack.ID)
		services = operationServicesFromStackKitOutputs(outputs, servers)
	}
	return dedupeServices(services)
}

func (h stackOperationsRouteHandlers) operationServicesFromRuntimeRegistry(ctx context.Context, tenantID, stackID string, servers []stackOperationServer) []stackOperationService {
	if h.serviceStore == nil {
		return nil
	}
	rows, err := h.serviceStore.ListServiceRuntimes(ctx, tenantID, stackID, "")
	if err != nil || len(rows) == 0 {
		return nil
	}
	serverRows := map[string]*controlplane.ServerRuntime{}
	if h.serverStore != nil {
		if runtimes, listErr := h.serverStore.ListServerRuntimesByTenant(ctx, tenantID, stackID); listErr == nil {
			for i := range runtimes {
				serverRows[runtimes[i].ID] = &runtimes[i]
			}
		}
	}
	projector := serviceRuntimeHandlers{now: func() time.Time { return time.Now().UTC() }}
	result := make([]stackOperationService, 0, len(rows))
	for _, row := range rows {
		projected := projector.response(row, serverRows[row.ServerID])
		targetName := row.ServerID
		for _, server := range servers {
			if server.ID == row.ServerID {
				targetName = firstNonEmptyString(server.Hostname, server.ID)
				break
			}
		}
		result = append(result, stackOperationService{
			ID: row.ID, Name: normalizeServiceKey(row.ServiceKey), DisplayName: row.Name,
			Type:   firstNonEmptyString(stringFromAnyMap(row.Metadata, "type"), canonicalServiceType(row.ServiceKey)),
			Status: projected.Health.State, URL: stringFromAnyMap(projected.Access, serviceAccessURLKey),
			Port: intFromAnyMap(row.Metadata, "port"), TargetServer: targetName, TargetServerID: row.ServerID,
			DesiredState: projected.DesiredState, ObservedState: projected.ObservedState,
			Access: projected.Access, AllowedActions: projected.AllowedActions,
		})
	}
	return result
}

// operationServicesFromRegistry is the source adapter shared by the Store and
// legacy projections. Source precedence, sorting, deduplication and fallback
// remain caller-specific because their public contracts intentionally differ.
func (h stackOperationsRouteHandlers) operationServicesFromRegistry(ctx context.Context, tenantID, stackID string, servers []stackOperationServer) ([]stackOperationService, bool) {
	if h.registryStore == nil {
		return nil, false
	}
	nodesByID := map[string]controlplane.Node{}
	if nodes, err := h.registryStore.ListNodesByStack(ctx, tenantID, stackID); err == nil {
		for _, node := range nodes {
			nodesByID[node.ID] = node
		}
	}
	rows, err := h.registryStore.ListServicesByStack(ctx, tenantID, stackID)
	if err != nil || len(rows) == 0 {
		return nil, false
	}
	services := make([]stackOperationService, 0, len(rows))
	for _, row := range rows {
		services = append(services, operationServiceFromControlPlane(row, nodesByID[row.NodeID], servers))
	}
	return services, true
}

func operationServiceFromControlPlane(service controlplane.Service, node controlplane.Node, servers []stackOperationServer) stackOperationService {
	name := normalizeServiceKey(firstNonEmptyString(service.ServiceKey, service.Name, service.ID))
	targetServer := firstNonEmptyString(node.Name, node.WorkerID, node.ID, service.NodeID)
	targetServerID := firstNonEmptyString(matchingServerID(targetServer, servers), node.ID, node.WorkerID, service.NodeID)
	status := firstNonEmptyString(service.Status, registryUnknownStatus)
	serviceURL := service.URL
	if service.Source == stackKitOutputKey {
		// Persisted action output is useful provenance, but it is not a live
		// observation and must not become a green dashboard service.
		status = registryUnknownStatus
		serviceURL = ""
	}
	if service.Source == stackKitsInventorySource {
		serverFresh := operationServerHasCurrentHeartbeat(servers, targetServerID)
		observationFresh := operationServiceObservationFresh(service, time.Now().UTC())
		if !serverFresh || !observationFresh {
			// The inventory is historical once either its own observation or the
			// agent heartbeat expires. Keep metadata visible, but remove access.
			status = registryUnknownStatus
			serviceURL = ""
		}
	}
	return stackOperationService{
		ID:             service.ID,
		Name:           name,
		DisplayName:    firstNonEmptyString(stringFromAnyMap(service.Metadata, "display_name"), canonicalServiceDisplayName(name), name),
		Type:           firstNonEmptyString(stringFromAnyMap(service.Metadata, "type"), canonicalServiceType(name)),
		Status:         status,
		URL:            serviceURL,
		Port:           intFromAnyMap(service.Metadata, "port"),
		TargetServer:   targetServer,
		TargetServerID: targetServerID,
	}
}

func operationServiceObservationFresh(service controlplane.Service, now time.Time) bool {
	observedAt, err := time.Parse(time.RFC3339Nano, stringFromAnyMap(service.Metadata, "observed_at"))
	if err != nil {
		return false
	}
	age := now.Sub(observedAt.UTC())
	return age >= -30*time.Second && age <= runtimehealth.FreshHeartbeatWindow
}

func operationServerHasCurrentHeartbeat(servers []stackOperationServer, serverID string) bool {
	for _, server := range servers {
		if server.ID != serverID && server.AgentID != serverID {
			continue
		}
		switch server.Health.State {
		case string(runtimehealth.ServerHealthy), string(runtimehealth.ServerDegraded):
			return true
		default:
			return false
		}
	}
	return false
}

func operationServicesFromRuntimeSummary(stack *controlplane.Stack, servers []stackOperationServer) []stackOperationService {
	if stack == nil {
		return []stackOperationService{}
	}
	outputs, ok := mapFromJSONAny(stack.RuntimeSummary["stackkit_outputs"])
	if !ok {
		outputs, ok = mapFromJSONAny(stack.RuntimeSummary["outputs"])
	}
	if !ok {
		return []stackOperationService{}
	}
	return operationServicesFromStackKitOutputs(outputs, servers)
}

func operationServicesFromStackKitOutputs(outputs map[string]any, servers []stackOperationServer) []stackOperationService {
	items := stackKitServiceOutputItems(outputs)
	if len(items) == 0 {
		return []stackOperationService{}
	}
	services := make([]stackOperationService, 0, len(items))
	for _, item := range items {
		name := normalizeServiceKey(firstNonEmptyString(stringFromAnyMap(item, "name"), stringFromAnyMap(item, "service_key"), stringFromAnyMap(item, "key")))
		if name == "" {
			continue
		}
		target := firstNonEmptyString(stringFromAnyMap(item, "target_server"), stringFromAnyMap(item, "server"), stringFromAnyMap(item, "node"), stringFromAnyMap(item, "node_id"))
		targetServerID := firstNonEmptyString(stringFromAnyMap(item, "target_server_id"), matchingServerID(target, servers))
		if target == "" && targetServerID == "" && len(servers) == 1 {
			target = servers[0].Hostname
			targetServerID = servers[0].ID
		}
		now := time.Now().UTC()
		services = append(services, stackOperationService{
			ID:          firstNonEmptyString(stringFromAnyMap(item, "id"), name),
			Name:        name,
			DisplayName: firstNonEmptyString(stringFromAnyMap(item, "display_name"), canonicalServiceDisplayName(name), name),
			Type:        firstNonEmptyString(stringFromAnyMap(item, "type"), canonicalServiceType(name)),
			// Ordinary StackKits action output remains unknown. A versioned
			// runtime observation is explicitly measured evidence and is only
			// accepted while fresh.
			Status:         stackKitObservedServiceStatus(item, now),
			URL:            stackKitObservedServiceURL(item, now),
			Port:           intFromAnyMap(item, "port"),
			TargetServer:   target,
			TargetServerID: targetServerID,
		})
	}
	return services
}

func stackKitServiceOutputItems(outputs map[string]any) []map[string]any {
	if len(outputs) == 0 {
		return nil
	}
	items := make([]map[string]any, 0)
	for _, key := range []string{"services", "service_links", "serviceLinks"} {
		items = append(items, stackKitServiceOutputItemsFromAny(outputs[key])...)
	}
	items = mergeStackKitObservationServiceItems(items, stackKitObservationServiceItems(outputs))
	sort.SliceStable(items, func(i, j int) bool {
		return strings.ToLower(firstNonEmptyString(stringFromAnyMap(items[i], "name"), stringFromAnyMap(items[i], "service_key"), stringFromAnyMap(items[i], "key"))) <
			strings.ToLower(firstNonEmptyString(stringFromAnyMap(items[j], "name"), stringFromAnyMap(items[j], "service_key"), stringFromAnyMap(items[j], "key")))
	})
	return items
}

const (
	stackKitRuntimeObservationV1 = "stackkit.runtime-observation/v1"
	stackKitRuntimeObservationV2 = "stackkit.runtime-observation/v2"
)

// stackKitObservationServiceItems turns the versioned StackKits observation
// into the existing service-output shape. This is intentionally separate from
// generic job output: only the known observation version can affect health.
func stackKitObservationServiceItems(outputs map[string]any) []map[string]any {
	observation, ok := mapFromJSONAny(outputs["observation"])
	version := stringFromAnyMap(observation, "version")
	if !ok || (version != stackKitRuntimeObservationV1 && version != stackKitRuntimeObservationV2) {
		return nil
	}
	observedAt := stringFromAnyMap(observation, "observed_at")
	host, _ := mapFromJSONAny(observation["host"])
	platform, _ := mapFromJSONAny(observation["platform"])
	services := sliceOfMapsFromAny(observation["services"])
	items := make([]map[string]any, 0, len(services))
	for _, service := range services {
		name := firstNonEmptyString(stringFromAnyMap(service, "name"), stringFromAnyMap(service, "service_key"), stringFromAnyMap(service, "key"))
		if name == "" {
			continue
		}
		item := make(map[string]any, len(service)+12)
		for key, value := range service {
			item[key] = value
		}
		item["name"] = name
		item["runtime_observation"] = true
		item["observation_version"] = version
		item["observed_at"] = observedAt
		if reachable, ok := boolValueFromAny(host["reachable"]); ok {
			item["observation_host_reachable"] = reachable
		}
		if dockerReachable, ok := boolValueFromAny(host["docker_reachable"]); ok {
			item["observation_docker_reachable"] = dockerReachable
		}
		if serverID := stringFromAnyMap(platform, "server_id"); serverID != "" {
			item["target_server_id"] = serverID
		}
		if platformAppID := stringFromAnyMap(item, "platform_app_id"); platformAppID != "" {
			item["platform_id"] = platformAppID
		}
		if probe, ok := mapFromJSONAny(item["probe"]); ok {
			if stringFromAnyMap(item, "url") == "" {
				item["url"] = stringFromAnyMap(probe, "url")
			}
			health, _ := mapFromJSONAny(item["health"])
			if health == nil {
				health = map[string]any{}
			}
			if reached, ok := boolValueFromAny(probe["reached"]); ok {
				health["probe_reached"] = reached
			}
			health["probe_status_code"] = probe["status_code"]
			if failureClass := stringFromAnyMap(probe, "failure_class"); failureClass != "" {
				health["probe_failure_class"] = failureClass
			}
			item["health"] = health
		}
		items = append(items, item)
	}
	return items
}

func mergeStackKitObservationServiceItems(items, observations []map[string]any) []map[string]any {
	if len(observations) == 0 {
		return items
	}
	indices := make(map[string]int, len(items))
	for index, item := range items {
		name := normalizeServiceKey(firstNonEmptyString(stringFromAnyMap(item, "name"), stringFromAnyMap(item, "service_key"), stringFromAnyMap(item, "key")))
		if name != "" {
			indices[name] = index
		}
	}
	for _, observation := range observations {
		name := normalizeServiceKey(firstNonEmptyString(stringFromAnyMap(observation, "name"), stringFromAnyMap(observation, "service_key"), stringFromAnyMap(observation, "key")))
		if index, exists := indices[name]; exists {
			merged := make(map[string]any, len(items[index])+len(observation))
			for key, value := range items[index] {
				merged[key] = value
			}
			for key, value := range observation {
				merged[key] = value
			}
			items[index] = merged
			continue
		}
		indices[name] = len(items)
		items = append(items, observation)
	}
	return items
}

func stackKitObservedServiceStatus(item map[string]any, now time.Time) string {
	serverState := stackKitObservationServerState(item, now)
	if serverState != runtimehealth.ServerHealthy && serverState != runtimehealth.ServerDegraded {
		return registryUnknownStatus
	}
	status := strings.ToLower(strings.TrimSpace(stringFromAnyMap(item, "status")))
	switch status {
	case string(runtimehealth.ServiceStarting), string(runtimehealth.ServiceHealthy), string(runtimehealth.ServiceReachable), string(runtimehealth.ServiceUnhealthy), string(runtimehealth.ServiceUnknown):
		if serverState == runtimehealth.ServerDegraded && status == string(runtimehealth.ServiceHealthy) {
			return registryUnknownStatus
		}
		return status
	default:
		return registryUnknownStatus
	}
}

func stackKitObservationServerState(item map[string]any, now time.Time) runtimehealth.ServerState {
	observed, isObservation := boolValueFromAny(item["runtime_observation"])
	version := stringFromAnyMap(item, "observation_version")
	if !isObservation || !observed || (version != stackKitRuntimeObservationV1 && version != stackKitRuntimeObservationV2) {
		return runtimehealth.ServerProvisioned
	}
	observedAt, err := time.Parse(time.RFC3339Nano, stringFromAnyMap(item, "observed_at"))
	if err != nil || observedAt.After(now.Add(time.Minute)) {
		return runtimehealth.ServerProvisioned
	}
	observedState := ""
	if stackKitObservationReachabilityDegraded(item) {
		observedState = string(runtimehealth.ServerDegraded)
	}
	return runtimehealth.DeriveServerState(runtimehealth.ServerInput{Now: now, HeartbeatAt: &observedAt, ObservedState: observedState})
}

func stackKitObservationReachabilityDegraded(item map[string]any) bool {
	for _, key := range []string{"observation_host_reachable", "observation_docker_reachable"} {
		if reachable, ok := boolValueFromAny(item[key]); ok && !reachable {
			return true
		}
	}
	return false
}

func stackKitServiceOutputItemsFromAny(value any) []map[string]any {
	if items := sliceOfMapsFromAny(value); len(items) > 0 {
		return items
	}
	mapped, ok := mapFromJSONAny(value)
	if !ok {
		return nil
	}
	if looksLikeStackKitOutputServiceItem(mapped) {
		return []map[string]any{mapped}
	}
	items := make([]map[string]any, 0, len(mapped))
	for key, value := range mapped {
		name := normalizeServiceKey(key)
		switch typed := value.(type) {
		case string:
			items = append(items, map[string]any{"name": name, "url": typed})
		case bool:
			if typed {
				items = append(items, map[string]any{"name": name})
			}
		default:
			item, ok := mapFromJSONAny(typed)
			if !ok {
				continue
			}
			if firstNonEmptyString(stringFromAnyMap(item, "name"), stringFromAnyMap(item, "service_key"), stringFromAnyMap(item, "key")) == "" {
				item["name"] = name
			}
			items = append(items, item)
		}
	}
	return items
}

func looksLikeStackKitOutputServiceItem(item map[string]any) bool {
	for _, key := range []string{"name", "service_key", "key", "service", "url", "service_url", "public_url", "href", "endpoint"} {
		if _, ok := item[key]; ok {
			return true
		}
	}
	return false
}

func serviceURLFromStackKitOutput(item map[string]any) string {
	return firstNonEmptyString(
		stringFromAnyMap(item, "url"),
		stringFromAnyMap(item, "service_url"),
		stringFromAnyMap(item, "public_url"),
		stringFromAnyMap(item, "href"),
		stringFromAnyMap(item, "endpoint"),
	)
}

func stackKitObservedServiceURL(item map[string]any, now time.Time) string {
	serverState := stackKitObservationServerState(item, now)
	if serverState != runtimehealth.ServerHealthy && serverState != runtimehealth.ServerDegraded {
		return ""
	}
	return serviceURLFromStackKitOutput(item)
}

func stackKitOutputsFromLatestDeployJob(ctx context.Context, store controlplane.JobStore, tenantID, stackID string) map[string]any {
	if store == nil {
		return nil
	}
	jobs, err := store.ListJobsByStack(ctx, tenantID, stackID, 20)
	if err != nil {
		return nil
	}
	for _, job := range jobs {
		if job.Type != "deploy" || job.State != "completed" {
			continue
		}
		outputs, ok := mapFromJSONAny(job.Result["stackkit_outputs"])
		if ok {
			return outputs
		}
	}
	return nil
}

func sliceOfMapsFromAny(value any) []map[string]any {
	switch raw := value.(type) {
	case []map[string]any:
		return raw
	case []any:
		out := make([]map[string]any, 0, len(raw))
		for _, item := range raw {
			if mapped, ok := mapFromJSONAny(item); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return []map[string]any{}
	}
}

func (h stackOperationsRouteHandlers) operationServices(stack *core.Record, servers []stackOperationServer, tenantID string) []stackOperationService { // pocketbase-migration-compat: moved legacy projection only
	if h.registryStore != nil {
		tenantID = firstNonEmptyString(tenantID, stack.GetString("tenant_id"))
		services, ok := h.operationServicesFromRegistry(context.Background(), tenantID, stack.Id, servers)
		if ok {
			sortServicesByDisplayName(services)
			return dedupeServices(services)
		}
	}
	nodeNames := map[string]string{}
	nodeServerIDs := map[string]string{}
	if nodes, err := h.app.FindRecordsByFilter("nodes", "stack_id = {:stackId}", "name", 200, 0, map[string]any{"stackId": stack.Id}); err == nil { // pocketbase-migration-compat: moved legacy projection only
		for _, node := range nodes {
			name := firstNonEmptyString(node.GetString("hostname"), node.GetString("name"), node.Id)
			nodeNames[node.Id] = name
			nodeServerIDs[node.Id] = matchingServerID(name, servers)
		}
	}

	services := []stackOperationService{}
	for nodeID, nodeName := range nodeNames {
		records, err := h.app.FindRecordsByFilter("services", "node_id = {:nodeId}", "name", 100, 0, map[string]any{"nodeId": nodeID}) // pocketbase-migration-compat: moved legacy projection only
		if err != nil {
			continue
		}
		for _, record := range records {
			services = append(services, serviceFromRecord(record, nodeName, nodeServerIDs[nodeID]))
		}
	}
	if len(services) == 0 {
		services = operationServicesFromStackKitOutputs(
			h.stackKitOutputsFromLatestDeployRecord(stack.Id),
			servers,
		)
	}
	sortServicesByDisplayName(services)
	return dedupeServices(services)
}

func sortServicesByDisplayName(services []stackOperationService) {
	sort.SliceStable(services, func(i, j int) bool {
		return strings.ToLower(services[i].DisplayName) < strings.ToLower(services[j].DisplayName)
	})
}

func (h stackOperationsRouteHandlers) stackKitOutputsFromLatestDeployRecord(stackID string) map[string]any {
	if h.app == nil {
		return nil
	}
	records, err := h.app.FindRecordsByFilter( // pocketbase-migration-compat: moved legacy projection only
		"jobs",
		"stack_id = {:stackId} && type = 'deploy' && state = 'completed'",
		"-updated",
		20,
		0,
		map[string]any{"stackId": stackID},
	)
	if err != nil {
		return nil
	}
	for _, record := range records {
		result, ok := mapFromJSONAny(record.Get("result"))
		if !ok {
			continue
		}
		if outputs, ok := mapFromJSONAny(result["stackkit_outputs"]); ok {
			return outputs
		}
		if outputs, ok := mapFromJSONAny(result["stackkitOutputs"]); ok {
			return outputs
		}
		if outputs, ok := mapFromJSONAny(result["outputs"]); ok {
			return outputs
		}
	}
	return nil
}

func serviceFromRecord(record *core.Record, targetServer, targetServerID string) stackOperationService { // pocketbase-migration-compat: moved legacy projection only
	name := normalizeServiceKey(record.GetString("name"))
	display := firstNonEmptyString(record.GetString("display_name"), canonicalServiceDisplayName(name), record.GetString("name"))
	status := strings.TrimSpace(record.GetString("status"))
	if status == "" {
		status = registryUnknownStatus
	}
	return stackOperationService{
		ID:             record.Id,
		Name:           name,
		DisplayName:    display,
		Type:           firstNonEmptyString(record.GetString("type"), canonicalServiceType(name)),
		Status:         status,
		URL:            record.GetString("url"),
		Port:           record.GetInt("port"),
		TargetServer:   targetServer,
		TargetServerID: targetServerID,
	}
}

// fallbackStackServices and the hardcoded demo list were removed: when no
// services are registered for a stack, the operations endpoint now returns an
// empty list so the UI shows an honest empty state instead of five "unknown"
// services that look like a half-broken deployment. See the stack creation
// honesty fix in 2026-05-20.

func normalizeServiceKey(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.ReplaceAll(key, "-", "_")
	switch key {
	case "pocketid", "pocket_id", "pocketbase_identity", "identity":
		return "pocket_id"
	default:
		return key
	}
}

func canonicalServiceDisplayName(key string) string {
	switch normalizeServiceKey(key) {
	case "pocket_id":
		return "Pocket ID"
	case "pocketbase":
		return "PocketBase"
	case "traefik":
		return "Traefik"
	case "monitoring":
		return "Monitoring"
	case "vaultwarden":
		return "Vaultwarden"
	case "immich":
		return "Immich"
	default:
		if strings.TrimSpace(key) == "" {
			return "Service"
		}
		return titleWords(strings.ReplaceAll(key, "_", " "))
	}
}

func titleWords(value string) string {
	words := strings.Fields(value)
	for i, word := range words {
		if word == "" {
			continue
		}
		if len(word) == 1 {
			words[i] = strings.ToUpper(word)
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	if len(words) == 0 {
		return "Service"
	}
	return strings.Join(words, " ")
}

func canonicalServiceType(key string) string {
	switch normalizeServiceKey(key) {
	case "pocket_id":
		return "identity"
	case "tinyauth":
		return "auth"
	case "traefik":
		return "ingress"
	case "monitoring", "uptime", "uptime_kuma":
		return "monitoring"
	case "coolify":
		return "paas"
	case "vaultwarden":
		return "secrets"
	case "immich":
		return "media"
	default:
		return "custom"
	}
}

func dedupeServices(services []stackOperationService) []stackOperationService {
	seen := map[string]bool{}
	out := make([]stackOperationService, 0, len(services))
	for _, service := range services {
		key := normalizeServiceKey(service.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, service)
	}
	return out
}

func servicesForServer(services []stackOperationService, server stackOperationServer) []stackOperationService {
	out := []stackOperationService{}
	for _, service := range services {
		if service.TargetServerID == server.ID || strings.EqualFold(service.TargetServer, server.Hostname) {
			out = append(out, service)
		}
	}
	return out
}

func matchingServerID(nodeName string, servers []stackOperationServer) string {
	for _, server := range servers {
		if strings.EqualFold(nodeName, server.Hostname) || strings.EqualFold(nodeName, server.ID) {
			return server.ID
		}
	}
	return ""
}
