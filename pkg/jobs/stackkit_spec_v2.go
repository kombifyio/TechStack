package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/pkg/unifier"
	"gopkg.in/yaml.v3"
)

const (
	// stackKitSpecTemplateEnv points at the canonical v2 StackSpec documents the
	// image authored at build time, one directory per kit.
	stackKitSpecTemplateEnv = "TECHSTACK_STACKKIT_SPEC_TEMPLATES"

	// canonicalStackSpecAPIVersion is the only shape the pinned CLI executes.
	canonicalStackSpecAPIVersion = "stackkit/v2alpha1"

	// canonicalStackSpecFilename is derived state. It is written next to the
	// persisted handoff rather than over it: the routing overlay, the
	// managed-runtime hydration, and the v1 repair all own stack-spec.yaml and
	// write shapes a canonical document rejects -- a bare network.domain string
	// where v2 has a network.domain.base mapping, legacy top-level ssh and
	// nodes[].ip, and routing_* metadata keys. Deriving a fresh document each
	// rollout also means a later domain change reaches the CLI.
	canonicalStackSpecFilename = "stack-spec.v2.json"

	// payloadKeyStackSpecV2 is the provision-payload / config_json key carrying
	// the wizard-run projection (a full canonical v2 StackSpec). The literal is
	// mirrored by internal/routes/stacks (stackConfigKeySpecV2) — the payload
	// contract between the facade and this handler.
	payloadKeyStackSpecV2 = "stack_spec_v2"

	// projectedStackSpecFilename is the wizard-run projection persisted at
	// provision time beside the v1 handoff. canonicalStackSpecFor prefers it
	// over the build-time template: the projection carries deltas (role
	// overrides, joined nodes, workloads, fleetRef) a template derivation
	// would silently drop.
	projectedStackSpecFilename = "stack-spec.projected.v2.json"
)

// canonicalStackSpecFor returns the path of the canonical v2 StackSpec the
// pinned CLI should execute, deriving it from the persisted handoff when needed.
//
// Techstack hand-authors the handoff in the v1 shape. StackKits 0.8 reads v1
// only through the migration adapter, so every rollout died at generate:
//
//	Error: architecturev2 migration_required: StackSpec v1 is readable only
//	through the migration adapter and cannot enter validate
//
// The migration adapter is not the way out: it still requires a complete
// explicit v2 draft as its input, so it only adds lineage. The canonical
// document is produced by "stackkit init <kit>", which the image runs at build
// time -- init resolves and verifies the published release over the network, so
// running it per rollout would put GitHub in the path of every deploy.
//
// Only the two fields init itself overrides are applied here, metadata.name and
// network.domain.base; everything else stays exactly as StackKits authored it,
// and the pinned CLI's own validate remains the authority.
//
// canonicalStackSpec is the document the pinned CLI executes plus the two facts
// the caller needs about it.
type canonicalStackSpec struct {
	// Path is the document to pass as --spec.
	Path string
	// Derived reports whether it was projected from a legacy handoff rather
	// than already canonical.
	Derived bool
	// OutputRoot is the directory the resolved plan governs. The CLI refuses
	// any other destination -- "architecture v2 --output must resolve to
	// governed ResolvedPlan outputRoot deploy" -- so the caller must generate
	// into this and not into a directory of its own choosing.
	OutputRoot string
}

