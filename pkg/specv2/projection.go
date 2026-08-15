package specv2

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// workloadTemplate captures how one wizard goal materializes as an
// Architecture v2 workload selection. Only goals with a shipped v2 workload
// appear here (D6b: everything else stays honest intent, never spec).
type workloadTemplate struct {
	workloadID  string
	alternative string
	// requiredSecretKeys lists the workload's required secretRefs inputs;
	// the projection mints deterministic techstack:// handles for them and
	// the wizard-run path (phase 3) binds real custody behind the handle.
	requiredSecretKeys []string
}

// goalWorkloads maps wizard goal tiles onto shipped v2 workloads. The nine
// tiles stay visible in the UI; unmapped goals are returned as intent so the
// caller persists them on the homelab (auto-activation when StackKits ships
// the workload — bead kombify-StackKits-hil).
var goalWorkloads = map[string]workloadTemplate{
	"photos": {
		workloadID:         "photos",
		alternative:        "immich",
		requiredSecretKeys: []string{"database-password"},
	},
}

// Projection is the result of applying wizard intent to a kit seed.
type Projection struct {
	// Spec is the projected document, ready for pinned `stackkit validate`.
	Spec map[string]any
	// NodeID is the node this run onboarded (join) or the seed's primary
	// node (found).
	NodeID string
	// UnmappedGoals are selected goals without a shipped v2 workload; they
	// belong in homelabs.intent_json, never in the spec.
	UnmappedGoals []string
	// UnmappedPurpose is set when the server purpose (e.g. "backup") has no
	// shipped v2 capability yet and was therefore recorded as intent only.
	UnmappedPurpose string
}

// Project applies the wizard intent onto a materialized kit seed. The seed is
// never mutated; the returned spec is a deep copy. Callers must re-validate
// the result with the pinned CLI — this function performs only the closed,
// mechanical projection.
func Project(seed map[string]any, intent WizardIntent, homelabID string) (*Projection, error) {
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	if len(seed) == 0 {
		return nil, fmt.Errorf("specv2: empty seed")
	}
	spec := deepCopyValue(seed).(map[string]any)

	metadata, ok := spec["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("specv2: seed has no metadata object")
	}
	// metadata.name is a #ContractID (^[a-z][a-z0-9-]*$); the human display
	// name lives on the homelab/identity, never in the spec.
	metadata["name"] = contractID(intent.Name)
	if strings.TrimSpace(homelabID) != "" {
		metadata["fleetRef"] = strings.TrimSpace(homelabID)
	}

	result := &Projection{Spec: spec}

	switch intent.KitAssignment.Mode {
	case KitAssignmentFound:
		nodeID, err := applyFoundRoles(spec, intent.Server.Roles)
		if err != nil {
			return nil, err
		}
		result.NodeID = nodeID
	case KitAssignmentJoin:
		nodeID, err := appendJoinNode(spec, intent.Server)
		if err != nil {
			return nil, err
		}
		result.NodeID = nodeID
	}

	unmapped, err := applyGoalWorkloads(spec, intent.Goals)
	if err != nil {
		return nil, err
	}
	result.UnmappedGoals = unmapped

	if purpose := strings.ToLower(strings.TrimSpace(intent.Server.Purpose)); purpose != "" {
		// No v2 catalog capability maps a dedicated-purpose server yet
		// (backup topologies are contract-only in StackKits); fail closed
		// into intent instead of inventing spec state.
		result.UnmappedPurpose = purpose
	}

	return result, nil
}

// applyFoundRoles optionally overrides the seed's primary-node roles. The
// primary node keeps its controller role unconditionally: a kit deployment
// without a controller cannot pass kit binding.
func applyFoundRoles(spec map[string]any, roles []string) (string, error) {
	nodes, err := specNodes(spec)
	if err != nil {
		return "", err
	}
	primary := primaryNode(nodes)
	if primary == nil {
		return "", fmt.Errorf("specv2: seed has no controller node")
	}
	nodeID, _ := primary["id"].(string)
	if len(roles) == 0 {
		return nodeID, nil
	}
	normalized := normalizeRoles(roles)
	if !slices.Contains(normalized, RoleController) {
		normalized = append([]string{RoleController}, normalized...)
	}
	primary["roles"] = toAnySlice(normalized)
	return nodeID, nil
}

// appendJoinNode adds the run's server as a new node on the existing kit
// deployment, placed on the controller's site (single-site kits; multi-site
// placement arrives with the federation work).
func appendJoinNode(spec map[string]any, server ServerIntent) (string, error) {
	nodes, err := specNodes(spec)
	if err != nil {
		return "", err
	}
	primary := primaryNode(nodes)
	if primary == nil {
		return "", fmt.Errorf("specv2: existing spec has no controller node")
	}
	siteRef, _ := primary["siteRef"].(string)

	roles := normalizeRoles(server.Roles)
	if len(roles) == 0 {
		roles = []string{RoleWorker}
	}
	if slices.Contains(roles, RoleController) {
		return "", fmt.Errorf("specv2: joining a second controller is not supported; found a new kit deployment instead")
	}

	prefix := roles[0]
	nodeID := nextNodeID(nodes, prefix)
	profile := "standard"
	if slices.Contains(roles, RoleStorage) {
		profile = "storage"
	}

	node := map[string]any{
		"id":            nodeID,
		"siteRef":       siteRef,
		"roles":         toAnySlice(roles),
		"failureDomain": "node-" + nodeID,
		"enabled":       true,
		"hardware": map[string]any{
			"arch":    "amd64",
			"profile": profile,
		},
	}
	spec["nodes"] = append(nodes, node)
	return nodeID, nil
}

