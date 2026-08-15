package jobs

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/pkg/unifier"
	"gopkg.in/yaml.v3"
)

const (
	addressModeKombifyMe              = "kombify-me"
	addressModeKombifyMeDomain        = "kombify.me"
	metadataKeyAddressMode            = "address_mode"
	metadataKeyRequestedAddressMode   = "requested_address_mode"
	metadataKeyKombifyMeAddressStatus = "kombify_me_address_status"
	metadataKeyKombifyMeAddressWarn   = "kombify_me_address_warning"
)

func stackKitSpecBytesForPayload(specData interface{}) ([]byte, error) {
	dataMap, ok := specData.(map[string]interface{})
	if !ok {
		return nil, nil
	}
	if _, ok := dataMap["stackkit"]; !ok {
		return nil, nil
	}
	// The wizard-run projection rides in the payload under stack_spec_v2 and
	// is persisted separately (persistProjectedStackSpec); the v1 StackSpec
	// decoder refuses unknown v2-only top-level fields, so it must never
	// enter the handoff.
	if _, ok := dataMap[payloadKeyStackSpecV2]; ok {
		next := make(map[string]interface{}, len(dataMap))
		for key, value := range dataMap {
			if key == payloadKeyStackSpecV2 {
				continue
			}
			next[key] = value
		}
		dataMap = next
	}
	return yaml.Marshal(stackKitSpecWithManagedRuntimeDefaults(dataMap))
}

func stackKitSpecWithManagedRuntimeDefaults(spec map[string]interface{}) map[string]interface{} {
	if !stackKitSpecUsesManagedRuntime(spec) {
		return spec
	}
	next := make(map[string]interface{}, len(spec)+1)
	for key, value := range spec {
		next[key] = value
	}
	next["mode"] = "bootstrapped"
	return next
}

func stackKitSpecUsesManagedRuntime(spec map[string]interface{}) bool {
	metadata := mapFromInterface(spec["metadata"])
	if strings.EqualFold(strings.TrimSpace(stringFromInterface(metadata[metadataKeyServerProvisionMode])), serverProvisionModeKombifyCloud) ||
		strings.EqualFold(strings.TrimSpace(stringFromInterface(metadata[metadataKeyServerMode])), serverModeMonthlyRuntime) ||
		strings.EqualFold(strings.TrimSpace(stringFromInterface(metadata[metadataKeyRuntimeLane])), serverModeMonthlyRuntime) {
		return true
	}
	for _, raw := range interfaceSlice(spec["nodes"]) {
		node := mapFromInterface(raw)
		if provider := normalizeProvider(stringFromInterface(node[providerField])); provider != "" && provider != providerLocal {
			return true
		}
	}
	provider := normalizeProvider(stringFromInterface(spec[providerField]))
	return provider != "" && provider != providerLocal
}

func hydratePersistedStackSpecTarget(persister *unifier.SpecPersister, target *ManagedRuntimeTarget) (string, string, error) {
	target = normalizeManagedRuntimeTarget(target)
	if persister == nil || target == nil || !persister.StackSpecExists() {
		return "", "", nil
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		return "", "", fmt.Errorf("read StackKits handoff spec: %w", err)
	}
	var spec map[string]interface{}
	if unmarshalErr := yaml.Unmarshal(data, &spec); unmarshalErr != nil {
		return "", "", fmt.Errorf("parse StackKits handoff spec: %w", unmarshalErr)
	}
	if spec == nil {
		spec = map[string]interface{}{}
	}

	host := firstNonEmpty(target.Host, target.PublicIP, target.PrivateIP)
	publicIP := firstNonEmpty(target.PublicIP, target.Host)
	nodes := interfaceSlice(spec["nodes"])
	node := mapFromInterface(nil)
	if len(nodes) > 0 {
		node = mapFromInterface(nodes[0])
	}
	if len(node) == 0 {
		node = map[string]interface{}{
			"name": stackRoleMain,
			"role": "standalone",
		}
	}
	if publicIP != "" {
		node["ip"] = publicIP
	}
	if host != "" && host != publicIP {
		node["host"] = host
	}
	nodeSSH := mapFromInterface(node["ssh"])
	if len(nodeSSH) == 0 {
		nodeSSH = map[string]interface{}{}
	}
	if host != "" {
		nodeSSH["host"] = host
	}
	if target.SSHUser != "" {
		nodeSSH["user"] = target.SSHUser
	}
	if target.SSHPort > 0 {
		nodeSSH["port"] = target.SSHPort
	}
	if len(nodeSSH) > 0 {
		node["ssh"] = nodeSSH
	}
	if len(nodes) == 0 {
		nodes = []interface{}{node}
	} else {
		nodes[0] = node
	}
	spec["nodes"] = nodes

	ssh := mapFromInterface(spec["ssh"])
	if len(ssh) == 0 {
		ssh = map[string]interface{}{}
	}
	if target.SSHUser != "" {
		ssh["user"] = target.SSHUser
	}
	if target.SSHPort > 0 {
		ssh["port"] = target.SSHPort
	}
	if len(ssh) > 0 {
		spec["ssh"] = ssh
	}

	metadata := mapFromInterface(spec["metadata"])
	if len(metadata) == 0 {
		metadata = map[string]interface{}{}
	}
	metadata[metadataKeyRuntimeSSHHost] = host
	metadata[metadataKeyRuntimePublicIP] = publicIP
	metadata[metadataKeyRuntimeSSHPort] = strconv.Itoa(firstPositiveInt(target.SSHPort, 22))
	if target.PrivateIP != "" {
		metadata[metadataKeyRuntimePrivateIP] = target.PrivateIP
	}
	if target.SSHUser != "" {
		metadata[metadataKeyRuntimeSSHUser] = target.SSHUser
	}
	spec["metadata"] = metadata

	next, err := yaml.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("serialize hydrated StackKits handoff spec: %w", err)
	}
	path, hash, err := persister.SaveStackSpecBytes(next)
	if err != nil {
		return "", "", err
	}
	return path, hash, nil
}