// It reports whether the document was derived rather than already canonical.
func canonicalStackSpecFor(stackSpecPath, stackKit, stackName string) (canonicalStackSpec, error) {
	current, err := os.ReadFile(stackSpecPath) // #nosec G304 -- path is validated against the work directory by the caller.
	if err != nil {
		return canonicalStackSpec{}, fmt.Errorf("read StackKits handoff spec: %w", err)
	}
	handoff, err := decodeStackSpecDocument(current)
	if err != nil {
		return canonicalStackSpec{}, err
	}
	if strings.TrimSpace(stringFromInterface(handoff["apiVersion"])) == canonicalStackSpecAPIVersion {
		outputRoot, rootErr := stackSpecOutputRoot(handoff)
		if rootErr != nil {
			return canonicalStackSpec{}, rootErr
		}
		return canonicalStackSpec{Path: stackSpecPath, OutputRoot: outputRoot}, nil
	}
	if canonical, ok, projErr := canonicalFromProjected(stackSpecPath, handoff); projErr != nil {
		return canonicalStackSpec{}, projErr
	} else if ok {
		return canonical, nil
	}

	kit := strings.TrimSpace(stackKit)
	if kit == "" {
		return canonicalStackSpec{}, fmt.Errorf("canonical StackSpec authoring requires the target StackKit")
	}
	name := firstNonEmpty(
		strings.TrimSpace(stringFromInterface(mapFromInterface(handoff["metadata"])["name"])),
		strings.TrimSpace(stringFromInterface(handoff["name"])),
		strings.TrimSpace(stackName),
	)
	if name == "" {
		return canonicalStackSpec{}, fmt.Errorf("canonical StackSpec authoring requires the stack name")
	}
	domain, err := stackSpecDomainBase(handoff)
	if err != nil {
		return canonicalStackSpec{}, err
	}

	template, err := readStackKitSpecTemplate(kit)
	if err != nil {
		return canonicalStackSpec{}, err
	}
	template["metadata"] = map[string]interface{}{"name": name}
	network := mapFromInterface(template["network"])
	if len(network) == 0 {
		return canonicalStackSpec{}, fmt.Errorf("canonical %s StackSpec template has no network intent", kit)
	}
	networkDomain := mapFromInterface(network["domain"])
	if len(networkDomain) == 0 {
		return canonicalStackSpec{}, fmt.Errorf("canonical %s StackSpec template has no network domain", kit)
	}
	networkDomain["base"] = domain
	network["domain"] = networkDomain
	template["network"] = network

	// The canonical document is deterministic JSON, and the CLI reads it as
	// YAML. Encoding it as JSON keeps the bytes closest to what init wrote.
	outputRoot, err := stackSpecOutputRoot(template)
	if err != nil {
		return canonicalStackSpec{}, err
	}

	next, err := json.Marshal(template)
	if err != nil {
		return canonicalStackSpec{}, fmt.Errorf("serialize canonical StackSpec: %w", err)
	}
	canonicalPath := filepath.Join(filepath.Dir(stackSpecPath), canonicalStackSpecFilename)
	if err := os.WriteFile(canonicalPath, next, 0o600); err != nil {
		return canonicalStackSpec{}, fmt.Errorf("persist canonical StackSpec: %w", err)
	}
	return canonicalStackSpec{Path: canonicalPath, Derived: true, OutputRoot: outputRoot}, nil
}

// canonicalFromProjected derives the canonical document from the wizard-run
// projection persisted at provision time, when one exists. Only the routing
// domain is refreshed from the live handoff chain (same rule as the template
// derivation, so a later domain change reaches the CLI); every projection
// delta — role overrides, joined nodes, workloads, fleetRef — stays exactly
// as the facade validated it. A missing projection falls back to the
// template path; a corrupt one fails the rollout instead of silently losing
// the projection.
func canonicalFromProjected(stackSpecPath string, handoff map[string]interface{}) (canonicalStackSpec, bool, error) {
	projectedPath := filepath.Join(filepath.Dir(stackSpecPath), projectedStackSpecFilename)
	data, err := os.ReadFile(projectedPath) // #nosec G304 -- sibling of the caller-validated handoff path.
	if err != nil {
		if os.IsNotExist(err) {
			return canonicalStackSpec{}, false, nil
		}
		return canonicalStackSpec{}, false, fmt.Errorf("read projected StackSpec: %w", err)
	}
	projected, err := decodeStackSpecDocument(data)
	if err != nil {
		return canonicalStackSpec{}, false, fmt.Errorf("projected StackSpec: %w", err)
	}
	if got := strings.TrimSpace(stringFromInterface(projected["apiVersion"])); got != canonicalStackSpecAPIVersion {
		return canonicalStackSpec{}, false, fmt.Errorf("projected StackSpec has apiVersion %q, want %q", got, canonicalStackSpecAPIVersion)
	}
	// Best effort: the wizard handoff usually resolves no domain of its own;
	// the projection then keeps the domain it was validated with.
	if domain, domainErr := stackSpecDomainBase(handoff); domainErr == nil && strings.TrimSpace(domain) != "" {
		network := mapFromInterface(projected["network"])
		networkDomain := mapFromInterface(network["domain"])
		if len(network) > 0 && len(networkDomain) > 0 {
			networkDomain["base"] = domain
			network["domain"] = networkDomain
			projected["network"] = network
		}
	}
	outputRoot, err := stackSpecOutputRoot(projected)
	if err != nil {
		return canonicalStackSpec{}, false, err
	}
	next, err := json.Marshal(projected)
	if err != nil {
		return canonicalStackSpec{}, false, fmt.Errorf("serialize canonical StackSpec: %w", err)
	}
	canonicalPath := filepath.Join(filepath.Dir(stackSpecPath), canonicalStackSpecFilename)
	if err := os.WriteFile(canonicalPath, next, 0o600); err != nil {
		return canonicalStackSpec{}, false, fmt.Errorf("persist canonical StackSpec: %w", err)
	}
	return canonicalStackSpec{Path: canonicalPath, Derived: true, OutputRoot: outputRoot}, true, nil
}

