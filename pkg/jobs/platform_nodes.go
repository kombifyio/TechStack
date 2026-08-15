package jobs

import (
	"strings"
)

func runtimeActionPlatformNodesFromPayload(payload map[string]interface{}) []PlatformNode {
	if payload == nil {
		return nil
	}
	nodes := []PlatformNode{}
	nodes = append(nodes, parsePlatformNodes(payload["platform_nodes"])...)
	nodes = append(nodes, parseWorkerPayloadPlatformNodes(payload["workers"])...)
	return mergePlatformNodes(nodes)
}

func parsePlatformNodes(value interface{}) []PlatformNode {
	switch typed := value.(type) {
	case nil:
		return nil
	case []PlatformNode:
		return normalizePlatformNodes(typed)
	case []interface{}:
		nodes := make([]PlatformNode, 0, len(typed))
		for _, item := range typed {
			if node := platformNodeFromMap(mapFromInterface(item), false); platformNodeIdentity(node) != "" {
				nodes = append(nodes, node)
			}
		}
		return normalizePlatformNodes(nodes)
	case []map[string]interface{}:
		nodes := make([]PlatformNode, 0, len(typed))
		for _, item := range typed {
			if node := platformNodeFromMap(item, false); platformNodeIdentity(node) != "" {
				nodes = append(nodes, node)
			}
		}
		return normalizePlatformNodes(nodes)
	default:
		return nil
	}
}

func parseWorkerPayloadPlatformNodes(value interface{}) []PlatformNode {
	switch typed := value.(type) {
	case nil:
		return nil
	case []interface{}:
		nodes := make([]PlatformNode, 0, len(typed))
		for _, item := range typed {
			if node := platformNodeFromMap(mapFromInterface(item), true); isSupplementalPlatformNode(node) {
				nodes = append(nodes, node)
			}
		}
		return normalizePlatformNodes(nodes)
	case []map[string]interface{}:
		nodes := make([]PlatformNode, 0, len(typed))
		for _, item := range typed {
			if node := platformNodeFromMap(item, true); isSupplementalPlatformNode(node) {
				nodes = append(nodes, node)
			}
		}
		return normalizePlatformNodes(nodes)
	default:
		return nil
	}
}

func platformNodeFromMap(values map[string]interface{}, workerDerived bool) PlatformNode {
	if len(values) == 0 {
		return PlatformNode{}
	}
	role := canonicalPlatformNodeRole(firstNonEmpty(
		stringFromMap(values, "role"),
		stringFromMap(values, "node_role"),
		stringFromMap(values, "server_node_role"),
		stringFromMap(values, "type"),
	))
	if role == "" && workerDerived {
		role = "worker"
	}
	name := firstNonEmpty(
		stringFromMap(values, "name"),
		stringFromMap(values, "hostname"),
		stringFromMap(values, "id"),
		role,
	)
	host := firstNonEmpty(
		stringFromMap(values, "runtime_ssh_host"),
		stringFromMap(values, "ssh_host"),
		stringFromMap(values, "host"),
		stringFromMap(values, "address"),
		stringFromMap(values, "public_ip"),
		stringFromMap(values, "runtime_public_ip"),
		stringFromMap(values, "ip"),
	)
	ip := firstNonEmpty(
		stringFromMap(values, "ip"),
		stringFromMap(values, "runtime_public_ip"),
		stringFromMap(values, "public_ip"),
		stringFromMap(values, "runtime_private_ip"),
		stringFromMap(values, "private_ip"),
	)
	node := PlatformNode{
		Name:     name,
		Role:     role,
		IP:       ip,
		Host:     host,
		Services: compactStrings(firstStringSlice(values["services"], values["requested_services"])),
		Platform: nodePlatformTargetFromMap(values),
	}
	if bootstrap := nodeBootstrapFromMap(values, node); bootstrap != nil {
		node.Bootstrap = bootstrap
	}
	return normalizePlatformNode(node)
}

func nodePlatformTargetFromMap(values map[string]interface{}) NodePlatformTarget {
	platform := mapFromInterface(values["platform"])
	if len(platform) == 0 {
		platform = values
	}
	return NodePlatformTarget{
		ServerID: firstNonEmpty(
			stringFromMap(platform, "serverId"),
			stringFromMap(platform, "server_id"),
			stringFromMap(values, "platform_server_id"),
		),
		DestinationUUID: firstNonEmpty(
			stringFromMap(platform, "destinationUuid"),
			stringFromMap(platform, "destination_uuid"),
			stringFromMap(values, "platform_destination_uuid"),
		),
		EnvironmentID: firstNonEmpty(
			stringFromMap(platform, "environmentId"),
			stringFromMap(platform, "environment_id"),
			stringFromMap(values, "platform_environment_id"),
		),
		ProjectUUID: firstNonEmpty(
			stringFromMap(platform, "projectUuid"),
			stringFromMap(platform, "project_uuid"),
			stringFromMap(values, "platform_project_uuid"),
		),
		EnvironmentUUID: firstNonEmpty(
			stringFromMap(platform, "environmentUuid"),
			stringFromMap(platform, "environment_uuid"),
			stringFromMap(values, "platform_environment_uuid"),
		),
	}
}