func hydratePersistedStackSpecPlatformNodes(persister *unifier.SpecPersister, platformNodes []PlatformNode) (string, string, error) {
	platformNodes = normalizePlatformNodes(platformNodes)
	if persister == nil || len(platformNodes) == 0 || !persister.StackSpecExists() {
		return "", "", nil
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		return "", "", fmt.Errorf("read StackKits handoff spec: %w", err)
	}
	var spec map[string]interface{}
	if unmarshalErr := yaml.Unmarshal(data, &spec); unmarshalErr != nil {
		return "", "", fmt.Errorf("parse StackKits handoff spec: %w", unmarshalErr)
	}
	if spec == nil {
		spec = map[string]interface{}{}
	}

	nodes := interfaceSlice(spec["nodes"])
	byKey := map[string]int{}
	for i, raw := range nodes {
		node := mapFromInterface(raw)
		key := stackSpecNodeIdentity(node)
		if key != "" {
			byKey[key] = i
		}
	}
	for _, platformNode := range platformNodes {
		nodeMap := stackSpecNodeFromPlatformNode(platformNode)
		key := stackSpecNodeIdentity(nodeMap)
		if key == "" {
			continue
		}
		if index, ok := byKey[key]; ok {
			nodes[index] = mergeStackSpecNode(mapFromInterface(nodes[index]), nodeMap)
			continue
		}
		byKey[key] = len(nodes)
		nodes = append(nodes, nodeMap)
	}
	if len(nodes) == 0 {
		return "", "", nil
	}
	spec["nodes"] = nodes

	next, err := yaml.Marshal(spec)
	if err != nil {
		return "", "", fmt.Errorf("serialize StackKits supplemental node handoff spec: %w", err)
	}
	path, hash, err := persister.SaveStackSpecBytes(next)
	if err != nil {
		return "", "", err
	}
	return path, hash, nil
}

func stackSpecNodeFromPlatformNode(node PlatformNode) map[string]interface{} {
	out := map[string]interface{}{}
	if node.Name != "" {
		out["name"] = node.Name
	}
	if role := stackSpecRoleFromPlatformNode(node.Role); role != "" {
		out["role"] = role
	}
	if node.IP != "" {
		out["ip"] = node.IP
	}
	if node.Host != "" && node.Host != node.IP {
		out["host"] = node.Host
	}
	if len(node.Services) > 0 {
		out["services"] = append([]string(nil), node.Services...)
	}
	if platform := stackSpecPlatformTarget(node.Platform); len(platform) > 0 {
		out["platform"] = platform
	}
	if ssh := stackSpecSSHFromPlatformNode(node); len(ssh) > 0 {
		out["ssh"] = ssh
	}
	return out
}

func stackSpecRoleFromPlatformNode(role string) string {
	switch canonicalPlatformNodeRole(role) {
	case "main":
		return "standalone"
	default:
		return canonicalPlatformNodeRole(role)
	}
}