// persistProjectedStackSpec persists the wizard-run projection carried in the
// provision payload (spec[stack_spec_v2]) beside the v1 handoff. It is the
// disk copy canonicalFromProjected executes; the durable authority stays in
// the control plane's config_json, re-materialized by every provision run.
func persistProjectedStackSpec(persister *unifier.SpecPersister, specData interface{}) (string, error) {
	dataMap, ok := specData.(map[string]interface{})
	if !ok {
		return "", nil
	}
	path := filepath.Join(filepath.Dir(persister.GetStackSpecPath()), projectedStackSpecFilename)
	projected := mapFromInterface(dataMap[payloadKeyStackSpecV2])
	if len(projected) == 0 {
		// A provision without a projection (e.g. an explicit body spec)
		// retires any stale sibling — the projection must always mirror the
		// LATEST provision payload, never override a newer explicit spec.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove stale projected StackSpec: %w", err)
		}
		return "", nil
	}
	if got := strings.TrimSpace(stringFromInterface(projected["apiVersion"])); got != canonicalStackSpecAPIVersion {
		return "", fmt.Errorf("payload %s has apiVersion %q, want %q", payloadKeyStackSpecV2, got, canonicalStackSpecAPIVersion)
	}
	next, err := json.Marshal(projected)
	if err != nil {
		return "", fmt.Errorf("serialize projected StackSpec: %w", err)
	}
	if err := os.WriteFile(path, next, 0o600); err != nil {
		return "", fmt.Errorf("persist projected StackSpec: %w", err)
	}
	return path, nil
}

// stackSpecOutputRoot reads the destination the resolved plan governs.
//
// Techstack used to generate into its own tofu/ directory, and the CLI refused
// it outright: "architecture v2 --output must resolve to governed ResolvedPlan
// outputRoot deploy". The destination is the plan's to decide, so it is read
// rather than assumed, and a value that is not a single relative segment is
// rejected instead of being turned into a path traversal.
func stackSpecOutputRoot(document map[string]interface{}) (string, error) {
	root := strings.TrimSpace(stringFromInterface(mapFromInterface(document["generation"])["outputRoot"]))
	if root == "" {
		return "", fmt.Errorf("canonical StackSpec has no generation.outputRoot")
	}
	if root != filepath.Base(filepath.Clean(root)) || root == "." || root == ".." || filepath.IsAbs(root) {
		return "", fmt.Errorf("canonical StackSpec generation.outputRoot %q is not a single relative directory", root)
	}
	return root, nil
}

