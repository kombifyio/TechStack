package specv2

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
)

// GoalAuthoring is the release-owned workload projection for selected wizard
// goals. Workloads and local custody references come from the pinned StackKits
// CLI; Techstack preserves them and honestly persists UnmappedGoals.
// CLI-authored useCases stay off the operational spec: #KitSpecBinding
// rejects value.spec.useCases.
type GoalAuthoring struct {
	UseCases  []string
	Workloads map[string]any
	// ModuleProfiles associates each newly authored workload with its
	// release-owned module profile intent. Seed-owned workloads remain intact.
	ModuleProfiles map[string]map[string]any
	UnmappedGoals  []string
}

// GoalAuthor resolves shipped use cases from a published compatibility
// manifest and asks that exact release's CLI to author their workload entries.
type GoalAuthor interface {
	AuthorGoals(ctx context.Context, kitSlug, name, domainBase, apiVersion string, goals []string) (GoalAuthoring, error)
}

// Projector is the single projection path shared by preview and wizard runs.
type Projector interface {
	Project(ctx context.Context, seed map[string]any, intent WizardIntent, homelabID string) (*Projection, error)
}

// ReleaseProjector applies Techstack-owned identity/topology intent around
// workload entries authored by the pinned StackKits release.
type ReleaseProjector struct {
	author GoalAuthor
}

func NewReleaseProjector(author GoalAuthor) *ReleaseProjector {
	return &ReleaseProjector{author: author}
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
func (p *ReleaseProjector) Project(ctx context.Context, seed map[string]any, intent WizardIntent, homelabID string) (*Projection, error) {
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	if len(intent.Access.Publications) > 0 {
		return nil, fmt.Errorf("specv2: explicit public service publication is unavailable in the pinned StackKits release")
	}
	if err := RequireCanonicalV2(seed); err != nil {
		return nil, fmt.Errorf("specv2: projection seed: %w", err)
	}
	spec := deepCopyValue(seed).(map[string]any)
	DropBindingRejectedFields(spec)

	metadata, ok := spec["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("specv2: seed has no metadata object")
	}
	// metadata.name is a #ContractID (^[a-z][a-z0-9-]*$); the human display
	// name lives on the homelab/identity, never in the spec.
	metadata["name"] = contractID(intent.Name)
	stackID, _ := metadata["stackId"].(string)
	// StackKits owns this identity. Make it explicit for a new deployment so
	// Techstack never has to equate its internal kit-deployment UUID with the
	// public ResolvedPlan stackId. Expansion preserves an already-owned id.
	if intent.KitAssignment.Mode == KitAssignmentFound || strings.TrimSpace(stackID) == "" {
		metadata["stackId"] = metadata["name"]
	}
	if strings.TrimSpace(homelabID) != "" {
		metadata["fleetRef"] = strings.TrimSpace(homelabID)
	}
	if err := projectFoundDomain(spec, intent); err != nil {
		return nil, err
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

	if len(intent.Goals) > 0 {
		if err := applyJoinOrFoundGoals(ctx, p, spec, intent, result); err != nil {
			return nil, err
		}
	}
	DropBindingRejectedFields(spec)
	if err := RequireCanonicalV2(spec); err != nil {
		return nil, fmt.Errorf("specv2: projected spec: %w", err)
	}

	if purpose := strings.ToLower(strings.TrimSpace(intent.Server.Purpose)); purpose != "" {
		// No v2 catalog capability maps a dedicated-purpose server yet
		// (backup topologies are contract-only in StackKits); fail closed
		// into intent instead of inventing spec state.
		result.UnmappedPurpose = purpose
	}

	return result, nil
}

// A build-time template address is not customer intent. Resolve the address
// before authoring workloads so their routes use the same domain as the core.
// Joining an existing deployment preserves its already-owned address.
func projectFoundDomain(spec map[string]any, intent WizardIntent) error {
	if intent.KitAssignment.Mode != KitAssignmentFound {
		return nil
	}
	network, _ := spec["network"].(map[string]any)
	domain, _ := network["domain"].(map[string]any)
	if domain == nil {
		return fmt.Errorf("specv2: kit seed has no network domain")
	}
	requested := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(intent.DomainBase)), ".")
	if intent.KitAssignment.KitSlug == KitSlugCloud && strings.TrimSpace(intent.Server.Transport) == TransportKombifyCloud {
		if requested != "" && requested != "kombify.me" {
			return fmt.Errorf("specv2: new managed Cloud deployments require platform addressing")
		}
		domain["base"] = "kombify.me"
		return projectFoundDomainConsumers(spec, intent.KitAssignment.KitSlug, "kombify.me")
	}
	if reservedDomain(requested) {
		return fmt.Errorf("specv2: choose a usable domain; .invalid addresses cannot serve an installed StackKit")
	}
	if requested == "" {
		requested, _ = domain["base"].(string)
		requested = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(requested)), ".")
		if requested == "" || reservedDomain(requested) {
			if intent.KitAssignment.KitSlug != KitSlugBasement ||
				(intent.Access.Mode != "" && intent.Access.Mode != AccessModeLocal) {
				return fmt.Errorf("specv2: choose a domain for remote access before creating this StackKit")
			}
			// StackKits Basement's canonical LAN domain (stackfile.cue).
			// Its existing internal TLS and LAN DNS owners implement this path.
			requested = "home"
		}
	}
	domain["base"] = requested
	return projectFoundDomainConsumers(spec, intent.KitAssignment.KitSlug, requested)
}