func stackSpecPlatformTarget(platform NodePlatformTarget) map[string]interface{} {
	out := map[string]interface{}{}
	if platform.ServerID != "" {
		out["serverId"] = platform.ServerID
	}
	if platform.DestinationUUID != "" {
		out["destinationUuid"] = platform.DestinationUUID
	}
	if platform.EnvironmentID != "" {
		out["environmentId"] = platform.EnvironmentID
	}
	if platform.ProjectUUID != "" {
		out["projectUuid"] = platform.ProjectUUID
	}
	if platform.EnvironmentUUID != "" {
		out["environmentUuid"] = platform.EnvironmentUUID
	}
	return out
}

func stackSpecSSHFromPlatformNode(node PlatformNode) map[string]interface{} {
	out := map[string]interface{}{}
	if node.Bootstrap == nil || node.Bootstrap.SSH == nil {
		return out
	}
	ssh := node.Bootstrap.SSH
	host := firstNonEmpty(ssh.Host, node.Host, node.IP)
	if host != "" {
		out["host"] = host
	}
	if ssh.User != "" {
		out["user"] = ssh.User
	}
	if ssh.Port > 0 {
		out["port"] = ssh.Port
	}
	return out
}

func stackSpecNodeIdentity(node map[string]interface{}) string {
	return strings.ToLower(firstNonEmpty(
		stringFromMap(node, "name"),
		stringFromMap(node, "role"),
		stringFromMap(node, "type"),
	))
}

func mergeStackSpecNode(base, next map[string]interface{}) map[string]interface{} {
	if base == nil {
		base = map[string]interface{}{}
	}
	for key, value := range next {
		if existing, ok := base[key]; !ok || isEmptyStackSpecValue(existing) {
			base[key] = value
		}
	}
	return base
}

func isEmptyStackSpecValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []string:
		return len(typed) == 0
	case []interface{}:
		return len(typed) == 0
	case map[string]interface{}:
		return len(typed) == 0
	default:
		return false
	}
}

func interfaceSlice(value interface{}) []interface{} {
	switch typed := value.(type) {
	case []interface{}:
		return typed
	case []map[string]interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

// restoreStackSpecFromIntent rebuilds the StackKits CLI handoff spec when the
// instance no longer has it.
//
// The spec is written to the instance's local disk at creation time, and that
// disk is replaced on every deploy. A stack created before the running
// container therefore has an intent but no handoff spec, and the rollout fails
// at artifact generation with no way to recover: nothing regenerates it, so the
// stack can never be rolled out again.
//
// It is a pure projection of the intent, so rebuilding it restores exactly what
// creation would have written. An existing spec is never overwritten: it may
// already carry a routing overlay or a resolved managed-runtime target that the
// intent alone does not describe.
func restoreStackSpecFromIntent(persister *unifier.SpecPersister, intentBytes []byte) error {
	if persister == nil {
		return nil
	}
	if persister.StackSpecExists() {
		return repairPersistedStackSpec(persister)
	}
	if len(intentBytes) == 0 {
		return nil
	}
	var intent map[string]interface{}
	if err := yaml.Unmarshal(intentBytes, &intent); err != nil {
		return fmt.Errorf("parse persisted intent for StackKits handoff: %w", err)
	}
	specBytes, err := stackKitSpecBytesForPayload(withStackKitSpecShape(intent))
	if err != nil {
		return fmt.Errorf("rebuild StackKits handoff spec: %w", err)
	}
	if len(specBytes) == 0 {
		// Not a StackKits stack; the caller's own error path stays authoritative.
		return nil
	}
	if _, _, err := persister.SaveStackSpecBytes(specBytes); err != nil {
		return fmt.Errorf("persist rebuilt StackKits handoff spec: %w", err)
	}
	return nil
}

// withStackKitFromKitAlias fills in the handoff spec's stackkit field from the
// intent's kit alias.
//
// core.InputSpec accepts the kit under either name (kit or stackkit), but the
// handoff projection recognises only stackkit. An intent written with kit
// therefore projected to nothing, no handoff spec was ever persisted, and the
// rollout failed at artifact generation with no way to recover. Observed live
// on 2026-07-27: a cloud-kit stack whose intent carried kit: cloud-kit could
// not be rolled out at all.
//
// An explicit stackkit always wins; the input map is never mutated.
func withStackKitFromKitAlias(intent map[string]interface{}) map[string]interface{} {
	if intent == nil {
		return nil
	}
	if kit := strings.TrimSpace(stringFromInterface(intent["stackkit"])); kit != "" {
		return intent
	}
	alias := strings.TrimSpace(stringFromInterface(intent["kit"]))
	if alias == "" {
		return intent
	}
	// The alias is moved, not copied. A v1 StackSpec that also carries the
	// v2-only "kit" field is rejected outright by the StackKits CLI --
	// "v1 StackSpec contains v2-only top-level fields kit; refusing to discard"
	// -- so leaving both in place traded one rollout failure for another.
	next := make(map[string]interface{}, len(intent))
	for key, value := range intent {
		if key == "kit" {
			continue
		}
		next[key] = value
	}
	next["stackkit"] = alias
	return next
}

// repairPersistedStackSpec brings an already persisted handoff spec back to a
// shape the StackKits CLI accepts.
//
// A spec is normally left untouched -- it may carry a routing overlay or a
// resolved managed-runtime target that the intent alone does not describe. But
// a spec the StackKits CLI refuses outright is worth nothing to preserve:
//
//	StackSpec execution classification: v1 StackSpec contains v2-only
//	top-level fields kit; refusing to discard
//
// An earlier release wrote exactly that shape by copying the alias instead of
// moving it, and the bad spec then survived every later rollout because nothing
// would overwrite it. Only the offending field is touched; everything else in
// the persisted spec is preserved byte-for-byte in content.
func repairPersistedStackSpec(persister *unifier.SpecPersister) error {
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		return fmt.Errorf("read StackKits handoff spec: %w", err)
	}
	var spec map[string]interface{}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parse StackKits handoff spec: %w", err)
	}
	// Captured before any repair so the comparison below is content against
	// content. Comparing the repaired encoding to the original file bytes would
	// always differ -- re-marshalling reorders keys and reindents -- and would
	// rewrite a healthy spec on every rollout.
	before, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("re-encode StackKits handoff spec: %w", err)
	}

	// Repairing must not key on one field. Gating on "kit" meant that once the
	// v2-only field was removed the function returned before it ever looked at
	// anything else, so a spec that was also list-shaped stayed broken and the
	// rollout kept failing on the identical decode error.
	//
	// withStackKitFromKitAlias leaves the map alone when stackkit is already
	// set, which is exactly one of the shapes being repaired here: both fields
	// present. The v2-only field has to go regardless, so remove it explicitly
	// and only fall back to it when stackkit is genuinely missing.
	repaired := withServicesAsMap(spec)
	if strings.TrimSpace(stringFromInterface(repaired["stackkit"])) == "" {
		if alias := strings.TrimSpace(stringFromInterface(repaired["kit"])); alias != "" {
			repaired["stackkit"] = alias
		}
	}
	delete(repaired, "kit")
	next, err := yaml.Marshal(repaired)
	if err != nil {
		return fmt.Errorf("re-encode StackKits handoff spec: %w", err)
	}
	// Write only on a real change, so a healthy spec keeps its exact bytes and
	// nothing churns on every rollout.
	if string(next) == string(before) {
		return nil
	}
	if _, _, err := persister.SaveStackSpecBytes(next); err != nil {
		return fmt.Errorf("persist repaired StackKits handoff spec: %w", err)
	}
	return nil
}

