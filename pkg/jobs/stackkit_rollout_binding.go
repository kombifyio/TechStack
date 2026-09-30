package jobs

import (
	"strings"
)

// StackKitRolloutBindingResultField is the deploy-result and stack-config key
// that records the node-local StackSpec and kit a managed rollout applied.
const StackKitRolloutBindingResultField = "stackkit_rollout_binding"

// defaultStackKitSpecPath is the StackKits CLI's own default StackSpec path. It
// is only the fallback for a stack no managed rollout has bound yet.
const defaultStackKitSpecPath = "stack-spec.yaml"

// StackKitRolloutBinding names what a managed rollout left on the node: the
// kit it applied and the StackSpec generate, plan and apply read. Every later
// typed operation must address that same document; the CLI default
// stack-spec.yaml is only the pre-bind intent when an address bind ran, and
// absent otherwise.
type StackKitRolloutBinding struct {
	StackKit string
	// SpecPath is the StackSpec Apply used: the bound spec when ADDRESS_BIND
	// ran, the init spec otherwise.
	SpecPath string
	// InitSpecPath and BoundSpecPath record the two documents when an address
	// bind split them.
	InitSpecPath  string
	BoundSpecPath string
}

// stackKitRolloutBindingFor derives the binding from the request the rollout
// dispatched, mirroring how the apply sequence swaps in the bound spec.
func stackKitRolloutBindingFor(request StackKitLifecycleRequest) StackKitRolloutBinding {
	binding := StackKitRolloutBinding{
		StackKit: strings.TrimSpace(request.StackKit),
		SpecPath: strings.TrimSpace(request.SpecPath),
	}
	if bound := strings.TrimSpace(request.BoundSpecPath); bound != "" {
		binding.InitSpecPath = binding.SpecPath
		binding.BoundSpecPath = bound
		binding.SpecPath = bound
	}
	return binding
}

// Map is the persisted wire form.
func (b StackKitRolloutBinding) Map() map[string]interface{} {
	out := map[string]interface{}{"stackkit": b.StackKit, "spec_path": b.SpecPath}
	if b.BoundSpecPath != "" {
		out["init_spec_path"] = b.InitSpecPath
		out["bound_spec_path"] = b.BoundSpecPath
	}
	return out
}

// DecodeStackKitRolloutBinding reads a persisted binding. A value without a
// spec path is not a binding.
func DecodeStackKitRolloutBinding(value interface{}) (StackKitRolloutBinding, bool) {
	raw, ok := value.(map[string]interface{})
	if !ok {
		return StackKitRolloutBinding{}, false
	}
	binding := StackKitRolloutBinding{
		StackKit:      strings.TrimSpace(stringFromInterface(raw["stackkit"])),
		SpecPath:      strings.TrimSpace(stringFromInterface(raw["spec_path"])),
		InitSpecPath:  strings.TrimSpace(stringFromInterface(raw["init_spec_path"])),
		BoundSpecPath: strings.TrimSpace(stringFromInterface(raw["bound_spec_path"])),
	}
	if binding.SpecPath == "" {
		return StackKitRolloutBinding{}, false
	}
	return binding, true
}

// ApplyStackKitRolloutDefaults fills what an operator request omitted from
// the stack's recorded rollout: the kit whose local execution binding the node
// owns, and the StackSpec the rollout applied. Explicit request values win. A
// stack with no recorded rollout keeps the CLI default spec path.
func ApplyStackKitRolloutDefaults(request StackKitLifecycleRequest, binding StackKitRolloutBinding, fallbackKit string) StackKitLifecycleRequest {
	if strings.TrimSpace(request.StackKit) == "" {
		request.StackKit = firstNonEmpty(binding.StackKit, NormalizeStackKitRef(fallbackKit))
	}
	if strings.TrimSpace(request.SpecPath) == "" {
		request.SpecPath = binding.SpecPath
	}
	if strings.TrimSpace(request.SpecPath) == "" {
		request.SpecPath = defaultStackKitSpecPath
	}
	return request
}

// NormalizeStackKitRef maps the catalog-ref spellings a stack record carries
// onto the kit slugs that have a local execution binding. Anything else is
// returned trimmed and fails closed at dispatch.
func NormalizeStackKitRef(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "basement", "basementkit", "basement-kit":
		return "basement-kit"
	case "cloud", "cloudkit", "kombify-cloud-kit", "cloud-kit":
		return "cloud-kit"
	default:
		return strings.TrimSpace(value)
	}
}