// applyGoalWorkloads adds the workload selection for every mapped goal that
// the seed does not already carry (modern-homelab preseeds photos — the seed
// wins). Unmapped goals are returned for intent persistence.
func applyGoalWorkloads(spec map[string]any, goals []string) ([]string, error) {
	var unmapped []string
	if len(goals) == 0 {
		return nil, nil
	}
	workloads, _ := spec["workloads"].(map[string]any)
	if workloads == nil {
		workloads = map[string]any{}
	}
	specName, _ := spec["metadata"].(map[string]any)["name"].(string)

	seen := map[string]bool{}
	for _, goal := range goals {
		goal = strings.ToLower(strings.TrimSpace(goal))
		if goal == "" || seen[goal] {
			continue
		}
		seen[goal] = true
		template, mapped := goalWorkloads[goal]
		if !mapped {
			unmapped = append(unmapped, goal)
			continue
		}
		if _, exists := workloads[template.workloadID]; exists {
			continue
		}
		siteRef, err := workloadSite(spec, template.workloadID)
		if err != nil {
			return nil, err
		}
		secretRefs := map[string]any{}
		for _, key := range template.requiredSecretKeys {
			secretRefs[key] = fmt.Sprintf("techstack://%s/%s/%s", slugify(specName), template.workloadID, key)
		}
		entry := map[string]any{
			"alternative": template.alternative,
			"placement": map[string]any{
				"siteRefs":      []any{siteRef},
				"nodeRefs":      []any{},
				"requiresRoles": []any{},
			},
		}
		if len(secretRefs) > 0 {
			entry["secretRefs"] = secretRefs
		}
		workloads[template.workloadID] = entry
	}
	if len(workloads) > 0 {
		spec["workloads"] = workloads
	}
	sort.Strings(unmapped)
	return unmapped, nil
}

// workloadSite resolves where a workload may run: the kit's data binding for
// that workload is the site authority (modern binds photos to home; placing
// it on the cloud site is rejected by the bridge envelope), falling back to
// the controller's site.
func workloadSite(spec map[string]any, workloadID string) (string, error) {
	if data, ok := spec["data"].(map[string]any); ok {
		if bindings, ok := data["bindings"].(map[string]any); ok {
			if binding, ok := bindings[workloadID].(map[string]any); ok {
				if site, ok := binding["primarySiteRef"].(string); ok && site != "" {
					return site, nil
				}
			}
		}
	}
	nodes, err := specNodes(spec)
	if err != nil {
		return "", err
	}
	primary := primaryNode(nodes)
	if primary == nil {
		return "", fmt.Errorf("specv2: no controller node to derive workload placement from")
	}
	site, _ := primary["siteRef"].(string)
	if site == "" {
		return "", fmt.Errorf("specv2: controller node has no siteRef")
	}
	return site, nil
}

func specNodes(spec map[string]any) ([]any, error) {
	nodes, ok := spec["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return nil, fmt.Errorf("specv2: seed has no nodes")
	}
	return nodes, nil
}

// primaryNode returns the first controller-bearing node.
func primaryNode(nodes []any) map[string]any {
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		roles, _ := node["roles"].([]any)
		for _, role := range roles {
			if role == RoleController {
				return node
			}
		}
	}
	return nil
}

// nextNodeID allocates the lowest free "<prefix>-N" id.
func nextNodeID(nodes []any, prefix string) string {
	used := map[string]bool{}
	for _, raw := range nodes {
		if node, ok := raw.(map[string]any); ok {
			if id, ok := node["id"].(string); ok {
				used[id] = true
			}
		}
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d", prefix, i)
		if !used[candidate] {
			return candidate
		}
	}
}

func normalizeRoles(roles []string) []string {
	var out []string
	for _, role := range roles {
		normalized := NormalizeRole(role)
		if normalized != "" && !slices.Contains(out, normalized) {
			out = append(out, normalized)
		}
	}
	return out
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func slugify(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "stack"
	}
	return slug
}

// contractID shapes a value into the #ContractID pattern ^[a-z][a-z0-9-]*$.
func contractID(value string) string {
	slug := slugify(value)
	if slug[0] >= '0' && slug[0] <= '9' {
		slug = "s-" + slug
	}
	return slug
}

// deepCopyValue clones JSON-shaped data (maps, slices, scalars).
func deepCopyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, inner := range typed {
			out[key] = deepCopyValue(inner)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, inner := range typed {
			out[i] = deepCopyValue(inner)
		}
		return out
	default:
		return typed
	}
}