// These concrete seed endpoints cannot be re-evaluated by CUE after authoring.
// Mirror the pinned release's initial domain projection, only for new stacks.
func projectFoundDomainConsumers(spec map[string]any, kitSlug, domainBase string) error {
	switch kitSlug {
	case KitSlugCloud:
		routes, ok := spec["routes"].(map[string]any)
		if !ok || len(routes) == 0 {
			return fmt.Errorf("specv2: Cloud seed has no service routes")
		}
		for id, raw := range routes {
			route, ok := raw.(map[string]any)
			if !ok || route == nil {
				return fmt.Errorf("specv2: Cloud route %s is not an object", id)
			}
			if route["exposure"] != "public" {
				continue
			}
			service, _ := route["serviceRef"].(string)
			if strings.TrimSpace(service) == "" {
				return fmt.Errorf("specv2: Cloud route %s has no service reference", id)
			}
			route["host"] = service + "." + domainBase
		}
	case KitSlugModern:
		bridge, _ := spec["bridge"].(map[string]any)
		publications, _ := bridge["publications"].([]any)
		if len(publications) != 1 {
			return fmt.Errorf("specv2: Modern seed has no single default publication")
		}
		publication, _ := publications[0].(map[string]any)
		if publication == nil || publication["serviceRef"] != "photos" {
			return fmt.Errorf("specv2: Modern seed has no default photos publication")
		}
		publication["host"] = "photos." + domainBase
	}
	return nil
}

func reservedDomain(domain string) bool {
	return domain == "invalid" || strings.HasSuffix(domain, ".invalid")
}

func applyJoinOrFoundGoals(ctx context.Context, p *ReleaseProjector, spec map[string]any, intent WizardIntent, result *Projection) error {
	if p == nil || p.author == nil {
		if hasSmartHomeSelection(intent) {
			return fmt.Errorf("specv2: Smart Home selection requires pinned release authoring")
		}
		result.UnmappedGoals = appendUnmappedGoals(result.UnmappedGoals, intent.Goals)
		return nil
	}
	kit, _ := spec["kit"].(map[string]any)
	kitSlug, _ := kit["slug"].(string)
	network, _ := spec["network"].(map[string]any)
	domain, _ := network["domain"].(map[string]any)
	domainBase, _ := domain["base"].(string)
	apiVersion, _ := spec["apiVersion"].(string)
	authored, err := p.author.AuthorGoals(
		ctx,
		strings.TrimSpace(kitSlug),
		contractID(intent.Name),
		strings.TrimSpace(domainBase),
		apiVersion,
		intent.Goals,
	)
	if err != nil {
		if hasSmartHomeSelection(intent) {
			return fmt.Errorf("specv2: selected Smart Home installation needs release authoring: %w", err)
		}
		result.UnmappedGoals = appendUnmappedGoals(result.UnmappedGoals, intent.Goals)
		return nil
	}
	if err := proposeSmartHomeSelection(spec, intent, &authored); err != nil {
		return err
	}
	if err := applyAuthoredWorkloads(spec, authored); err != nil {
		if hasSmartHomeSelection(intent) {
			return err
		}
		result.UnmappedGoals = appendUnmappedGoals(result.UnmappedGoals, intent.Goals)
		return nil
	}
	result.UnmappedGoals = appendUnmappedGoals(result.UnmappedGoals, authored.UnmappedGoals)
	return nil
}

func appendUnmappedGoals(existing, extra []string) []string {
	out := append([]string(nil), existing...)
	for _, raw := range extra {
		goal := strings.ToLower(strings.TrimSpace(raw))
		if goal != "" && !slices.Contains(out, goal) {
			out = append(out, goal)
		}
	}
	sort.Strings(out)
	return out
}

