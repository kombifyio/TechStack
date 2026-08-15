package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	stackKitOutputKey         = "stackkit_outputs"
	stackKitServiceLinksKey   = "service_links"
	stackKitServiceLinksAlt   = "serviceLinks"
	stackKitServicesKey       = "services"
	stackKitNodeNameField     = "name"
	stackKitNodeHostField     = "hostname"
	stackKitNodeIPField       = "ip_address"
	stackKitNodeRoleField     = "role"
	stackKitNodeStatusField   = "status"
	stackKitNodeStackIDField  = "stack_id"
	stackKitNodeMetaField     = "metadata"
	stackKitServiceNameField  = "name"
	stackKitServiceTypeField  = "type"
	stackKitServicePortField  = "port"
	stackKitServiceURLField   = "url"
	stackKitServiceStateField = "status"
	stackKitServiceNodeField  = "node_id"
	stackKitRuntimePublicIP   = "runtime_public_ip"
)

type stackKitServiceProjection struct {
	Name        string
	DisplayName string
	Type        string
	URL         string
	Port        int
	Status      string
	Node        string
	Metadata    map[string]any
}

type stackKitRuntimeTarget struct {
	Hostname  string
	PublicIP  string
	PrivateIP string
}

type stackKitNodeProjection struct {
	Name      string
	Role      string
	Hostname  string
	PublicIP  string
	PrivateIP string
	Status    string
	Metadata  map[string]any
}

func (o *Orchestrator) syncStackKitRuntimeInventory(stack *core.Record, job *jobs.Job) error {
	if job == nil {
		return nil
	}
	return o.syncStackKitRuntimeInventorySnapshot(stack, job.Snapshot())
}