func nodeBootstrapFromMap(values map[string]interface{}, node PlatformNode) *NodeBootstrap {
	bootstrapMap := mapFromInterface(values["bootstrap"])
	ssh := sshBootstrapFromMaps(mapFromInterface(bootstrapMap["ssh"]), mapFromInterface(values["ssh"]), values, node)
	coreAddress := firstNonEmpty(
		stringFromMap(bootstrapMap, "komodo_core_address"),
		stringFromMap(bootstrapMap, "komodoCoreAddress"),
		stringFromMap(values, "komodo_core_address"),
		stringFromMap(values, "komodoCoreAddress"),
	)
	onboardingKey := firstNonEmpty(
		stringFromMap(bootstrapMap, "komodo_onboarding_key"),
		stringFromMap(bootstrapMap, "komodoOnboardingKey"),
		stringFromMap(values, "komodo_onboarding_key"),
		stringFromMap(values, "komodoOnboardingKey"),
	)
	if ssh == nil && coreAddress == "" && onboardingKey == "" {
		return nil
	}
	return &NodeBootstrap{KomodoCoreAddress: coreAddress, KomodoOnboardingKey: onboardingKey, SSH: ssh}
}

func sshBootstrapFromMaps(primary, secondary, values map[string]interface{}, node PlatformNode) *SSHBootstrap {
	ssh := SSHBootstrap{
		Host: firstNonEmpty(
			stringFromMap(primary, "host"),
			stringFromMap(primary, "ssh_host"),
			stringFromMap(secondary, "host"),
			stringFromMap(secondary, "ssh_host"),
			stringFromMap(values, "runtime_ssh_host"),
			stringFromMap(values, "ssh_host"),
			stringFromMap(values, "server_remote_host"),
			node.Host,
			node.IP,
		),
		User: firstNonEmpty(
			stringFromMap(primary, "user"),
			stringFromMap(primary, "ssh_user"),
			stringFromMap(secondary, "user"),
			stringFromMap(secondary, "ssh_user"),
			stringFromMap(values, "runtime_ssh_user"),
			stringFromMap(values, "ssh_user"),
			stringFromMap(values, "server_remote_user"),
		),
		Port: firstPositiveInt(
			intFromMap(primary, "port"),
			intFromMap(primary, "ssh_port"),
			intFromMap(secondary, "port"),
			intFromMap(secondary, "ssh_port"),
			intFromMap(values, "runtime_ssh_port"),
			intFromMap(values, "ssh_port"),
			intFromMap(values, "server_remote_port"),
		),
		KeyPath: firstNonEmpty(
			stringFromMap(primary, "key_path"),
			stringFromMap(primary, "keyPath"),
			stringFromMap(secondary, "key_path"),
			stringFromMap(secondary, "keyPath"),
			stringFromMap(values, "ssh_key_path"),
		),
		KeyPEM: firstNonEmpty(
			stringFromMap(primary, "key_pem"),
			stringFromMap(primary, "keyPem"),
			stringFromMap(secondary, "key_pem"),
			stringFromMap(secondary, "keyPem"),
		),
		PrivateKey: firstNonEmpty(
			stringFromMap(primary, "private_key"),
			stringFromMap(primary, "privateKey"),
			stringFromMap(secondary, "private_key"),
			stringFromMap(secondary, "privateKey"),
			stringFromMap(values, "ssh_private_key"),
		),
		ClientPrivateKey: firstNonEmpty(
			stringFromMap(primary, "client_private_key"),
			stringFromMap(primary, "clientPrivateKey"),
			stringFromMap(secondary, "client_private_key"),
			stringFromMap(secondary, "clientPrivateKey"),
		),
		ProxyJump: firstNonEmpty(
			stringFromMap(primary, "proxy_jump"),
			stringFromMap(primary, "proxyJump"),
			stringFromMap(secondary, "proxy_jump"),
			stringFromMap(secondary, "proxyJump"),
		),
	}
	ssh.Host = strings.TrimSpace(ssh.Host)
	ssh.User = strings.TrimSpace(ssh.User)
	ssh.KeyPath = strings.TrimSpace(ssh.KeyPath)
	ssh.KeyPEM = strings.TrimSpace(ssh.KeyPEM)
	ssh.PrivateKey = strings.TrimSpace(ssh.PrivateKey)
	ssh.ClientPrivateKey = strings.TrimSpace(ssh.ClientPrivateKey)
	ssh.ProxyJump = strings.TrimSpace(ssh.ProxyJump)
	if ssh.Host == "" && firstNonEmpty(ssh.KeyPath, ssh.KeyPEM, ssh.PrivateKey, ssh.ClientPrivateKey, ssh.ProxyJump) == "" {
		return nil
	}
	if ssh.User == "" {
		ssh.User = "root"
	}
	if ssh.Port <= 0 {
		ssh.Port = 22
	}
	return &ssh
}