// StackKitInstanceID returns the explicit StackKits identity projected into a
// validated v2 spec. Callers must not fall back to a Techstack record id.
func StackKitInstanceID(spec map[string]any) string {
	metadata, _ := spec["metadata"].(map[string]any)
	value, _ := metadata["stackId"].(string)
	return strings.TrimSpace(value)
}

// ResolvedStackKitInstanceID mirrors StackKits' v2 resolution rule for
// existing documents: an explicit metadata.stackId wins, while metadata.name
// remains the compatibility identity for specs authored before stackId.
func ResolvedStackKitInstanceID(spec map[string]any) string {
	if explicit := StackKitInstanceID(spec); explicit != "" {
		return explicit
	}
	metadata, _ := spec["metadata"].(map[string]any)
	value, _ := metadata["name"].(string)
	return strings.TrimSpace(value)
}

// applyFoundRoles extends the seed's primary-node roles. Kit seeds own their
// required baseline roles (Basement needs its controller to remain a worker),
// while the wizard may add a more specific role such as storage.
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
	merged := make([]string, 0, len(roles)+2)
	if seedRoles, ok := primary["roles"].([]any); ok {
		for _, raw := range seedRoles {
			if role, ok := raw.(string); ok && !slices.Contains(merged, role) {
				merged = append(merged, role)
			}
		}
	}
	for _, role := range normalizeRoles(roles) {
		if !slices.Contains(merged, role) {
			merged = append(merged, role)
		}
	}
	if !slices.Contains(merged, RoleController) {
		merged = append([]string{RoleController}, merged...)
	}
	primary["roles"] = toAnySlice(merged)
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
	if slices.Contains(roles, RoleController) {
		filtered := make([]string, 0, len(roles))
		for _, role := range roles {
			if role != RoleController {
				filtered = append(filtered, role)
			}
		}
		roles = filtered
	}
	if len(roles) == 0 {
		roles = []string{RoleWorker}
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

// applyAuthoredWorkloads adds release-authored workload selections that the
// seed does not already carry (a seed-owned entry wins). Secret references stay
// release-owned so the CLI can establish local owner-bound custody at init.
func applyAuthoredWorkloads(spec map[string]any, authored GoalAuthoring) error {
	workloads, _ := spec["workloads"].(map[string]any)
	workloads, _ = deepCopyValue(workloads).(map[string]any)
	if workloads == nil {
		workloads = map[string]any{}
	}
	modules, _ := spec["modules"].(map[string]any)
	modules, _ = deepCopyValue(modules).(map[string]any)
	if modules == nil {
		modules = map[string]any{}
	}
	ids := make([]string, 0, len(authored.Workloads))
	for id := range authored.Workloads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if existing, exists := workloads[id]; exists {
			entry, ok := existing.(map[string]any)
			if !ok {
				return fmt.Errorf("specv2: release-authored seed contains invalid workload %q", id)
			}
			if err := validateSecretCustodyHandles(entry, id); err != nil {
				return err
			}
			continue
		}
		entry, ok := deepCopyValue(authored.Workloads[id]).(map[string]any)
		if !ok || len(entry) == 0 {
			return fmt.Errorf("specv2: pinned StackKits release authored invalid workload %q", id)
		}
		if err := validateSecretCustodyHandles(entry, id); err != nil {
			return err
		}
		for moduleID, profile := range authored.ModuleProfiles[id] {
			if existing, exists := modules[moduleID]; exists && !reflect.DeepEqual(existing, profile) {
				return fmt.Errorf("specv2: authored workload %q conflicts with existing module profile %q", id, moduleID)
			}
			modules[moduleID] = deepCopyValue(profile)
		}
		workloads[id] = entry
	}
	if len(workloads) > 0 {
		spec["workloads"] = workloads
	}
	if len(modules) > 0 {
		spec["modules"] = modules
	}
	return nil
}

func validateSecretCustodyHandles(workload map[string]any, workloadID string) error {
	refs, ok := workload["secretRefs"].(map[string]any)
	if !ok {
		return nil
	}
	for key, raw := range refs {
		value, ok := raw.(string)
		releaseHandle := fmt.Sprintf("secret://workloads/%s/%s", workloadID, key)
		if !ok || value != releaseHandle {
			return fmt.Errorf("specv2: pinned StackKits workload %q secret %q has non-canonical custody ref", workloadID, key)
		}
	}
	return nil
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

var _ Projector = (*ReleaseProjector)(nil)