// withStackKitSpecShape projects a Techstack intent onto the shape a v1
// StackSpec accepts.
func withStackKitSpecShape(intent map[string]interface{}) map[string]interface{} {
	return withServicesAsMap(withStackKitFromKitAlias(intent))
}

// withServicesAsMap converts a list-shaped services block into the map the
// StackKits v1 StackSpec requires.
//
// core.InputServiceSpecs accepts both shapes, so a Techstack intent may carry
// either, but the StackSpec decoder accepts only a mapping and fails the whole
// rollout on a sequence:
//
//	cannot decode v1 StackSpec: yaml: unmarshal errors:
//	  line 37: cannot unmarshal !!seq into map[string]interface {}
//
// Each entry keys on its name; the remaining fields are preserved, and a service
// listed without an explicit flag is enabled, because listing it is the
// enablement. Entries without a usable name are dropped rather than guessed at.
// The input map is never mutated.
func withServicesAsMap(spec map[string]interface{}) map[string]interface{} {
	if spec == nil {
		return nil
	}
	list, isList := spec["services"].([]interface{})
	if !isList {
		return spec
	}
	services := make(map[string]interface{}, len(list))
	for _, raw := range list {
		entry := mapFromInterface(raw)
		name := strings.TrimSpace(stringFromInterface(entry["name"]))
		if name == "" {
			if scalar := strings.TrimSpace(stringFromInterface(raw)); scalar != "" {
				services[scalar] = map[string]interface{}{"enabled": true}
			}
			continue
		}
		value := make(map[string]interface{}, len(entry))
		for key, item := range entry {
			if key == "name" {
				continue
			}
			value[key] = item
		}
		if _, stated := value["enabled"]; !stated {
			value["enabled"] = true
		}
		services[name] = value
	}
	next := make(map[string]interface{}, len(spec))
	for key, value := range spec {
		next[key] = value
	}
	next["services"] = services
	return next
}