// stackSpecDomainBase resolves the routing domain for the handoff.
//
// This mirrors stackKitNetworkToKombinationNetwork, which is how the rest of
// Techstack already reads a domain out of the same document: an explicit
// domain, then network.domain, then a network mode that is really a domain,
// then kombify.me for a cloud-context stack in that address mode. metadata
// .domain is checked too because the routing overlay writes a custom domain
// there. Guessing outside that chain would silently generate routes for the
// wrong host, so an unresolvable domain fails closed and names what it saw.
func stackSpecDomainBase(handoff map[string]interface{}) (string, error) {
	network := mapFromInterface(handoff["network"])
	metadata := mapFromInterface(handoff["metadata"])
	for _, candidate := range []string{
		stringFromInterface(handoff["domain"]),
		stringFromInterface(network["domain"]),
		stringFromInterface(mapFromInterface(network["domain"])["base"]),
		stringFromInterface(metadata["domain"]),
	} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed, nil
		}
	}
	if mode := strings.TrimSpace(stringFromInterface(network["mode"])); networkModeAsLegacyDomain(mode) {
		return mode, nil
	}
	addressMode := strings.TrimSpace(firstNonEmpty(
		stringFromInterface(metadata[metadataKeyAddressMode]),
		stringFromInterface(metadata[metadataKeyRequestedAddressMode]),
	))
	if strings.EqualFold(addressMode, addressModeKombifyMe) &&
		strings.EqualFold(strings.TrimSpace(stringFromInterface(handoff["context"])), "cloud") {
		return addressModeKombifyMeDomain, nil
	}
	// A managed cloud runtime has no operator-chosen domain: the platform
	// assigns the address. The live demo stack's handoff carries no network
	// block at all -- "document keys: metadata,mode,name,nodes,services,ssh,
	// stackkit" -- so nothing above can resolve, yet the rollout is exactly the
	// class the UI already renders under kombify.me. stackKitSpecUsesManagedRuntime
	// is the product's own predicate for that class, so it decides here too
	// rather than a second, divergent rule.
	if stackKitSpecUsesManagedRuntime(handoff) {
		return addressModeKombifyMeDomain, nil
	}
	return "", fmt.Errorf(
		"canonical StackSpec authoring requires a routing domain; the handoff resolves none from domain, network.domain, network.mode, metadata.domain, or a cloud-context %s address mode (document keys: %s; metadata keys: %s)",
		addressModeKombifyMe,
		strings.Join(sortedKeys(handoff), ","),
		strings.Join(sortedKeys(metadata), ","),
	)
}

// sortedKeys names what a rejected handoff actually carried. Only key names are
// reported: the values can hold operator identity and routing provenance.
func sortedKeys(document map[string]interface{}) []string {
	names := make([]string, 0, len(document))
	for name := range document {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readStackKitSpecTemplate(kit string) (map[string]interface{}, error) {
	root := strings.TrimSpace(os.Getenv(stackKitSpecTemplateEnv))
	if root == "" {
		return nil, fmt.Errorf("canonical StackSpec authoring requires %s", stackKitSpecTemplateEnv)
	}
	path := filepath.Join(filepath.Clean(root), kit, "stack-spec.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- root comes from the image environment and the kit is a validated slug.
	if err != nil {
		return nil, fmt.Errorf("read canonical %s StackSpec template: %w", kit, err)
	}
	template, err := decodeStackSpecDocument(data)
	if err != nil {
		return nil, fmt.Errorf("canonical %s StackSpec template: %w", kit, err)
	}
	if got := strings.TrimSpace(stringFromInterface(template["apiVersion"])); got != canonicalStackSpecAPIVersion {
		return nil, fmt.Errorf("canonical %s StackSpec template has apiVersion %q, want %q", kit, got, canonicalStackSpecAPIVersion)
	}
	if got := strings.TrimSpace(stringFromInterface(mapFromInterface(template["kit"])["slug"])); got != kit {
		return nil, fmt.Errorf("canonical StackSpec template under %s declares kit %q", kit, got)
	}
	return template, nil
}

func decodeStackSpecDocument(data []byte) (map[string]interface{}, error) {
	var document map[string]interface{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse StackSpec document: %w", err)
	}
	if document == nil {
		return nil, fmt.Errorf("StackSpec document is empty")
	}
	return document, nil
}