func (o *Orchestrator) syncStackKitRuntimeInventorySnapshot(stack *core.Record, job jobs.JobSnapshot) error { // pocketbase-migration-compat: legacy stack projection only
	if stack == nil || job.Result == nil {
		return nil
	}
	outputs := stackKitOutputsFromJobResult(job.Result)
	services := stackKitServiceProjections(outputs)

	target := runtimeTargetFromJobSnapshot(stack, job)
	if len(outputs) == 0 && !stackKitRuntimeTargetPresent(target) {
		return nil
	}
	if o.registry != nil {
		return syncStackKitRuntimeInventoryStore(o.ctx, o.registry, stackKitRegistrySyncRequest{
			TenantID:   stack.GetString("tenant_id"),
			InstanceID: stack.GetString("instance_id"),
			StackID:    stack.Id,
			StackName:  stack.GetString("name"),
			Target:     target,
			Nodes:      stackKitNodeProjectionsFromJobResult(job.Result, outputs, stack.GetString("name"), target),
			Resources:  runtimeResourcesFromJobResult(job.Result),
			Metrics:    runtimeMetricsFromJobResult(job.Result),
			Services:   services,
		})
	}
	node, err := o.upsertStackKitNode(stack, target)
	if err != nil {
		return err
	}
	for _, service := range services {
		if err := o.upsertStackKitService(stack, node.Id, service); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) syncStackKitRuntimeInventoryFromJob(tenantID string, job *jobs.Job) error {
	if job == nil {
		return nil
	}
	return o.syncStackKitRuntimeInventoryFromJobSnapshot(tenantID, job.Snapshot())
}

func (o *Orchestrator) syncStackKitRuntimeInventoryFromJobSnapshot(tenantID string, job jobs.JobSnapshot) error {
	if o.registry == nil || job.Result == nil || strings.TrimSpace(tenantID) == "" {
		return nil
	}
	outputs := stackKitOutputsFromJobResult(job.Result)
	services := stackKitServiceProjections(outputs)
	target := runtimeTargetFromJobResult(job.Result)
	if len(outputs) == 0 && !stackKitRuntimeTargetPresent(target) {
		return nil
	}
	return syncStackKitRuntimeInventoryStore(o.ctx, o.registry, stackKitRegistrySyncRequest{
		TenantID:  tenantID,
		StackID:   job.TargetID,
		StackName: firstNonEmptyString(job.TargetName, stringFromAny(job.Result["stack_name"]), job.TargetID),
		Target:    target,
		Nodes:     stackKitNodeProjectionsFromJobResult(job.Result, outputs, firstNonEmptyString(job.TargetName, stringFromAny(job.Result["stack_name"]), job.TargetID), target),
		Resources: runtimeResourcesFromJobResult(job.Result),
		Metrics:   runtimeMetricsFromJobResult(job.Result),
		Services:  services,
	})
}

type stackKitRegistrySyncRequest struct {
	TenantID   string
	InstanceID string
	StackID    string
	StackName  string
	Target     stackKitRuntimeTarget
	Nodes      []stackKitNodeProjection
	Resources  map[string]any
	Metrics    map[string]any
	Services   []stackKitServiceProjection
}

func syncStackKitRuntimeInventoryStore(ctx context.Context, store controlplane.RegistryStore, req stackKitRegistrySyncRequest) error {
	if store == nil || strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.StackID) == "" {
		return nil
	}
	nodes := req.Nodes
	if len(nodes) == 0 {
		nodes = []stackKitNodeProjection{stackKitNodeProjectionFromTarget(req.StackName, req.Target)}
	}
	upsertedNodes := make([]controlplane.Node, 0, len(nodes))
	nodesByKey := map[string]controlplane.Node{}
	for _, projected := range nodes {
		projected.Name = firstNonEmptyString(projected.Name, req.StackName, req.StackID)
		projected.Role = normalizeStackKitNodeRole(firstNonEmptyString(projected.Role, "main"))
		projected.Status = firstNonEmptyString(projected.Status, "online")
		metadata := map[string]any{
			"source":                stackKitOutputKey,
			"runtime_ssh_host":      projected.Hostname,
			stackKitRuntimePublicIP: projected.PublicIP,
			"runtime_private_ip":    projected.PrivateIP,
		}
		for key, value := range projected.Metadata {
			metadata[key] = value
		}
		if projected.Role == "main" || len(upsertedNodes) == 0 {
			for key, value := range req.Metrics {
				metadata[key] = value
			}
			for key, value := range req.Resources {
				metadata[key] = value
			}
		}
		node, err := store.UpsertNode(ctx, controlplane.Node{
			ID:         stackKitRegistryID("node", req.TenantID, req.StackID, projected.Role, projected.Name),
			TenantID:   req.TenantID,
			InstanceID: req.InstanceID,
			StackID:    req.StackID,
			Name:       projected.Name,
			Role:       projected.Role,
			Status:     projected.Status,
			Address:    firstNonEmptyString(projected.PublicIP, projected.PrivateIP, projected.Hostname),
			Metadata:   metadata,
		})
		if err != nil {
			return err
		}
		upsertedNodes = append(upsertedNodes, *node)
		indexStackKitNodeKeys(nodesByKey, *node)
	}
	if len(upsertedNodes) == 0 {
		return nil
	}
	for _, service := range req.Services {
		metadata := map[string]any{}
		for key, value := range service.Metadata {
			metadata[key] = value
		}
		if service.Port > 0 {
			metadata["port"] = service.Port
		}
		metadata["display_name"] = firstNonEmptyString(service.DisplayName, canonicalStackKitServiceDisplayName(service.Name))
		metadata["type"] = firstNonEmptyString(service.Type, canonicalStackKitServiceType(service.Name))
		node := stackKitNodeForService(service, upsertedNodes, nodesByKey)
		_, err := store.UpsertService(ctx, controlplane.Service{
			ID:         stackKitRegistryID("service", req.TenantID, req.StackID, node.ID, service.Name),
			TenantID:   req.TenantID,
			InstanceID: req.InstanceID,
			StackID:    req.StackID,
			NodeID:     node.ID,
			ServiceKey: service.Name,
			Name:       firstNonEmptyString(service.DisplayName, service.Name),
			Status:     firstNonEmptyString(service.Status, "running"),
			Source:     stackKitOutputKey,
			URL:        strings.TrimSpace(service.URL),
			Metadata:   metadata,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func stackKitNodeProjectionFromTarget(stackName string, target stackKitRuntimeTarget) stackKitNodeProjection {
	return stackKitNodeProjection{
		Name:      firstNonEmptyString(stackName, target.Hostname, target.PublicIP, "main"),
		Role:      "main",
		Hostname:  target.Hostname,
		PublicIP:  target.PublicIP,
		PrivateIP: target.PrivateIP,
		Status:    "online",
	}
}

func stackKitNodeProjectionsFromJobResult(result, outputs map[string]interface{}, stackName string, target stackKitRuntimeTarget) []stackKitNodeProjection {
	nodes := make([]stackKitNodeProjection, 0)
	if stackKitRuntimeTargetPresent(target) {
		nodes = append(nodes, stackKitNodeProjectionFromTarget(stackName, target))
	}
	for _, raw := range []interface{}{
		result["platform_nodes"],
		result["platformNodes"],
		result["nodes"],
		outputs["platform_nodes"],
		outputs["platformNodes"],
		outputs["nodes"],
	} {
		nodes = append(nodes, stackKitNodeProjectionsFromAny(raw)...)
	}
	return dedupeStackKitNodeProjections(nodes)
}

func stackKitNodeProjectionsFromAny(raw interface{}) []stackKitNodeProjection {
	if values := listValue(raw); len(values) > 0 {
		out := make([]stackKitNodeProjection, 0, len(values))
		for _, value := range values {
			if mapped, ok := mapValue(value); ok {
				out = append(out, stackKitNodeProjectionFromMap(mapped))
			}
		}
		return out
	}
	if mapped, ok := mapValue(raw); ok {
		if looksLikeStackKitNodeItem(mapped) {
			return []stackKitNodeProjection{stackKitNodeProjectionFromMap(mapped)}
		}
		out := make([]stackKitNodeProjection, 0, len(mapped))
		for key, value := range mapped {
			if node, ok := mapValue(value); ok {
				if _, exists := node["name"]; !exists {
					node["name"] = key
				}
				out = append(out, stackKitNodeProjectionFromMap(node))
			}
		}
		return out
	}
	return nil
}

func stackKitNodeProjectionFromMap(item map[string]interface{}) stackKitNodeProjection {
	metadata, _ := mapValue(firstNonNil(item["metadata"], item["meta"]))
	return stackKitNodeProjection{
		Name: firstNonEmptyString(
			stringFromAny(item["name"]),
			stringFromAny(item["id"]),
			stringFromAny(item["hostname"]),
			stringFromAny(item["host"]),
		),
		Role: firstNonEmptyString(
			stringFromAny(item["role"]),
			stringFromAny(item["type"]),
			stringFromAny(item["server_node_role"]),
			stringFromAny(metadata["role"]),
		),
		Hostname: firstNonEmptyString(
			stringFromAny(item["hostname"]),
			stringFromAny(item["host"]),
			stringFromAny(item["address"]),
		),
		PublicIP: firstNonEmptyString(
			stringFromAny(item["public_ip"]),
			stringFromAny(item["publicIP"]),
			stringFromAny(item["ip_address"]),
			stringFromAny(item["ip"]),
			stringFromAny(metadata["runtime_public_ip"]),
		),
		PrivateIP: firstNonEmptyString(
			stringFromAny(item["private_ip"]),
			stringFromAny(item["privateIP"]),
			stringFromAny(metadata["runtime_private_ip"]),
		),
		Status: firstNonEmptyString(
			stringFromAny(item["status"]),
			stringFromAny(item["state"]),
			stringFromAny(metadata["status"]),
		),
		Metadata: metadata,
	}
}

func looksLikeStackKitNodeItem(item map[string]interface{}) bool {
	for _, key := range []string{"name", "id", "hostname", "host", "public_ip", "ip", "role", "type"} {
		if _, ok := item[key]; ok {
			return true
		}
	}
	return false
}

func dedupeStackKitNodeProjections(nodes []stackKitNodeProjection) []stackKitNodeProjection {
	seen := map[string]bool{}
	out := make([]stackKitNodeProjection, 0, len(nodes))
	for _, node := range nodes {
		node.Name = firstNonEmptyString(node.Name, node.Hostname, node.PublicIP)
		if node.Name == "" {
			continue
		}
		node.Role = normalizeStackKitNodeRole(firstNonEmptyString(node.Role, "worker"))
		key := strings.ToLower(node.Role + "\x00" + node.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, node)
	}
	return out
}

func normalizeStackKitNodeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "foundation", "control-plane", "primary", "main":
		return "main"
	case "storage":
		return "storage"
	default:
		return "worker"
	}
}

func indexStackKitNodeKeys(out map[string]controlplane.Node, node controlplane.Node) {
	for _, key := range []string{node.ID, node.Name, node.WorkerID, node.Address, stringFromAnyMap(node.Metadata, "runtime_ssh_host"), node.Role} {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			out[key] = node
		}
	}
}

func stackKitNodeForService(service stackKitServiceProjection, nodes []controlplane.Node, nodesByKey map[string]controlplane.Node) controlplane.Node {
	for _, key := range []string{service.Node, stringFromAnyMap(service.Metadata, "node_id"), stringFromAnyMap(service.Metadata, "node"), stringFromAnyMap(service.Metadata, "target_server"), stringFromAnyMap(service.Metadata, "server")} {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		if node, ok := nodesByKey[normalized]; ok {
			return node
		}
	}
	for _, node := range nodes {
		if node.Role == "main" {
			return node
		}
	}
	return nodes[0]
}

func runtimeResourcesFromJobResult(result map[string]interface{}) map[string]any {
	if result == nil {
		return nil
	}
	requirements, _ := mapValue(result["requirements"])
	resources := map[string]any{}
	copyInt := func(outKey string, keys ...string) {
		for _, key := range keys {
			if value := intFromAny(requirements[key]); value > 0 {
				resources[outKey] = value
				return
			}
			if value := intFromAny(result[key]); value > 0 {
				resources[outKey] = value
				return
			}
		}
	}
	copyInt("cpu_cores", "minCPU", "cpu_cores", "vcpus", "vcpu")
	copyInt("ram_mb", "minRAM", "ram_mb", "memory_mb")
	copyInt("disk_gb", "minDisk", "disk_gb", "storage_gb")
	if _, ok := resources["disk_gb"]; !ok && len(resources) > 0 {
		resources["disk_gb"] = 10
	}
	if len(resources) == 0 {
		return nil
	}
	return resources
}

func runtimeMetricsFromJobResult(result map[string]interface{}) map[string]any {
	if result == nil {
		return nil
	}
	metrics, _ := mapValue(result["runtime_metrics"])
	if len(metrics) == 0 {
		return nil
	}
	out := map[string]any{}
	copyMetric := func(outKey string, keys ...string) {
		for _, key := range keys {
			if value := stringFromAny(metrics[key]); value != "" {
				out[outKey] = value
				return
			}
			if value := intFromAny(metrics[key]); value > 0 {
				out[outKey] = value
				return
			}
		}
	}
	copyMetric("runtime_cpu_percent", "cpu_percent", "cpuPercent")
	copyMetric("runtime_memory_percent", "memory_percent", "memoryPercent")
	copyMetric("runtime_disk_percent", "disk_percent", "diskPercent")
	copyMetric("runtime_uptime_seconds", "uptime_seconds", "uptimeSeconds")
	copyMetric("runtime_metrics_updated_at", "updated_at", "updatedAt")
	if len(out) == 0 {
		return nil
	}
	return out
}

func runtimeTargetFromJobResult(result map[string]interface{}) stackKitRuntimeTarget {
	if result == nil {
		return stackKitRuntimeTarget{}
	}
	return stackKitRuntimeTarget{
		Hostname: firstNonEmptyString(
			stringFromAny(result["runtime_ssh_host"]),
			stringFromAny(result["runtime_host"]),
			stringFromAny(result[stackKitRuntimePublicIP]),
			stringFromAny(result["runtime_docker_host"]),
		),
		PublicIP: firstNonEmptyString(
			stringFromAny(result[stackKitRuntimePublicIP]),
			stringFromAny(result["public_ip"]),
		),
		PrivateIP: firstNonEmptyString(
			stringFromAny(result["runtime_private_ip"]),
			stringFromAny(result["private_ip"]),
		),
	}
}

func stackKitRuntimeTargetPresent(target stackKitRuntimeTarget) bool {
	return firstNonEmptyString(target.Hostname, target.PublicIP, target.PrivateIP) != ""
}

func stackKitOutputsFromJobResult(result map[string]interface{}) map[string]interface{} {
	if result == nil {
		return nil
	}
	if outputs, ok := mapValue(result[stackKitOutputKey]); ok {
		return outputs
	}
	if data, ok := mapValue(result["data"]); ok {
		return stackKitOutputsFromJobResult(data)
	}
	return nil
}

func stackKitServiceProjections(outputs map[string]interface{}) []stackKitServiceProjection {
	services := make([]stackKitServiceProjection, 0)
	services = append(services, serviceProjectionsFromAny(outputs[stackKitServicesKey])...)
	services = append(services, serviceProjectionsFromAny(outputs[stackKitServiceLinksKey])...)
	services = append(services, serviceProjectionsFromAny(outputs[stackKitServiceLinksAlt])...)

	seen := map[string]bool{}
	out := make([]stackKitServiceProjection, 0, len(services))
	for _, service := range services {
		service.Name = normalizeStackKitServiceKey(service.Name)
		if service.Name == "" {
			service.Name = serviceNameFromURL(service.URL)
		}
		if service.Name == "" {
			continue
		}
		if seen[service.Name] {
			continue
		}
		seen[service.Name] = true
		if strings.TrimSpace(service.DisplayName) == "" {
			service.DisplayName = canonicalStackKitServiceDisplayName(service.Name)
		}
		if strings.TrimSpace(service.Type) == "" {
			service.Type = canonicalStackKitServiceType(service.Name)
		}
		if strings.TrimSpace(service.Status) == "" {
			service.Status = "running"
		}
		if service.Port == 0 {
			service.Port = portFromServiceURL(service.URL)
		}
		out = append(out, service)
	}
	return out
}

func serviceProjectionsFromAny(raw interface{}) []stackKitServiceProjection {
	if values := listValue(raw); len(values) > 0 {
		return serviceProjectionsFromValues(values)
	}
	items, ok := mapValue(raw)
	if !ok {
		return nil
	}
	if looksLikeStackKitServiceItem(items) {
		return []stackKitServiceProjection{serviceProjectionFromMap("", items)}
	}
	out := make([]stackKitServiceProjection, 0, len(items))
	for key, value := range items {
		name := normalizeStackKitServiceKey(key)
		switch typed := value.(type) {
		case string:
			out = append(out, stackKitServiceProjection{Name: name, URL: typed})
		case map[string]interface{}:
			out = append(out, serviceProjectionFromMap(name, typed))
		case map[string]string:
			mapped, _ := mapValue(typed)
			out = append(out, serviceProjectionFromMap(name, mapped))
		case bool:
			if typed {
				out = append(out, stackKitServiceProjection{Name: name})
			}
		default:
			if mapped, ok := mapValue(typed); ok {
				out = append(out, serviceProjectionFromMap(name, mapped))
			}
		}
	}
	return out
}

func serviceProjectionsFromValues(values []interface{}) []stackKitServiceProjection {
	out := make([]stackKitServiceProjection, 0, len(values))
	for _, value := range values {
		item, ok := mapValue(value)
		if !ok {
			continue
		}
		out = append(out, serviceProjectionFromMap("", item))
	}
	return out
}

func looksLikeStackKitServiceItem(item map[string]interface{}) bool {
	for _, key := range []string{"name", "key", "service", "service_key", "url", "URL", "admin_url", "adminUrl", "href", "endpoint"} {
		if _, ok := item[key]; ok {
			return true
		}
	}
	return false
}

func serviceProjectionFromMap(defaultName string, item map[string]interface{}) stackKitServiceProjection {
	metadata, _ := mapValue(firstNonNil(item["metadata"], item["meta"]))
	serviceURL := firstNonEmptyString(
		stringFromAny(item["url"]),
		stringFromAny(item["URL"]),
		stringFromAny(item["service_url"]),
		stringFromAny(item["serviceUrl"]),
		stringFromAny(item["public_url"]),
		stringFromAny(item["publicUrl"]),
		stringFromAny(item["admin_url"]),
		stringFromAny(item["adminUrl"]),
		stringFromAny(item["href"]),
		stringFromAny(item["endpoint"]),
	)
	return stackKitServiceProjection{
		Name: firstNonEmptyString(
			stringFromAny(item["name"]),
			stringFromAny(item["key"]),
			stringFromAny(item["service"]),
			stringFromAny(item["service_key"]),
			stringFromAny(metadata["name"]),
			defaultName,
		),
		DisplayName: firstNonEmptyString(
			stringFromAny(item["display_name"]),
			stringFromAny(item["displayName"]),
			stringFromAny(item["label"]),
			stringFromAny(metadata["display_name"]),
		),
		Type: firstNonEmptyString(
			stringFromAny(item["type"]),
			stringFromAny(metadata["type"]),
		),
		URL: serviceURL,
		Port: firstNonZeroInt(
			intFromAny(item["port"]),
			intFromAny(metadata["port"]),
		),
		Status: firstNonEmptyString(
			stringFromAny(item["status"]),
			stringFromAny(item["state"]),
			stringFromAny(metadata["status"]),
		),
		Node: firstNonEmptyString(
			stringFromAny(item["node_id"]),
			stringFromAny(item["node"]),
			stringFromAny(item["target_server"]),
			stringFromAny(item["target_server_id"]),
			stringFromAny(item["server"]),
			stringFromAny(metadata["node_id"]),
			stringFromAny(metadata["node"]),
		),
		Metadata: metadata,
	}
}

func (o *Orchestrator) upsertStackKitNode(stack *core.Record, target stackKitRuntimeTarget) (*core.Record, error) {
	stackID := stack.Id
	nodeName := firstNonEmptyString(stack.GetString("name"), stackID)
	node, err := o.findFirstByFilter(
		"nodes",
		"stack_id = {:stackId} && name = {:name}",
		map[string]any{"stackId": stackID, "name": nodeName},
	)
	if err != nil {
		return nil, err
	}
	if node == nil {
		collection, err := o.app.FindCollectionByNameOrId("nodes")
		if err != nil {
			return nil, err
		}
		node = core.NewRecord(collection)
		node.Set(stackKitNodeStackIDField, stackID)
		node.Set(stackKitNodeNameField, nodeName)
	}

	node.Set(stackKitNodeHostField, nodeName)
	node.Set(stackKitNodeRoleField, "main")
	node.Set(stackKitNodeStatusField, "online")
	if ip := firstNonEmptyString(target.PublicIP, target.PrivateIP); ip != "" {
		node.Set(stackKitNodeIPField, ip)
	}
	node.Set(stackKitNodeMetaField, map[string]any{
		"source":                stackKitOutputKey,
		"runtime_ssh_host":      target.Hostname,
		stackKitRuntimePublicIP: target.PublicIP,
		"runtime_private_ip":    target.PrivateIP,
	})
	setRecordTenantIDFromStack(node, stack)
	if err := o.app.Save(node); err != nil {
		return nil, err
	}
	return node, nil
}

func (o *Orchestrator) upsertStackKitService(stack *core.Record, nodeID string, service stackKitServiceProjection) error {
	record, err := o.findFirstByFilter(
		"services",
		"node_id = {:nodeId} && name = {:name}",
		map[string]any{"nodeId": nodeID, "name": service.Name},
	)
	if err != nil {
		return err
	}
	if record == nil {
		collection, err := o.app.FindCollectionByNameOrId("services")
		if err != nil {
			return err
		}
		record = core.NewRecord(collection)
		record.Set(stackKitServiceNodeField, nodeID)
		record.Set(stackKitServiceNameField, service.Name)
	}
	record.Set("display_name", firstNonEmptyString(service.DisplayName, canonicalStackKitServiceDisplayName(service.Name)))
	record.Set(stackKitServiceTypeField, firstNonEmptyString(service.Type, canonicalStackKitServiceType(service.Name)))
	record.Set(stackKitServiceStateField, firstNonEmptyString(service.Status, "running"))
	if service.Port > 0 {
		record.Set(stackKitServicePortField, service.Port)
	}
	if strings.TrimSpace(service.URL) != "" {
		record.Set(stackKitServiceURLField, strings.TrimSpace(service.URL))
	}
	setRecordTenantIDFromStack(record, stack)
	return o.app.Save(record)
}

func (o *Orchestrator) findFirstByFilter(collection, filter string, params map[string]any) (*core.Record, error) {
	records, err := o.app.FindRecordsByFilter(collection, filter, "", 1, 0, dbx.Params(params))
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[0], nil
}

func runtimeTargetFromJob(stack *core.Record, job *jobs.Job) stackKitRuntimeTarget {
	if job == nil {
		return stackKitRuntimeTarget{}
	}
	return runtimeTargetFromJobSnapshot(stack, job.Snapshot())
}

func runtimeTargetFromJobSnapshot(stack *core.Record, job jobs.JobSnapshot) stackKitRuntimeTarget { // pocketbase-migration-compat: legacy stack projection only
	target := stackKitRuntimeTarget{}
	target.Hostname = firstNonEmptyString(
		stringFromAny(job.Result["runtime_ssh_host"]),
		stringFromAny(job.Result["runtime_host"]),
		stringFromAny(job.Result["host"]),
	)
	target.PublicIP = firstNonEmptyString(
		stringFromAny(job.Result[stackKitRuntimePublicIP]),
		stringFromAny(job.Result["node_public_ip"]),
		stringFromAny(job.Result["public_ip"]),
	)
	target.PrivateIP = firstNonEmptyString(
		stringFromAny(job.Result["runtime_private_ip"]),
		stringFromAny(job.Result["node_private_ip"]),
		stringFromAny(job.Result["private_ip"]),
	)
	if target.Hostname == "" || target.PublicIP == "" {
		if nested, ok := mapValue(job.Result["runtime_target"]); ok {
			target.Hostname = firstNonEmptyString(target.Hostname, stringFromAny(nested["host"]))
			target.PublicIP = firstNonEmptyString(target.PublicIP, stringFromAny(nested["public_ip"]), stringFromAny(nested["publicIP"]))
			target.PrivateIP = firstNonEmptyString(target.PrivateIP, stringFromAny(nested["private_ip"]), stringFromAny(nested["privateIP"]))
		}
	}
	if target.Hostname == "" || target.PublicIP == "" {
		if worker := firstWorkerFromPayload(job.Payload["workers"]); len(worker) > 0 {
			target.Hostname = firstNonEmptyString(target.Hostname, stringFromAny(worker["hostname"]), stringFromAny(worker["name"]), stringFromAny(worker["id"]))
			target.PublicIP = firstNonEmptyString(target.PublicIP, stringFromAny(worker["ip"]), stringFromAny(worker["ip_address"]), stringFromAny(worker["public_ip"]))
		}
	}
	if target.Hostname == "" && stack != nil {
		target.Hostname = firstNonEmptyString(stack.GetString("name"), stack.Id)
	}
	return target
}

func firstWorkerFromPayload(raw interface{}) map[string]interface{} {
	switch workers := raw.(type) {
	case []interface{}:
		if len(workers) == 0 {
			return nil
		}
		worker, _ := mapValue(workers[0])
		return worker
	case []map[string]interface{}:
		if len(workers) == 0 {
			return nil
		}
		return workers[0]
	default:
		return nil
	}
}

func mapValue(raw interface{}) (map[string]interface{}, bool) {
	switch value := raw.(type) {
	case map[string]interface{}:
		return value, true
	case map[string]string:
		out := make(map[string]interface{}, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func listValue(raw interface{}) []interface{} {
	switch value := raw.(type) {
	case []interface{}:
		return value
	case []map[string]interface{}:
		out := make([]interface{}, 0, len(value))
		for _, item := range value {
			out = append(out, item)
		}
		return out
	case []map[string]string:
		out := make([]interface{}, 0, len(value))
		for _, item := range value {
			out = append(out, item)
		}
		return out
	case []string:
		out := make([]interface{}, 0, len(value))
		for _, item := range value {
			out = append(out, map[string]interface{}{"name": item})
		}
		return out
	default:
		return nil
	}
}

func firstNonNil(values ...interface{}) interface{} {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stackKitRegistryID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func stringFromAny(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func stringFromAnyMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	return stringFromAny(values[key])
}

func intFromAny(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n)
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return 0
}

func normalizeStackKitServiceKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func serviceNameFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	host := parsed.Hostname()
	label := strings.Split(host, ".")[0]
	return normalizeStackKitServiceKey(label)
}

func portFromServiceURL(raw string) int {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err == nil {
			return port
		}
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

func canonicalStackKitServiceDisplayName(key string) string {
	switch normalizeStackKitServiceKey(key) {
	case "pocket_id", "pocketid":
		return "Pocket ID"
	case "tinyauth", "auth":
		return "TinyAuth"
	case "coolify":
		return "Coolify"
	case "komodo":
		return "Komodo"
	case "dokploy":
		return "Dokploy"
	case "dockge":
		return "Dockge"
	case "traefik":
		return "Traefik"
	case "uptime_kuma", "monitoring", "kuma":
		return "Monitoring"
	case "base", "homepage":
		return "Base Hub"
	case "jellyfin":
		return "Jellyfin"
	case "files":
		return "Files"
	case "whoami":
		return "Whoami"
	default:
		parts := strings.Split(strings.ReplaceAll(key, "-", "_"), "_")
		for i, part := range parts {
			if part == "" {
				continue
			}
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
		return strings.Join(parts, " ")
	}
}

func canonicalStackKitServiceType(key string) string {
	switch normalizeStackKitServiceKey(key) {
	case "pocket_id", "pocketid", "tinyauth", "auth":
		return "identity"
	case "traefik":
		return "reverse-proxy"
	case "coolify", "komodo", "dokploy", "dockge":
		return "paas"
	case "uptime_kuma", "monitoring", "kuma":
		return "monitoring"
	case "vaultwarden":
		return "secrets"
	case "immich", "photos", "jellyfin":
		return "media"
	case "files":
		return "storage"
	case "base", "homepage", "whoami":
		return "custom"
	default:
		return "custom"
	}
}