func normalizePlatformNodes(nodes []PlatformNode) []PlatformNode {
	out := make([]PlatformNode, 0, len(nodes))
	for _, node := range nodes {
		node = normalizePlatformNode(node)
		if platformNodeIdentity(node) == "" {
			continue
		}
		out = append(out, node)
	}
	return out
}

func normalizePlatformNode(node PlatformNode) PlatformNode {
	node.Name = strings.TrimSpace(node.Name)
	node.Role = canonicalPlatformNodeRole(node.Role)
	node.IP = strings.TrimSpace(node.IP)
	node.Host = strings.TrimSpace(node.Host)
	node.Services = compactStrings(node.Services)
	node.Platform.ServerID = strings.TrimSpace(node.Platform.ServerID)
	node.Platform.DestinationUUID = strings.TrimSpace(node.Platform.DestinationUUID)
	node.Platform.EnvironmentID = strings.TrimSpace(node.Platform.EnvironmentID)
	node.Platform.ProjectUUID = strings.TrimSpace(node.Platform.ProjectUUID)
	node.Platform.EnvironmentUUID = strings.TrimSpace(node.Platform.EnvironmentUUID)
	if node.Bootstrap != nil {
		node.Bootstrap.KomodoCoreAddress = strings.TrimSpace(node.Bootstrap.KomodoCoreAddress)
		node.Bootstrap.KomodoOnboardingKey = strings.TrimSpace(node.Bootstrap.KomodoOnboardingKey)
		if node.Bootstrap.SSH == nil && node.Bootstrap.KomodoCoreAddress == "" && node.Bootstrap.KomodoOnboardingKey == "" {
			node.Bootstrap = nil
		}
	}
	if node.Role == "" {
		node.Role = "worker"
	}
	return node
}

func mergePlatformNodes(nodes []PlatformNode) []PlatformNode {
	merged := []PlatformNode{}
	byKey := map[string]int{}
	for _, node := range normalizePlatformNodes(nodes) {
		key := platformNodeIdentity(node)
		if key == "" {
			continue
		}
		if index, ok := byKey[key]; ok {
			merged[index] = mergePlatformNode(merged[index], node)
			continue
		}
		byKey[key] = len(merged)
		merged = append(merged, node)
	}
	return merged
}

func mergePlatformNode(base, next PlatformNode) PlatformNode {
	if base.Name == "" {
		base.Name = next.Name
	}
	if base.Role == "" {
		base.Role = next.Role
	}
	if base.IP == "" {
		base.IP = next.IP
	}
	if base.Host == "" {
		base.Host = next.Host
	}
	if len(base.Services) == 0 {
		base.Services = next.Services
	}
	base.Platform = mergeNodePlatformTarget(base.Platform, next.Platform)
	if base.Bootstrap == nil {
		base.Bootstrap = next.Bootstrap
	}
	return normalizePlatformNode(base)
}

func mergeNodePlatformTarget(base, next NodePlatformTarget) NodePlatformTarget {
	if base.ServerID == "" {
		base.ServerID = next.ServerID
	}
	if base.DestinationUUID == "" {
		base.DestinationUUID = next.DestinationUUID
	}
	if base.EnvironmentID == "" {
		base.EnvironmentID = next.EnvironmentID
	}
	if base.ProjectUUID == "" {
		base.ProjectUUID = next.ProjectUUID
	}
	if base.EnvironmentUUID == "" {
		base.EnvironmentUUID = next.EnvironmentUUID
	}
	return base
}

func platformNodeIdentity(node PlatformNode) string {
	if node.Name != "" {
		return strings.ToLower(node.Name)
	}
	return strings.ToLower(node.Role)
}

func isSupplementalPlatformNode(node PlatformNode) bool {
	if platformNodeIdentity(node) == "" {
		return false
	}
	switch canonicalPlatformNodeRole(node.Role) {
	case "", "main", "standalone", "foundation", "control-plane":
		return false
	default:
		return true
	}
}

func canonicalPlatformNodeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "foundation", "standalone", "control_plane", "control-plane":
		return "main"
	default:
		return strings.ToLower(strings.TrimSpace(role))
	}
}

func firstStringSlice(values ...interface{}) []string {
	for _, value := range values {
		switch typed := value.(type) {
		case []string:
			if len(compactStrings(typed)) > 0 {
				return typed
			}
		case []interface{}:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok {
					out = append(out, text)
				}
			}
			if len(compactStrings(out)) > 0 {
				return out
			}
		}
	}
	return nil
}
